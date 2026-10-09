package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runDeliveryReverification(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("delivery-reverify", errout)
	directory := f.String("store", "", "private existing session store")
	commandFile := f.String("command-file", "", "reviewed source-bound creation command JSON")
	jsonMode := f.Bool("json", false, "new immutable task state")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one parent task required"), *jsonMode)
	}
	if task == "" || *directory == "" || *commandFile == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "parent task, existing store and command file required"), *jsonMode)
	}
	var command agent.DeliveryReverificationCommand
	if err := readJSON(*commandFile, &command); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if command.ParentTask != task || command.CommandID == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "delivery command task/ID binding mismatch"), *jsonMode)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	raw, routed, err := ownerCall(ctx, *directory, task, "delivery-reverify", command.CommandID, command)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var state c.TaskState
	if routed {
		err = c.DecodeStrict(raw, &state)
	} else {
		session, openErr := agent.OpenExisting(ctx, *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		state, err = session.CreateDeliveryReverification(ctx, command)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, state); err != nil {
			return 4
		}
	} else {
		encoded, _ := json.Marshal(state.TaskID)
		fmt.Fprintf(out, "Merged task %s · quality %s · live workspace written: false\nRun registered checks: viber resume %s --store PATH\n", encoded, state.Quality, encoded)
	}
	return 0
}
