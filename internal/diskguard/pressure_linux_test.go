//go:build linux

package diskguard

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"golang.org/x/sys/unix"
)

func TestBoundedFilesystemPressureRefusesAnyOtherLocationBeforeAllocation(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "-test.run=^TestActualBoundedFilesystemPressurePreservesControlPublicationAndReopen$", "-test.count=1")
	child.Env = []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "VIBER_BOUNDED_DISK_PRESSURE=") {
			child.Env = append(child.Env, value)
		}
	}
	child.Env = append(child.Env, "VIBER_BOUNDED_DISK_PRESSURE=/tmp")
	raw, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 || !strings.Contains(string(raw), "only exact disposable /pressure mount accepted") {
		t.Fatal("unbounded location was not rejected before filesystem admission", string(raw), err)
	}
}

// This opt-in never fills a host filesystem. The caller must supply the exact
// disposable /pressure tmpfs with a measured total size of 128..256 MiB.
func TestActualBoundedFilesystemPressurePreservesControlPublicationAndReopen(t *testing.T) {
	path := os.Getenv("VIBER_BOUNDED_DISK_PRESSURE")
	if path == "" {
		t.Skip("bounded disposable tmpfs opt-in required")
	}
	if path != "/pressure" {
		t.Fatal("only exact disposable /pressure mount accepted")
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		t.Fatal(err)
	}
	if fs.Type != unix.TMPFS_MAGIC || fs.Bsize <= 0 || fs.Blocks < uint64(128<<20)/uint64(fs.Bsize) || fs.Blocks > uint64(256<<20)/uint64(fs.Bsize) {
		t.Fatal("bounded 128..256 MiB tmpfs required; refusing filesystem pressure", fs.Type, fs.Blocks, fs.Bsize)
	}
	directory, err := os.MkdirTemp(path, "viber-owned-pressure-")
	if err != nil {
		t.Fatal(err)
	}
	// Only the directory just created inside the validated disposable mount.
	defer os.RemoveAll(directory)
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	r, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { r.Close() }()
	if err = r.Admit(4096, false); err != nil {
		t.Fatal(err)
	}
	initial, err := r.Status()
	if err != nil || !initial.WorkReady || initial.RetainedReserveBytes != ReserveBytes || initial.AvailableBytes <= 1<<20 || initial.AvailableBytes > 256<<20 {
		t.Fatal(initial, err)
	}
	filler, err := root.OpenFile("owned-pressure.bin", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer filler.Close()
	if _, err = io.CopyN(filler, rand.Reader, int64(initial.AvailableBytes)-(64<<10)); err != nil {
		t.Fatal("bounded pressure allocation", err)
	}
	// The final write really fails with ENOSPC; no free-space callback override.
	_, err = io.CopyN(filler, rand.Reader, 1<<20)
	if !errors.Is(err, unix.ENOSPC) {
		t.Fatal("expected measured ENOSPC on bounded tmpfs", err)
	}
	if err = filler.Sync(); err != nil {
		t.Fatal(err)
	}
	fillerIdentity, err := filler.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if allocated, err := fileguard.AllocatedBytes(filler); err != nil || allocated < fillerIdentity.Size() {
		t.Fatal("pressure fixture must be physically allocated", allocated, err)
	}
	pressure, err := r.Status()
	if err != nil || pressure.AvailableBytes >= controlMargin || pressure.WorkReady {
		t.Fatal(pressure, err)
	}
	if err = r.Admit(4096, false); err == nil {
		t.Fatal("physical ENOSPC admitted ordinary work")
	}
	unchanged, err := r.Status()
	if err != nil || unchanged.RetainedReserveBytes != ReserveBytes {
		t.Fatal("denied work consumed control reserve", unchanged, err)
	}
	// Account for both temporary and final publication footprint before writing.
	control := bytes.Repeat([]byte("c"), 2<<20)
	if err = r.Admit(int64(len(control))*2, true); err != nil {
		t.Fatal("bounded control admission", err)
	}
	if err = fileguard.Publish(root, "last-durable-control.bin", control); err != nil {
		t.Fatal("control publication under actual pressure", err)
	}
	retained, err := r.Status()
	if err != nil || retained.RetainedReserveBytes >= ReserveBytes || retained.WorkReady {
		t.Fatal(retained, err)
	}
	if err = r.Admit(ReserveBytes, true); err == nil {
		t.Fatal("exhausted control reserve admitted an oversized next publication")
	}
	denied, err := r.Status()
	if err != nil || denied.RetainedReserveBytes != retained.RetainedReserveBytes {
		t.Fatal("denial consumed last reserve", denied, err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	r = reopened
	if raw, err := fileguard.ReadRegular(root, "last-durable-control.bin", int64(len(control))); err != nil || c.HashBytes(raw) != c.HashBytes(control) {
		t.Fatal("last durable control bytes lost on fresh open", err)
	}
	if err = r.Admit(4096, false); err == nil {
		t.Fatal("fresh open silently discarded physical pressure")
	}
	// The disposable fixture owns this exact inode; it is not user-file cleanup.
	current, err := root.Lstat("owned-pressure.bin")
	if err != nil || !os.SameFile(current, fillerIdentity) {
		t.Fatal("owned pressure inode changed", err)
	}
	if err = filler.Close(); err != nil {
		t.Fatal(err)
	}
	if err = root.Remove("owned-pressure.bin"); err != nil {
		t.Fatal(err)
	}
	if err = fileguard.SyncDirectory(root, "."); err != nil {
		t.Fatal(err)
	}
	if err = r.Admit(4096, false); err != nil {
		t.Fatal("work after pressure removal and physical rearm", err)
	}
	final, err := r.Status()
	if err != nil || !final.WorkReady || final.RetainedReserveBytes != ReserveBytes {
		t.Fatal(final, err)
	}
	if _, err = root.Stat("last-durable-control.bin"); err != nil {
		t.Fatal("reserve rearm removed durable control receipt", err)
	}
	t.Logf("BOUNDED_TMPFS_PRESSURE total_bytes=%d full_available_bytes=%d reserve_after_control=%d final_reserve_bytes=%d", fs.Blocks*uint64(fs.Bsize), pressure.AvailableBytes, retained.RetainedReserveBytes, final.RetainedReserveBytes)
}
