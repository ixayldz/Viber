package delivery

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
	"os"
	"path/filepath"
	"testing"
)

func capturedVersion(t *testing.T, root string, files map[string]string) workspace.Capture {
	t.Helper()
	for path, raw := range files {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestDeliveryPreviewPreservesUserFileAndReverifiesCombinedSource(t *testing.T) {
	root := t.TempDir()
	baseline := capturedVersion(t, root, map[string]string{"a.txt": "one\ntwo\nthree\nfour\n", "unchanged.txt": "base\n"})
	candidate := capturedVersion(t, root, map[string]string{"a.txt": "one\ncandidate\nthree\nfour\n"})
	current := capturedVersion(t, root, map[string]string{"a.txt": "one\ntwo\nthree\nuser\n", "user.txt": "own\n"})
	preview, merged, err := Preview(context.Background(), baseline, candidate, current)
	if err != nil || preview.Status != "READY" || preview.Verification != "UNVERIFIED" || !preview.RequiresReverification || preview.LiveWorkspaceWritten {
		t.Fatal("overclaimed delivery", preview, err)
	}
	if string(merged.Contents["a.txt"]) != "one\ncandidate\nthree\nuser\n" || string(merged.Contents["user.txt"]) != "own\n" {
		t.Fatal("user content lost")
	}
	if preview.Baseline != baseline.Snapshot.Digest || preview.Candidate != candidate.Snapshot.Digest || preview.Target != current.Snapshot.Digest || preview.Result != merged.Snapshot.Digest || !c.ValidDigest(preview.Digest) {
		t.Fatal("unbound preview")
	}
	live, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(live) != "one\ntwo\nthree\nuser\n" {
		t.Fatal("preview wrote source")
	}
	merged.Contents["user.txt"][0] = '!'
	if string(current.Contents["user.txt"]) != "own\n" {
		t.Fatal("merge aliases captured user bytes")
	}
}
func TestDeliveryPreviewConflictHasNoPartialResult(t *testing.T) {
	root := t.TempDir()
	baseline := capturedVersion(t, root, map[string]string{"a.txt": "base\n", "b.txt": "base\n"})
	candidate := capturedVersion(t, root, map[string]string{"a.txt": "candidate\n", "b.txt": "candidate\n"})
	current := capturedVersion(t, root, map[string]string{"a.txt": "user\n", "b.txt": "base\n"})
	preview, merged, err := Preview(context.Background(), baseline, candidate, current)
	if err != nil || preview.Status != "CONFLICT" || preview.Result != "" || len(merged.Contents) != 0 {
		t.Fatal("partial conflict published", preview, err)
	}
	if preview.Decisions[0].Action != "CONFLICT" || preview.Decisions[1].Action != "APPLY_CANDIDATE" {
		t.Fatal("missing review decisions")
	}
}
func TestDeliveryPreviewDeleteEditAdditionAndTargetIdentity(t *testing.T) {
	root := t.TempDir()
	baseline := capturedVersion(t, root, map[string]string{"delete.txt": "base", "keep.txt": "keep"})
	if err := os.Remove(filepath.Join(root, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	candidate := capturedVersion(t, root, map[string]string{"new.txt": "candidate"})
	current := capturedVersion(t, root, map[string]string{"delete.txt": "user edit", "new.txt": "outside"})
	preview, _, err := Preview(context.Background(), baseline, candidate, current)
	if err != nil || preview.Status != "CONFLICT" {
		t.Fatal("delete/edit or add/add admitted", err)
	}
	different := capturedVersion(t, t.TempDir(), map[string]string{"keep.txt": "keep"})
	if _, _, err = Preview(context.Background(), baseline, candidate, different); err == nil {
		t.Fatal("changed target identity admitted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = Preview(canceled, baseline, baseline, baseline); err == nil {
		t.Fatal("zero-change cancellation ignored")
	}
}
