package diskguard

import (
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"testing"
)

func TestPhysicalReserveLowWaterControlConsumptionAndReplenishment(t *testing.T) {
	path := t.TempDir()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.Admit(1024, false); err != nil {
		t.Fatal(err)
	}
	st, err := r.Status()
	if err != nil || st.RetainedReserveBytes != ReserveBytes || !st.WorkReady {
		t.Fatal(st, err)
	}
	// Simulate a full disk while retaining real allocated bytes. The available
	// counter rises only by the actual truncation of the reserve file.
	original := r.free
	controlWritten := int64(0)
	r.free = func(*os.Root) (uint64, error) {
		info, err := r.file.Stat()
		if err != nil {
			return 0, err
		}
		return uint64(ReserveBytes - info.Size() - controlWritten), nil
	}
	if err = r.Admit(4096, false); err == nil {
		t.Fatal("low disk admitted work")
	}
	if err = r.Admit(2<<20, true); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil || st.RetainedReserveBytes != ReserveBytes-(3<<20) {
		t.Fatal(st, err)
	}
	controlWritten = 2 << 20
	if err = r.Admit(ReserveBytes-1<<20, true); err == nil {
		t.Fatal("exhausted control reserve admitted oversized write")
	}
	r.free = original
	if err = r.Admit(4096, false); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil || st.RetainedReserveBytes != ReserveBytes {
		t.Fatal("work resumed without rearming", st, err)
	}
}
func TestReserveRejectsTamperingAndNeverUsesSparsePlaceholder(t *testing.T) {
	path := t.TempDir()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.Admit(1, false); err != nil {
		t.Fatal(err)
	}
	if err = r.file.Truncate(ReserveBytes + 1); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Status(); err == nil {
		t.Fatal("oversized reserve accepted")
	}
	if err = r.file.Truncate(ReserveBytes); err != nil {
		t.Fatal(err)
	}
	// A single-link rooted handle must remain the exact path.
	if err = os.Rename(filepath.Join(path, "control.reserve"), filepath.Join(path, "moved")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "control.reserve"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.Admit(1, false); err == nil {
		t.Fatal("replacement reserve used")
	}
}
func TestDiskUnknownCapacityFailsClosed(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.free = func(*os.Root) (uint64, error) { return 0, c.Fail(c.UnsupportedCapability, "unavailable") }
	for _, control := range []bool{false, true} {
		var typed *c.Error
		err = r.Admit(1, control)
		if !errors.As(err, &typed) || typed.Code != c.UnsupportedCapability {
			t.Fatal(err)
		}
	}
}
