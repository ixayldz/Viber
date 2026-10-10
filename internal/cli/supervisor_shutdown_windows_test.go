//go:build windows

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/ipc"
	"golang.org/x/sys/windows"
)

func TestSupervisorShutdownRetriesActualWindowsPermissionWithoutClaimingAbsence(t *testing.T) {
	disableFixtureBackupPrivilege(t)
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
	// First prove the DACL independently of discovery. Elevated CI runners may
	// have SeBackupPrivilege enabled; Go's rooted opens request backup intent.
	probe, probeErr := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if probeErr == nil {
		windows.CloseHandle(probe)
	}
	if !errors.Is(probeErr, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("independent native read did not prove ACCESS_DENIED", probeErr)
	}
	if _, err = os.ReadFile(filepath.Join(directory, "owner.json")); !errors.Is(err, os.ErrPermission) {
		t.Fatal("independent Go read did not prove permission denial", err)
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

// Only reduce a privilege already present in this test process, and restore
// its original attributes afterwards. No account, host policy or other
// process token is modified. This test must remain non-parallel.
func disableFixtureBackupPrivilege(t *testing.T) {
	t.Helper()
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES, &token); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { token.Close() })
	name, err := windows.UTF16PtrFromString("SeBackupPrivilege")
	if err != nil {
		t.Fatal(err)
	}
	var luid windows.LUID
	if err = windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		t.Fatal(err)
	}
	read := func() (uint32, bool) {
		var size uint32
		err := windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size)
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || size < uint32(unsafe.Sizeof(windows.Tokenprivileges{})) || size > 64<<10 {
			t.Fatal("bounded token privilege query", err, size)
		}
		buffer := make([]byte, size)
		if err = windows.GetTokenInformation(token, windows.TokenPrivileges, &buffer[0], size, &size); err != nil {
			t.Fatal(err)
		}
		privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0]))
		for _, privilege := range privileges.AllPrivileges() {
			if privilege.Luid == luid {
				return privilege.Attributes, true
			}
		}
		return 0, false
	}
	attributes, present := read()
	enabled := attributes&windows.SE_PRIVILEGE_ENABLED != 0
	t.Logf("fixture backup privilege present=%t initially_enabled=%t", present, enabled)
	if enabled {
		next := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: attributes &^ windows.SE_PRIVILEGE_ENABLED}}}
		if err = windows.AdjustTokenPrivileges(token, false, &next, 0, nil, nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			next.Privileges[0].Attributes = attributes
			if err := windows.AdjustTokenPrivileges(token, false, &next, 0, nil, nil); err != nil {
				t.Error("restore original fixture token privilege", err)
			}
			actual, exists := read()
			if !exists || actual != attributes {
				t.Error("original fixture privilege attributes were not restored")
			}
		})
	}
	actual, _ := read()
	if actual&windows.SE_PRIVILEGE_ENABLED != 0 {
		t.Fatal("fixture backup privilege still enabled")
	}
}
