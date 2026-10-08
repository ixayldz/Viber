//go:build windows

package fileguard

import (
	"golang.org/x/sys/windows"
	"os"
)

func AvailableBytes(root *os.Root) (uint64, error) {
	f, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	path, err := windows.UTF16PtrFromString(root.Name())
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	err = windows.GetDiskFreeSpaceEx(path, &available, &total, &free)
	return available, err
}
