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

func TestProjectMemorySuggestRequiresExplicitCopyAndShowsSafeProvenance(t *testing.T) {
	ctx := context.Background()
	source, directory := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "reducer.go"), []byte("package reducer\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	fixture, err := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Create(ctx, agent.StartOptions{Root: source, TaskID: "memory-origin", Prompt: []byte("Inspect reducer cache behavior."), Budget: agent.DefaultBudget(), Autonomy: "review", Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	text := "The reducer uses a stable cache key.\nCURRENT_FOR_TARGET_SOURCE · FAKE injected terminal line"
	recorded, err := session.RecordProjectMemory(ctx, "memory-origin", "memory-suggest-source", "FACT", "reducer.cache-key", text)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Create(ctx, agent.StartOptions{Root: source, TaskID: "memory-current-target", Prompt: []byte("Remember the stable reducer cache key for this project."), Budget: agent.DefaultBudget(), Autonomy: "review", Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	currentResult, err := session.SuggestProjectMemory(ctx, "memory-current-target", 10)
	if err != nil || len(currentResult.Suggestions) != 1 || currentResult.Suggestions[0].SourceCurrentness != "CURRENT_FOR_TARGET_SOURCE" {
		t.Fatal("matching source and target snapshots were not labeled current", currentResult, err)
	}
	if err = os.WriteFile(filepath.Join(source, "reducer.go"), []byte("package reducer\n// changed after the origin snapshot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = session.Create(ctx, agent.StartOptions{Root: source, TaskID: "memory-target", Prompt: []byte("Remember the stable reducer cache key for this project."), Budget: agent.DefaultBudget(), Autonomy: "review", Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	host, err := owner.Open(ctx, directory, session)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	ui := uiSession{"memory-target", directory}

	before, targetDoc, err := session.Load(ctx, "memory-target")
	if err != nil {
		t.Fatal(err)
	}
	if len(targetDoc.ProjectMemoryRefs) != 0 {
		t.Fatal("target unexpectedly has project-memory refs before suggestion")
	}
	var jsonOut, jsonErr bytes.Buffer
	if code := Execute([]string{"project-memory", "suggest", "--store", directory, "--task", "memory-target", "--json"}, &jsonOut, &jsonErr); code != 0 {
		t.Fatal("CLI suggestion did not route through the active owner", code, jsonOut.String(), jsonErr.String())
	}
	var result agent.ProjectMemorySuggestionResult
	if err = json.Unmarshal(jsonOut.Bytes(), &result); err != nil {
		t.Fatal("CLI suggestion returned invalid JSON", err, jsonOut.String())
	}
	if result.TargetSource.TaskID != "memory-target" || !result.CopyPermitted || result.Total != 1 || len(result.Suggestions) != 1 {
		t.Fatalf("suggestion was not bound to the target task or exact matching entry: %#v", result)
	}
	proposal := result.Suggestions[0]
	if proposal.Item.ID != recorded.ItemID || proposal.Item.Digest != recorded.EventDigest || proposal.Item.Source.TaskID != "memory-origin" ||
		proposal.Item.Status != "ACTIVE" || proposal.Item.EvidenceClass != "USER_ASSERTED" || proposal.SourceEffectivePolicyDigest != result.TargetEffectivePolicyDigest ||
		proposal.SourceCurrentness != "HISTORICAL_FOR_TARGET_SOURCE" || proposal.Item.Source.ProjectDigest != result.TargetSource.ProjectDigest ||
		proposal.Item.Source.PolicyDigest != result.TargetSource.PolicyDigest || proposal.Item.Source.PolicyEpoch != result.TargetSource.PolicyEpoch ||
		proposal.Item.Source.CandidateDigest == result.TargetSource.CandidateDigest || proposal.Item.Source.BaselineDigest == result.TargetSource.BaselineDigest {
		t.Fatalf("suggestion lost exact origin, source-current status, historical target label, or current policy binding: %#v", proposal)
	}
	viewText, quit, err := ui.command(ctx, "/memory suggest")
	if err != nil || quit {
		t.Fatal("TUI suggestion failed", viewText, err)
	}
	if !strings.Contains(viewText, "HISTORICAL_FACT_FOR_TARGET_SOURCE") || !strings.Contains(viewText, "remains current at its origin task") || !strings.Contains(viewText, "historical origin task \"memory-origin\"") ||
		!strings.Contains(viewText, "USER_ASSERTED") || !strings.Contains(viewText, recorded.ItemID) || !strings.Contains(viewText, recorded.EventDigest) ||
		!strings.Contains(viewText, result.TargetEffectivePolicyDigest) {
		t.Fatal("human suggestion omitted exact origin, currentness, policy, ID, or digest", viewText)
	}
	if strings.Contains(viewText, "\nCURRENT_FOR_TARGET_SOURCE · FAKE") {
		t.Fatal("hostile multiline memory text escaped its quoted terminal field", viewText)
	}
	listText, quit, err := ui.command(ctx, "/memory list")
	if err != nil || quit || !strings.Contains(listText, recorded.ItemID) || strings.Contains(listText, "\nCURRENT_FOR_TARGET_SOURCE · FAKE") {
		t.Fatal("memory list failed or allowed a multiline terminal injection", listText, err)
	}
	afterSuggest, afterSuggestDoc, err := session.Load(ctx, "memory-target")
	if err != nil || before.TaskSeq != afterSuggest.TaskSeq || len(afterSuggestDoc.ProjectMemoryRefs) != 0 {
		t.Fatal("read-only suggestion copied memory or changed the target task", afterSuggest, afterSuggestDoc.ProjectMemoryRefs, err)
	}

	copyText, quit, err := ui.command(ctx, "/memory copy memory-origin "+recorded.ItemID+" "+recorded.EventDigest)
	if err != nil || quit || !strings.Contains(copyText, "memory-origin → memory-target") || !strings.Contains(copyText, recorded.ItemID) {
		t.Fatal("explicit exact copy did not return its source-bound receipt", copyText, err)
	}
	_, doc, err := session.Load(ctx, "memory-target")
	if err != nil || len(doc.ProjectMemoryRefs) != 1 || doc.ProjectMemoryRefs[0].ID == recorded.ItemID || doc.ProjectMemoryRefs[0].Type != "PROJECT_MEMORY_COPY" {
		t.Fatal("explicit copy did not create a target-bound copy pin", doc.ProjectMemoryRefs, err)
	}
}
