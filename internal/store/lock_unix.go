//go:build linux || darwin

package store

import (
	"os"

	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/unix"
)

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, c.Fail(c.StoreOwned, "another kernel owns this store")
	}
	return f, nil
}
