package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestPrivacyDeletesInterruptedRestoreBeforePublication(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	directory := filepath.Join(t.TempDir(), ".restore-interrupted")
	root, err := freshPrivate(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.allocateManagedCopy(ctx, root, "RESTORE_STAGE", []string{options.TaskID}); err != nil {
		t.Fatal(err)
	}
	raw := []byte("interrupted restore private content")
	blob, err := artifact.ScopedBlobPath(options.TaskID, c.HashBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.sqlite", "state.sqlite-wal", "state.sqlite-shm", "owner.lock", "control.reserve", runtimeInstanceFile, "artifacts/archive.lock", "artifacts/" + blob, ".publish-0123456789abcdef0123456789abcdef"} {
		if err = fileguard.Publish(root, name, raw); err != nil {
			t.Fatal(name, err)
		}
	}
	root.Close()
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil || len(plan.Copies) != 1 || plan.Copies[0].Kind != "RESTORE_STAGE" || len(plan.Copies[0].Files) != 9 {
		t.Fatal("interrupted restore absent from plan", plan.Copies, err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-interrupted-restore", Plan: plan}); err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Copies[0].Files {
		if _, err = os.Stat(filepath.Join(directory, filepath.FromSlash(file.Path))); !os.IsNotExist(err) {
			t.Fatal("interrupted restore content retained", file.Path, err)
		}
	}
}

func TestPrivacyCopyAddedAfterIntentRemainsPendingAndExactRetryRecovers(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	directory := filepath.Join(t.TempDir(), "export")
	if _, err := s.Export(ctx, options.TaskID, directory); err != nil {
		t.Fatal(err)
	}
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	command := DeletionCommand{CommandID: "new-derivative-after-intent", Plan: plan}
	registry, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	view, err := readPrivacy(registry)
	if err == nil {
		err = appendPrivacy(registry, view, privacyRecord{Type: "DELETE_INTENT", Command: &command})
	}
	registry.Close()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Publish(root, "after/late-private.txt", []byte("foreign late payload")); err != nil {
		t.Fatal(err)
	}
	root.Close()
	result, err := s.DeleteContent(ctx, command)
	if err == nil || result.Status != "PENDING" {
		t.Fatal("new derivative silently left behind with PURGED", result, err)
	}
	state, err := s.State(ctx, options.TaskID)
	if err != nil || state.Deletion == nil || state.Deletion.Status != "PENDING" {
		t.Fatal(state, err)
	}
	if err = os.Remove(filepath.Join(directory, "after", "late-private.txt")); err != nil {
		t.Fatal(err)
	}
	if result, err = s.DeleteContent(ctx, command); err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal("exact retry failed after foreign payload was removed", result, err)
	}
}

func TestPrivacyDeletesExportPreviewSupportAndInterruptedAllocation(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	exports := []string{filepath.Join(t.TempDir(), "export"), filepath.Join(t.TempDir(), "preview"), filepath.Join(t.TempDir(), "support"), filepath.Join(t.TempDir(), "interrupted")}
	if _, err := s.Export(ctx, options.TaskID, exports[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeliveryPreview(ctx, options.TaskID, exports[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportSupport(ctx, exports[2]); err != nil {
		t.Fatal(err)
	}
	// Allocation is durable before any payload is written or final marker exists.
	root, err := freshPrivate(exports[3])
	if err != nil {
		t.Fatal(err)
	}
	if err = s.allocateManagedCopy(ctx, root, "EXPORT", []string{options.TaskID}); err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Publish(root, "after/private.txt", []byte("interrupted private copy")); err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Publish(root, ".publish-0123456789abcdef0123456789abcdef", []byte("crash temporary bytes")); err != nil {
		t.Fatal(err)
	}
	root.Close()
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil || len(plan.Copies) != 4 {
		t.Fatal("derivatives absent", plan.Copies, err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-managed-derivatives", Plan: plan}); err != nil {
		t.Fatal(err)
	}
	for _, copy := range plan.Copies {
		for _, f := range copy.Files {
			if _, err = os.Stat(filepath.Join(copy.Directory, filepath.FromSlash(f.Path))); !os.IsNotExist(err) {
				t.Fatal("derivative retained", copy.Kind, f.Path, err)
			}
		}
	}
}
func TestPrivacyCopyAllocationRejectsForeignFileAndRootReplacement(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	output := filepath.Join(t.TempDir(), "output")
	root, err := freshPrivate(output)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.allocateManagedCopy(ctx, root, "EXPORT", []string{options.TaskID}); err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Publish(root, "unregistered-secret", []byte("foreign")); err != nil {
		t.Fatal(err)
	}
	root.Close()
	if _, err = s.DeletionPreview(ctx, options.TaskID); err == nil {
		t.Fatal("foreign file adopted into deletion plan")
	}
	if err = os.Remove(filepath.Join(output, "unregistered-secret")); err != nil {
		t.Fatal(err)
	}
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(output, output+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "replaced-root", Plan: plan}); err == nil {
		t.Fatal("replacement root accepted")
	}
	// No intent was written: the original task is still fully readable.
	if _, _, err = s.Load(ctx, options.TaskID); err != nil {
		t.Fatal("failed preflight revoked task", err)
	}
}
func TestPrivacyDuplicateCommandIDRejectedBeforeIrreversibleIntent(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	history, err := s.Journal.History(ctx, options.TaskID, 0, 64)
	if err != nil || len(history.Records) == 0 {
		t.Fatal(err)
	}
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: history.Records[0].Event.ID, Plan: plan}); err == nil {
		t.Fatal("existing command ID reused")
	}
	root, _ := openPrivacyRoot(*s.privacy)
	view, err := readPrivacy(root)
	root.Close()
	if err != nil || view.Watermark != 0 {
		t.Fatal("failed command published irreversible intent", view.Watermark, err)
	}
	if _, _, err = s.Load(ctx, options.TaskID); err != nil {
		t.Fatal("failed command revoked content", err)
	}
	// An otherwise valid plan cannot add a path outside its task or private copy.
	attacked := plan
	attacked.Objects = append([]artifact.Object{}, plan.Objects...)
	attacked.Objects[0].Path = "../outside"
	attacked.ObjectsDigest = deletionObjectsDigest(attacked)
	attacked.Digest = deletionDigest(attacked)
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "escape", Plan: attacked}); err == nil {
		t.Fatal("task path escaped")
	}
}
