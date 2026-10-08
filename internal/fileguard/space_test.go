package fileguard

import (
	"os"
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
