package resource

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// ValidateFreePending checks that a FREE_PENDING allocation still contains
// exactly the saved retained inventory, with at most its one persisted
// free-intent member absent. It is read-only: it neither resolves the intent
// nor changes any retained charge.
func (a Allocation) ValidateFreePending(ctx context.Context, current Inventory) error {
	if a.ledger == nil || !validID(a.ID) || a.targetRoot == "" {
		return c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	measured, err := validateInventory(current)
	if err != nil {
		return err
	}
	return a.ledger.withReadLock(ctx, func() error {
		tx, err := a.ledger.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var allocation pendingAllocation
		err = tx.QueryRowContext(ctx, `SELECT id,target_root,volume_identity,status,retained_bytes,retained_entries,receipt,free_before
			FROM quota_allocations WHERE id=?`, a.ID).Scan(&allocation.ID, &allocation.Target, &allocation.Volume, &allocation.Status, &allocation.RetainedBytes, &allocation.RetainedEntries, &allocation.Receipt, &allocation.Before)
		if err != nil {
			return err
		}
		if allocation.Status != "FREE_PENDING" || filepath.Clean(allocation.Target) != filepath.Clean(a.targetRoot) {
			return c.Fail(c.StaleAuthority, "physical quota allocation is not the expected pending free")
		}
		intent, found, err := readFreeIntentForValidation(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if !found {
			return c.Fail(c.StoreIntegrityError, "pending physical quota allocation lacks its exact free intent")
		}
		saved, err := validatePendingIntent(allocation, intent)
		if err != nil {
			return err
		}
		if filepath.Clean(measured.RootPath) != filepath.Clean(saved.RootPath) || measured.RootIdentity != saved.RootIdentity || measured.VolumeIdentity != saved.VolumeIdentity {
			return c.Fail(c.StaleAuthority, "pending physical quota inventory root changed")
		}
		if samePhysicalInventory(measured, saved) {
			return tx.Commit()
		}
		without, err := pendingInventoryWithoutIntentMember(saved, intent)
		if err != nil {
			return err
		}
		if samePhysicalInventory(measured, without) {
			return tx.Commit()
		}
		return c.Fail(c.StoreIntegrityError, "pending physical quota inventory differs beyond its exact free-intent member")
	})
}

type quotaPendingQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readFreeIntentForValidation(ctx context.Context, db quotaPendingQueryRower, id string) (freeIntent, bool, error) {
	var intent freeIntent
	var logical, allocated, charged, before int64
	err := db.QueryRowContext(ctx, `SELECT path,file_digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,free_before,volume_identity,root_identity
		FROM quota_free_intents WHERE allocation_id=?`, id).Scan(&intent.Path, &intent.Digest, &intent.Identity, &logical, &allocated, &charged, &before, &intent.Volume, &intent.Root)
	if errors.Is(err, sql.ErrNoRows) {
		return intent, false, nil
	}
	if err != nil {
		return intent, false, err
	}
	if logical < 0 || allocated < 0 || charged < 0 || before < 0 {
		return intent, false, c.Fail(c.StoreIntegrityError, "negative physical free-intent counter")
	}
	intent.Logical, intent.Allocated, intent.Charged, intent.Before = uint64(logical), uint64(allocated), uint64(charged), uint64(before)
	if !validRelativeReceiptPath(intent.Path) || !c.ValidDigest(intent.Digest) || !c.ValidDigest(intent.Identity) || !c.ValidDigest(intent.Volume) || !c.ValidDigest(intent.Root) || intent.Charged < intent.Logical || intent.Charged < intent.Allocated {
		return intent, false, c.Fail(c.StoreIntegrityError, "invalid physical free-intent receipt")
	}
	return intent, true, nil
}

func pendingInventoryWithoutIntentMember(saved Inventory, intent freeIntent) (Inventory, error) {
	if saved.Entries == 0 || len(saved.Files) == 0 || saved.AllocatedBytes < intent.Allocated || saved.LogicalBytes < intent.Logical || saved.ChargedBytes < intent.Charged {
		return Inventory{}, c.Fail(c.StoreIntegrityError, "pending free-intent counters exceed retained inventory")
	}
	expected := saved
	expected.Entries--
	expected.AllocatedBytes -= intent.Allocated
	expected.LogicalBytes -= intent.Logical
	expected.ChargedBytes -= intent.Charged
	expected.Files = make([]FileReceipt, 0, len(saved.Files)-1)
	removed := 0
	for _, file := range saved.Files {
		if file.Path == intent.Path {
			if !sameReceipt(file, receiptFromIntent(intent)) || removed != 0 {
				return Inventory{}, c.Fail(c.StoreIntegrityError, "pending free-intent member differs from its retained receipt")
			}
			removed++
			continue
		}
		expected.Files = append(expected.Files, file)
	}
	if removed != 1 {
		return Inventory{}, c.Fail(c.StoreIntegrityError, "pending free-intent member is not unique in retained inventory")
	}
	if _, err := validateInventory(expected); err != nil {
		return Inventory{}, err
	}
	return expected, nil
}

func samePhysicalInventory(left, right Inventory) bool {
	if filepath.Clean(left.RootPath) != filepath.Clean(right.RootPath) || left.RootIdentity != right.RootIdentity || left.VolumeIdentity != right.VolumeIdentity || left.AllocatedBytes != right.AllocatedBytes || left.LogicalBytes != right.LogicalBytes || left.ChargedBytes != right.ChargedBytes || left.Entries != right.Entries || len(left.Files) != len(right.Files) {
		return false
	}
	for i := range left.Files {
		if !sameReceipt(left.Files[i], right.Files[i]) {
			return false
		}
	}
	return true
}
