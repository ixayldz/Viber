//go:build darwin

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
	return unix.RenameatxNp(int(directory.Fd()), old, int(directory.Fd()), new, unix.RENAME_EXCL)
}
