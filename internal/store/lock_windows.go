//go:build windows

package store

import (
	"os"

	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/windows"
)

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		f.Close()
		return nil, c.Fail(c.StoreOwned, "another kernel owns this store")
	}
	return f, nil
}
