package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runDeliveryReconcile(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("delivery-reconcile", errout)
	directory := f.String("store", "", "private session store")
	checkpoint := f.String("checkpoint", "", "exact UNKNOWN delivery operation ID")
	jsonMode := f.Bool("json", false, "structured reconciliation result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" || *checkpoint == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task, store and exact UNKNOWN checkpoint required"), *jsonMode)
	}
	if len(*checkpoint) != 32 {
		return report(out, errout, c.Fail(c.InvalidArgument, "checkpoint must be the exact 32-character lowercase operation ID"), *jsonMode)
	}
	for _, r := range *checkpoint {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return report(out, errout, c.Fail(c.InvalidArgument, "checkpoint must be the exact 32-character lowercase operation ID"), *jsonMode)
		}
	}
	session, err := agent.OpenExisting(context.Background(), *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	result, err := session.DeliveryReconcile(context.Background(), task, *checkpoint)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, result); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Delivery reconciliation: %s · target written: %t · checkpoint: %s · receipt: %s · %s\n", result.Status, result.TargetWritten, result.OperationID, result.ReceiptDigest, result.Reason)
	}
	if strings.HasPrefix(result.Status, "RECONCILED_") || result.Status == "NO_UNKNOWN_EFFECT" {
		return 0
	}
	return 4
}
