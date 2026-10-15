package owner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

type GoalControlResult struct {
	SchemaVersion int            `json:"schema_version"`
	Parent        c.TaskState    `json:"parent_state"`
	Schedule      c.GoalSchedule `json:"schedule"`
	Child         c.TaskState    `json:"child_state,omitempty"`
	Recovery      string         `json:"recovery_status"`
}

func goalInvocationID(parent, goal string) string {
	digest := sha256.Sum256([]byte(parent + "\x00" + goal))
	return "goal-run-" + hex.EncodeToString(digest[:20])
}

func (o *Owner) wakeGoals() {
	select {
	case o.goalWake <- struct{}{}:
	default:
	}
}

func (o *Owner) goalLoop() {
	defer close(o.goalDone)
	for {
		if err := o.reconcileGoals(o.ctx); err != nil && o.ctx.Err() != nil {
			return
		}
		select {
		case <-o.ctx.Done():
			return
		case <-o.goalWake:
		}
	}
}

func (o *Owner) reconcileGoals(ctx context.Context) error {
	states, err := o.Session.Journal.Replay(ctx, "")
	if err != nil {
		return err
	}
	for parent, state := range states {
		for _, item := range state.GoalQueue {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if item.Status != "ACTIVATING" && item.Status != "ACTIVATED" {
				continue
			}
			o.reconcileGoal(ctx, parent, state, item)
		}
	}
	return nil
}

func (o *Owner) reconcileGoal(ctx context.Context, parent string, parentState c.TaskState, item c.GoalSchedule) {
	child, err := o.Session.CreateGoalTask(ctx, parent, item)
	if err != nil {
		return
	}
	if item.Status == "ACTIVATING" {
		parentState, err = o.Session.CommitGoalActivation(ctx, parent, item, child)
		if err != nil {
			return
		}
		for _, updated := range parentState.GoalQueue {
			if updated.ID == item.ID {
				item = updated
				break
			}
		}
	}
	// A child task that advanced beyond its activation receipt may already have
	// dispatched work. Recovery is explicit in that case; never start a second
	// invocation from a changed child state.
	if item.Status != "ACTIVATED" || child.TaskSeq != item.ChildTaskSeq || child.DocumentDigest != item.ChildDocumentDigest || child.Execution != c.Ready || child.InputBarrier {
		return
	}
	id := goalInvocationID(parent, item.ID)
	if _, found, receiptErr := o.Session.ControlReceipt(ctx, child.TaskID, id, "resume"); receiptErr != nil || found {
		return
	}
	if _, _, _, admitted, receiptErr := o.Session.Journal.CommandReceipt(ctx, id+"-admitted"); receiptErr != nil || admitted {
		return
	}
	if c.ValidReadWorkerRole(item.Role) {
		// A stale/expired parent lease is an admission failure. Keep the
		// immutable child result inspectable, but never start a new model/tool
		// turn without an explicit fresh worker request.
		if err := o.Session.ReadWorkerAdmission(ctx, child.TaskID); err != nil {
			return
		}
	}
	_, _ = o.StartDetached(ctx, child.TaskID, id)
}

func (o *Owner) goalControl(ctx context.Context, requestID, task string, command agent.GoalCommand) (GoalControlResult, error) {
	result := GoalControlResult{SchemaVersion: 1, Recovery: "NOT_REQUESTED"}
	if command.CommandID != requestID || command.TaskID != task || command.CommandID == "" {
		return result, c.Fail(c.InvalidArgument, "goal command envelope mismatch")
	}
	if command.Action == "add" || command.Action == "cancel" {
		state, err := o.Session.GoalControl(ctx, command)
		if err != nil {
			return result, err
		}
		result.Parent = state
		id := command.GoalID
		if command.Action == "add" {
			id = command.CommandID
		}
		if schedule, found := goalSchedule(state, id); found {
			result.Schedule = schedule
		}
		o.wakeGoals()
		return result, nil
	}
	if command.Action != "activate" && command.Action != "recover" {
		return result, c.Fail(c.InvalidArgument, "goal action must add, cancel, activate, or recover")
	}
	state, schedule, err := o.Session.BeginGoalActivation(ctx, command)
	if err != nil {
		return result, err
	}
	result.Parent, result.Schedule = state, schedule
	o.mu.Lock()
	busy := o.active != nil
	o.mu.Unlock()
	if busy {
		result.Recovery = "SCHEDULED_WAITING_FOR_ACTIVE_TASK"
		o.wakeGoals()
		return result, nil
	}
	child, err := o.Session.CreateGoalTask(ctx, task, schedule)
	if err != nil {
		result.Recovery = "CHILD_RECONCILIATION_REQUIRED"
		o.wakeGoals()
		return result, err
	}
	result.Child = child
	if schedule.Status == "ACTIVATING" {
		state, err = o.Session.CommitGoalActivation(ctx, task, schedule, child)
		if err != nil {
			result.Recovery = "ACTIVATION_RECEIPT_UNKNOWN"
			o.wakeGoals()
			return result, err
		}
		result.Parent = state
		for _, updated := range state.GoalQueue {
			if updated.ID == schedule.ID {
				result.Schedule = updated
				break
			}
		}
	}
	if result.Child.TaskSeq == result.Schedule.ChildTaskSeq && result.Child.DocumentDigest == result.Schedule.ChildDocumentDigest && result.Child.Execution == c.Ready && !result.Child.InputBarrier {
		result.Recovery = "SCHEDULED"
	} else {
		result.Recovery = "USER_RECOVERY_REQUIRED"
	}
	o.wakeGoals()
	return result, nil
}

func goalSchedule(state c.TaskState, id string) (c.GoalSchedule, bool) {
	for _, item := range state.GoalQueue {
		if item.ID == id {
			return item, true
		}
	}
	return c.GoalSchedule{}, false
}
