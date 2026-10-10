//go:build windows

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/ipc"
	"golang.org/x/sys/windows"
)

func TestSupervisorShutdownRetriesActualWindowsPermissionWithoutClaimingAbsence(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lease, err := fileguard.Lock(root, "owner.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	server, err := ipc.OpenServer(context.Background(), directory, 1, func(context.Context, ipc.Request) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	info := server.Info
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	// Reproduce a real ACCESS_DENIED using a private fixture descriptor ACL.
	// A static DeleteFile pending inode is already ErrNoOwner on this Go/NTFS
	// profile; the full suite instead observed ACCESS_DENIED between its two
	// metadata reads. Neither error is injected into the production reader.
	raw, _ := c.CanonicalV1(info)
	if err = fileguard.Publish(root, "owner.json", raw); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(directory, "owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	restore, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	allowed, _, err := restore.DACL()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, allowed, nil)
	denied, err := windows.SecurityDescriptorFromString("D:P(D;;FR;;;" + user.User.Sid.String() + ")(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := denied.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = ipc.Discover(directory); !errors.Is(err, os.ErrPermission) {
		t.Fatal("actual permission denial not observed", err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- waitSupervisorStopped(ctx, directory, info.ID) }()
	select {
	case err := <-done:
		t.Fatal("permission denial was treated as completion or permanent failure", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, allowed, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatal("restored access to an existing descriptor was treated as absence", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = root.Remove("owner.json"); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal("shutdown did not observe actual absence and released lock", err)
	}
	if _, err = ipc.Discover(directory); !errors.Is(err, ipc.ErrNoOwner) {
		t.Fatal(err)
	}
}
