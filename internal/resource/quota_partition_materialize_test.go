package resource

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestPartitionMaterializationRequiresExactPublishedSnapshot(t *testing.T) {
	archive := t.TempDir()
	snapshot := c.Snapshot{SchemaVersion: c.SchemaVersion, Root: filepath.Join(t.TempDir(), "source"), Consistency: "FROZEN", Entries: []c.Entry{}, Directories: []string{}, Exclusions: []string{}}
	digest, err := c.Digest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Digest = digest
	manifest := struct {
		SchemaVersion int               `json:"schema_version"`
		TaskID        string            `json:"task_id"`
		Snapshot      c.Snapshot        `json:"snapshot"`
		IndexBlob     string            `json:"index_blob"`
		IgnoreBlobs   map[string]string `json:"ignore_blobs"`
	}{c.SchemaVersion, "task-a", snapshot, "", nil}
	manifestPath := filepath.Join(archive, "tasks", "task-a", "snapshots", digest+".json")
	if err = os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, task, candidate, familyPath, intent string
		accept                                    bool
	}{
		{"exact", "task-a", digest, "candidates", "", true},
		{"foreign-task", "task-b", digest, "candidates", "", false},
		{"foreign-subtree", "task-a", digest, "blobs", "", false},
		{"missing-snapshot", "task-a", c.HashBytes([]byte("absent")), "candidates", "", false},
		{"different-intent", "task-a", digest, "candidates", c.HashBytes([]byte("other")), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := test.intent
			if intent == "" {
				intent = c.HashBytes([]byte("materialize:" + test.task + ":" + test.candidate))
			}
			target := filepath.Join(archive, "tasks", test.task, test.familyPath, test.candidate)
			err := verifyPartitionMaterialization(archive, target, Request{RequestDigest: intent})
			if (err == nil) != test.accept {
				t.Fatalf("admission accept=%v err=%v", test.accept, err)
			}
		})
	}
	manifest.Snapshot.Root = filepath.Join(archive, "tampered")
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = verifyPartitionMaterialization(archive, filepath.Join(archive, "tasks", "task-a", "candidates", digest), Request{RequestDigest: c.HashBytes([]byte("materialize:task-a:" + digest))}); err == nil {
		t.Fatal("modified immutable snapshot was admitted")
	}
}
