package fileguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestAvailableBytesUsesStoreFilesystem(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	available, err := AvailableBytes(root)
	root.Close()
	if err != nil || available == 0 {
		t.Fatal("filesystem space unavailable", available, err)
	}
	if _, err = AvailableBytes(root); err == nil {
		t.Fatal("closed root accepted")
	}
}

func TestAvailableBytesRemainsBoundToRenamedHandleWhenOldPathIsAFile(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "owned")
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = os.Rename(original, filepath.Join(parent, "moved")); err != nil {
		if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(32)) {
			t.Fatal(err)
		}
		available, spaceErr := AvailableBytes(root)
		if spaceErr != nil || available == 0 {
			t.Fatal("rename was fenced but owned volume query failed", available, spaceErr)
		}
		return
	}
	if err = os.WriteFile(original, []byte("foreign pathname"), 0600); err != nil {
		t.Fatal(err)
	}
	available, err := AvailableBytes(root)
	if err != nil || available == 0 {
		t.Fatal("space query followed the replaced textual root", available, err)
	}
	raw, err := os.ReadFile(original)
	if err != nil || string(raw) != "foreign pathname" {
		t.Fatal("space query touched replacement file", err)
	}
}
