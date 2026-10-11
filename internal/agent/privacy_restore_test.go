package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/model"
)

func TestPrivacyRestoreDeletionRetainsSiblingOwnerTask(t *testing.T) {
	ctx := context.Background()
	session, options, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	defer session.Close()
	if _, err := session.Run(ctx, options.TaskID); err != nil {
		t.Fatal(err)
	}
	sibling := options
	sibling.TaskID = "sibling-owner-task"
	sibling.Prompt = []byte("Keep this sibling task when another task is deleted.")
	if _, err := session.Create(ctx, sibling); err != nil {
		t.Fatal(err)
	}

	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := session.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackup(ctx, backup, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = restored.Load(ctx, sibling.TaskID); err != nil {
		restored.Close()
		t.Fatal("restored sibling task was unavailable before deletion", err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}

	plan, err := session.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Stores) != 1 || filepath.Clean(plan.Stores[0].Scope.Directory) != filepath.Clean(restoredPath) {
		t.Fatal("final restore owner was not bound as a per-task deletion store", plan.Stores)
	}
	for _, copy := range plan.Copies {
		if filepath.Clean(copy.Directory) == filepath.Clean(restoredPath) {
			t.Fatal("active restore owner was treated as a whole-store managed copy")
		}
	}
	if _, err = session.DeleteContent(ctx, DeletionCommand{CommandID: "delete-one-restored-task", Plan: plan}); err != nil {
		t.Fatal(err)
	}

	restored, err = OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, _, err = restored.Load(ctx, sibling.TaskID); err != nil {
		t.Fatal("deleting one task removed its restored sibling", err)
	}
	if _, _, err = restored.Load(ctx, options.TaskID); err == nil {
		t.Fatal("deleted task content remained available in the restored owner")
	}
}
