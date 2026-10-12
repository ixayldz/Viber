package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
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
