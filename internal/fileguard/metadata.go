package fileguard

import (
	"errors"
	"os"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// LockExisting never creates a substitute lease. Validation and the lock refer
// to the same single-link inode in a trusted private owner directory.
func LockExisting(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, c.Fail(c.StoreIntegrityError, "lease must be a regular file")
	}
	f, err := root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err == nil && (!opened.Mode().IsRegular() || !os.SameFile(before, opened)) {
		err = c.Fail(c.StaleBase, "lease identity changed")
	}
	if err == nil {
		err = SingleLink(f)
	}
	if err == nil {
		err = lockFile(f)
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

// RegularPath validates a trusted metadata path before an external library
// opens it by name. The private owner directory, not this preflight, excludes
// hostile same-user replacement races.
func RegularPath(root *os.Root, name string, allowMissing bool) error {
	info, err := root.Lstat(name)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return c.Fail(c.UnsupportedCapability, "metadata path must be a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err == nil && (!opened.Mode().IsRegular() || !os.SameFile(info, opened)) {
		err = c.Fail(c.Conflict, "metadata path changed during validation")
	}
	if err == nil {
		err = SingleLink(f)
	}
	return errors.Join(err, f.Close())
}
