package resource

import (
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// Materialization is a fresh managed copy of one already published snapshot,
// not a generic permission to write below the retained archive. The archive
// caller verifies all blobs before requesting this reservation; admission
// independently pins its task, manifest, destination and immutable intent.
func verifyPartitionMaterialization(archiveRoot, target string, request Request) error {
	rel, ok := partitionRelativePath(archiveRoot, target)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if !ok || len(parts) != 4 || parts[0] != "tasks" || parts[2] != "candidates" ||
		!materializationTaskID(parts[1]) || !c.ValidDigest(parts[3]) ||
		request.RequestDigest != c.HashBytes([]byte("materialize:"+parts[1]+":"+parts[3])) {
		return c.Fail(c.StaleAuthority, "materialization must bind one exact task candidate path and intent")
	}
	root, err := os.OpenRoot(archiveRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Join("tasks", parts[1], "snapshots", parts[3]+".json"), 8<<20)
	if err != nil {
		return c.Fail(c.StaleAuthority, "materialization requires its existing immutable snapshot manifest")
	}
	// This is the on-disk archive manifest envelope. Keeping its admission
	// reader here avoids an artifact -> resource -> artifact dependency cycle.
	var manifest struct {
		SchemaVersion int               `json:"schema_version"`
		TaskID        string            `json:"task_id"`
		Snapshot      c.Snapshot        `json:"snapshot"`
		IndexBlob     string            `json:"index_blob"`
		IgnoreBlobs   map[string]string `json:"ignore_blobs"`
	}
	if c.DecodeStrict(raw, &manifest) != nil || manifest.SchemaVersion != c.SchemaVersion ||
		manifest.TaskID != parts[1] || manifest.Snapshot.SchemaVersion != c.SchemaVersion ||
		manifest.Snapshot.Digest != parts[3] || len(manifest.Snapshot.Entries) > 100000 {
		return c.Fail(c.StaleAuthority, "materialization snapshot binding is invalid")
	}
	manifest.Snapshot.Digest = ""
	digest, err := c.Digest(manifest.Snapshot)
	if err != nil || digest != parts[3] {
		return c.Fail(c.StaleAuthority, "materialization snapshot digest differs from its immutable source")
	}
	return nil
}

func materializationTaskID(task string) bool {
	if len(task) == 0 || len(task) > 64 {
		return false
	}
	for i, ch := range task {
		if ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || i > 0 && (ch == '_' || ch == '-') {
			continue
		}
		return false
	}
	return true
}
