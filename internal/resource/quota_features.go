package resource

import "context"

// FeatureUsage is a bounded observation of durable protocol records. A
// committed record still requires its protocol even after payload erasure.
// This observation does not lock unrelated owner admission for its lifetime.
type FeatureUsage struct {
	ReservationIntents bool
	StageHandoffs      bool
	OwnerPartitions    bool
}

func (l *Ledger) FeatureUsage(ctx context.Context) (FeatureUsage, error) {
	var usage FeatureUsage
	err := l.withReadLock(ctx, func() error {
		return l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM quota_reservation_intents), EXISTS(SELECT 1 FROM quota_handoffs), EXISTS(SELECT 1 FROM quota_partitions)").Scan(&usage.ReservationIntents, &usage.StageHandoffs, &usage.OwnerPartitions)
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
