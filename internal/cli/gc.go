package cli

import (
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"io"
	"os"
	"os/signal"
)

func runGC(action string, args []string, out, errout io.Writer) int {
	f := flags(action, errout)
	directory := f.String("store", "", "existing private store")
	input := f.String("command-file", "", "reviewed GC command containing preview and stable ID")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" || action == "store-gc" && *input == "" || action == "store-gc-preview" && *input != "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "GC preview requires store; collection requires a reviewed command-file"), *jsonMode)
	}
	var command agent.GCCommand
	var payload any
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if action == "store-gc" {
		if err = readJSON(*input, &command); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		id = command.CommandID
		payload = command
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	raw, routed, err := ownerCall(ctx, *directory, "", action, id, payload)
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
	if action == "store-gc" {
		result, err = session.CollectGarbage(ctx, command)
	} else {
		result, err = session.GCPreview(ctx)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, result); err != nil {
		return 4
	}
	return 0
}
