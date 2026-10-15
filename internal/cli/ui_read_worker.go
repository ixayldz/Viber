package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
	"github.com/ixayldz/Viber/internal/tui"
)

func (u uiSession) llmReadWorkerCommand(ctx context.Context, command, arg string) (string, bool, error) {
	id, err := resolveCommandID("")
	if err != nil {
		return "", false, err
	}
	request := agent.LLMReadWorkerCommand{CommandID: id, TaskID: u.task}
	switch command {
	case "/explore", "/review":
		if strings.TrimSpace(arg) == "" {
			return "", false, c.Fail(c.InvalidArgument, command+" requires a question about the captured candidate")
		}
		request.Action = strings.TrimPrefix(command, "/")
		request.Prompt = arg
	case "/worker":
		parts := strings.Fields(arg)
		request.Action = agent.ReadWorkerActionStatus
		if len(parts) > 0 {
			request.Action = parts[0]
		}
		if len(parts) > 2 || request.Action != agent.ReadWorkerActionStatus && request.Action != agent.ReadWorkerActionResult && request.Action != agent.ReadWorkerActionCancel || request.Action == agent.ReadWorkerActionCancel && len(parts) != 2 {
			return "", false, c.Fail(c.InvalidArgument, "/worker [status|result [GOAL_ID]|cancel GOAL_ID]")
		}
		if len(parts) == 2 {
			request.GoalID = parts[1]
		}
	default:
		return "", false, c.Fail(c.InvalidArgument, "unknown model worker command")
	}
	raw, err := u.call(ctx, "read-worker", request)
	if err != nil {
		return "", false, err
	}
	var control owner.ReadWorkerControl
	if err = c.DecodeStrict(raw, &control); err != nil {
		return "", false, err
	}
	return renderLLMReadWorker(control.Result), false, nil
}

func renderLLMReadWorker(result agent.LLMReadWorkerResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s worker: %s\nGoal: %s\nChild: %s\nSource: %s\n", tui.Safe(result.Role), tui.Safe(result.Status), tui.Safe(result.GoalID), tui.Safe(result.ChildTaskID), tui.Safe(result.SourceCandidateDigest))
	if result.StaleSource {
		out.WriteString("The parent candidate changed. This result describes the earlier captured source.\n")
	}
	if result.Summary != "" {
		fmt.Fprintf(&out, "\n%s\n", tui.Safe(result.Summary))
	}
	if result.Failure != nil {
		fmt.Fprintf(&out, "Stopped: %s\n", tui.Safe(result.Failure.Message))
	}
	out.WriteString("Model advice is unverified. The child cannot edit the candidate; its allocation comes from the parent budget.\nUse /worker result to inspect the latest worker.")
	return out.String()
}
