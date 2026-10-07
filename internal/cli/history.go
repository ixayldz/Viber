package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/ixayldz/Viber/internal/agent"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runHistory(command string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(command, errout)
	directory := f.String("store", "", "private session store")
	at := f.Int64("at", 0, "historical task sequence for inspect (0=current)")
	until := f.Int64("until", 0, "final task sequence for replay (0=current)")
	after := f.Int64("after", 0, "exclusive task sequence for events")
	limit := f.Int("limit", 64, "event page size, 1..256")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	invalid := false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "at" && command != "inspect" || value.Name == "until" && command != "replay" || (value.Name == "after" || value.Name == "limit") && command != "events" {
			invalid = true
		}
	})
	if invalid || task == "" || *directory == "" || *at < 0 || *until < 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "invalid task/store/history flags"), *jsonMode)
	}
	session, err := agent.OpenExisting(context.Background(), *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	if command == "events" {
		page, err := session.Journal.History(context.Background(), task, *after, *limit)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if *jsonMode {
			if err = jsonWrite(out, page); err != nil {
				return 4
			}
		} else {
			for _, record := range page.Records {
				fmt.Fprintf(out, "%d %s · %s\n", record.Event.TaskSeq, strconv.QuoteToASCII(record.Event.Type), strconv.QuoteToASCII(record.Event.ID))
			}
			fmt.Fprintf(out, "Next cursor %d · head %d · more %t\n", page.Next, page.Head, page.HasMore)
		}
		return 0
	}
	sequence := *at
	if command == "replay" {
		sequence = *until
	}
	state, doc, err := session.Inspect(context.Background(), task, sequence)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		err = jsonWrite(out, struct {
			SchemaVersion   int          `json:"schema_version"`
			State           c.TaskState  `json:"state"`
			Budget          agent.Budget `json:"budget"`
			Blocker         string       `json:"blocker"`
			Candidate       artifact.Ref `json:"candidate"`
			EffectsExecuted bool         `json:"effects_executed"`
		}{SchemaVersion: 1, State: state, Budget: doc.Budget, Blocker: doc.Blocker, Candidate: doc.Candidate})
		if err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "%s · task sequence %d · %s · quality %s · effects executed: false\n", strconv.QuoteToASCII(task), state.TaskSeq, state.Execution, state.Quality)
	}
	return 0
}
