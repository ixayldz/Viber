package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	"github.com/ixayldz/Viber/internal/owner"
)

func TestUICommandPaletteMemoryAndWorkersUseOwner(t *testing.T) {
	ctx := context.Background()
	source, directory := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	if _, err = session.Create(ctx, agent.StartOptions{Root: source, TaskID: "ui-adoption", Prompt: []byte("Inspect this project"), Budget: agent.DefaultBudget(), Autonomy: "review", Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	if _, err = session.Create(ctx, agent.StartOptions{Root: source, TaskID: "ui-source", Prompt: []byte("Record an explicit project fact"), Budget: agent.DefaultBudget(), Autonomy: "review", Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	cliSource, err := session.RecordProjectMemory(ctx, "ui-source", "memory-copy-cli-source", "FACT", "cli.copy", "User explicitly selected this source entry for the target task.")
	if err != nil {
		t.Fatal(err)
	}
	uiSource, err := session.RecordProjectMemory(ctx, "ui-source", "memory-copy-ui-source", "FACT", "ui.copy", "User explicitly selected this separate entry for the target task.")
	if err != nil {
		t.Fatal(err)
	}
	host, err := owner.Open(ctx, directory, session)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ui := uiSession{"ui-adoption", directory}
	menu, quit, err := ui.command(ctx, "/")
	if err != nil || quit || !strings.Contains(menu, "/memory") || !strings.Contains(menu, "copy SOURCE_TASK ITEM_ID DIGEST") || !strings.Contains(menu, "/workers") {
		t.Fatal("command discovery missing", menu, err)
	}
	var copyOut, copyErr bytes.Buffer
	cliArgs := []string{"project-memory", "copy", "--store", directory, "--from", "ui-source", "--to", "ui-adoption", "--id", cliSource.ItemID, "--digest", cliSource.EventDigest, "--command-id", "cli-memory-copy", "--json"}
	if exit := Execute(cliArgs, &copyOut, &copyErr); exit != 0 {
		t.Fatal("CLI copy did not route through the active owner", exit, copyOut.String(), copyErr.String())
	}
	var cliReceipt agent.ProjectMemoryCopyReceipt
	if err = json.Unmarshal(copyOut.Bytes(), &cliReceipt); err != nil || cliReceipt.Status != "COPIED" || cliReceipt.SourceTask != "ui-source" || cliReceipt.TargetTask != "ui-adoption" || cliReceipt.SourceItemID != cliSource.ItemID || cliReceipt.SourceItemDigest != cliSource.EventDigest || !cliReceipt.AdvisoryOnly {
		t.Fatal("CLI copy receipt lost exact source/target lineage", err, string(copyOut.Bytes()), cliReceipt)
	}
	var retryOut, retryErr bytes.Buffer
	if exit := Execute(cliArgs, &retryOut, &retryErr); exit != 0 || !bytes.Equal(copyOut.Bytes(), retryOut.Bytes()) {
		t.Fatal("exact owner retry did not return the original immutable copy receipt", exit, string(retryOut.Bytes()), retryErr.String())
	}
	copyText, _, err := ui.command(ctx, "/memory copy ui-source "+uiSource.ItemID+" "+uiSource.EventDigest)
	if err != nil || !strings.Contains(copyText, "ui-source → ui-adoption") || !strings.Contains(copyText, "While the target retains its pin, source deletion is blocked") || !strings.Contains(copyText, "Check current deletion/retention status") {
		t.Fatal("owner-backed TUI copy lost visible lineage or privacy caveat", copyText, err)
	}
	if _, _, err = ui.command(ctx, "/memory record DECISION storage Use SQLite for the local journal"); err != nil {
		t.Fatal(err)
	}
	memory, _, err := ui.command(ctx, "/memory search SQLite")
	if err != nil || !strings.Contains(memory, "ACTIVE") || !strings.Contains(memory, "Use SQLite") {
		t.Fatal("owner-backed memory unavailable", memory, err)
	}
	before, _, err := session.Load(ctx, "ui-adoption")
	if err != nil {
		t.Fatal(err)
	}
	analysis, _, err := ui.command(ctx, "/workers inventory,requirements")
	if err != nil || !strings.Contains(analysis, "COMPLETE") || !strings.Contains(analysis, "Inventory: 1 files") {
		t.Fatal("owner-backed local analyzers unavailable", analysis, err)
	}
	after, _, err := session.Load(ctx, "ui-adoption")
	if err != nil || after.WorkBudget == nil || before.WorkBudget == nil || after.WorkBudget.Steps-before.WorkBudget.Steps != 2 || after.WorkBudget.ActiveMillis-before.WorkBudget.ActiveMillis != 2000 || len(after.ReadWorkerAdmissions) != 1 || after.Quality != before.Quality {
		t.Fatal("owner command lost charge or changed quality", after, err)
	}
	if _, quit, err = ui.command(ctx, "/clear"); err != nil || quit {
		t.Fatal("display clear failed", err)
	}
	if _, quit, err = ui.command(ctx, "/exit"); err != nil || !quit {
		t.Fatal("exit must detach", err)
	}
	final, _, err := session.Load(ctx, "ui-adoption")
	if err != nil || final.TaskSeq != after.TaskSeq || final.Execution != after.Execution {
		t.Fatal("display commands changed durable task state", final, err)
	}
	if _, _, err = ui.command(ctx, "/compact 0"); err == nil {
		t.Fatal("compact must reject a value outside the kernel's 2..64 range")
	}
}
