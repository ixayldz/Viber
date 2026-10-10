package diskguard

import (
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
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

func TestRootedReserveOpeningNeverAdoptsReplacementDirectory(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "owned")
	moved := filepath.Join(parent, "moved")
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = os.Rename(original, moved); err != nil {
		if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(32)) {
			t.Fatal(err)
		}
		reserve, openErr := OpenRoot(root)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer reserve.Close()
		if openErr = reserve.Admit(4096, false); openErr != nil {
			t.Fatal(openErr)
		}
		status, openErr := reserve.Status()
		if openErr != nil || status.RetainedReserveBytes != ReserveBytes {
			t.Fatal("root rename fenced but rooted reserve unavailable", status, openErr)
		}
		return
	}
	if err = os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(original, "control.reserve")
	if err = os.WriteFile(foreign, []byte("foreign reserve must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	reserve, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reserve.Close()
	if err = reserve.Admit(4096, false); err != nil {
		t.Fatal(err)
	}
	status, err := reserve.Status()
	if err != nil || status.RetainedReserveBytes != ReserveBytes {
		t.Fatal(status, err)
	}
	raw, err := os.ReadFile(foreign)
	if err != nil || string(raw) != "foreign reserve must survive" {
		t.Fatal("root pathname replacement redirected reserve write", err)
	}
	owned, err := os.Stat(filepath.Join(moved, "control.reserve"))
	if err != nil || owned.Size() != ReserveBytes {
		t.Fatal("pinned owned reserve not allocated", err)
	}
}
