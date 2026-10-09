package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGCCLIThroughOwnerAndFreshRestore(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	if _, err = s.Create(ctx, agent.StartOptions{Root: source, TaskID: "task", Prompt: []byte("Review"), Budget: agent.DefaultBudget(), Autonomy: "guided", Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Run(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Archive.PutBytes("task", []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	host, err := owner.Open(ctx, directory, s)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	defer s.Close()
	var out, errs bytes.Buffer
	if code := Execute([]string{"store-gc-preview", "--store", directory, "--json"}, &out, &errs); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	var plan agent.GCPlan
	if err = c.DecodeStrict(out.Bytes(), &plan); err != nil || len(plan.Objects) != 1 {
		t.Fatal(plan, err)
	}
	command := agent.GCCommand{CommandID: "cli-gc", Plan: plan}
	raw, _ := json.Marshal(command)
	input := filepath.Join(t.TempDir(), "gc.json")
	if err = os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	if code := Execute([]string{"store-gc", "--store", directory, "--command-file", input, "--json"}, &out, &errs); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	var first agent.GCResult
	if err = c.DecodeStrict(out.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err = agent.RestoreBackup(ctx, backup, target); err != nil {
		t.Fatal(err)
	}
	restored, err := agent.OpenExisting(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	second, err := restored.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("restored GC command lost idempotency", second, err)
	}
}
