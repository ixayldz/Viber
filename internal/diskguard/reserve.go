// Package diskguard keeps physically allocated, private emergency space.
// It admits work above a low watermark and releases reserve only for bounded
// control writes. It does not promise filesystem-wide power-loss conformance.
package diskguard

import (
	"crypto/rand"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"io"
	"os"
	"sync"
)

const ReserveBytes int64 = 32 << 20
const LowWaterBytes uint64 = 64 << 20
const controlMargin uint64 = 1 << 20

type Status struct {
	SchemaVersion        int    `json:"schema_version"`
	AvailableBytes       uint64 `json:"available_bytes"`
	RetainedReserveBytes int64  `json:"physical_reserve_bytes"`
	TargetReserveBytes   int64  `json:"target_reserve_bytes"`
	LowWaterBytes        uint64 `json:"low_water_bytes"`
	WorkReady            bool   `json:"work_ready"`
}
type Reserve struct {
	mu   sync.Mutex
	root *os.Root
	file *os.File
	free func(*os.Root) (uint64, error)
}

func Open(directory string) (*Reserve, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	if err = fileguard.RegularPath(root, "control.reserve", true); err != nil {
		root.Close()
		return nil, err
	}
	f, err := root.OpenFile("control.reserve", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		return nil, err
	}
	r := &Reserve{root: root, file: f, free: fileguard.AvailableBytes}
	if _, err = r.retained(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}
func (r *Reserve) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := errors.Join(r.file.Close(), r.root.Close())
	r.file = nil
	return err
}
func (r *Reserve) retained() (int64, error) {
	if r.file == nil {
		return 0, os.ErrClosed
	}
	path, err := r.root.Lstat("control.reserve")
	if err != nil {
		return 0, err
	}
	st, err := r.file.Stat()
	if err != nil {
		return 0, err
	}
	if !st.Mode().IsRegular() || !os.SameFile(path, st) || st.Size() < 0 || st.Size() > ReserveBytes {
		return 0, c.Fail(c.StoreIntegrityError, "physical reserve replaced or invalid")
	}
	if err = fileguard.SingleLink(r.file); err != nil {
		return 0, err
	}
	allocated, err := fileguard.AllocatedBytes(r.file)
	if err != nil {
		return 0, err
	}
	if allocated < st.Size() {
		return 0, c.Fail(c.UnsupportedCapability, "control reserve is not physically allocated")
	}
	return st.Size(), nil
}
func (r *Reserve) status() (Status, error) {
	size, err := r.retained()
	if err != nil {
		return Status{}, err
	}
	free, err := r.free(r.root)
	if err != nil {
		return Status{}, err
	}
	return Status{1, free, size, ReserveBytes, LowWaterBytes, size == ReserveBytes && free >= LowWaterBytes}, nil
}
func (r *Reserve) Status() (Status, error) { r.mu.Lock(); defer r.mu.Unlock(); return r.status() }
func (r *Reserve) Admit(bytes int64, control bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if bytes < 0 || bytes > 64<<20 {
		return c.Fail(c.InvalidArgument, "disk admission size outside bounded profile")
	}
	current, err := r.status()
	if err != nil {
		return err
	}
	needed := uint64(bytes)
	if !control {
		missing := ReserveBytes - current.RetainedReserveBytes
		if current.AvailableBytes < LowWaterBytes+needed+uint64(missing) {
			return c.Fail(c.BudgetLimitReached, "disk low watermark; work denied and control reserve retained")
		}
		if missing > 0 {
			if _, err = r.file.Seek(current.RetainedReserveBytes, io.SeekStart); err != nil {
				return err
			}
			// Random bytes prevent a compressed zero/sparse placeholder from masquerading
			// as physical capacity. Sync and query actual allocated blocks before work.
			if _, err = io.CopyN(r.file, rand.Reader, missing); err != nil {
				return err
			}
			if err = r.file.Sync(); err != nil {
				return err
			}
			size, err := r.retained()
			if err != nil {
				return err
			}
			if size != ReserveBytes {
				return c.Fail(c.StoreIntegrityError, "reserve replenishment incomplete")
			}
			if err = fileguard.SyncDirectory(r.root, "."); err != nil {
				return err
			}
		}
		return nil
	}
	if needed+controlMargin <= current.AvailableBytes {
		return nil
	}
	release := needed + controlMargin - current.AvailableBytes
	if release > uint64(current.RetainedReserveBytes) {
		return c.Fail(c.BudgetLimitReached, "bounded control reserve exhausted; retain last durable checkpoint")
	}
	newSize := current.RetainedReserveBytes - int64(release)
	if err = r.file.Truncate(newSize); err != nil {
		return err
	}
	if err = r.file.Sync(); err != nil {
		return err
	}
	available, err := r.free(r.root)
	if err != nil {
		return err
	}
	if available < needed {
		return c.Fail(c.BudgetLimitReached, "control space unavailable after reserve release")
	}
	return nil
}
