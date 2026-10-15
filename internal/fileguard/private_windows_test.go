//go:build windows

package fileguard

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateRechecksLiveProtectedDACLAndRepairsChangedAccess(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = Private(root); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(root.Name())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	read := func() *windows.SECURITY_DESCRIPTOR {
		t.Helper()
		sd, readErr := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return sd
	}
	if !privateDACLMatches(read(), wanted) {
		t.Fatal("first private ACL does not match its protected owner/SYSTEM policy")
	}
	if err = Private(root); err != nil || !privateDACLMatches(read(), wanted) {
		t.Fatal("already-private directory did not preserve its exact live ACL", err)
	}
	broad, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	broadDACL, _, err := broad.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, broadDACL, nil); err != nil {
		t.Fatal(err)
	}
	if privateDACLMatches(read(), wanted) {
		t.Fatal("extra Everyone ACE was accepted as private")
	}
	if err = Private(root); err != nil || !privateDACLMatches(read(), wanted) {
		t.Fatal("changed live ACL was not repaired", err)
	}
	unprotected, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	if privateDACLMatches(unprotected, wanted) {
		t.Fatal("unprotected inheritance was accepted as a private descriptor")
	}
	nullDACL, err := windows.NewSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	if err = nullDACL.SetDACL(nil, true, false); err != nil {
		t.Fatal(err)
	}
	if err = nullDACL.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		t.Fatal(err)
	}
	if privateDACLMatches(nullDACL, wanted) {
		t.Fatal("permissive null DACL was accepted as a private descriptor")
	}
	if err = wanted.SetControl(windows.SE_DACL_AUTO_INHERIT_REQ, windows.SE_DACL_AUTO_INHERIT_REQ); err != nil {
		t.Fatal(err)
	}
	if privateDACLMatches(wanted, wanted) {
		t.Fatal("pending auto-inheritance request used the private ACL fast path")
	}
	if privateDACLMatches(nil, wanted) {
		t.Fatal("missing live descriptor was accepted")
	}
}
