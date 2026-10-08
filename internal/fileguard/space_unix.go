//go:build linux || darwin

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/unix"
	"os"
)

func AvailableBytes(root *os.Root) (uint64, error) {
	f, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var status unix.Statfs_t
	if err = unix.Fstatfs(int(f.Fd()), &status); err != nil {
		return 0, err
	}
	if status.Bsize <= 0 {
		return 0, c.Fail(c.UnsupportedCapability, "filesystem space profile unavailable")
	}
	size := uint64(status.Bsize)
	blocks := uint64(status.Bavail)
	if blocks > ^uint64(0)/size {
		return 0, c.Fail(c.UnsupportedCapability, "filesystem space counter overflow")
	}
	return blocks * size, nil
}
