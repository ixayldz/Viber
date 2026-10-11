package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runDeliveryRestore(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("restore", errout)
	directory := f.String("store", "", "private session store")
	checkpoint := f.String("checkpoint", "", "owned live apply receipt ID")
	jsonMode := f.Bool("json", false, "structured restore result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task and store required"), *jsonMode)
	}
	session, err := agent.OpenExisting(context.Background(), *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	result, err := session.DeliveryRestore(context.Background(), task)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *checkpoint != "" {
		if len(*checkpoint) > 128 || !utf8.ValidString(*checkpoint) || strings.ContainsAny(*checkpoint, "\x00\r\n\x1b") {
			return report(out, errout, c.Fail(c.InvalidArgument, "invalid checkpoint ID"), *jsonMode)
		}
		result.Reason += "; the supplied checkpoint has no registered applied ownership receipt"
	}
	if *jsonMode {
		if err = jsonWrite(out, result); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Restore unavailable · live workspace written: false · %s\n", result.Reason)
	}
	return 4
}
