package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
)

func resolveCommandID(id string) (string, error) {
	if id == "" {
		return ipc.NewID()
	}
	if len(id) > 128 {
		return "", c.Fail(c.InvalidArgument, "command ID exceeds 128 bytes")
	}
	return id, nil
}

// A lost response after dispatch never falls back to another local execution.
// An unreachable leftover descriptor is reconciled by acquiring the OS store lock.
func ownerCall(ctx context.Context, directory, task, command, id string, payload any) (json.RawMessage, bool, error) {
	info, err := ipc.Discover(directory)
	if errors.Is(err, ipc.ErrNoOwner) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	raw, err := c.CanonicalV1(payload)
	if err != nil {
		return nil, true, err
	}
	response, err := ipc.Call(ctx, info, ipc.Request{ID: id, Command: command, TaskID: task, Payload: raw})
	if err != nil {
		var call *ipc.CallError
		if errors.As(err, &call) {
			if call.Dispatched {
				return nil, true, c.Fail(c.UnknownOutcome, "owner response lost for command "+id+"; inspect task/events before another command; no automatic retry")
			}
			if ctx.Err() != nil {
				return nil, true, ctx.Err()
			}
			return nil, false, nil
		}
		return nil, true, err
	}
	return response.Result, true, nil
}
func resultView(out, errout io.Writer, view owner.View, jsonMode bool) int {
	return taskResult(out, errout, view.State, agent.Document{Budget: view.Budget, Blocker: view.Blocker, FinalSummary: view.Summary, FinalReady: view.FinalReady, Candidate: view.Candidate}, jsonMode)
}
func runServe(args []string, out, errout io.Writer) int {
	f := flags("serve", errout)
	directory := f.String("store", "", "private session store")
	jsonMode := f.Bool("json", false, "owner readiness JSON")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "explicit store required"), *jsonMode)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return serveOwner(ctx, *directory, out, errout, *jsonMode)
}
func serveOwner(ctx context.Context, directory string, out, errout io.Writer, jsonMode bool) int {
	session, err := agent.OpenExisting(ctx, directory)
	if err != nil {
		return report(out, errout, err, jsonMode)
	}
	defer session.Close()
	host, err := owner.Open(context.Background(), directory, session)
	if err != nil {
		return report(out, errout, err, jsonMode)
	}
	defer host.Close()
	if jsonMode {
		if err = jsonWrite(out, struct {
			SchemaVersion int      `json:"schema_version"`
			Owner         ipc.Info `json:"owner"`
			ReleaseReady  bool     `json:"release_ready"`
		}{1, host.Server.Info, false}); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Owner ready · PID %d · generation %d · store %s\n", host.Server.Info.PID, host.Server.Info.Generation, strconv.QuoteToASCII(directory))
	}
	select {
	case <-ctx.Done():
	case <-host.Done():
	}
	if err = host.Close(); err != nil {
		return report(out, errout, err, jsonMode)
	}
	return 0
}
