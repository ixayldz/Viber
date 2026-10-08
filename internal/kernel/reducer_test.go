package kernel

import (
	"fmt"
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func advance(t *testing.T, s *c.TaskState, kind string, p c.EventPayload, generation int64) c.TaskState {
	t.Helper()
	seq := int64(1)
	if s != nil {
		seq = s.TaskSeq + 1
	}
	e := c.Event{SchemaVersion: 1, ID: fmt.Sprintf("event-%d", seq), TaskID: "task", TaskSeq: seq, StoreSeq: seq, KernelGeneration: generation, Actor: "user", Type: kind}
	next, err := Reduce(s, e, p)
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func TestLifecyclePauseRecoveryAndBarrier(t *testing.T) {
	s := advance(t, nil, "TaskCreated", c.EventPayload{SpecVersion: 1}, 1)
	for _, state := range []c.ExecutionState{c.Scoping, c.Ready, c.Running, c.Pausing, c.Paused} {
		s = advance(t, &s, "StateTransitioned", c.EventPayload{State: state}, 1)
	}
	if LegalTransition(c.Paused, c.Running) {
		t.Fatal("pause skipped recovery")
	}
	s = advance(t, &s, "StateTransitioned", c.EventPayload{State: c.Recovering}, 2)
	s = advance(t, &s, "InputRecorded", c.EventPayload{}, 2)
	e := c.Event{SchemaVersion: 1, ID: "event", TaskID: "task", TaskSeq: s.TaskSeq + 1, StoreSeq: s.StoreSeq + 1, KernelGeneration: 2, Actor: "user", Type: "StateTransitioned"}
	if _, err := Reduce(&s, e, c.EventPayload{State: c.Ready}); err == nil {
		t.Fatal("pending input barrier bypassed")
	}
	s = advance(t, &s, "InputResolved", c.EventPayload{Reason: "NO_POLICY_CHANGE", InputID: s.PendingInputIDs[0]}, 2)
	s = advance(t, &s, "StateTransitioned", c.EventPayload{State: c.Ready}, 2)
	if s.InputBarrier {
		t.Fatal("resolved input still pending")
	}
}
func TestOldGenerationAndIllegalEventsRejected(t *testing.T) {
	s := advance(t, nil, "TaskCreated", c.EventPayload{SpecVersion: 1}, 2)
	e := c.Event{SchemaVersion: 1, ID: "event", TaskID: "task", TaskSeq: 2, StoreSeq: 2, KernelGeneration: 1, Actor: "user", Type: "StateTransitioned"}
	if _, err := Reduce(&s, e, c.EventPayload{State: c.Scoping}); err == nil {
		t.Fatal("old generation admitted")
	}
	e.KernelGeneration = 3
	if _, err := Reduce(&s, e, c.EventPayload{State: c.Scoping}); err == nil {
		t.Fatal("new generation skipped recovery")
	}
	e.KernelGeneration = 2
	e.Type = "ModelSaysDone"
	if _, err := Reduce(&s, e, c.EventPayload{}); err == nil {
		t.Fatal("unknown event accepted")
	}
}
func TestNoModelFinishedTransition(t *testing.T) {
	s := advance(t, nil, "TaskCreated", c.EventPayload{SpecVersion: 1}, 1)
	e := c.Event{SchemaVersion: 1, ID: "event", TaskID: "task", TaskSeq: 2, StoreSeq: 2, KernelGeneration: 1, Actor: "model", Type: "StateTransitioned"}
	if _, err := Reduce(&s, e, c.EventPayload{State: c.Terminated, Outcome: c.Finished, Quality: c.Verified, Fulfillment: c.Satisfied}); err == nil {
		t.Fatal("model self-report accepted")
	}
}
func TestInvocationExitPrecedence(t *testing.T) {
	s := c.TaskState{Execution: c.Terminated, Outcome: c.Finished, Quality: c.Verified, Fulfillment: c.Satisfied}
	if InvocationExit(s, false) != 0 {
		t.Fatal("strict success rejected")
	}
	s.Fulfillment = c.Conflicted
	if InvocationExit(s, false) != 2 {
		t.Fatal("verified candidate confused with delivery")
	}
	s.Outcome = c.Cancelled
	if InvocationExit(s, false) != 130 {
		t.Fatal("cancelled reused historical verdict")
	}
	s.Outcome = c.BudgetExhausted
	if InvocationExit(s, false) != 5 {
		t.Fatal("budget precedence")
	}
	s.Outcome = c.Failed
	if InvocationExit(s, false) != 4 {
		t.Fatal("failure precedence")
	}
	s.Outcome = ""
	s.Execution = c.WaitingUser
	if InvocationExit(s, false) != 3 {
		t.Fatal("wait terminalized")
	}
	if InvocationExit(s, true) != 130 {
		t.Fatal("interrupt precedence")
	}
}

func TestMultipleInputsCannotClearEachOthersBarrier(t *testing.T) {
	s := advance(t, nil, "TaskCreated", c.EventPayload{SpecVersion: 1}, 1)
	s = advance(t, &s, "InputRecorded", c.EventPayload{}, 1)
	first := s.PendingInputIDs[0]
	s = advance(t, &s, "InputRecorded", c.EventPayload{}, 1)
	second := s.PendingInputIDs[1]
	before := append([]string{}, s.PendingInputIDs...)
	s = advance(t, &s, "InputResolved", c.EventPayload{InputID: first, Reason: "NO_POLICY_CHANGE"}, 1)
	if !s.InputBarrier || len(s.PendingInputIDs) != 1 || s.PendingInputIDs[0] != second {
		t.Fatal("one acknowledgement cleared another input")
	}
	if len(before) != 2 || before[0] != first {
		t.Fatal("reducer mutated prior state")
	}
	s = advance(t, &s, "PolicyRevised", c.EventPayload{InputID: second, PolicyEpoch: 2, Reason: "POLICY_CHANGED"}, 1)
	if s.InputBarrier || s.PolicyEpoch != 2 {
		t.Fatal("resolved policy barrier")
	}
}

func TestReducerOwnsTokenAccountAcrossHistoricalStates(t *testing.T) {
	before := c.TaskState{SchemaVersion: 1, TaskID: "owned", SpecVersion: 1, Execution: c.Ready, TaskSeq: 3, StoreSeq: 3, KernelGeneration: 1, PolicyEpoch: 1, Tokens: &c.TokenAccount{SchemaVersion: 1, Limits: c.DefaultTokenLimits(), Reservations: []c.TokenReservation{{ID: "original"}}}}
	after, err := Reduce(&before, c.Event{SchemaVersion: 1, ID: "running", Actor: "kernel", TaskID: "owned", TaskSeq: 4, StoreSeq: 4, KernelGeneration: 1, Type: "StateTransitioned"}, c.EventPayload{State: c.Running})
	if err != nil {
		t.Fatal(err)
	}
	after.Tokens.Limits.Input = 4096
	after.Tokens.Reservations[0].ID = "changed"
	if before.Tokens.Limits != c.DefaultTokenLimits() || before.Tokens.Reservations[0].ID != "original" {
		t.Fatal("returned state aliased historical resource account")
	}
}
