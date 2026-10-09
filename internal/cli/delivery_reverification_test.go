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
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/verify"
)

func TestDeliveryReverificationCLIUsesOwnerAndDeduplicatesImmutableCreation(t *testing.T) {
	ctx := context.Background()
	source, directory := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("original source"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	profile := runner.DefaultProfile("golang@sha256:" + strings.Repeat("a", 64))
	digest, _ := c.Digest(profile)
	plan, _ := c.CanonicalV1(verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{ID: "registered", Kind: "STATIC", RequirementIDs: []string{"user-goal"}, Argv: []string{"/bin/true"}, RunnerDigest: digest, Selection: "full", Closure: []verify.CheckScope{{Path: "checks", Recursive: true}}}}})
	fixture, _ := c.CanonicalV1(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "complete", UsageKnown: true}}})
	if _, err = s.Create(ctx, agent.StartOptions{Root: source, TaskID: "parent", Prompt: []byte("Inspect original source"), Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture, CheckPlan: plan, CheckRuntime: &agent.CheckRuntime{SchemaVersion: 1, Profile: profile}}); err != nil {
		t.Fatal(err)
	}
	parent, err := s.Controls(ctx, "parent", "cancel")
	if err != nil {
		t.Fatal(err)
	}
	previewPath := filepath.Join(t.TempDir(), "preview")
	preview, err := s.DeliveryPreview(ctx, "parent", previewPath)
	if err != nil {
		t.Fatal(err)
	}
	command := agent.DeliveryReverificationCommand{CommandID: "reviewed-merged-cli", ParentTask: "parent", NewTask: "merged", ParentSequence: parent.TaskSeq, PreviewDirectory: previewPath, ManifestDigest: preview.Digest}
	raw, _ := json.Marshal(command)
	input := filepath.Join(t.TempDir(), "command.json")
	if err = os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	host, err := owner.Open(ctx, directory, s)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	var first c.TaskState
	for n := 0; n < 2; n++ {
		var out, errout bytes.Buffer
		if exit := Execute([]string{"delivery-reverify", "parent", "--store", directory, "--command-file", input, "--json"}, &out, &errout); exit != 0 {
			t.Fatal(exit, out.String(), errout.String())
		}
		var state c.TaskState
		if err = c.DecodeStrict(out.Bytes(), &state); err != nil || state.TaskID != "merged" || state.Execution != c.Ready || state.Quality != c.Unverified {
			t.Fatal(state, err)
		}
		if n == 0 {
			first = state
		} else if !reflect.DeepEqual(first, state) {
			t.Fatal("owner duplicate changed creation")
		}
	}
	_, child, err := s.Load(ctx, "merged")
	if err != nil || child.MergedDelivery == nil || child.Runtime != nil || len(child.CheckRuns) != 0 || child.GoalCoverage != nil {
		t.Fatal(child, err)
	}
}

func TestDeliveryReverificationMissingStoreAndTaskMismatchHaveNoEffects(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing")
	command := agent.DeliveryReverificationCommand{CommandID: "invalid", ParentTask: "other"}
	raw, _ := json.Marshal(command)
	input := filepath.Join(t.TempDir(), "command.json")
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if exit := Execute([]string{"delivery-reverify", "parent", "--store", directory, "--command-file", input, "--json"}, &out, &errout); exit != 4 {
		t.Fatal(exit, out.String())
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("mismatched command created a store", err)
	}
}
