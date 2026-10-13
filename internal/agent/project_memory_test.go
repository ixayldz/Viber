package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/store"
)

func projectMemorySibling(t *testing.T, s *Session, original StartOptions, task string, turns []Turn) StartOptions {
	t.Helper()
	raw, err := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	if err != nil {
		t.Fatal(err)
	}
	options := original
	options.TaskID = task
	options.Prompt = []byte("Inspect the same source snapshot.")
	options.Fixture = raw
	if _, err = s.Create(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	return options
}

func projectMemoryItemByID(t *testing.T, result ProjectMemorySearchResult, id string) ProjectMemoryItem {
	t.Helper()
	for _, item := range result.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("project memory item %s not found in %+v", id, result.Items)
	return ProjectMemoryItem{}
}

func changeMemoryTaskAutonomy(t *testing.T, s *Session, task, autonomy string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	state, doc, err := s.Load(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	doc.Autonomy = autonomy
	if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectMemoryFreshnessUsesQueryCandidateAndPolicy(t *testing.T) {
	ctx := context.Background()
	s, original, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	query := projectMemorySibling(t, s, original, "task-two", []Turn{
		{Calls: []model.Call{patchCall()}, UsageKnown: true},
		{Text: "done", UsageKnown: true},
	})
	baseState, err := s.State(ctx, original.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	queryState, err := s.State(ctx, query.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if baseState.BaselineDigest != queryState.BaselineDigest || baseState.CandidateDigest != queryState.CandidateDigest {
		t.Fatal("fixture siblings did not start from the same source and candidate")
	}

	recorded, err := s.RecordProjectMemory(ctx, original.TaskID, "memory-source-fact", "FACT", "build.command", "Run go test ./internal/agent before changing the reducer.")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.SearchProjectMemory(ctx, query.TaskID, "build.command", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if item := projectMemoryItemByID(t, result, recorded.ItemID); item.Status != "ACTIVE" {
		t.Fatal("matching query source did not admit active advisory memory", item)
	}
	if _, err = s.CorrectProjectMemory(ctx, query.TaskID, "cross-task-correction", recorded.ItemID, recorded.EventDigest, "FACT", "build.command", "Use a different command."); err == nil {
		t.Fatal("cross-task correction bypassed same-task privacy lineage")
	}

	changeMemoryTaskAutonomy(t, s, query.TaskID, "auto")
	result, err = s.SearchProjectMemory(ctx, query.TaskID, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	item := projectMemoryItemByID(t, result, recorded.ItemID)
	if item.Status != "STALE" || !item.Redacted || item.Text != "" || item.Key != "" || item.ContentDigest != "" {
		t.Fatal("policy-mismatched memory body was not redacted", item)
	}
	changeMemoryTaskAutonomy(t, s, query.TaskID, "guided")

	if _, err = s.Run(ctx, query.TaskID); err != nil {
		t.Fatal(err)
	}
	result, err = s.SearchProjectMemory(ctx, query.TaskID, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	item = projectMemoryItemByID(t, result, recorded.ItemID)
	if item.Status != "STALE" || item.Redacted || item.Text == "" {
		t.Fatal("candidate-stale memory should remain visible as advisory text", item)
	}
	state, doc, err := s.Load(ctx, query.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	contextItems, _, err := s.projectMemoryContextLocked(ctx, state, doc)
	s.mu.Unlock()
	if err != nil || len(contextItems) != 0 {
		t.Fatal("candidate-stale memory entered model context", contextItems, err)
	}
}

func TestAutomaticProjectMemoryConflictMetadataFindsForeignAssertionWithoutHydratingBody(t *testing.T) {
	ctx := context.Background()
	s, original, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	other := projectMemorySibling(t, s, original, "task-two", []Turn{{Text: "done", UsageKnown: true}})

	local, err := s.RecordProjectMemory(ctx, original.TaskID, "memory-local-assertion", "CONVENTION", "build.command", "Use the focused agent test command.")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.RecordProjectMemory(ctx, other.TaskID, "memory-other-assertion", "CONVENTION", "build.command", "Use a different project test command.")
	if err != nil {
		t.Fatal(err)
	}

	// Explicit manual same-project search retains its cross-task conflict view.
	search, err := s.SearchProjectMemory(ctx, original.TaskID, "build.command", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if item := projectMemoryItemByID(t, search, local.ItemID); item.Status != "CONFLICT" {
		t.Fatalf("manual same-project search lost its cross-task conflict semantics: %#v", item)
	}
	if item := projectMemoryItemByID(t, search, foreign.ItemID); item.Status != "CONFLICT" {
		t.Fatalf("manual same-project search lost the foreign conflicting assertion: %#v", item)
	}

	// Once manual search has proven the third-task body is valid, remove that
	// body. Automatic context must still detect the journal metadata conflict,
	// and must not try to hydrate that unrelated archive object.
	blob, err := artifact.ScopedBlobPath(other.TaskID, foreign.EventDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(s.directory, "artifacts", filepath.FromSlash(blob))); err != nil {
		t.Fatal(err)
	}
	state, doc, err := s.Load(ctx, original.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	var contextItems []ProjectMemoryItem
	var truncated bool
	s.mu.Lock()
	contextItems, truncated, err = s.projectMemoryContextLocked(ctx, state, doc)
	s.mu.Unlock()
	if err != nil || truncated || len(contextItems) != 0 {
		t.Fatalf("foreign current conflict was not excluded by metadata-only automatic context: items=%#v truncated=%v err=%v", contextItems, truncated, err)
	}
}

func TestProjectMemoryConflictComparisonIncludesCopiedItemContent(t *testing.T) {
	project := c.HashBytes([]byte("project"))
	ownDigest := c.HashBytes([]byte("copied assertion"))
	otherDigest := c.HashBytes([]byte("one current foreign assertion"))
	item := ProjectMemoryItem{Source: ProjectMemorySource{ProjectDigest: project}, Kind: "FACT", Key: "release.rule",
		ContentDigest: ownDigest, EvidenceClass: "USER_COPIED", Status: "ACTIVE"}
	key := project + "\x00FACT\x00release.rule"
	if !projectMemoryItemHasCurrentConflict(item, map[string]map[string]bool{key: {otherDigest: true}}) {
		t.Fatal("copied assertion was not compared against the one differing current metadata digest")
	}
	if projectMemoryItemHasCurrentConflict(item, map[string]map[string]bool{key: {ownDigest: true}}) {
		t.Fatal("matching assertion digest was treated as a conflict")
	}
	item.Status = "STALE"
	if projectMemoryItemHasCurrentConflict(item, map[string]map[string]bool{key: {otherDigest: true}}) {
		t.Fatal("stale copied assertion was treated as a current conflict")
	}
	item.Status = "ACTIVE"
	item.Redacted = true
	if projectMemoryItemHasCurrentConflict(item, map[string]map[string]bool{key: {otherDigest: true}}) {
		t.Fatal("redacted assertion was treated as a comparable conflict")
	}
}

func TestAutomaticProjectMemoryConflictIgnoresStaleThirdTaskAssertion(t *testing.T) {
	ctx := context.Background()
	s, original, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	third := projectMemorySibling(t, s, original, "task-three-stale-conflict", []Turn{
		{Calls: []model.Call{patchCall()}, UsageKnown: true},
		{Text: "done", UsageKnown: true},
	})
	local, err := s.RecordProjectMemory(ctx, original.TaskID, "memory-current-local", "FACT", "release.rule", "Keep the local release rule.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordProjectMemory(ctx, third.TaskID, "memory-stale-third", "FACT", "release.rule", "A conflicting rule from the old source."); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Run(ctx, third.TaskID); err != nil {
		t.Fatal(err)
	}
	state, doc, err := s.Load(ctx, original.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	items, truncated, err := s.projectMemoryContextLocked(ctx, state, doc)
	s.mu.Unlock()
	if err != nil || truncated || len(items) != 1 || items[0].ID != local.ItemID || items[0].Status != "ACTIVE" {
		t.Fatalf("stale third-task source incorrectly suppressed current local memory: items=%#v truncated=%v err=%v", items, truncated, err)
	}
}

func TestAutomaticProjectMemoryConflictIgnoresPrivacyDeletedThirdTask(t *testing.T) {
	ctx := context.Background()
	s, original, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	third := projectMemorySibling(t, s, original, "task-three-revoked-conflict", []Turn{{Text: "done", UsageKnown: true}})
	if _, err := s.Run(ctx, third.TaskID); err != nil {
		t.Fatal("complete third task before privacy revocation", err)
	}
	local, err := s.RecordProjectMemory(ctx, original.TaskID, "memory-current-local-after-revocation", "FACT", "privacy.rule", "Keep the local privacy rule.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordProjectMemory(ctx, third.TaskID, "memory-revoked-third", "FACT", "privacy.rule", "A conflicting rule from a task later deleted."); err != nil {
		t.Fatal(err)
	}
	plan, err := s.DeletionPreview(ctx, third.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-revoked-memory-source", Plan: plan}); err != nil {
		t.Fatal(err)
	}
	state, doc, err := s.Load(ctx, original.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	items, truncated, err := s.projectMemoryContextLocked(ctx, state, doc)
	s.mu.Unlock()
	if err != nil || truncated || len(items) != 1 || items[0].ID != local.ItemID || items[0].Status != "ACTIVE" {
		t.Fatalf("privacy-deleted third-task assertion remained a conflict: items=%#v truncated=%v err=%v", items, truncated, err)
	}
}

func TestAutomaticCopiedMemoryConflictUsesThirdTaskMetadataAndLegacyIsExplicit(t *testing.T) {
	ctx := context.Background()
	s, original, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	target := projectMemorySibling(t, s, original, "task-two-copy-target", []Turn{{Text: "done", UsageKnown: true}})
	third := projectMemorySibling(t, s, original, "task-three-copy-conflict", []Turn{{Text: "done", UsageKnown: true}})
	source, err := s.RecordProjectMemory(ctx, original.TaskID, "memory-copy-source", "DECISION", "review.rule", "Review the source change before merging.")
	if err != nil {
		t.Fatal(err)
	}
	copied, err := s.CopyProjectMemory(ctx, original.TaskID, target.TaskID, "memory-copy-to-target", source.ItemID, source.EventDigest)
	if err != nil {
		t.Fatal(err)
	}
	conflict, err := s.RecordProjectMemory(ctx, third.TaskID, "memory-third-conflict", "DECISION", "review.rule", "Skip review before merging.")
	if err != nil {
		t.Fatal(err)
	}
	// Conflict/redacted copies intentionally omit their key and text, so an
	// unfiltered metadata listing is needed to inspect their current status.
	search, err := s.SearchProjectMemory(ctx, target.TaskID, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	copyItem := projectMemoryItemByID(t, search, copied.CopyID)
	if copyItem.Status != "CONFLICT" {
		t.Fatalf("manual search did not include the third-task conflict for the explicit copy: %#v", copyItem)
	}
	conflictBlob, err := artifact.ScopedBlobPath(third.TaskID, conflict.EventDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(s.directory, "artifacts", filepath.FromSlash(conflictBlob))); err != nil {
		t.Fatal(err)
	}
	state, doc, err := s.Load(ctx, target.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	items, truncated, err := s.projectMemoryContextLocked(ctx, state, doc)
	s.mu.Unlock()
	if err != nil || truncated || len(items) != 0 {
		t.Fatalf("explicit copied memory stayed active despite metadata-only third-task conflict: items=%#v truncated=%v err=%v", items, truncated, err)
	}

	legacyTask := projectMemorySibling(t, s, original, "task-four-legacy-memory", []Turn{{Text: "done", UsageKnown: true}})
	legacyState, err := s.State(ctx, legacyTask.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	legacyMutation := &c.ProjectMemoryMutation{SourceDocumentDigest: legacyState.DocumentDigest, EventDigest: c.HashBytes([]byte("legacy memory")),
		CommandID: "legacy-memory-command", EventBytes: 64, SourceTaskSeq: legacyState.TaskSeq, SpecVersion: legacyState.SpecVersion,
		PolicyEpoch: legacyState.PolicyEpoch, KernelGeneration: s.Journal.Generation(), Action: "ASSERT"}
	if _, err = s.Journal.Execute(ctx, store.Command{ID: "legacy-memory-command", TaskID: legacyTask.TaskID, Actor: "user",
		ExpectedTaskSeq: legacyState.TaskSeq, Type: "ProjectMemoryRecorded", Payload: c.EventPayload{
			DocumentDigest: c.HashBytes([]byte("legacy memory document")), ProjectMemoryMutation: legacyMutation}}); err != nil {
		t.Fatal("legacy metadata-only event no longer replays", err)
	}
	index, err := s.Journal.ProjectMemoryConflictMetadata(ctx, source.Source.ProjectDigest, projectMemoryMaxEvents, projectMemoryMaxBytes)
	if err != nil || index.Complete || index.Reason != store.ProjectMemoryConflictLegacyWitness {
		t.Fatalf("legacy journal event did not make conflict coverage explicitly incomplete: index=%#v err=%v", index, err)
	}
	localState, localDoc, err := s.Load(ctx, original.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	contextText, err := s.projectMemoryAdvisoryContextLocked(ctx, localState, localDoc)
	s.mu.Unlock()
	if err != nil || !strings.Contains(contextText, "PROJECT_MEMORY_CONFLICT_INDEX_INCOMPLETE") ||
		strings.Contains(contextText, "ACTIVE_MEMORY_OUTPUT_LIMIT") || strings.Contains(contextText, "OPERATOR_CONTEXT_READ_DENIED") {
		t.Fatalf("legacy conflict uncertainty was not reported with its own omission reason: text=%s err=%v", contextText, err)
	}
}

func TestProjectMemoryConflictCapabilityDetectionSurvivesReferenceRemoval(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	if _, err := s.RecordProjectMemory(ctx, options.TaskID, "memory-capability-durable", "FACT", "durable.rule", "A durable advisory statement."); err != nil {
		t.Fatal(err)
	}
	used, err := s.Journal.ProjectMemoryConflictMetadataUsed(ctx)
	if err != nil || !used {
		t.Fatalf("durable witness use was not detected: used=%v err=%v", used, err)
	}
	s.mu.Lock()
	state, doc, err := s.Load(ctx, options.TaskID)
	if err == nil {
		doc.ProjectMemoryRefs = nil
		_, err = s.record(ctx, state, doc, "SessionRecorded", c.EventPayload{})
	}
	s.mu.Unlock()
	if err != nil {
		t.Fatal("remove live references for durable-capability regression", err)
	}
	used, err = s.Journal.ProjectMemoryConflictMetadataUsed(ctx)
	if err != nil || !used {
		t.Fatalf("witness capability scan depended on live task references: used=%v err=%v", used, err)
	}
}

func TestProjectMemorySourceRejectsFalseJournalCursor(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	recorded, err := s.RecordProjectMemory(ctx, options.TaskID, "memory-cursor-source", "FACT", "test.command", "Run focused source-bound tests.")
	if err != nil {
		t.Fatal(err)
	}
	changeMemoryTaskAutonomy(t, s, options.TaskID, "auto")

	forged := recorded.Source
	forged.TaskSeq++ // Sequence 2 has a valid archived document with the same snapshots/policy, but a different digest.
	stateAtForgedCursor, err := s.Journal.ReplayAt(ctx, forged.TaskID, forged.TaskSeq)
	if err != nil {
		t.Fatal(err)
	}
	if stateAtForgedCursor.TaskID != forged.TaskID || stateAtForgedCursor.DocumentDigest == forged.DocumentDigest ||
		stateAtForgedCursor.BaselineDigest != forged.BaselineDigest || stateAtForgedCursor.CandidateDigest != forged.CandidateDigest ||
		stateAtForgedCursor.PolicyEpoch != forged.PolicyEpoch {
		t.Fatalf("fixture did not create a valid different document at the forged cursor: %#v source=%#v", stateAtForgedCursor, forged)
	}
	if raw, readErr := s.Archive.GetBytes(forged.TaskID, forged.DocumentDigest); readErr != nil || c.HashBytes(raw) != forged.DocumentDigest {
		t.Fatalf("original source document is not valid retained content: readErr=%v", readErr)
	}
	if err = s.validateProjectMemorySource(ctx, forged, nil); err == nil {
		t.Fatal("source document was accepted at a different journal sequence")
	}
}

func TestTerminalProjectMemoryMutationsSurviveOwnerReopen(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	state, err := s.Run(ctx, options.TaskID)
	if err != nil || state.Execution != c.Terminated {
		t.Fatalf("fixture did not reach a terminal task: state=%#v err=%v", state, err)
	}
	directory := s.directory
	baseline := state

	reopen := func() {
		t.Helper()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		var openErr error
		s, openErr = Open(ctx, directory)
		if openErr != nil {
			t.Fatal(openErr)
		}
	}
	assertMetadataOnly := func(wantRevision int64) c.TaskState {
		t.Helper()
		got, err := s.State(ctx, options.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Execution != baseline.Execution || got.Outcome != baseline.Outcome || got.Quality != baseline.Quality ||
			got.Fulfillment != baseline.Fulfillment || got.SpecVersion != baseline.SpecVersion || got.PolicyEpoch != baseline.PolicyEpoch ||
			got.ProjectMemoryRevision != wantRevision || !sameProjectMemoryWorkBudget(baseline.WorkBudget, got.WorkBudget) {
			t.Fatalf("memory mutation changed task authority/work state: before=%#v after=%#v", baseline, got)
		}
		return got
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()

	reopen()
	first, err := s.RecordProjectMemory(ctx, options.TaskID, "terminal-memory-record", "DECISION", "tests.command", "Run the focused memory lifecycle tests.")
	if err != nil {
		t.Fatalf("record after reopening a terminal task: %v", err)
	}
	assertMetadataOnly(1)

	reopen()
	corrected, err := s.CorrectProjectMemory(ctx, options.TaskID, "terminal-memory-correct", first.ItemID, first.EventDigest,
		"DECISION", "tests.command", "Run the focused lifecycle test after each reducer change.")
	if err != nil {
		t.Fatalf("correct after reopening a terminal task: %v", err)
	}
	assertMetadataOnly(2)

	reopen()
	if _, err = s.RetireProjectMemory(ctx, options.TaskID, "terminal-memory-retire", corrected.ItemID, corrected.EventDigest); err != nil {
		t.Fatalf("retire after reopening a terminal task: %v", err)
	}
	assertMetadataOnly(3)
	result, err := s.SearchProjectMemory(ctx, options.TaskID, "tests.command", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if item := projectMemoryItemByID(t, result, corrected.ItemID); item.Status != "RETIRED" {
		t.Fatalf("terminal retirement was not retained: %#v", item)
	}
}

func TestProjectMemoryHonorsOperatorContextReadRestriction(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	storeDir := t.TempDir()
	policyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("before\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	effects := []string{"snapshot.read", "model.infer"}
	operator := config.Authority{SchemaVersion: 1, Revision: 1, AllowedEffects: &effects}
	policyPath := filepath.Join(policyDir, "operator.json")
	policyBytes, err := c.CanonicalV1(operator)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(policyPath, policyBytes, 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := config.BindAuthority(policyPath, source)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Resolve(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved.Authority = &ref
	s, err := Open(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fixture, err := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "done", UsageKnown: true}}})
	if err != nil {
		t.Fatal(err)
	}
	options := StartOptions{Root: source, Prompt: []byte("Inspect source."), TaskID: "task-context-read", Budget: DefaultBudget(),
		Autonomy: "guided", AllowUnverified: true, Fixture: fixture, Config: &resolved}
	if _, err = s.Create(ctx, options); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SearchProjectMemory(ctx, options.TaskID, "", 0, 20); err == nil {
		t.Fatal("manual memory search ignored denied context.read effect")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.PolicyDenied {
			t.Fatalf("search returned an untyped denial: %v", err)
		}
	}
	for _, kind := range []string{"continuity-info", "context-why", "context-page", "history-page"} {
		_, observeErr := s.Observe(ctx, options.TaskID, Observation{Kind: kind, Limit: 64})
		var typed *c.Error
		if !errors.As(observeErr, &typed) || typed.Code != c.PolicyDenied {
			t.Fatalf("%s exposed retained context without operator context.read: %v", kind, observeErr)
		}
	}
	state, doc, err := s.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	contextText, contextErr := s.projectMemoryAdvisoryContextLocked(ctx, state, doc)
	s.mu.Unlock()
	if contextErr != nil {
		t.Fatal(contextErr)
	}
	if !strings.Contains(contextText, "OPERATOR_CONTEXT_READ_DENIED") || strings.Contains(contextText, "\"items\":[{") {
		t.Fatalf("automatic context did not expose a bounded denial omission: %s", contextText)
	}
}

func TestProjectMemoryCorrectionBackupGCAndPrivacyDeletion(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()

	first, err := s.RecordProjectMemory(ctx, options.TaskID, "memory-record", "DECISION", "tests.command", "Use focused tests for irreversible lifecycle changes.")
	if err != nil {
		t.Fatal(err)
	}
	stateAfterRecord, err := s.State(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.RecordProjectMemory(ctx, options.TaskID, "memory-record", "DECISION", "tests.command", "Use focused tests for irreversible lifecycle changes.")
	if err != nil || retry.EventDigest != first.EventDigest || retry.EventID != first.EventID {
		t.Fatal("record retry was not idempotent", retry, err)
	}
	stateAfterRetry, err := s.State(ctx, options.TaskID)
	if err != nil || stateAfterRetry.TaskSeq != stateAfterRecord.TaskSeq {
		t.Fatal("idempotent retry appended a second journal event", stateAfterRetry, err)
	}

	corrected, err := s.CorrectProjectMemory(ctx, options.TaskID, "memory-correct", first.ItemID, first.EventDigest, "DECISION", "tests.command", "Run the agent lifecycle acceptance suite before changing privacy behavior.")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.SearchProjectMemory(ctx, options.TaskID, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if old := projectMemoryItemByID(t, result, first.ItemID); old.Status != "SUPERSEDED" {
		t.Fatal("correction did not preserve the old fact as superseded", old)
	}
	if replacement := projectMemoryItemByID(t, result, corrected.ItemID); replacement.Status != "ACTIVE" {
		t.Fatal("corrected fact was not active", replacement)
	}
	retired, err := s.RetireProjectMemory(ctx, options.TaskID, "memory-retire", corrected.ItemID, corrected.EventDigest)
	if err != nil {
		t.Fatal(err)
	}
	retireRetry, err := s.RetireProjectMemory(ctx, options.TaskID, "memory-retire", corrected.ItemID, corrected.EventDigest)
	if err != nil || retireRetry.EventDigest != retired.EventDigest {
		t.Fatal("retirement retry was not idempotent", retireRetry, err)
	}
	result, err = s.SearchProjectMemory(ctx, options.TaskID, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if item := projectMemoryItemByID(t, result, corrected.ItemID); item.Status != "RETIRED" || item.Text == "" {
		t.Fatal("explicit retirement did not remain visible", item)
	}
	if _, err = s.Run(ctx, options.TaskID); err != nil {
		t.Fatal("complete source task before backup and privacy deletion", err)
	}

	_, currentDoc, err := s.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unreachable := map[string]bool{}
	for _, object := range plan.Objects {
		unreachable[object.Path] = true
	}
	for _, ref := range currentDoc.ProjectMemoryRefs {
		path, pathErr := artifact.ScopedBlobPath(options.TaskID, ref.Digest)
		if pathErr != nil || unreachable[path] {
			t.Fatal("GC failed to retain a live memory event", ref, pathErr)
		}
	}

	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(ctx, backup, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredResult, err := restored.SearchProjectMemory(ctx, options.TaskID, "", 0, 20)
	if err != nil {
		restored.Close()
		t.Fatal(err)
	}
	if item := projectMemoryItemByID(t, restoredResult, corrected.ItemID); item.Status != "RETIRED" {
		restored.Close()
		t.Fatal("restore lost project memory lifecycle state", item)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}

	deletion, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-project-memory-source", Plan: deletion})
	if err != nil || deleted.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal("privacy deletion did not purge task-scoped memory", deleted, err)
	}
	if _, _, err = s.Load(ctx, options.TaskID); err == nil {
		t.Fatal("deleted task memory source remained readable")
	}
	if _, err = RestoreBackup(ctx, backup, filepath.Join(t.TempDir(), "stale-restore")); err == nil {
		t.Fatal("pre-deletion backup restored retired task memory")
	}
	if _, err = os.Stat(filepath.Join(restoredPath, "database", "state.sqlite")); !os.IsNotExist(err) {
		t.Fatal("managed restored copy survived deletion", err)
	}
}

func TestProjectMemoryCopyRestoreAndDeletionClosure(t *testing.T) {
	ctx := context.Background()
	s, source, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	target := projectMemorySibling(t, s, source, "copy-target-task", []Turn{{Text: "done", UsageKnown: true}})
	if _, err := s.Run(ctx, source.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, target.TaskID); err != nil {
		t.Fatal(err)
	}

	assertion, err := s.RecordProjectMemory(ctx, source.TaskID, "copy-source-record", "DECISION", "build.validation", "Run the focused lifecycle test before release.")
	if err != nil {
		t.Fatal(err)
	}
	copyReceipt, err := s.CopyProjectMemory(ctx, source.TaskID, target.TaskID, "copy-source-to-target", assertion.ItemID, assertion.EventDigest)
	if err != nil || copyReceipt.Status != "COPIED" || !copyReceipt.AdvisoryOnly {
		t.Fatal("explicit copy was not durably recorded", copyReceipt, err)
	}

	backup := filepath.Join(t.TempDir(), "copy-backup")
	if _, err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "copy-restored")
	if _, err = RestoreBackup(ctx, backup, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	state, doc, err := restored.Load(ctx, target.TaskID)
	if err != nil {
		restored.Close()
		t.Fatal("restored copy did not validate against its current owner pin", err)
	}
	if state.ProjectMemoryCopyCount != 1 || len(doc.ProjectMemoryRefs) == 0 || doc.ProjectMemoryRefs[len(doc.ProjectMemoryRefs)-1].Type != "PROJECT_MEMORY_COPY" {
		restored.Close()
		t.Fatal("restored target lost its durable copy marker", state.ProjectMemoryCopyCount, doc.ProjectMemoryRefs)
	}
	search, err := restored.SearchProjectMemory(ctx, target.TaskID, "lifecycle", 0, 20)
	if err != nil {
		restored.Close()
		t.Fatal(err)
	}
	if item := projectMemoryItemByID(t, search, copyReceipt.CopyID); item.Status != "ACTIVE" || item.EvidenceClass != "USER_COPIED" {
		restored.Close()
		t.Fatal("restored current copy lost its advisory lineage", item)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = s.DeletionPreview(ctx, source.TaskID); err == nil {
		t.Fatal("source deletion preview ignored an unpurged explicit copy")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.PolicyDenied {
			t.Fatal("unpurged copy did not block source deletion with policy denial", err)
		}
	}
	targetPlan, err := s.DeletionPreview(ctx, target.TaskID)
	if err != nil {
		t.Fatal("copied target could not be purged", err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-memory-copy-target", Plan: targetPlan}); err != nil {
		t.Fatal("target copy purge failed", err)
	}
	retry, err := s.CopyProjectMemory(ctx, source.TaskID, target.TaskID, "copy-source-to-target", assertion.ItemID, assertion.EventDigest)
	if err != nil || retry != copyReceipt {
		t.Fatal("post-purge retry changed the immutable historical copy receipt", retry, copyReceipt, err)
	}

	sourcePlan, err := s.DeletionPreview(ctx, source.TaskID)
	if err != nil {
		t.Fatal("source remained pinned after every registered copy target was purged", err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-memory-copy-source", Plan: sourcePlan}); err != nil {
		t.Fatal("source purge failed after copy target purge", err)
	}
}

func TestProjectMemoryCopyPreparedPublicationRecoversAfterRestart(t *testing.T) {
	for _, stage := range []string{"prepared-before-archive", "target-journal-committed"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			s, source, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
			defer func() {
				if s != nil {
					_ = s.Close()
				}
			}()
			target := projectMemorySibling(t, s, source, "copy-recovery-target", []Turn{{Text: "done", UsageKnown: true}})
			if _, err := s.Run(ctx, source.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Run(ctx, target.TaskID); err != nil {
				t.Fatal(err)
			}
			assertion, err := s.RecordProjectMemory(ctx, source.TaskID, "copy-recovery-source-record", "DECISION", "build.validation", "Run the focused recovery test.")
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.State(ctx, target.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			s.projectMemoryCopyFault = func(point string) error {
				if point == stage {
					return errors.New("injected process interruption")
				}
				return nil
			}
			if _, err = s.CopyProjectMemory(ctx, source.TaskID, target.TaskID, "copy-recovery-command", assertion.ItemID, assertion.EventDigest); err == nil {
				t.Fatal("copy interruption point was not reached")
			}
			s.projectMemoryCopyFault = nil
			interrupted, err := s.State(ctx, target.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "prepared-before-archive" {
				if interrupted.TaskSeq != before.TaskSeq || interrupted.ProjectMemoryCopyCount != before.ProjectMemoryCopyCount {
					t.Fatal("pre-archive interruption published target state", before, interrupted)
				}
			} else {
				if interrupted.TaskSeq != before.TaskSeq+1 || interrupted.ProjectMemoryCopyCount != before.ProjectMemoryCopyCount+1 {
					t.Fatal("post-journal interruption did not leave the exact committed target", before, interrupted)
				}
				if _, _, err = s.Load(ctx, target.TaskID); err == nil {
					t.Fatal("target exposed a copy before its ACTIVE registry record")
				}
			}

			directory := s.directory
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s = nil
			s, err = OpenExisting(ctx, directory)
			if err != nil {
				t.Fatal("prepared copy store did not reopen", err)
			}
			receipt, err := s.CopyProjectMemory(ctx, source.TaskID, target.TaskID, "copy-recovery-command", assertion.ItemID, assertion.EventDigest)
			if err != nil || receipt.Status != "COPIED" {
				t.Fatal("exact retry did not recover the prepared copy", receipt, err)
			}
			recovered, doc, err := s.Load(ctx, target.TaskID)
			if err != nil {
				t.Fatal("recovered copy failed active reference validation", err)
			}
			if recovered.ProjectMemoryCopyCount != before.ProjectMemoryCopyCount+1 ||
				recovered.ProjectMemoryRevision != before.ProjectMemoryRevision+1 ||
				len(doc.ProjectMemoryRefs) != 1 {
				t.Fatal("recovery duplicated or lost the target copy counters and reference", recovered, doc.ProjectMemoryRefs)
			}
			search, err := s.SearchProjectMemory(ctx, target.TaskID, "recovery", 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if item := projectMemoryItemByID(t, search, receipt.CopyID); item.Status != "ACTIVE" || item.EvidenceClass != "USER_COPIED" {
				t.Fatal("recovered copy was not active advisory memory", item)
			}
		})
	}
}

func TestProjectMemoryCopyPolicyDenialPrecedesSourceMemoryHydration(t *testing.T) {
	ctx := context.Background()
	sourceRoot := t.TempDir()
	storeDir := t.TempDir()
	policyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "a.txt"), []byte("before\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fixture, err := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "done", UsageKnown: true}}})
	if err != nil {
		t.Fatal(err)
	}
	sourceOptions := StartOptions{Root: sourceRoot, Prompt: []byte("Inspect source."), TaskID: "copy-policy-source", Budget: DefaultBudget(),
		Autonomy: "guided", AllowUnverified: true, Fixture: fixture}
	if _, err = s.Create(ctx, sourceOptions); err != nil {
		t.Fatal(err)
	}
	assertion, err := s.RecordProjectMemory(ctx, sourceOptions.TaskID, "copy-policy-source-memory", "DECISION", "transfer.rule", "Do not read this body when context.read is denied.")
	if err != nil {
		t.Fatal(err)
	}
	effects := []string{"snapshot.read", "model.infer"}
	authority := config.Authority{SchemaVersion: 1, Revision: 1, AllowedEffects: &effects}
	policyPath := filepath.Join(policyDir, "operator.json")
	policyBytes, err := c.CanonicalV1(authority)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(policyPath, policyBytes, 0600); err != nil {
		t.Fatal(err)
	}
	authorityRef, err := config.BindAuthority(policyPath, sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Resolve(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved.Authority = &authorityRef
	sourceState, sourceDoc, err := s.Load(ctx, sourceOptions.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	sourceDoc.Config = &resolved // Bind a current operator policy that denies context.read.
	if _, err = s.record(ctx, sourceState, sourceDoc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	targetOptions := sourceOptions
	targetOptions.TaskID = "copy-policy-target"
	if _, err = s.Create(ctx, targetOptions); err != nil {
		t.Fatal(err)
	}
	blobPath, err := artifact.ScopedBlobPath(sourceOptions.TaskID, assertion.EventDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(s.directory, "artifacts", filepath.FromSlash(blobPath))); err != nil {
		t.Fatal(err)
	}
	targetBefore, err := s.State(ctx, targetOptions.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CopyProjectMemory(ctx, sourceOptions.TaskID, targetOptions.TaskID, "copy-policy-denied", assertion.ItemID, assertion.EventDigest); err == nil {
		t.Fatal("copy unexpectedly crossed the current source context.read denial")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.PolicyDenied {
			t.Fatalf("copy hydrated the missing source body before denying context.read: %v", err)
		}
	}
	targetAfter, err := s.State(ctx, targetOptions.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if targetAfter.TaskSeq != targetBefore.TaskSeq || targetAfter.DocumentDigest != targetBefore.DocumentDigest ||
		targetAfter.ProjectMemoryRevision != targetBefore.ProjectMemoryRevision || targetAfter.ProjectMemoryCopyCount != targetBefore.ProjectMemoryCopyCount {
		t.Fatal("denied copy wrote target task state", targetBefore, targetAfter)
	}
	used, err := ProjectMemoryCopyCapabilityUsed(ctx, s.Journal)
	if err != nil || used {
		t.Fatalf("denied copy left a PREPARED capability intent: used=%v err=%v", used, err)
	}
}
