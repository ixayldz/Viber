package resource

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// FeatureUsage is a bounded observation of durable protocol records. A
// committed record still requires its protocol even after payload erasure.
// This observation does not lock unrelated owner admission for its lifetime.
type FeatureUsage struct {
	ReservationIntents    bool
	StageHandoffs         bool
	OwnerPartitions       bool
	RestorePartitions     bool
	PrivacyMetadata       bool
	AllocatorSelfOverhead bool
}

func (l *Ledger) FeatureUsage(ctx context.Context) (FeatureUsage, error) {
	var usage FeatureUsage
	err := l.withReadLock(ctx, func() error {
		tableExists := func(name string) (bool, error) {
			var exists bool
			if err := l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", name).Scan(&exists); err != nil {
				return false, err
			}
			return exists, nil
		}
		reservationIntents, err := tableExists("quota_reservation_intents")
		if err != nil {
			return err
		}
		if reservationIntents {
			if err = l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM quota_reservation_intents)").Scan(&usage.ReservationIntents); err != nil {
				return err
			}
		}
		handoffs, err := tableExists("quota_handoffs")
		if err != nil {
			return err
		}
		if handoffs {
			if err = l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM quota_handoffs)").Scan(&usage.StageHandoffs); err != nil {
				return err
			}
		}
		partitions, err := tableExists("quota_partitions")
		if err != nil {
			return err
		}
		if partitions {
			if err = l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM quota_partitions)").Scan(&usage.OwnerPartitions); err != nil {
				return err
			}
		}
		if handoffs && partitions {
			if err = l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM quota_partitions p JOIN quota_handoffs h ON h.allocation_id=p.allocation_id)").Scan(&usage.RestorePartitions); err != nil {
				return err
			}
		}
		metadataState, err := tableExists("quota_metadata_state")
		if err != nil {
			return err
		}
		metadataFiles, err := tableExists("quota_metadata_files")
		if err != nil {
			return err
		}
		if metadataState != metadataFiles {
			return c.Fail(c.StoreIntegrityError, "physical privacy metadata catalog is partial")
		}
		if metadataState {
			var version, imported int
			if err = l.db.QueryRowContext(ctx, "SELECT schema_version,imported FROM quota_metadata_state WHERE singleton=1").Scan(&version, &imported); err != nil {
				return err
			}
			if version != 1 || (imported != 0 && imported != 1) {
				return c.Fail(c.StoreIntegrityError, "physical privacy metadata feature marker is invalid")
			}
			// Creating the bound state row is itself a durable protocol change.
			// Keep advertising the feature while imported is zero: that state can
			// be left behind by a crash between the owner bootstrap reservation
			// and its inventory receipt, and an older runtime must not reopen it
			// while bypassing the new metadata/self-overhead rules.
			usage.PrivacyMetadata = true
			usage.AllocatorSelfOverhead = true
		}
		return nil
	})
	return usage, err
}

// PartitionFeatureInUse observes whether any durable owner partition exists.
// The query is a bounded SQLite EXISTS and intentionally does not hydrate the
// partition or member records; OpenPinned performs their full validation.
func (l *Ledger) PartitionFeatureInUse(ctx context.Context) (bool, error) {
	used := false
	err := l.withReadLock(ctx, func() error {
		var tableExists bool
		if err := l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='quota_partitions')").Scan(&tableExists); err != nil {
			return err
		}
		if !tableExists {
			return nil
		}
		return l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM quota_partitions)").Scan(&used)
	})
	return used, err
}

// NativeEnginePartitionFeatureInUse reports whether a durable OWNER_STATIC
// member receipt binds native-engine.json. It uses a bounded EXISTS query and
// leaves full row and receipt validation to OpenPinned.
func (l *Ledger) NativeEnginePartitionFeatureInUse(ctx context.Context) (bool, error) {
	used := false
	err := l.withReadLock(ctx, func() error {
		var tableExists bool
		if err := l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='quota_partition_members')").Scan(&tableExists); err != nil {
			return err
		}
		if !tableExists {
			return nil
		}
		return l.db.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM quota_partition_members AS member
			JOIN quota_allocations AS allocation ON allocation.id=member.member_id
			WHERE member.kind='OWNER_STATIC'
			AND instr(CAST(allocation.receipt AS TEXT), '"path":"native-engine.json"')>0
		)`).Scan(&used)
	})
	return used, err
}
