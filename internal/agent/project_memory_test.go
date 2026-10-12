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

func TestAutomaticProjectMemoryConflictScopeIsSameTask(t *testing.T) {
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

	state, doc, err := s.Load(ctx, original.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	var contextItems []ProjectMemoryItem
	var truncated bool
	s.mu.Lock()
	contextItems, truncated, err = s.projectMemoryContextLocked(ctx, state, doc)
	s.mu.Unlock()
	if err != nil || truncated || len(contextItems) != 1 || contextItems[0].ID != local.ItemID || contextItems[0].Status != "ACTIVE" {
		t.Fatalf("foreign task conflict affected same-task automatic context: items=%#v truncated=%v err=%v", contextItems, truncated, err)
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
