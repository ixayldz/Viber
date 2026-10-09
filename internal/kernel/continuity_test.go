package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func TestContinuityGenerationRenewalRequiresSettledUserBoundary(t *testing.T) {
	base := advance(t, nil, "TaskCreated", c.EventPayload{SpecVersion: 1}, 1)
	base = advance(t, &base, "StateTransitioned", c.EventPayload{State: c.Scoping}, 1)
	base = advance(t, &base, "StateTransitioned", c.EventPayload{State: c.Ready}, 1)
	for _, kind := range []string{"valid", "model", "running", "pending", "unknown", "tokens-mutation", "old-owner"} {
		t.Run(kind, func(t *testing.T) {
			s := base
			e := c.Event{SchemaVersion: 1, ID: "renew", TaskID: s.TaskID, TaskSeq: s.TaskSeq + 1, StoreSeq: s.StoreSeq + 1, KernelGeneration: 2, Actor: "user", Type: "ContinuityRevised"}
			p := c.EventPayload{DocumentDigest: c.HashBytes([]byte("document")), Reason: c.HashBytes([]byte("command"))}
			switch kind {
			case "model":
				e.Actor = "model"
			case "running":
				s.Execution = c.Running
			case "pending":
				s.InputBarrier = true
			case "unknown":
				s.Tokens = &c.TokenAccount{SchemaVersion: 1, Limits: c.DefaultTokenLimits(), Reservations: []c.TokenReservation{{Status: "UNKNOWN"}}}
			case "tokens-mutation":
				p.Tokens = &c.TokenMutation{Action: "INIT", Limits: c.DefaultTokenLimits()}
			case "old-owner":
				s.KernelGeneration = 3
			}
			next, err := Reduce(&s, e, p)
			if kind == "valid" {
				if err != nil || next.KernelGeneration != 2 || next.DocumentDigest != p.DocumentDigest || next.SpecVersion != s.SpecVersion || next.Execution != s.Execution {
					t.Fatal(next, err)
				}
			} else if err == nil {
				t.Fatal("unsafe renewal", kind)
			}
		})
	}
}
