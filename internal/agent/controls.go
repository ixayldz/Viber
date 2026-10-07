package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

func commandDigest(task, action string) string {
	digest, _ := c.Digest(struct {
		Task   string
		Action string
	}{task, action})
	return digest
}
func (s *Session) receipt(ctx context.Context, task, id, action string) (c.TaskState, bool, error) {
	state, event, payload, found, err := s.Journal.CommandReceipt(ctx, id)
	if err != nil || !found {
		return state, found, err
	}
	if event.TaskID != task || event.Actor != "kernel" || (event.Type != "ControlAcknowledged" && event.Type != "StateTransitioned") || payload.Reason != commandDigest(task, action) {
		return state, true, c.Fail(c.CommandIDConflict, "control command ID reused with different input")
	}
	if payload.CommandError != nil {
		return state, true, payload.CommandError
	}
	return state, true, nil
}
func (s *Session) acknowledge(ctx context.Context, state c.TaskState, id, action string) (c.TaskState, error) {
	return s.Journal.Execute(ctx, store.Command{ID: id, TaskID: state.TaskID, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "ControlAcknowledged", Payload: c.EventPayload{DocumentDigest: state.DocumentDigest, Reason: commandDigest(state.TaskID, action)}})
}
func (s *Session) Controls(ctx context.Context, task, action string) (c.TaskState, error) {
	return s.ControlsWithID(ctx, task, action, newID("control-"))
}
func (s *Session) ControlsWithID(ctx context.Context, task, action, id string) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controlsLocked(ctx, task, action, id)
}
func (s *Session) controlsLocked(ctx context.Context, task, action, id string) (c.TaskState, error) {
	if id == "" || action != "pause" && action != "cancel" {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "invalid task control")
	}
	if receipt, found, err := s.receipt(ctx, task, id, action); found || err != nil {
		return receipt, err
	}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return state, err
	}
	if state.Execution == c.Terminated {
		if action == "cancel" && state.Outcome == c.Cancelled {
			return s.acknowledge(ctx, state, id, action)
		}
		return state, c.Fail(c.InvalidArgument, "task already terminal")
	}
	state, err = s.recover(ctx, state)
	if err != nil {
		return state, err
	}
	if doc.Pending != nil {
		doc.Pending.Status = "UNKNOWN"
		doc.UnknownEffect = true
		state, err = s.record(ctx, state, doc, "SessionRecorded", c.EventPayload{})
		if err != nil {
			return state, err
		}
	}
	final := func(to c.ExecutionState, outcome c.Outcome) (c.TaskState, error) {
		return s.Journal.Execute(ctx, store.Command{ID: id, TaskID: task, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "StateTransitioned", Payload: c.EventPayload{State: to, Outcome: outcome, Reason: commandDigest(task, action)}})
	}
	if action == "cancel" {
		return final(c.Terminated, c.Cancelled)
	}
	if state.Execution == c.Paused {
		return s.acknowledge(ctx, state, id, action)
	}
	if state.Execution == c.Recovering {
		target := c.Ready
		if state.InputBarrier {
			target = c.WaitingUser
		}
		state, err = s.transition(ctx, state, target, "", "control reconciliation")
		if err != nil {
			return state, err
		}
	}
	if state.Execution != c.Pausing {
		state, err = s.transition(ctx, state, c.Pausing, "", "admission stopped")
		if err != nil {
			return state, err
		}
	}
	if _, err = s.Journal.Checkpoint(ctx, task); err != nil {
		return state, err
	}
	return final(c.Paused, "")
}

// RunControlled drains the native fixture loop on cancellation. Pause retains a
// nonterminal task; cancel assigns CANCELLED. Pending effects remain unknown.
func (s *Session) RunControlled(ctx context.Context, task string, pause *atomic.Bool) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.runLocked(ctx, task)
	if ctx.Err() == nil {
		return state, err
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	current, loadErr := s.State(cleanup, task)
	if loadErr != nil {
		return state, errors.Join(err, loadErr)
	}
	if current.Execution == c.Terminated {
		return current, err
	}
	action := "cancel"
	if pause != nil && pause.Load() {
		action = "pause"
	}
	controlled, controlErr := s.controlsLocked(cleanup, task, action, newID("interrupt-"))
	if controlErr != nil {
		return controlled, errors.Join(err, controlErr)
	}
	return controlled, nil
}
func (s *Session) Run(ctx context.Context, task string) (c.TaskState, error) {
	return s.RunControlled(ctx, task, nil)
}

// InvocationStart publishes admission before a supervised run. A missing
// completion receipt forbids retrying that same command ID after owner loss.
func (s *Session) InvocationStart(ctx context.Context, task, id string) (c.TaskState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return c.TaskState{}, false, c.Fail(c.InvalidArgument, "invocation command ID required")
	}
	if receipt, found, err := s.receipt(ctx, task, id, "resume"); found || err != nil {
		return receipt, found, err
	}
	if _, found, err := s.receipt(ctx, task, id+"-admitted", "resume"); found || err != nil {
		if err == nil {
			err = c.Fail(c.Conflict, "admitted invocation lacks completion receipt; inspect task before a new invocation")
		}
		return c.TaskState{}, false, err
	}
	state, err := s.State(ctx, task)
	if err != nil {
		return state, false, err
	}
	state, err = s.recover(ctx, state)
	if err != nil {
		return state, false, err
	}
	state, err = s.acknowledge(ctx, state, id+"-admitted", "resume")
	return state, false, err
}
func (s *Session) InvocationFinish(ctx context.Context, task, id string) (c.TaskState, error) {
	return s.InvocationFinishWithError(ctx, task, id, nil)
}
func (s *Session) InvocationFinishWithError(ctx context.Context, task, id string, failure error) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt, found, err := s.receipt(ctx, task, id, "resume"); found || err != nil {
		return receipt, err
	}
	if _, found, err := s.receipt(ctx, task, id+"-admitted", "resume"); err != nil || !found {
		if err == nil {
			err = c.Fail(c.PolicyDenied, "invocation admission missing")
		}
		return c.TaskState{}, err
	}
	state, err := s.State(ctx, task)
	if err != nil {
		return state, err
	}
	var commandError *c.Error
	if failure != nil {
		if !errors.As(failure, &commandError) {
			commandError = &c.Error{Code: c.StoreIntegrityError, Message: "supervised invocation failed; inspect task"}
		}
	}
	return s.Journal.Execute(ctx, store.Command{ID: id, TaskID: task, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "ControlAcknowledged", Payload: c.EventPayload{DocumentDigest: state.DocumentDigest, Reason: commandDigest(task, "resume"), CommandError: commandError}})
}

func (s *Session) ControlReceipt(ctx context.Context, task, id, action string) (c.TaskState, bool, error) {
	return s.receipt(ctx, task, id, action)
}
