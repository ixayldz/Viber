package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ixayldz/Viber/internal/model"
)

func deletionFixture(t *testing.T) (*Session, StartOptions, string) {
	t.Helper()
	s, options, source := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true, InputTokens: 11, OutputTokens: 3}, {Text: "private final narrative", UsageKnown: true, InputTokens: 9, OutputTokens: 4}}, true)
	t.Cleanup(func() { s.Close() })
	if _, err := s.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Archive.Materialize(doc.Candidate); err != nil {
		t.Fatal(err)
	}
	return s, options, source
}
func TestTaskContentDeletionPurgesCASMaterializationsAndManagedBackups(t *testing.T) {
	ctx := context.Background()
	s, options, source := deletionFixture(t)
	before, _, err := s.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	m, err := s.Backup(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Objects) == 0 || len(plan.Copies) != 1 {
		t.Fatal("incomplete plan", plan)
	}
	command := DeletionCommand{CommandID: "delete-one", Plan: plan}
	first, err := s.DeleteContent(ctx, command)
	if err != nil || first.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal(first, err)
	}
	second, err := s.DeleteContent(ctx, command)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("dedup failed", second, err)
	}
	after, err := s.State(ctx, options.TaskID)
	if err != nil || after.Deletion == nil || after.Deletion.Status != "PURGED" || !reflect.DeepEqual(before.Tokens, after.Tokens) || !reflect.DeepEqual(before.Resources, after.Resources) || after.Quality != before.Quality || after.TaskSeq != before.TaskSeq+2 {
		t.Fatal("ledger changed", after, err)
	}
	for _, seq := range []int64{1, before.TaskSeq} {
		if _, _, err = s.Inspect(ctx, options.TaskID, seq); err == nil {
			t.Fatal("historical raw content exposed", seq)
		}
	}
	if _, _, err = s.Load(ctx, options.TaskID); err == nil {
		t.Fatal("deleted document readable")
	}
	if _, err = s.RecordSteering(ctx, SteeringInput{CommandID: "post-delete", TaskID: options.TaskID, Text: "must not be published"}); err == nil {
		t.Fatal("raw content revived")
	}
	if _, err = s.QueueControl(ctx, QueueCommand{CommandID: "post-delete-queue", TaskID: options.TaskID, Action: "add", Text: "must not be queued"}); err == nil {
		t.Fatal("queue resurrected raw bytes")
	}
	if _, err = s.Create(ctx, options); err == nil {
		t.Fatal("creation reused deleted task identity")
	}
	support, err := s.Support(ctx)
	if err != nil || len(support.Tasks) != 1 || support.Tasks[0].ContentDeletion != "PURGED" {
		t.Fatal("minimal support unavailable", support, err)
	}
	inventory, err := s.Archive.TaskInventory(ctx, options.TaskID)
	if err != nil || len(inventory) != 0 {
		t.Fatal("local content remains", inventory, err)
	}
	for _, file := range m.Files {
		if _, err = os.Stat(filepath.Join(backup, filepath.FromSlash(file.Path))); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("backup bytes retained", file.Path, err)
		}
	}
	if _, err = RestoreBackup(ctx, backup, filepath.Join(t.TempDir(), "restore-old")); err == nil {
		t.Fatal("deleted backup restored")
	}
	live, err := os.ReadFile(filepath.Join(source, "a.txt"))
	if err != nil || string(live) != "before\r\n" {
		t.Fatal("live source affected", err)
	}
	newBackup := filepath.Join(t.TempDir(), "after")
	newer, err := s.Backup(ctx, newBackup)
	if err != nil || newer.DeletionWatermark != 1 {
		t.Fatal("tombstone-aware backup", newer, err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(ctx, newBackup, restored); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, _, err = reopened.Load(ctx, options.TaskID); err == nil {
		t.Fatal("new backup resurrected deleted content")
	}
	if _, err = s.GCPreview(ctx); err != nil {
		t.Fatal("deleted task breaks unrelated GC", err)
	}
}
func TestTaskContentDeletionRefusesUnknownActiveAndRetainedDescendants(t *testing.T) {
	ctx := context.Background()
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "unknown"}[unknown], func(t *testing.T) {
			s, options, _ := startFixture(t, []Turn{{Text: "unknown", UsageKnown: !unknown}}, true)
			defer s.Close()
			if unknown {
				if _, err := s.Run(ctx, options.TaskID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.DeletionPreview(ctx, options.TaskID); err == nil {
				t.Fatal("risk was waived")
			}
		})
	}
	s, options, _ := deletionFixture(t)
	parent, err := s.State(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "child", UsageKnown: true}}})
	child, err := s.NewAttempt(ctx, AttemptOptions{ParentTask: options.TaskID, NewTask: "child", ExpectedParentSequence: parent.TaskSeq, Fixture: fixture, AllowUnverified: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Run(ctx, child.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeletionPreview(ctx, options.TaskID); err == nil {
		t.Fatal("retained descendant lost its parent evidence")
	}
	childPlan, err := s.DeletionPreview(ctx, child.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-child", Plan: childPlan}); err != nil {
		t.Fatal(err)
	}
	parentPlan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-parent", Plan: parentPlan}); err != nil {
		t.Fatal(err)
	}
}
func TestDeletionIntentRecoversAfterProcessRestartAndPartialUnlink(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	command := DeletionCommand{CommandID: "interrupted-deletion", Plan: plan}
	registry, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	view, err := readPrivacy(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = appendPrivacy(registry, view, privacyRecord{Type: "DELETE_INTENT", Command: &command}); err != nil {
		t.Fatal(err)
	}
	registry.Close()
	// Simulate loss immediately after the durable external intent.
	directory := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, _, err = reopened.Load(ctx, options.TaskID); err == nil {
		t.Fatal("intent did not revoke raw read")
	}
	// Simulate a later retry already having unlinked one planned object.
	if _, err = reopened.Archive.RemoveTaskContent(options.TaskID, plan.Objects[0]); err != nil {
		t.Fatal(err)
	}
	result, err := reopened.DeleteContent(ctx, command)
	if err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal("recovery", result, err)
	}
	command.CommandID = "forged-retry"
	if _, err = reopened.DeleteContent(ctx, command); err == nil {
		t.Fatal("different command adopted prior deletion")
	}
}
func TestDeletionRejectsStalePlanAndChangedManagedCopyWithoutFalseReceipt(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	first, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Archive.PutBytes(options.TaskID, []byte("orphan after preview")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "stale", Plan: first}); err == nil {
		t.Fatal("stale object set accepted")
	}
	backup := filepath.Join(t.TempDir(), "backup")
	m, err := s.Backup(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	command := DeletionCommand{CommandID: "changed-backup", Plan: plan}
	// Persist intent, then alter a managed byte as a hostile external writer.
	registry, _ := openPrivacyRoot(*s.privacy)
	view, _ := readPrivacy(registry)
	if err = appendPrivacy(registry, view, privacyRecord{Type: "DELETE_INTENT", Command: &command}); err != nil {
		t.Fatal(err)
	}
	registry.Close()
	f := m.Files[0]
	path := filepath.Join(backup, filepath.FromSlash(f.Path))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("foreign mutation"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := s.DeleteContent(ctx, command)
	if err == nil || result.Status != "PENDING" {
		t.Fatal("false purge", result, err)
	}
	if _, _, err = s.Load(ctx, options.TaskID); err == nil {
		t.Fatal("pending purge exposed raw bytes")
	}
	if _, err = RestoreBackup(ctx, backup, filepath.Join(t.TempDir(), "old")); err == nil {
		t.Fatal("watermark bypass")
	}
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, command); err != nil {
		t.Fatal("exact retry did not complete", err)
	}
}
func TestDeletionAuthorityLossAndConcurrentRestoredOwnerFailClosed(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackup(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	clone, err := OpenExisting(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeletionPreview(ctx, options.TaskID); err == nil {
		t.Fatal("active family owner not fenced")
	}
	clone.Close()
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil || len(plan.Stores) != 1 {
		t.Fatal("restored scope not traced", plan.Stores, err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-family-scopes", Plan: plan}); err != nil {
		t.Fatal(err)
	}
	clone, err = OpenExisting(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	cloneState, err := clone.State(ctx, options.TaskID)
	if err != nil || cloneState.Deletion == nil || cloneState.Deletion.Status != "PURGED" {
		t.Fatal("clone tombstone missing", cloneState, err)
	}
	cloneObjects, err := clone.Archive.TaskInventory(ctx, options.TaskID)
	if err != nil || len(cloneObjects) != 0 {
		t.Fatal("clone content retained", cloneObjects, err)
	}
	clone.Close()
	registry := filepath.Join(s.privacy.Directory, "authority.json")
	if err = os.Rename(registry, registry+"-missing"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(registry+"-missing", registry)
	if _, _, err = s.Load(ctx, options.TaskID); err == nil {
		t.Fatal("missing authority fell back to local content")
	}
	if _, err = RestoreBackup(ctx, backup, filepath.Join(t.TempDir(), "unsafe")); err == nil {
		t.Fatal("missing authority trusted old restore")
	}
}
