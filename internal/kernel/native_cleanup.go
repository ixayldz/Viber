package kernel

import c "github.com/ixayldz/Viber/internal/contracts"

func nativeCleanupAllowed(s c.TaskState, p c.EventPayload, kind string, generation int64) bool {
	if s.Execution != c.Blocked && s.Execution != c.Paused && s.Execution != c.Recovering && s.Execution != c.WaitingResource && s.Execution != c.Terminated || s.Resources == nil || p.Tokens != nil || !c.ValidDigest(p.DocumentDigest) || !c.ValidDigest(p.Reason) || p.SnapshotDigest != "" || p.SpecVersion != 0 || p.PolicyEpoch != 0 || p.State != "" || p.Quality != "" || p.Fulfillment != "" || p.Outcome != "" {
		return false
	}
	var old *c.ResourceReservation
	for _, r := range s.Resources.Reservations {
		if r.Status != "SETTLED" {
			if old != nil || r.Status != "UNKNOWN" || r.Kind != "NATIVE_TOOL" {
				return false
			}
			value := r
			old = &value
		}
	}
	if old == nil || generation <= old.Generation {
		return false
	}
	if kind == "NativeCleanupObserved" {
		return p.Resources == nil
	}
	if kind != "NativeRiskReconciled" || p.Resources == nil || p.Resources.Action != "SETTLE" {
		return false
	}
	rr := p.Resources.Reservation
	expected := old.Upper
	expected.Children = 0
	return rr.ID == old.ID && rr.Status == "SETTLED" && rr.Kind == "NATIVE_TOOL" && rr.Meter == "KERNEL_FENCED_NATIVE_UPPER_BOUND" && rr.Used == expected && c.ValidDigest(rr.ReceiptDigest)
}
