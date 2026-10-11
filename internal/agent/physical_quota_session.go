package agent

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/resource"
	"github.com/ixayldz/Viber/internal/store"
)

// Attach quota before checkpoint/restore pin mutation when an authority is
// already retained. Fresh stores attach after authority initialization.
func (s *Session) attachPhysicalQuota(ctx context.Context, authority *PrivacyAuthority) error {
	if authority == nil || s.PhysicalQuota != nil {
		return nil
	}
	pin, err := s.Journal.PhysicalQuotaBinding(ctx)
	if err != nil {
		return err
	}
	expectedLedgerID := ""
	if pin != nil {
		if pin.AuthorityID != authority.ID || pin.Directory != authority.Directory || pin.PhysicalRoot != authority.PhysicalRoot {
			return c.Fail(c.StaleAuthority, "owner physical quota pin differs from current privacy authority")
		}
		expectedLedgerID = pin.LedgerID
	}
	ledger, err := resource.OpenPinned(resource.Authority{ID: authority.ID, Directory: authority.Directory, PhysicalRoot: authority.PhysicalRoot}, resource.DefaultLimits(), expectedLedgerID)
	if err != nil {
		return err
	}
	s.PhysicalQuota = ledger
	binding := store.PhysicalQuotaBinding{SchemaVersion: 1, AuthorityID: authority.ID, Directory: authority.Directory, PhysicalRoot: authority.PhysicalRoot, LedgerID: ledger.LedgerID}
	if err = s.Journal.BindPhysicalQuota(ctx, binding); err == nil {
		err = s.Journal.AttachPhysicalQuota(ledger)
	}
	if err == nil {
		err = s.Archive.BindPhysicalQuota(ledger)
	}
	return err
}
