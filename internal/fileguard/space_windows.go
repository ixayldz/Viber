//go:build windows

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/windows"
	"os"
	"strings"
)

func AvailableBytes(root *os.Root) (uint64, error) {
	f, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// VOLUME_NAME_GUID (1) binds the free-space query to the opened handle's
	// volume instead of the pathname, which may have been renamed or replaced.
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buffer[0], uint32(len(buffer)), 1)
	if err != nil {
		return 0, err
	}
	if n == 0 || n >= uint32(len(buffer)) {
		return 0, c.Fail(c.UnsupportedCapability, "bounded filesystem volume identity unavailable")
	}
	final := windows.UTF16ToString(buffer[:n])
	end := strings.Index(final, "}\\")
	if !strings.HasPrefix(final, `\\?\Volume{`) || end < 0 || end > 96 {
		return 0, c.Fail(c.UnsupportedCapability, "local filesystem volume identity unavailable")
	}
	path, err := windows.UTF16PtrFromString(final[:end+2])
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	err = windows.GetDiskFreeSpaceEx(path, &available, &total, &free)
	return available, err
}
