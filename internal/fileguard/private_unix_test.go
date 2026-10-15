//go:build linux || darwin

package fileguard

import (
	"os"
	"testing"
)

func TestCheckPrivateDoesNotRepairBroadPermissions(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = Private(root); err != nil {
		t.Fatal(err)
	}
	if err = CheckPrivate(root); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err = CheckPrivate(root); err == nil {
		t.Fatal("read-only inspection accepted broad permissions")
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("read-only inspection changed permissions", info, err)
	}
}
