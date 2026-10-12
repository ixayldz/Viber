package cli

import (
	"context"
	"io"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runRetrievalReport(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("retrieval-report", errout)
	directory := f.String("store", "", "private existing session store")
	callID := f.String("call-id", "", "retained fs_search model call ID")
	jsonMode := f.Bool("json", false, "structured exact retrieval evidence")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task required"), *jsonMode)
	}
	if task == "" || *directory == "" || *callID == "" || !*jsonMode {
		return report(out, errout, c.Fail(c.InvalidArgument, "task, store, retained fs_search call ID, and --json are required"), *jsonMode)
	}
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	query := struct {
		CallID string `json:"call_id"`
	}{*callID}
	raw, routed, err := ownerCall(context.Background(), *directory, task, "retrieval-report", id, query)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var value agent.CapturedRetrievalReport
	if routed {
		if err = c.DecodeStrict(raw, &value); err != nil {
			return report(out, errout, c.Fail(c.StoreIntegrityError, "owner returned an invalid captured retrieval report"), *jsonMode)
		}
	} else {
		session, openErr := agent.OpenExisting(context.Background(), *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		value, err = session.CapturedRetrievalReport(context.Background(), task, *callID)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	// Encoded JSON escapes terminal control bytes in the exact retained reply.
	if err = jsonWrite(out, value); err != nil {
		return 4
	}
	return 0
}
