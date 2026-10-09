package fileguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// RemoveBound is for private, quiescent managed copies. It grants no live source
// exclusivity. Missing files are accepted only by callers with a durable intent.
func RemoveBound(root *os.Root, name, digest string, size, limit int64) (bool, error) {
	if !c.ValidDigest(digest) || size < 0 || size > limit {
		return false, c.Fail(c.InvalidArgument, "bounded unlink binding required")
	}
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(name)
		for parent != "." {
			info, lookupErr := root.Lstat(parent)
			if lookupErr == nil {
				if !info.IsDir() {
					return false, c.Fail(c.Conflict, "absent file has a replaced parent namespace")
				}
				break
			}
			if !errors.Is(lookupErr, os.ErrNotExist) {
				return false, lookupErr
			}
			parent = filepath.Dir(parent)
		}
		return false, SyncParents(root, parent)
	}
	if err != nil {
		return false, err
	}
	raw, err := ReadRegular(root, name, limit)
	if err != nil {
		return false, err
	}
	if int64(len(raw)) != size || c.HashBytes(raw) != digest {
		return false, c.Fail(c.StaleBase, "managed content changed before purge")
	}
	current, err := root.Lstat(name)
	if err != nil {
		return false, err
	}
	if !os.SameFile(before, current) || !current.Mode().IsRegular() {
		return false, c.Fail(c.Conflict, "managed file identity changed")
	}
	if runtime.GOOS == "windows" && current.Mode().Perm()&0222 == 0 {
		if err = root.Chmod(name, 0600); err != nil {
			return false, err
		}
	}
	if err = root.Remove(name); err != nil {
		return false, err
	}
	return true, SyncParents(root, filepath.Dir(name))
}
