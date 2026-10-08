package fileguard

import (
	"errors"
	"os"

	c "github.com/ixayldz/Viber/internal/contracts"
)

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
