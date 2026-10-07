//go:build linux

package fileguard

import (
	"golang.org/x/sys/unix"
	"os"
)

func publishName(root *os.Root, old, new string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return unix.Renameat2(int(directory.Fd()), old, int(directory.Fd()), new, unix.RENAME_NOREPLACE)
}
