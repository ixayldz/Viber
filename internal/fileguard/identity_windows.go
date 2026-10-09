//go:build windows

package fileguard

import (
	"fmt"
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/windows"
	"os"
)

func DirectoryIdentity(root *os.Root) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return "", err
	}
	return c.HashBytes([]byte(fmt.Sprintf("WINDOWS_DIRECTORY_V1:%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow))), nil
}
