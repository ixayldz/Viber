//go:build windows

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

func AllocatedBytes(f *os.File) (int64, error) {
	var attr windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &attr); err != nil {
		return 0, err
	}
	if attr.FileAttributes&windows.FILE_ATTRIBUTE_SPARSE_FILE != 0 {
		return 0, c.Fail(c.UnsupportedCapability, "sparse file cannot supply a physical control reserve")
	}
	var info struct {
		AllocationSize int64
		EndOfFile      int64
		NumberOfLinks  uint32
		DeletePending  uint8
		Directory      uint8
		Padding        [2]byte
	}
	if err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileStandardInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, err
	}
	if info.AllocationSize < 0 || info.Directory != 0 || info.DeletePending != 0 {
		return 0, c.Fail(c.UnsupportedCapability, "physical reserve allocation unavailable")
	}
	return info.AllocationSize, nil
}
