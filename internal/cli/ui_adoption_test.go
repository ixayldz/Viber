package cli

import (
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
	host, err := owner.Open(ctx, directory, session)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ui := uiSession{"ui-adoption", directory}
	menu, quit, err := ui.command(ctx, "/")
	if err != nil || quit || !strings.Contains(menu, "/memory") || !strings.Contains(menu, "/workers") {
		t.Fatal("command discovery missing", menu, err)
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
