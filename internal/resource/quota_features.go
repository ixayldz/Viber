package resource

import "context"

// FeatureUsage is a bounded observation of durable protocol records. A
// committed record still requires its protocol even after payload erasure.
// This observation does not lock unrelated owner admission for its lifetime.
type FeatureUsage struct {
	ReservationIntents bool
	StageHandoffs      bool
}

func (l *Ledger) FeatureUsage(ctx context.Context) (FeatureUsage, error) {
	var usage FeatureUsage
	err := l.withReadLock(ctx, func() error {
		return l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM quota_reservation_intents), EXISTS(SELECT 1 FROM quota_handoffs)").Scan(&usage.ReservationIntents, &usage.StageHandoffs)
	})
	return usage, err
}
