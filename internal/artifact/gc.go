package artifact

import (
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"os"
	"path/filepath"
	"strings"
)

type Object struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func ScopedBlobPath(task, digest string) (string, error) {
	if !taskValid(task) || !c.ValidDigest(digest) {
		return "", c.Fail(c.InvalidArgument, "invalid scoped blob")
	}
	return filepath.ToSlash(blobPath(task, digest)), nil
}
func ScopedSnapshotPath(task, digest string) (string, error) {
	ref := Ref{SchemaVersion: 1, TaskID: task, SnapshotDigest: digest}
	if err := validateRef(ref); err != nil {
		return "", err
	}
	return filepath.ToSlash(manifestPath(ref)), nil
}
func (a *Archive) Inventory(ctx context.Context) ([]Object, error) {
	objects := []Object{}
	err := a.VisitImmutable(ctx, func(name string, raw []byte) error {
		objects = append(objects, Object{name, c.HashBytes(raw), int64(len(raw))})
		return nil
	})
	return objects, err
}

// RemoveOrphan is trusted maintenance only. The agent supplies a revalidated
// unreachable object; this primitive never accepts host paths or directory trees.
func (a *Archive) RemoveOrphan(object Object) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false, os.ErrClosed
	}
	if !ImmutablePath(object.Path) || !c.ValidDigest(object.Digest) || object.Size < 0 || object.Size > 64<<20 {
		return false, c.Fail(c.InvalidArgument, "invalid bounded immutable GC object")
	}
	name := filepath.FromSlash(object.Path)
	before, err := a.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	raw, err := fileguard.ReadRegular(a.root, name, 64<<20)
	if err != nil {
		return false, err
	}
	if int64(len(raw)) != object.Size || c.HashBytes(raw) != object.Digest {
		return false, c.Fail(c.StaleBase, "GC object changed")
	}
	current, err := a.root.Lstat(name)
	if err != nil {
		return false, err
	}
	if !os.SameFile(before, current) || !current.Mode().IsRegular() {
		return false, c.Fail(c.Conflict, "GC object replaced")
	}
	if err = a.root.Remove(name); err != nil {
		return false, err
	}
	a.workUsed = max(int64(0), a.workUsed-object.Size)
	return true, fileguard.SyncParents(a.root, filepath.Dir(name))
}

// ReadManifest exposes a typed manifest to trusted reachability tracing.
func (a *Archive) ReadManifest(ref Ref) (Manifest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var result Manifest
	if a.closed {
		return result, os.ErrClosed
	}
	if err := validateRef(ref); err != nil {
		return result, err
	}
	raw, err := fileguard.ReadRegular(a.root, manifestPath(ref), 8<<20)
	if err != nil {
		return result, err
	}
	if c.DecodeStrict(raw, &result) != nil || result.TaskID != ref.TaskID || result.Snapshot.Digest != ref.SnapshotDigest || result.SchemaVersion != 1 {
		return result, c.Fail(c.StoreIntegrityError, "invalid typed snapshot root")
	}
	return result, nil
}
func TaskFromPath(name string) string {
	parts := strings.Split(name, "/")
	if !ImmutablePath(name) {
		return ""
	}
	return parts[1]
}
