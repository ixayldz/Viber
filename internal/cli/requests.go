package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runRequests(command string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(command, errout)
	directory := f.String("store", "", "private session store")
	requestID := f.String("request", "", "exact request ID")
	responsePath := f.String("response-file", "", "bound response JSON")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" || command == "respond" && (*requestID == "" || *responsePath == "") || command == "requests" && (*requestID != "" || *responsePath != "") {
		return report(out, errout, c.Fail(c.InvalidArgument, "task/store and bound response flags required"), *jsonMode)
	}
	var response agent.UserResponse
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var payload any
	if command == "respond" {
		if err = readJSON(*responsePath, &response); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if response.TaskID != task || response.RequestID != *requestID {
			return report(out, errout, c.Fail(c.StaleRequest, "response task/request binding mismatch"), *jsonMode)
		}
		id, err = resolveCommandID(response.CommandID)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		payload = response
	}
	raw, routed, err := ownerCall(context.Background(), *directory, task, command, id, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var requests []agent.UserRequest
	var state c.TaskState
	if routed {
		if command == "requests" {
			err = c.DecodeStrict(raw, &requests)
		} else {
			err = c.DecodeStrict(raw, &state)
		}
	} else {
		var session *agent.Session
		session, err = agent.OpenExisting(context.Background(), *directory)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		defer session.Close()
		if command == "requests" {
			requests, err = session.Requests(context.Background(), task)
		} else {
			state, err = session.Respond(context.Background(), response)
		}
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if command == "requests" {
		if *jsonMode {
			if err = jsonWrite(out, requests); err != nil {
				return 4
			}
		} else {
			for _, r := range requests {
				fmt.Fprintf(out, "%s %s %s · candidate %s · action %s\n", strconv.QuoteToASCII(r.ID), r.Kind, r.Status, r.Candidate, r.ActionDigest)
			}
		}
	} else if *jsonMode {
		if err = jsonWrite(out, state); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Response recorded · task %s · sequence %d · explicit resume required\n", strconv.QuoteToASCII(task), state.TaskSeq)
	}
	return 0
}
