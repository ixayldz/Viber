package artifact

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
)

func TaskContentPath(task, name string) bool {
	if !taskValid(task) {
		return false
	}
	prefix := "tasks/" + task + "/"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if ImmutablePath(name) {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(name, prefix), "/")
	if len(parts) < 3 || parts[0] != "candidates" {
		return false
	}
	candidate := parts[1]
	if !c.ValidDigest(candidate) {
		candidate = strings.TrimPrefix(candidate, ".prepare-")
		if len(candidate) != 32 {
			return false
		}
		for _, r := range candidate {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
	}
	return len(parts) == 3 && parts[2] == "manifest.json" || len(parts) >= 4 && parts[2] == "source" && policy.SafePath(strings.Join(parts[3:], "/"))
}

// TaskInventory includes raw CAS and published/interrupted materializations.
// Every entry is read through the private root; links and special files fail.
func (a *Archive) TaskInventory(ctx context.Context, task string) ([]Object, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := []Object{}
	if a.closed {
		return result, os.ErrClosed
	}
	if !taskValid(task) {
		return result, c.Fail(c.InvalidArgument, "invalid deletion task")
	}
	base := "tasks/" + task
	if _, err := a.root.Lstat(base); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	count := 0
	total := int64(0)
	err := fs.WalkDir(a.root.FS(), base, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 100000 {
			return c.Fail(c.BudgetLimitReached, "deletion tree entry quota exceeded")
		}
		if entry.IsDir() {
			return nil
		}
		if !TaskContentPath(task, name) {
			return c.Fail(c.StoreIntegrityError, "unregistered task content path")
		}
		raw, err := fileguard.ReadRegular(a.root, filepath.FromSlash(name), 64<<20)
		if err != nil {
			return err
		}
		total += int64(len(raw))
		if len(result) >= 32768 || total > 512<<20 {
			return c.Fail(c.BudgetLimitReached, "bounded deletion inventory exceeded")
		}
		result = append(result, Object{name, c.HashBytes(raw), int64(len(raw))})
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, err
}
func (a *Archive) RemoveTaskContent(task string, object Object) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false, os.ErrClosed
	}
	if !TaskContentPath(task, object.Path) {
		return false, c.Fail(c.PolicyDenied, "deletion cannot escape task scope")
	}
	removed, err := fileguard.RemoveBound(a.root, filepath.FromSlash(object.Path), object.Digest, object.Size, 64<<20)
	if removed {
		a.workUsed = max(int64(0), a.workUsed-object.Size)
	}
	return removed, err
}
