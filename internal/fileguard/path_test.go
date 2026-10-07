package fileguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDisjointResolvesExistingSymlinkAncestors(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Skip("host cannot create symlinks:", err)
	}
	if Disjoint(source, filepath.Join(alias, "new", "store")) {
		t.Fatal("alias bypassed overlap check")
	}
	if !Disjoint(source, filepath.Join(parent, "unrelated", "store")) {
		t.Fatal("disjoint path refused")
	}
}
func TestNoReplacePublicationPreservesExistingDestination(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = Publish(root, "target", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err = Publish(root, "target", []byte("replacement")); err == nil {
		t.Fatal("immutable destination replaced")
	}
	raw, err := ReadRegular(root, "target", 100)
	if err != nil || string(raw) != "original" {
		t.Fatal("published bytes changed", err)
	}
	if err = root.Mkdir("from", 0700); err != nil {
		t.Fatal(err)
	}
	if err = root.Mkdir("to", 0700); err != nil {
		t.Fatal(err)
	}
	if err = RenameNoReplace(root, "from", "to"); err == nil {
		t.Fatal("directory replaced")
	}
}
