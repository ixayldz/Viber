package store

import (
	"context"
	"errors"
	"fmt"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/resource"
)

func (s *Store) attachMutableSQLite(ledger *resource.Ledger) error {
	if ledger == nil {
		return c.Fail(c.InvalidArgument, "shared physical quota ledger required")
	}
	cleanup := func(err error) error {
		s.mutable = nil
		s.controlReserve = nil
		return errors.Join(err, s.disk.SetAdmissionHook(nil), s.disk.SetReserveReconcileHook(nil))
	}
	mutable, err := ledger.AttachMutableSQLite(context.Background(), s.directory)
	if err != nil {
		return cleanup(err)
	}
	s.mutable = mutable
	controlReserve, err := ledger.AttachControlReserve(context.Background(), s.directory)
	if err != nil {
		return cleanup(err)
	}
	s.controlReserve = controlReserve
	if s.mutable == nil && s.controlReserve == nil {
		if err = s.disk.SetReserveReconcileHook(nil); err != nil {
			return cleanup(err)
		}
		if err = s.disk.SetAdmissionHook(nil); err != nil {
			return cleanup(err)
		}
		return nil
	}
	if err = s.disk.SetAdmissionHook(func(bytes int64, control bool) error {
		ctx := s.physicalCtx
		if ctx == nil {
			ctx = context.Background()
		}
		digest := s.physicalDigest
		if !c.ValidDigest(digest) {
			digest = c.HashBytes([]byte(fmt.Sprintf("store-physical-admission:%d:%t", bytes, control)))
		}
		if !control && s.controlReserve != nil {
			if err := s.controlReserve.Prepare(ctx, digest); err != nil {
				return err
			}
		}
		if s.mutable == nil {
			return nil
		}
		upper := s.physicalUpper
		if upper == 0 {
			var upperErr error
			upper, upperErr = resource.MutableSQLiteUpper(uint64(bytes))
			if upperErr != nil {
				return upperErr
			}
		}
		if !c.ValidDigest(digest) {
			digest = c.HashBytes([]byte(fmt.Sprintf("store-mutable-admission:%d:%t", bytes, control)))
		}
		return mutable.Prepare(ctx, digest, upper)
	}); err != nil {
		return cleanup(err)
	}
	if s.controlReserve == nil {
		if err = s.disk.SetReserveReconcileHook(nil); err != nil {
			return cleanup(err)
		}
		return nil
	}
	if err = s.disk.SetReserveReconcileHook(func() error {
		ctx := s.physicalCtx
		if ctx == nil {
			ctx = context.Background()
		}
		return s.controlReserve.Refresh(ctx)
	}); err != nil {
		return cleanup(err)
	}
	return nil
}

// preparePinnedQuotaForOpen attaches the owner's existing ledger before SQLite
// switches journal mode or increments the generation. A first-time/unpinned
// store has no external quota history to reconcile and is imported later by
// Session.attachPhysicalQuota.
func (s *Store) preparePinnedQuotaForOpen(ctx context.Context) (*resource.Ledger, error) {
	binding, err := s.physicalQuotaBindingLocked(ctx)
	if err != nil || binding == nil {
		return nil, err
	}
	rawAuthority, err := s.privacyAuthorityLocked(ctx)
	if err != nil {
		return nil, err
	}
	if rawAuthority == nil {
		return nil, c.Fail(c.StaleAuthority, "owner physical quota pin lacks its privacy authority")
	}
	var authority physicalQuotaAuthority
	if c.DecodeStrict(rawAuthority, &authority) != nil || authority.SchemaVersion != 1 || authority.ID != binding.AuthorityID || authority.Directory != binding.Directory || authority.PhysicalRoot != binding.PhysicalRoot {
		return nil, c.Fail(c.StaleAuthority, "owner physical quota and privacy authority pins differ")
	}
	ledger, err := resource.OpenPinned(resource.Authority{ID: authority.ID, Directory: authority.Directory, PhysicalRoot: authority.PhysicalRoot}, resource.DefaultLimits(), binding.LedgerID)
	if err != nil {
		return nil, err
	}
	if err = s.attachMutableSQLite(ledger); err != nil {
		return nil, errors.Join(err, ledger.Close())
	}
	return ledger, nil
}
