package cli

import (
	"context"
	"encoding/json"
	"io"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
)

func runGoalQueue(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("goal-queue", errout)
	directory := f.String("store", "", "existing private store")
	input := f.String("command-file", "", "bound add/cancel/activate/recover goal command; absent lists goals")
	jsonMode := f.Bool("json", false, "JSON result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || task == "" || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "goal-queue requires task/store"), *jsonMode)
	}
	action := "goal-list"
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var payload any
	if *input != "" {
		var command agent.GoalCommand
		if err = readJSON(*input, &command); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if command.TaskID != task || command.CommandID == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "goal command binding mismatch"), *jsonMode)
		}
		id, action, payload = command.CommandID, "goal-control", command
	}
	ctx := context.Background()
	if action == "goal-control" {
		command := payload.(agent.GoalCommand)
		if command.Action == "activate" || command.Action == "recover" {
			if _, err = startBackgroundOwner(ctx, *directory); err != nil {
				return report(out, errout, err, *jsonMode)
			}
		}
	}
	raw, routed, err := ownerCall(ctx, *directory, task, action, id, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result any = json.RawMessage(raw)
	if !routed {
		if command, ok := payload.(agent.GoalCommand); ok && (command.Action == "activate" || command.Action == "recover") {
			return report(out, errout, c.Fail(c.UnknownOutcome, "background owner disappeared before goal activation; inspect the durable goal queue before retrying"), *jsonMode)
		}
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
		encoded, encodeErr := c.CanonicalV1(payload)
		if encodeErr != nil {
			return report(out, errout, encodeErr, *jsonMode)
		}
		result, openErr = host.Handle(ctx, ipc.Request{ID: id, TaskID: task, Command: action, Payload: encoded})
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
	}
	if err = jsonWrite(out, result); err != nil {
		return 4
	}
	return 0
}
