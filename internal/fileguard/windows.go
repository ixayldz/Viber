//go:build windows

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

func Lock(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func lockFile(f *os.File) error {
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		return c.Fail(c.StoreOwned, "artifact store already owned")
	}
	return nil
}
func Private(root *os.Root) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	// No inherited access; only the calling user and local SYSTEM may access.
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(root.Name())
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
func SingleLink(f *os.File) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks == 0 {
		// A reader may still hold the old inode while its owner unlinks the
		// endpoint during shutdown. It is absent, not a hardlinked substitute.
		return os.ErrNotExist
	}
	if info.NumberOfLinks != 1 {
		return c.Fail(c.UnsupportedCapability, "hardlinked input is outside supported capture semantics")
	}
	return nil
}
func publishName(root *os.Root, old, new string) error {
	// MoveFileEx without REPLACE_EXISTING preserves existing published objects.
	from, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), old))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), new))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
func SyncDirectory(root *os.Root, name string) error {
	// Every publication uses the documented write-through move. This is not a
	// claim of power-loss conformance on arbitrary network/filesystem backends.
	return nil
}
