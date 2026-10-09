package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func TestNativeFenceReconciliationCannotForgeQualityReduceChargeOrCrossGeneration(t *testing.T) {
	old := c.ResourceReservation{ID: "native-operation", Kind: "NATIVE_TOOL", CallDigest: c.HashBytes([]byte("call")), RequestDigest: c.HashBytes([]byte("request")), ProfileDigest: c.HashBytes([]byte("profile")), SpecVersion: 1, PolicyEpoch: 1, Generation: 1, Upper: c.ResourceVector{CPUMillis: 1000, DiskBytes: 8 << 20, WallMillis: 1000, Children: 1}, Status: "UNKNOWN"}
	base := c.TaskState{SchemaVersion: 1, TaskID: "task", SpecVersion: 1, PolicyEpoch: 1, KernelGeneration: 1, TaskSeq: 10, StoreSeq: 10, Execution: c.Blocked, Quality: c.Unverified, Fulfillment: c.FulfillmentPending, Resources: &c.ResourceAccount{SchemaVersion: 1, Policy: c.DefaultResourcePolicy(), Reserved: old.Upper, Reservations: []c.ResourceReservation{old}}}
	settled := old
	settled.Status = "SETTLED"
	settled.Meter = "KERNEL_FENCED_NATIVE_UPPER_BOUND"
	settled.Used = old.Upper
	settled.Used.Children = 0
	settled.ReceiptDigest = c.HashBytes([]byte("fence-proof"))
	for _, mode := range []string{"valid", "terminal", "input-barrier-cleanup", "incomplete", "same-generation", "old-publisher", "kernel-manufactured", "model", "zero-charge", "child-risk", "altered-request", "grant-quality", "grant-candidate", "active", "missing-receipt"} {
		t.Run(mode, func(t *testing.T) {
			state := base
			account := *base.Resources
			account.Reservations = append([]c.ResourceReservation{}, base.Resources.Reservations...)
			state.Resources = &account
			event := c.Event{SchemaVersion: 1, ID: "cleanup", TaskID: "task", TaskSeq: 11, StoreSeq: 11, KernelGeneration: 2, Actor: "user", Type: "NativeRiskReconciled"}
			payload := c.EventPayload{DocumentDigest: c.HashBytes([]byte("doc")), Reason: c.HashBytes([]byte("command")), Resources: &c.ResourceMutation{Action: "SETTLE", Reservation: settled}}
			valid := mode == "valid" || mode == "terminal" || mode == "input-barrier-cleanup" || mode == "incomplete"
			switch mode {
			case "terminal":
				state.Execution = c.Terminated
				state.Outcome = c.Cancelled
			case "input-barrier-cleanup":
				state.PendingInputIDs = []string{"user-input"}
				state.InputBarrier = true
			case "incomplete":
				event.Type = "NativeCleanupObserved"
				payload.Resources = nil
			case "same-generation":
				event.KernelGeneration = 1
			case "old-publisher":
				state.KernelGeneration = 3
			case "kernel-manufactured":
				event.Type = "SessionRecorded"
				event.Actor = "kernel"
				event.KernelGeneration = 1
			case "model":
				event.Actor = "model"
			case "zero-charge":
				payload.Resources.Reservation.Used = c.ResourceVector{}
			case "child-risk":
				payload.Resources.Reservation.Used.Children = 1
			case "altered-request":
				payload.Resources.Reservation.RequestDigest = c.HashBytes([]byte("changed"))
			case "grant-quality":
				payload.Quality = c.Verified
			case "grant-candidate":
				payload.SnapshotDigest = c.HashBytes([]byte("changed"))
			case "active":
				state.Execution = c.Running
			case "missing-receipt":
				payload.Resources.Reservation.ReceiptDigest = ""
			}
			next, err := Reduce(&state, event, payload)
			if valid {
				if err != nil || next.Quality != state.Quality || next.Execution != state.Execution || next.InputBarrier != state.InputBarrier {
					t.Fatal("safe disposal changed task authority", next, err)
				}
				if mode == "incomplete" && next.Resources.Reservations[0].Status != "UNKNOWN" {
					t.Fatal("incomplete risk released")
				}
			} else if err == nil {
				t.Fatal("unsafe cleanup accepted", mode)
			}
			if state.Resources.Reservations[0].Status != "UNKNOWN" {
				t.Fatal("reducer changed input")
			}
		})
	}
}
