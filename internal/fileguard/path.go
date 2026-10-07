package fileguard

import (
	"errors"
	"os"
	"path/filepath"
)

// ResolveProspective resolves existing ancestors before appending missing
// components. A symlink alias cannot bypass source/store overlap checks.
// This is a preflight for trusted host directories, not an adversarial lease.
func ResolveProspective(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	suffix := []string{}
	current := absolute
	for {
		_, err = os.Lstat(current)
		if err == nil {
			canonical, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				canonical = filepath.Join(canonical, suffix[i])
			}
			return canonical, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

// RenameNoReplace publishes a prepared file or directory on the same volume.
func RenameNoReplace(root *os.Root, from, to string) error {
	if err := publishName(root, from, to); err != nil {
		return err
	}
	return SyncParents(root, filepath.Dir(to))
}
