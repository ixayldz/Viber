//go:build windows

package auth

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const credentialProtection = "WINDOWS_USER_DPAPI_AND_PRIVATE_ACL"

func crypt(raw []byte, encrypt bool) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return nil, c.Fail(c.StoreIntegrityError, "invalid credential envelope")
	}
	input := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var output windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
func protect(raw []byte) ([]byte, error)   { return crypt(raw, true) }
func unprotect(raw []byte) ([]byte, error) { return crypt(raw, false) }
func replaceCredential(root *os.Root, from, to string) error {
	old, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), from))
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), to))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(old, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func primeMigrationVault(data state) error { return nil }
