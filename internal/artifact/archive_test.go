package artifact

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
)

func archiveFixture(t *testing.T) (string, workspace.Capture) {
	t.Helper()
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("exact\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	capture, err := workspace.CaptureDirectory(source, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return source, capture
}
func TestArchiveRestartMaterializationAndCorruptCandidate(t *testing.T) {
	source, capture := archiveFixture(t)
	directory := t.TempDir()
	archive, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := archive.Put("task-one", capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(directory); err == nil {
		t.Fatal("two archive owners")
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	loaded, err := archive.Get(ref)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snapshot.Digest != capture.Snapshot.Digest || !bytes.Equal(loaded.Contents["a.txt"], capture.Contents["a.txt"]) {
		t.Fatal("restart lost exact bytes")
	}
	materialized, err := archive.Materialize(ref)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.SourceReadOnlyEnforced {
		t.Fatal("false runtime enforcement")
	}
	raw, _ := os.ReadFile(filepath.Join(materialized.SourceDirectory, "a.txt"))
	if !bytes.Equal(raw, capture.Contents["a.txt"]) {
		t.Fatal("materialized source mismatch")
	}
	if _, err = os.Stat(filepath.Join(materialized.SourceDirectory, "empty")); err != nil {
		t.Fatal("empty directory lost")
	}
	if _, err = os.Stat(filepath.Join(materialized.SourceDirectory, ".git")); !os.IsNotExist(err) {
		t.Fatal("Git directory exposed")
	}
	if _, err = archive.Materialize(ref); err != nil {
		t.Fatal("idempotent reopen", err)
	}
	live, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if !bytes.Equal(live, capture.Contents["a.txt"]) {
		t.Fatal("live source changed")
	}
	if err = os.Chmod(filepath.Join(materialized.SourceDirectory, "a.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(materialized.SourceDirectory, "a.txt"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = archive.Materialize(ref); err == nil {
		t.Fatal("corrupt candidate trusted")
	}
}
func TestArchiveScopeMissingBlobAndNoOverwrite(t *testing.T) {
	_, capture := archiveFixture(t)
	directory := t.TempDir()
	archive, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	ref, err := archive.Put("one", capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"two", "ONE", "con", "../one", "one/../one", ""} {
		wrong := ref
		wrong.TaskID = task
		if _, err = archive.Get(wrong); err == nil {
			t.Fatal("cross task hydration", task)
		}
	}
	refs, err := archive.List("one")
	if err != nil || len(refs) != 1 {
		t.Fatal("list", refs, err)
	}
	missing, err := archive.List("empty")
	if err != nil || len(missing) != 0 {
		t.Fatal("missing list", missing, err)
	}
	hash := capture.Snapshot.Entries[0].Hash
	if err = os.WriteFile(filepath.Join(directory, blobPath("one", hash)), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = archive.Get(ref); err == nil {
		t.Fatal("corrupt archive hydrated")
	}
	if _, err = archive.Put("one", capture); err == nil {
		t.Fatal("immutable corruption silently replaced")
	}
}
func TestArchiveManifestCannotEscapeOrDropParent(t *testing.T) {
	_, capture := archiveFixture(t)
	directory := t.TempDir()
	archive, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	capture.Snapshot.Entries[0].Path = "../escape"
	capture.Contents = map[string][]byte{"../escape": []byte("exact\r\n")}
	if _, err := archive.Put("one", capture); err == nil {
		t.Fatal("traversal published")
	}
	if _, err := archive.Get(Ref{SchemaVersion: 1, TaskID: "one", SnapshotDigest: "../escape"}); err == nil {
		t.Fatal("digest traversal")
	}
	_ = c.SchemaVersion
}
func TestArchiveRefIsPublishedAfterAllBlobWrites(t *testing.T) {
	_, capture := archiveFixture(t)
	directory := t.TempDir()
	archive, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	hash := capture.Snapshot.Entries[0].Hash
	blocker := filepath.Join(directory, "tasks", "one", "blobs", hash[:2])
	if err = os.MkdirAll(filepath.Dir(blocker), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(blocker, []byte("not directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put("one", capture); err == nil {
		t.Fatal("blocked blob publication succeeded")
	}
	if _, err = os.Stat(filepath.Join(directory, manifestPath(Ref{SchemaVersion: 1, TaskID: "one", SnapshotDigest: capture.Snapshot.Digest}))); !os.IsNotExist(err) {
		t.Fatal("manifest visible before blobs", err)
	}
}
func TestArchiveOverlapRejected(t *testing.T) {
	source, capture := archiveFixture(t)
	archive, err := Open(filepath.Join(source, ".viber-local"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.Put("one", capture); err == nil {
		t.Fatal("archive mounted inside source")
	}
}

func TestMaterializationRejectsModeAndScopedManifestTampering(t *testing.T) {
	for _, attack := range []string{"mode", "manifest-task"} {
		t.Run(attack, func(t *testing.T) {
			_, capture := archiveFixture(t)
			archive, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Close()
			ref, err := archive.Put("task-one", capture)
			if err != nil {
				t.Fatal(err)
			}
			output, err := archive.Materialize(ref)
			if err != nil {
				t.Fatal(err)
			}
			if attack == "mode" {
				if err = os.Chmod(filepath.Join(output.SourceDirectory, "a.txt"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				raw := []byte("{\"schema_version\":1,\"task_id\":\"other\",\"snapshot_digest\":\"" + ref.SnapshotDigest + "\"}")
				if err = os.WriteFile(filepath.Join(filepath.Dir(output.SourceDirectory), "manifest.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = archive.Materialize(ref); err == nil {
				t.Fatal("altered candidate reused", attack)
			}
		})
	}
}
