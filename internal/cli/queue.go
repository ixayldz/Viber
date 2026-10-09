package cli

import (
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
	"io"
)

func runQueue(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("queue", errout)
	directory := f.String("store", "", "existing private store")
	input := f.String("command-file", "", "bound add/remove/activate command; absent lists queue")
	jsonMode := f.Bool("json", false, "JSON result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || task == "" || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "queue requires task/store"), *jsonMode)
	}
	action := "queue-list"
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var payload any
	if *input != "" {
		var command agent.QueueCommand
		if err = readJSON(*input, &command); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if command.TaskID != task || command.CommandID == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "queue command binding mismatch"), *jsonMode)
		}
		id = command.CommandID
		action = "queue-control"
		payload = command
	}
	ctx := context.Background()
	raw, routed, err := ownerCall(ctx, *directory, task, action, id, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result any = json.RawMessage(raw)
	if !routed {
		s, err := agent.OpenExisting(ctx, *directory)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		defer s.Close()
		h, err := owner.Open(ctx, *directory, s)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		defer h.Close()
		encoded, err := c.CanonicalV1(payload)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		result, err = h.Handle(ctx, ipc.Request{ID: id, TaskID: task, Command: action, Payload: encoded})
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if err = jsonWrite(out, result); err != nil {
		return 4
	}
	return 0
}
