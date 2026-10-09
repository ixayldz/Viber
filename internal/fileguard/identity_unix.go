//go:build linux || darwin

package fileguard

import (
	"fmt"
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/unix"
	"os"
)

func DirectoryIdentity(root *os.Root) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	var info unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &info); err != nil {
		return "", err
	}
	return c.HashBytes([]byte(fmt.Sprintf("UNIX_DIRECTORY_V1:%d:%d", info.Dev, info.Ino))), nil
}
