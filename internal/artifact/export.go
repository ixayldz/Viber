package artifact

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// ImmutablePath accepts only published task blobs and snapshot manifests.
// Materializations, temporary files and locks are never restore inputs.
func ImmutablePath(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) < 4 || parts[0] != "tasks" || !taskValid(parts[1]) {
		return false
	}
	switch parts[2] {
	case "blobs":
		return len(parts) == 5 && c.ValidDigest(parts[4]) && parts[3] == parts[4][:2]
	case "snapshots":
		return len(parts) == 4 && strings.HasSuffix(parts[3], ".json") && c.ValidDigest(strings.TrimSuffix(parts[3], ".json"))
	}
	return false
}

// VisitImmutable walks a locked, append-only archive without loading the bundle
// into memory. The visitor must not call back into this archive.
func (a *Archive) VisitImmutable(ctx context.Context, visit func(string, []byte) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return os.ErrClosed
	}
	if _, err := a.root.Lstat("tasks"); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	count := 0
	return fs.WalkDir(a.root.FS(), "tasks", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 100000 {
			return c.Fail(c.UnsupportedCapability, "archive backup entry limit exceeded")
		}
		parts := strings.Split(name, "/")
		if d.IsDir() {
			if len(parts) == 3 && parts[2] == "candidates" {
				return fs.SkipDir
			}
			if len(parts) > 4 || len(parts) > 1 && !taskValid(parts[1]) {
				return c.Fail(c.StoreIntegrityError, "unexpected archive directory")
			}
			return nil
		}
		if !ImmutablePath(name) {
			return c.Fail(c.StoreIntegrityError, "unexpected archive object")
		}
		raw, err := fileguard.ReadRegular(a.root, filepath.FromSlash(name), 64<<20)
		if err != nil {
			return err
		}
		if parts[2] == "blobs" {
			if c.HashBytes(raw) != path.Base(name) {
				return c.Fail(c.StoreIntegrityError, "backup blob digest mismatch")
			}
		} else {
			if _, err = a.get(Ref{SchemaVersion: 1, TaskID: parts[1], SnapshotDigest: strings.TrimSuffix(parts[3], ".json")}); err != nil {
				return err
			}
		}
		return visit(name, raw)
	})
}
