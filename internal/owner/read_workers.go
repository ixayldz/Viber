package owner

import (
	"context"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

// ReadWorkerControl is the owner-facing result for Explore/Review commands.
// The embedded agent result is advisory and source/receipt bound; it never
// changes the parent's plan, candidate, verification or completion state.
type ReadWorkerControl struct {
	SchemaVersion int                       `json:"schema_version"`
	Result        agent.LLMReadWorkerResult `json:"result"`
}

func readWorkerActivationID(commandID string) string {
	return "read-worker-activate-" + c.HashBytes([]byte(commandID))[:32]
}

func (o *Owner) readWorkerControl(ctx context.Context, requestID, task string, command agent.LLMReadWorkerCommand) (ReadWorkerControl, error) {
	result := ReadWorkerControl{SchemaVersion: 1}
	if command.CommandID != requestID || command.TaskID != task || requestID == "" {
		return result, c.Fail(c.InvalidArgument, "read-worker command envelope mismatch")
	}
	action := strings.ToLower(strings.TrimSpace(command.Action))
	command.Action = action
	switch action {
	case agent.ReadWorkerActionExplore, agent.ReadWorkerActionReview:
		_, schedule, err := o.Session.ScheduleLLMReadWorker(ctx, command)
		if err != nil {
			return result, err
		}
		activate := agent.GoalCommand{CommandID: readWorkerActivationID(requestID), TaskID: task, Action: "activate", GoalID: schedule.ID}
		if _, _, err = o.Session.BeginGoalActivation(ctx, activate); err != nil {
			return result, err
		}
		o.wakeGoals()
		advisory, err := o.Session.ReadWorkerResult(ctx, task, schedule.ID)
		if err != nil {
			return result, err
		}
		result.Result = advisory
		return result, nil
	case agent.ReadWorkerActionStatus, agent.ReadWorkerActionResult:
		if command.Prompt != "" || command.ChildTaskID != "" {
			return result, c.Fail(c.InvalidArgument, "read-worker status/result take only a goal ID")
		}
		advisory, err := o.Session.ReadWorkerResult(ctx, task, command.GoalID)
		if err != nil {
			return result, err
		}
		result.Result = advisory
		return result, nil
	case agent.ReadWorkerActionCancel:
		if command.GoalID == "" {
			return result, c.Fail(c.InvalidArgument, "read-worker cancellation requires a goal ID")
		}
		// If this child currently owns the foreground invocation, cancel its
		// supervised context first. The Session path below still commits the
		// durable child receipt; only a still-queued intent releases its parent
		// tranche. Every activated or unknown outcome keeps the reservation,
		// including a receipt-free ACTIVATING intent because child creation may
		// already have happened before its activation receipt was committed.
		if advisory, readErr := o.Session.ReadWorkerResult(ctx, task, command.GoalID); readErr == nil && advisory.ChildTaskID != "" {
			o.mu.Lock()
			activeChild := o.active != nil && o.active.task == advisory.ChildTaskID
			o.mu.Unlock()
			if activeChild {
				_, _ = o.taskControl(ctx, advisory.ChildTaskID, "cancel", command.CommandID+"-child")
			}
		}
		_, err := o.Session.CancelReadWorker(ctx, command)
		if err != nil {
			return result, err
		}
		o.wakeGoals()
		advisory, err := o.Session.ReadWorkerResult(ctx, task, command.GoalID)
		if err != nil {
			return result, err
		}
		result.Result = advisory
		return result, nil
	default:
		return result, c.Fail(c.InvalidArgument, "read-worker action must be explore, review, status, result, or cancel")
	}
}
