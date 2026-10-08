package cli

import (
	"context"
	"fmt"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
	"io"
	"strconv"
)

func runTokenLedger(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("budget", errout)
	directory := f.String("store", "", "private session store")
	jsonMode := f.Bool("json", false, "structured ledger view")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task and existing store required"), *jsonMode)
	}
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	raw, routed, err := ownerCall(context.Background(), *directory, task, "budget", id, nil)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var view store.TokenLedgerView
	if routed {
		err = c.DecodeStrict(raw, &view)
	} else {
		session, openErr := agent.OpenExisting(context.Background(), *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		if _, err = session.State(context.Background(), task); err == nil {
			view, err = session.Journal.TokenLedger(context.Background())
		}
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, view); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Token ledger %s · tasks %d · unknown %d\nInput charged %d + reserved %d / %d · output charged %d + reserved %d / %d\n", strconv.QuoteToASCII(view.Coverage), view.Tasks, view.Unknown, view.Used.Input, view.Reserved.Input, view.Limits.Input, view.Used.Output, view.Reserved.Output, view.Limits.Output)
	}
	return 0
}
