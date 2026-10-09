package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runPrivacy(action string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(action, errout)
	directory := f.String("store", "", "existing private store")
	input := f.String("command-file", "", "reviewed deletion preview with stable command ID")
	jsonMode := f.Bool("json", false, "structured plan or purge receipt")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" || action == "delete" && *input == "" || action == "delete-preview" && *input != "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "preview requires task/store; delete requires reviewed command-file"), *jsonMode)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var command agent.DeletionCommand
	var payload any
	if action == "delete" {
		if err = readJSON(*input, &command); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if command.Plan.TaskID != task {
			return report(out, errout, c.Fail(c.InvalidArgument, "command task differs from CLI task"), *jsonMode)
		}
		id, payload = command.CommandID, command
	}
	raw, routed, err := ownerCall(ctx, *directory, task, action, id, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if routed {
		if err = jsonWrite(out, json.RawMessage(raw)); err != nil {
			return 4
		}
		return 0
	}
	session, err := agent.OpenExisting(ctx, *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	var result any
	if action == "delete" {
		result, err = session.DeleteContent(ctx, command)
	} else {
		result, err = session.DeletionPreview(ctx, task)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, result); err != nil {
		return 4
	}
	return 0
}
