package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
)

func TestPrivacyCLIThroughOwnerPreviewDeleteDedupAndDeletedObservations(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("private source"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "private summary", UsageKnown: true}}})
	if _, err = s.Create(ctx, agent.StartOptions{TaskID: "task", Root: source, Prompt: []byte("private goal"), Fixture: fixture, Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Run(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	host, err := owner.Open(ctx, directory, s)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	var out, errs bytes.Buffer
	for _, archivePath := range []string{directory, filepath.Join(directory, "artifacts")} {
		out.Reset()
		errs.Reset()
		if code := Execute([]string{"snapshot-save", "--root", source, "--store", archivePath, "--task", "bypass", "--json"}, &out, &errs); code != 4 || !strings.Contains(out.String()+errs.String(), "standalone archive command cannot bypass") {
			t.Fatal("session archive accepted standalone publication", code, out.String(), errs.String())
		}
	}
	out.Reset()
	errs.Reset()
	if code := Execute([]string{"delete-preview", "task", "--store", directory, "--json"}, &out, &errs); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	var plan agent.DeletionPlan
	if err = c.DecodeStrict(out.Bytes(), &plan); err != nil || len(plan.Objects) == 0 {
		t.Fatal(plan, err)
	}
	command := agent.DeletionCommand{CommandID: "user-reviewed-delete", Plan: plan}
	raw, _ := json.Marshal(command)
	input := filepath.Join(t.TempDir(), "delete.json")
	if err = os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var first agent.DeletionResult
	for attempt := 0; attempt < 2; attempt++ {
		out.Reset()
		errs.Reset()
		if code := Execute([]string{"delete", "task", "--store", directory, "--command-file", input, "--json"}, &out, &errs); code != 0 {
			t.Fatal(code, out.String(), errs.String())
		}
		var result agent.DeletionResult
		if err = c.DecodeStrict(out.Bytes(), &result); err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
			t.Fatal(result, err)
		}
		if attempt == 0 {
			first = result
		} else if !reflect.DeepEqual(first, result) {
			t.Fatal("duplicate changed receipt")
		}
	}
	for _, args := range [][]string{
		{"inspect", "task", "--at", "1", "--store", directory, "--json"},
		{"diff", "task", "--store", directory, "--json"},
		{"delete-preview", "task", "--store", directory, "--json"},
		{"snapshot-list", "--task", "task", "--store", filepath.Join(directory, "artifacts"), "--json"},
	} {
		out.Reset()
		errs.Reset()
		if code := Execute(args, &out, &errs); code != 4 || bytes.Contains(out.Bytes(), []byte("private goal")) || bytes.Contains(out.Bytes(), []byte("private source")) {
			t.Fatal("deleted observation leaked", args, code, out.String())
		}
	}
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	if code := Execute([]string{"delete", "task", "--store", directory, "--command-file", input, "--json"}, &out, &errs); code != 0 {
		t.Fatal("standalone retry", code, out.String(), errs.String())
	}
}
func TestPrivacyMissingStoreAndTaskCommandMismatchHaveNoEffects(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing")
	var out, errs bytes.Buffer
	if code := Execute([]string{"delete-preview", "task", "--store", directory, "--json"}, &out, &errs); code != 4 {
		t.Fatal(code, out.String())
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("missing store created", err)
	}
	input := filepath.Join(t.TempDir(), "command.json")
	raw, _ := json.Marshal(agent.DeletionCommand{CommandID: "invalid", Plan: agent.DeletionPlan{TaskID: "other"}})
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Execute([]string{"delete", "task", "--store", directory, "--command-file", input, "--json"}, &out, &errs); code != 4 {
		t.Fatal(code, out.String())
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("bad command created store", err)
	}
}
