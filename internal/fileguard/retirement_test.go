package fileguard

import (
	"errors"
	"os"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestExistingLeaseAndRetirementNeverAdoptMissingOrReplacementIdentity(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err = LockExisting(root, "missing.lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err = root.Lstat("missing.lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock probe created missing lease", err)
	}
	raw := []byte("owned-operation")
	if err = Publish(root, "lease.lock", raw); err != nil {
		t.Fatal(err)
	}
	lease, err := LockExisting(root, "lease.lock")
	if err != nil {
		t.Fatal(err)
	}
	if second, err := LockExisting(root, "lease.lock"); err == nil {
		second.Close()
		t.Fatal("two leases acquired same inode")
	}
	identity, err := lease.Stat()
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	// Keep the original inode allocated so a filesystem cannot reuse its ID.
	if err = root.Rename("lease.lock", "original.lock"); err != nil {
		t.Fatal(err)
	}
	if err = Publish(root, "lease.lock", raw); err != nil {
		t.Fatal(err)
	}
	if _, err = RemoveBoundIdentity(root, "lease.lock", identity, c.HashBytes(raw), int64(len(raw)), 4096); err == nil {
		t.Fatal("identical replacement adopted for retirement")
	}
	if current, err := ReadRegular(root, "lease.lock", 4096); err != nil || string(current) != string(raw) {
		t.Fatal("foreign replacement removed", err)
	}
	removed, err := RemoveBoundIdentity(root, "original.lock", identity, c.HashBytes(raw), int64(len(raw)), 4096)
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
}

func TestUnlinkedOpenMetadataIsAbsentWhileHardlinkRemainsDenied(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = Publish(root, "owner.json", []byte("owned")); err != nil {
		t.Fatal(err)
	}
	f, err := root.Open("owner.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = os.Link(root.Name()+string(os.PathSeparator)+"owner.json", root.Name()+string(os.PathSeparator)+"foreign.json"); err != nil {
		t.Fatal(err)
	}
	if err = SingleLink(f); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatal("real hardlink lost denial", err)
	}
	if err = root.Remove("foreign.json"); err != nil {
		t.Fatal(err)
	}
	if err = SingleLink(f); err != nil {
		t.Fatal("single-link metadata denied", err)
	}
	if err = root.Remove("owner.json"); err != nil {
		t.Fatal("native unlink with held reader", err)
	}
	if err = SingleLink(f); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unlinked held inode classified as hardlink", err)
	}
	if err = Publish(root, "owner.json", []byte("new-owner")); err != nil {
		t.Fatal(err)
	}
	if err = SingleLink(f); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old inode adopted replacement name", err)
	}
	if raw, err := ReadRegular(root, "owner.json", 4096); err != nil || string(raw) != "new-owner" {
		t.Fatal("fresh owner unavailable", err)
	}
}
