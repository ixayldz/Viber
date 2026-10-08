package cli

import (
	"context"
	"encoding/json"
	"flag"
	"io"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runObservation(command string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(command, errout)
	directory := f.String("store", "", "private existing session store")
	offset := f.Int64("offset", 0, "byte offset")
	limit := f.Int64("limit", 16384, "exact-byte page size")
	runID := f.String("run-id", "", "retained check run ID")
	stream := f.String("stream", "stdout", "stdout or stderr")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task required"), *jsonMode)
	}
	invalid := false
	f.Visit(func(v *flag.Flag) {
		if (v.Name == "offset" || v.Name == "limit") && command != "context-page" && command != "check-output" || (v.Name == "run-id" || v.Name == "stream") && command != "check-output" {
			invalid = true
		}
	})
	if invalid || task == "" || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task/store and command-specific page flags required"), *jsonMode)
	}
	query := agent.Observation{Kind: command}
	if command == "context-page" || command == "check-output" {
		query.Offset = *offset
		query.Limit = *limit
	}
	if command == "check-output" {
		query.RunID = *runID
		query.Stream = *stream
	}
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	raw, routed, err := ownerCall(context.Background(), *directory, task, command, id, query)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var value any = json.RawMessage(raw)
	if !routed {
		session, err := agent.OpenExisting(context.Background(), *directory)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		defer session.Close()
		value, err = session.Observe(context.Background(), task, query)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	// JSON is also the text-mode representation: byte pages are base64, so source
	// content cannot inject terminal control sequences.
	if err = jsonWrite(out, value); err != nil {
		return 4
	}
	return 0
}
