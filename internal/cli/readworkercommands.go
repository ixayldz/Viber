package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
)

// runReadWorker exposes the durable, parent-bound Explore/Review child path.
// The command is deliberately routed through the owner whenever one is
// available; the local fallback is the same single-writer path used by the
// other goal commands when no owner is running.
func runReadWorker(args []string, out, errout io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return report(out, errout, c.Fail(c.InvalidArgument, "read-worker action and task required"), false)
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	task, rest := promptFirst(args[1:])
	f := flags("read-worker", errout)
	directory := f.String("store", "", "existing private store")
	prompt := f.String("prompt", "", "bounded Explore/Review question")
	goalID := f.String("goal", "", "durable read-worker goal ID")
	goalIDAlias := f.String("goal-id", "", "durable read-worker goal ID")
	childTask := f.String("child-task", "", "optional stable child task ID")
	commandID := f.String("command-id", "", "idempotent command ID")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(rest); err != nil {
		return 4
	}
	if *goalID != "" && *goalIDAlias != "" && *goalID != *goalIDAlias {
		return report(out, errout, c.Fail(c.InvalidArgument, "conflicting goal IDs"), *jsonMode)
	}
	if *goalID == "" {
		*goalID = *goalIDAlias
	}
	if f.NArg() == 1 && *prompt == "" {
		*prompt = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "unexpected read-worker arguments"), *jsonMode)
	}
	if task == "" || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "read-worker requires task/store"), *jsonMode)
	}
	switch action {
	case agent.ReadWorkerActionExplore, agent.ReadWorkerActionReview:
		if strings.TrimSpace(*prompt) == "" || *goalID != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "explore/review requires a prompt and takes no goal"), *jsonMode)
		}
	case agent.ReadWorkerActionStatus, agent.ReadWorkerActionResult:
		if *prompt != "" || *childTask != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "status/result take only a goal"), *jsonMode)
		}
		if *goalID == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "status/result require --goal"), *jsonMode)
		}
	case agent.ReadWorkerActionCancel:
		if *prompt != "" || *childTask != "" || *goalID == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "cancel requires --goal and takes no prompt/child"), *jsonMode)
		}
	default:
		return report(out, errout, c.Fail(c.InvalidArgument, "read-worker action must be explore, review, status, result, or cancel"), *jsonMode)
	}
	id, err := resolveCommandID(*commandID)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	command := agent.LLMReadWorkerCommand{CommandID: id, TaskID: task, Action: action, Prompt: *prompt, GoalID: *goalID, ChildTaskID: *childTask}
	ctx := context.Background()
	if action == agent.ReadWorkerActionExplore || action == agent.ReadWorkerActionReview {
		if _, err = startBackgroundOwner(ctx, *directory); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	raw, routed, err := ownerCall(ctx, *directory, task, "read-worker", id, command)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result any = json.RawMessage(raw)
	if !routed {
		session, openErr := agent.OpenExisting(ctx, *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		host, openErr := owner.Open(ctx, *directory, session)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer host.Close()
		encoded, encodeErr := c.CanonicalV1(command)
		if encodeErr != nil {
			return report(out, errout, encodeErr, *jsonMode)
		}
		result, err = host.Handle(ctx, ipc.Request{ID: id, TaskID: task, Command: "read-worker", Payload: encoded})
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if *jsonMode {
		if err = jsonWrite(out, result); err != nil {
			return 4
		}
		return 0
	}
	var control owner.ReadWorkerControl
	if rawResult, ok := result.(json.RawMessage); ok {
		if err = c.DecodeStrict(rawResult, &control); err != nil {
			return report(out, errout, err, false)
		}
	} else {
		encoded, encodeErr := c.CanonicalV1(result)
		if encodeErr != nil {
			return report(out, errout, encodeErr, false)
		}
		if err = c.DecodeStrict(encoded, &control); err != nil {
			return report(out, errout, err, false)
		}
	}
	res := control.Result
	fmt.Fprintf(out, "Read worker %s · %s · goal %s · child %s\n", res.Role, res.Status, res.GoalID, res.ChildTaskID)
	if res.StaleSource {
		fmt.Fprintln(out, "Source candidate changed; result is advisory and stale.")
	}
	if res.Summary != "" {
		fmt.Fprintf(out, "Summary %q\n", res.Summary)
	}
	return 0
}
