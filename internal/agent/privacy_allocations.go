package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sort"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
)

func safeManagedRelative(path string) bool { return policy.SafePath(path) }
func sameExcludedStore(a, b *privacyScope) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func sameManagedTasks(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Allocation precedes the first raw write, including SQLite snapshot generation.
// Interrupted publication is therefore still a managed privacy derivative.
func (s *Session) allocateManagedCopy(ctx context.Context, root *os.Root, kind string, tasks []string) error {
	if err := s.ensurePrivacy(ctx); err != nil {
		return err
	}
	if !fileguard.Disjoint(root.Name(), s.privacy.Directory) || !fileguard.Disjoint(root.Name(), s.directory) {
		return c.Fail(c.PolicyDenied, "managed copy cannot overlap its owner or deletion authority")
	}
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return err
	}
	tasks = append([]string{}, tasks...)
	sort.Strings(tasks)
	return s.registerManagedCopy(ctx, ManagedCopy{Kind: kind, Directory: root.Name(), PhysicalRoot: physical, Files: []BackupFile{}, Tasks: tasks})
}
func captureManagedAllocation(ctx context.Context, copy ManagedCopy) (ManagedCopy, error) {
	if len(copy.Files) != 0 {
		return copy, nil
	}
	root, err := os.OpenRoot(copy.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return copy, nil
	}
	if err != nil {
		return copy, err
	}
	defer root.Close()
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return copy, err
	}
	if physical != copy.PhysicalRoot {
		return copy, c.Fail(c.StaleBase, "managed allocation root replaced")
	}
	count := 0
	total := int64(0)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 100000 {
			return c.Fail(c.BudgetLimitReached, "managed allocation tree quota exceeded")
		}
		if entry.IsDir() {
			if copy.Kind == "EVALUATION_REPORT" && name == "owner" {
				if copy.ExcludedStore == nil {
					return c.Fail(c.StoreIntegrityError, "missing evaluator owner binding")
				}
				owned, openErr := os.OpenRoot(copy.ExcludedStore.Directory)
				if openErr != nil {
					return openErr
				}
				physical, identityErr := fileguard.DirectoryIdentity(owned)
				closeErr := owned.Close()
				if identityErr != nil || closeErr != nil {
					return errors.Join(identityErr, closeErr)
				}
				if physical != copy.ExcludedStore.PhysicalRoot {
					return c.Fail(c.StaleBase, "evaluator owner was replaced")
				}
				return fs.SkipDir
			}
			return nil
		}
		if !safeCopyPath(copy.Kind, name) {
			return c.Fail(c.StoreIntegrityError, "foreign file in managed allocation prevents purge")
		}
		raw, err := fileguard.ReadRegular(root, name, backupMaxBytes)
		if err != nil {
			return err
		}
		total += int64(len(raw))
		if len(copy.Files) >= backupMaxFiles+1 || total > backupMaxBytes {
			return c.Fail(c.BudgetLimitReached, "managed allocation content quota exceeded")
		}
		copy.Files = append(copy.Files, BackupFile{name, c.HashBytes(raw), int64(len(raw))})
		return nil
	})
	sort.Slice(copy.Files, func(i, j int) bool { return copy.Files[i].Path < copy.Files[j].Path })
	return copy, err
}
