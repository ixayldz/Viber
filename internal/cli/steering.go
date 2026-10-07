package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
)

func runSteering(command string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	text := ""
	if command == "steer" {
		text, args = promptFirst(args)
	}
	f := flags(command, errout)
	directory := f.String("store", "", "private session store")
	commandID := f.String("command-id", "", "stable steering command ID")
	revisionFile := f.String("revision-file", "", "bound scope revision JSON with fresh offline fixture")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || task == "" || *directory == "" || command == "steer" && (text == "" || *revisionFile != "") || command == "revise" && (*revisionFile == "" || *commandID != "") {
		return report(out, errout, c.Fail(c.InvalidArgument, "task/store and steering text or revision file required"), *jsonMode)
	}
	id, err := resolveCommandID(*commandID)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var payload any
	if command == "steer" {
		payload = agent.SteeringInput{CommandID: id, TaskID: task, Text: text}
	} else {
		var revision agent.ScopeRevision
		if err = readJSON(*revisionFile, &revision); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if revision.TaskID != task || revision.CommandID == "" {
			return report(out, errout, c.Fail(c.StaleRequest, "revision task/command binding mismatch"), *jsonMode)
		}
		id = revision.CommandID
		payload = revision
	}
	raw, routed, err := ownerCall(context.Background(), *directory, task, command, id, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result any
	if routed {
		if command == "steer" {
			var view owner.View
			err = c.DecodeStrict(raw, &view)
			result = view
		} else {
			var state c.TaskState
			err = c.DecodeStrict(raw, &state)
			result = state
		}
	} else {
		session, openErr := agent.OpenExisting(context.Background(), *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		host, openErr := owner.Open(context.Background(), *directory, session)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer host.Close()
		encoded, encodeErr := c.CanonicalV1(payload)
		if encodeErr != nil {
			return report(out, errout, encodeErr, *jsonMode)
		}
		result, err = host.Handle(context.Background(), ipc.Request{ID: id, TaskID: task, Command: command, Payload: encoded})
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if command == "steer" {
		return resultView(out, errout, result.(owner.View), *jsonMode)
	}
	state := result.(c.TaskState)
	if *jsonMode {
		if err = jsonWrite(out, state); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Scope revised · task %s · spec %d · epoch %d · explicit resume required\n", strconv.QuoteToASCII(task), state.SpecVersion, state.PolicyEpoch)
	}
	return 0
}
