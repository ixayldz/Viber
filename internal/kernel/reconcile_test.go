package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func TestModelRiskOperatorAuthorityAndExposureCannotBeForged(t *testing.T) {
	upper := c.TokenLimits{Input: 4096, Output: 512}
	request, profile := c.HashBytes([]byte("request")), c.HashBytes([]byte("profile"))
	tr := c.TokenReservation{ID: "op", RequestDigest: request, ProfileDigest: profile, SpecVersion: 1, PolicyEpoch: 1, Generation: 1, Upper: upper, Status: "UNKNOWN"}
	rr := c.ResourceReservation{ID: "op", Kind: "MODEL", Provider: "fixture", Model: "offline-fixture-v1", TokenUpper: upper, RequestDigest: request, ProfileDigest: profile, SpecVersion: 1, PolicyEpoch: 1, Generation: 1, Upper: c.ResourceVector{CPUMillis: 1000, DiskBytes: 8 << 20, WallMillis: 1000}, Status: "UNKNOWN"}
	state := c.TaskState{SchemaVersion: 1, TaskID: "task", SpecVersion: 1, PolicyEpoch: 1, KernelGeneration: 1, TaskSeq: 10, StoreSeq: 10, Execution: c.Blocked, Quality: c.Unverified, Fulfillment: c.FulfillmentPending, Tokens: &c.TokenAccount{SchemaVersion: 1, Limits: c.DefaultTokenLimits(), Reserved: upper, Reservations: []c.TokenReservation{tr}}, Resources: &c.ResourceAccount{SchemaVersion: 1, Policy: c.DefaultResourcePolicy(), Reserved: rr.Upper, Reservations: []c.ResourceReservation{rr}}}
	tr.Status = "SETTLED"
	tr.Used = upper
	tr.UsageSource = "OPERATOR_ASSUMED_UPPER_BOUND"
	tr.ResponseDigest = c.HashBytes([]byte("operator-proof"))
	rr.Status = "SETTLED"
	rr.Used = rr.Upper
	rr.TokenUsed = upper
	rr.Meter = "OPERATOR_ASSUMED_UPPER_BOUND"
	rr.ReceiptDigest = c.HashBytes([]byte("resource-proof"))
	for _, kind := range []string{"valid", "kernel-manufactured", "model", "zero", "missing-resource", "wrong-lineage", "grant-quality", "input-barrier", "old-generation", "native"} {
		t.Run(kind, func(t *testing.T) {
			s := state
			ownedTokens := *state.Tokens
			ownedTokens.Reservations = append([]c.TokenReservation{}, state.Tokens.Reservations...)
			s.Tokens = &ownedTokens
			ownedResources := *state.Resources
			ownedResources.Reservations = append([]c.ResourceReservation{}, state.Resources.Reservations...)
			s.Resources = &ownedResources
			e := c.Event{SchemaVersion: 1, ID: "risk", TaskID: "task", TaskSeq: 11, StoreSeq: 11, KernelGeneration: 2, Actor: "user", Type: "ModelRiskReconciled"}
			p := c.EventPayload{DocumentDigest: c.HashBytes([]byte("doc")), Reason: c.HashBytes([]byte("command")), Tokens: &c.TokenMutation{Action: "SETTLE", Reservation: tr}, Resources: &c.ResourceMutation{Action: "SETTLE", Reservation: rr}}
			switch kind {
			case "kernel-manufactured":
				e.Actor = "kernel"
				e.Type = "SessionRecorded"
				e.KernelGeneration = 1
			case "model":
				e.Actor = "model"
			case "zero":
				p.Tokens.Reservation.Used = c.TokenLimits{}
			case "missing-resource":
				p.Resources = nil
			case "wrong-lineage":
				p.Tokens.Reservation.RequestDigest = c.HashBytes([]byte("other"))
			case "grant-quality":
				p.Quality = c.Verified
			case "input-barrier":
				s.InputBarrier = true
				s.PendingInputIDs = []string{"pending"}
			case "old-generation":
				s.KernelGeneration = 3
			case "native":
				s.Resources.Reservations[0].Kind = "NATIVE_TOOL"
			}
			next, err := Reduce(&s, e, p)
			if kind == "valid" {
				if err != nil || next.Tokens.Used != upper || next.Resources.Used != rr.Upper || next.Execution != s.Execution || next.Quality != c.Unverified || next.KernelGeneration != 2 {
					t.Fatal(next, err)
				}
			} else if err == nil {
				t.Fatal("unsafe risk accounting", kind)
			}
			if s.Tokens.Reservations[0].Status != "UNKNOWN" {
				t.Fatal("reducer aliased input")
			}
		})
	}
}
