// Package kernel holds pure state logic. Replay never executes tools.
package kernel

import (
	"fmt"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func active(s c.ExecutionState) bool {
	switch s {
	case c.Scoping, c.Ready, c.Running, c.Verifying, c.Delivering:
		return true
	}
	return false
}
func nonterminal(s c.ExecutionState) bool {
	switch s {
	case c.Created, c.Scoping, c.Ready, c.Running, c.Verifying, c.Delivering, c.WaitingUser, c.WaitingResource, c.Blocked, c.Pausing, c.Paused, c.Recovering:
		return true
	}
	return false
}
func LegalTransition(from, to c.ExecutionState) bool {
	if from == to {
		return from == c.Recovering
	}
	if to == c.Recovering {
		return nonterminal(from)
	}
	if to == c.Terminated {
		return nonterminal(from)
	}
	if to == c.Pausing {
		return active(from) || from == c.WaitingUser || from == c.WaitingResource || from == c.Blocked
	}
	if to == c.WaitingUser {
		return active(from) || from == c.Pausing || from == c.Recovering
	}
	if to == c.WaitingResource || to == c.Blocked {
		return active(from) || from == c.Recovering
	}
	switch from {
	case c.Created:
		return to == c.Scoping
	case c.Scoping:
		return to == c.Ready
	case c.Ready:
		return to == c.Running
	case c.Running:
		return to == c.Verifying
	case c.Verifying:
		return to == c.Running || to == c.Delivering
	case c.Delivering:
		return to == c.Ready
	case c.Pausing:
		return to == c.Paused
	case c.Recovering, c.WaitingUser, c.WaitingResource, c.Blocked:
		return to == c.Ready || to == c.Scoping
	}
	return false
}

func Reduce(state *c.TaskState, event c.Event, p c.EventPayload) (c.TaskState, error) {
	if event.SchemaVersion != c.SchemaVersion || event.ID == "" || event.Actor == "" || event.TaskID == "" || event.StoreSeq < 1 || event.KernelGeneration < 1 {
		return c.TaskState{}, c.Fail(c.StoreIntegrityError, "invalid event envelope")
	}
	next := c.TaskState{}
	if state == nil {
		if event.Type != "TaskCreated" || event.TaskSeq != 1 || p.SpecVersion < 1 {
			return next, c.Fail(c.StoreIntegrityError, "task must begin with TaskCreated")
		}
		next = c.TaskState{SchemaVersion: c.SchemaVersion, TaskID: event.TaskID, SpecVersion: p.SpecVersion, Execution: c.Created, Quality: c.Unverified, Fulfillment: c.FulfillmentPending, PolicyEpoch: 1, KernelGeneration: event.KernelGeneration}
		if p.RequiredObligations < 0 {
			return next, c.Fail(c.InvalidArgument, "invalid initial obligations")
		}
		next.OpenRequiredObligations = p.RequiredObligations
		if p.DocumentDigest != "" {
			if !c.ValidDigest(p.DocumentDigest) || !c.ValidDigest(p.SnapshotDigest) {
				return next, c.Fail(c.InvalidArgument, "invalid initial durable bindings")
			}
			next.DocumentDigest = p.DocumentDigest
			next.BaselineDigest = p.SnapshotDigest
			next.CandidateDigest = p.SnapshotDigest
		}
	} else {
		next = *state
		if state.Tokens != nil {
			account := *state.Tokens
			account.Reservations = append([]c.TokenReservation{}, state.Tokens.Reservations...)
			next.Tokens = &account
		}
		next.PendingInputIDs = append([]string{}, state.PendingInputIDs...)
		if event.TaskID != next.TaskID || event.TaskSeq != next.TaskSeq+1 || event.StoreSeq <= next.StoreSeq || event.KernelGeneration < next.KernelGeneration {
			return next, c.Fail(c.StoreIntegrityError, "event sequence or generation mismatch")
		}
		if event.KernelGeneration > next.KernelGeneration && !(next.Execution == c.Terminated && event.Type == "ControlAcknowledged") && (event.Type != "StateTransitioned" || p.State != c.Recovering) {
			return next, c.Fail(c.StaleAuthority, "new generation requires recovery")
		}
		switch event.Type {
		case "ControlAcknowledged":
			if event.Actor != "kernel" || p.DocumentDigest != next.DocumentDigest || !c.ValidDigest(p.DocumentDigest) || !c.ValidDigest(p.Reason) {
				return next, c.Fail(c.PolicyDenied, "control receipt requires unchanged durable binding")
			}
		case "UserResponseRecorded":
			if event.Actor != "user" || !nonterminal(next.Execution) || next.InputBarrier || !c.ValidDigest(p.DocumentDigest) || !c.ValidDigest(p.Reason) || p.InputID == "" {
				return next, c.Fail(c.PolicyDenied, "bound user response required")
			}
			next.DocumentDigest = p.DocumentDigest
		case "SessionRecorded":
			if event.Actor != "kernel" || !nonterminal(next.Execution) || !c.ValidDigest(p.DocumentDigest) {
				return next, c.Fail(c.InvalidArgument, "invalid durable session update")
			}
			next.DocumentDigest = p.DocumentDigest
		case "CandidateRecorded":
			if event.Actor != "kernel" || next.Execution != c.Running || next.InputBarrier || !c.ValidDigest(p.DocumentDigest) || !c.ValidDigest(p.SnapshotDigest) {
				return next, c.Fail(c.PolicyDenied, "candidate publication requires active authority")
			}
			next.DocumentDigest = p.DocumentDigest
			next.CandidateDigest = p.SnapshotDigest
			next.Quality = c.Unverified
		case "LimitedResultFinalized":
			if event.Actor != "kernel" || next.Execution != c.Delivering || next.InputBarrier || !c.ValidDigest(p.SnapshotDigest) || !c.ValidDigest(next.DocumentDigest) || !c.ValidDigest(p.DocumentDigest) || p.SnapshotDigest != next.CandidateDigest || p.Reason != "EXPLICIT_LIMITED_RESULT_POLICY" || p.Quality != c.Unverified || p.Fulfillment != c.Satisfied {
				return next, c.Fail(c.PolicyDenied, "limited final transaction preconditions missing")
			}
			next.DocumentDigest = p.DocumentDigest
			next.Execution = c.Terminated
			next.Outcome = c.Finished
			next.Quality = c.Unverified
			next.Fulfillment = c.Satisfied
		case "StateTransitioned":
			if next.Execution == c.Recovering && p.State == c.Recovering && event.KernelGeneration <= next.KernelGeneration {
				return next, c.Fail(c.StaleAuthority, "recovery requires a renewed generation")
			}
			if !LegalTransition(next.Execution, p.State) {
				return next, c.Fail(c.InvalidArgument, fmt.Sprintf("illegal transition %s -> %s", next.Execution, p.State))
			}
			if p.State == c.Terminated {
				switch p.Outcome {
				case c.Cancelled, c.Failed, c.BudgetExhausted:
				default:
					return next, c.Fail(c.InvalidArgument, "FINISHED requires guarded final transaction")
				}
				next.Outcome = p.Outcome
			} else if p.Outcome != "" {
				return next, c.Fail(c.InvalidArgument, "nonterminal outcome forbidden")
			}
			if next.InputBarrier && (p.State == c.Ready || p.State == c.Running || p.State == c.Verifying || p.State == c.Delivering) {
				return next, c.Fail(c.PolicyDenied, "unresolved input barrier")
			}
			next.Execution = p.State
		case "SpecRevised":
			if event.Actor != "user" || !next.InputBarrier || p.SpecVersion != next.SpecVersion+1 || p.PolicyEpoch != next.PolicyEpoch+1 || !c.ValidDigest(p.DocumentDigest) || !c.ValidDigest(p.Reason) || p.RequiredObligations < next.OpenRequiredObligations || p.RequiredObligations < 1 || next.Execution == c.Terminated || active(next.Execution) {
				return next, c.Fail(c.PolicyDenied, "scope revision requires a quiescent barrier and current bindings")
			}
			pending, err := resolveInput(next.PendingInputIDs, p.InputID)
			if err != nil {
				return next, err
			}
			next.PendingInputIDs = pending
			next.SpecVersion = p.SpecVersion
			next.PolicyEpoch = p.PolicyEpoch
			next.DocumentDigest = p.DocumentDigest
			next.Quality = c.Unverified
			next.Fulfillment = c.FulfillmentPending
			next.OpenRequiredObligations = p.RequiredObligations
			next.Execution = c.Paused
		case "InputRecorded":
			if p.InputDigest != "" && (event.Actor != "user" || !c.ValidDigest(p.InputDigest) || p.InputBytes <= 0 || p.InputBytes > 64<<10 || p.InputID != event.ID) {
				return next, c.Fail(c.InvalidArgument, "invalid raw steering binding")
			}
			if !nonterminal(next.Execution) {
				return next, c.Fail(c.InvalidArgument, "terminal task requires a new attempt")
			}
			if len(next.PendingInputIDs) >= 256 {
				return next, c.Fail(c.InvalidArgument, "pending input queue is full")
			}
			next.PendingInputIDs = append(next.PendingInputIDs, event.ID)
			next.InputBarrier = true
		case "InputResolved":
			if !next.InputBarrier {
				return next, c.Fail(c.InvalidArgument, "no pending input")
			}
			if p.Reason != "NO_POLICY_CHANGE" {
				return next, c.Fail(c.InvalidArgument, "explicit policy decision required")
			}
			pending, err := resolveInput(next.PendingInputIDs, p.InputID)
			if err != nil {
				return next, err
			}
			next.PendingInputIDs = pending
			next.InputBarrier = len(pending) > 0
		case "PolicyRevised":
			if !nonterminal(next.Execution) || !next.InputBarrier || p.PolicyEpoch != next.PolicyEpoch+1 || p.Reason != "POLICY_CHANGED" {
				return next, c.Fail(c.PolicyDenied, "policy revision requires pending input and next epoch")
			}
			next.PolicyEpoch = p.PolicyEpoch
			pending, err := resolveInput(next.PendingInputIDs, p.InputID)
			if err != nil {
				return next, err
			}
			next.PendingInputIDs = pending
			next.InputBarrier = len(pending) > 0
			next.Quality = c.Unverified
		default:
			return next, c.Fail(c.StoreIntegrityError, "unknown required event type")
		}
		next.KernelGeneration = event.KernelGeneration
	}
	if p.Tokens != nil {
		if event.Actor != "kernel" || !c.ValidDigest(p.DocumentDigest) || (state == nil && (event.Type != "TaskCreated" || p.Tokens.Action != "INIT")) || (state != nil && (event.Type != "SessionRecorded" || p.Tokens.Action == "INIT")) {
			return next, c.Fail(c.PolicyDenied, "token mutation requires an atomic kernel document event")
		}
		var err error
		next.Tokens, err = c.ApplyTokens(next.Tokens, *p.Tokens, next)
		if err != nil {
			return next, err
		}
	}
	next.InputBarrier = len(next.PendingInputIDs) > 0
	next.TaskSeq = event.TaskSeq
	next.StoreSeq = event.StoreSeq
	return next, nil
}

func StrictSuccess(s c.TaskState) bool {
	return s.Execution == c.Terminated && s.Outcome == c.Finished && s.Quality == c.Verified && s.Fulfillment == c.Satisfied && s.OpenRequiredObligations == 0 && !s.InputBarrier && len(s.PendingInputIDs) == 0
}
func InvocationExit(s c.TaskState, interrupted bool) int {
	if interrupted || s.Outcome == c.Cancelled {
		return 130
	}
	if s.Outcome == c.BudgetExhausted {
		return 5
	}
	if s.Outcome == c.Failed || (s.Execution == c.Terminated && s.Quality == c.QualityFailed) {
		return 4
	}
	if s.Execution != c.Terminated {
		return 3
	}
	if StrictSuccess(s) {
		return 0
	}
	if s.Outcome == c.Finished {
		return 2
	}
	return 4
}

func resolveInput(pending []string, id string) ([]string, error) {
	if id == "" {
		return nil, c.Fail(c.InvalidArgument, "input resolution requires its durable input ID")
	}
	for i, existing := range pending {
		if existing == id {
			next := append([]string{}, pending[:i]...)
			return append(next, pending[i+1:]...), nil
		}
	}
	return nil, c.Fail(c.StaleBase, "input resolution ID is not pending")
}
