//go:build linux || darwin

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/unix"
	"os"
)

func Lock(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func lockFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return c.Fail(c.StoreOwned, "artifact store already owned")
	}
	return nil
}
func Private(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Uid != uint32(os.Geteuid()) {
		return c.Fail(c.PolicyDenied, "artifact directory belongs to another user")
	}
	return f.Chmod(0700)
}
func SingleLink(f *os.File) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Nlink == 0 {
		return os.ErrNotExist
	}
	if st.Nlink != 1 {
		return c.Fail(c.UnsupportedCapability, "hardlinked input is outside supported capture semantics")
	}
	return nil
}

func SyncDirectory(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
