package artifact

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"io/fs"
	"os"
)

func (a *Archive) BindDiskGuard(quota int64, admission func(int64, bool) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return os.ErrClosed
	}
	if a.admission != nil || quota < 1<<20 || quota > 1<<48 || admission == nil {
		return c.Fail(c.InvalidArgument, "one bounded archive disk guard required")
	}
	used := int64(0)
	count := 0
	err := fs.WalkDir(a.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > 1000000 {
			return c.Fail(c.BudgetLimitReached, "archive entry quota exceeded")
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return c.Fail(c.PolicyDenied, "nonregular entry in managed private archive")
		}
		used += info.Size()
		if used > 1<<48 {
			return c.Fail(c.StoreIntegrityError, "archive size overflow")
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.workQuota = quota
	a.workUsed = used
	a.admission = admission
	return nil
}
func (a *Archive) admit(bytes int64, control bool) error {
	if a.admission == nil {
		return nil
	}
	if !control && bytes > a.workQuota-a.workUsed {
		return c.Fail(c.BudgetLimitReached, "managed archive work disk quota reached; control records remain admitted")
	}
	return a.admission(bytes, control)
}
func (a *Archive) PutControlBytes(task string, raw []byte, control bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", os.ErrClosed
	}
	if !taskValid(task) || len(raw) > 8<<20 {
		return "", c.Fail(c.InvalidArgument, "invalid scoped control document")
	}
	a.controlWrite = control
	defer func() { a.controlWrite = false }()
	return a.putBlob(task, raw)
}

func (a *Archive) SetWorkDiskQuota(quota int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return os.ErrClosed
	}
	if a.admission == nil || quota < 1<<20 || quota > 1<<48 {
		return c.Fail(c.InvalidArgument, "invalid work disk quota")
	}
	if a.workUsed > quota {
		return c.Fail(c.BudgetLimitReached, "existing private archive exceeds requested work quota")
	}
	a.workQuota = quota
	return nil
}
