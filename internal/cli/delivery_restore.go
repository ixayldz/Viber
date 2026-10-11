package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

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
	if task == "" || *directory == "" || *checkpoint == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task, store and exact applied checkpoint required"), *jsonMode)
	}
	if !validDeliveryOperationID(*checkpoint) {
		return report(out, errout, c.Fail(c.InvalidArgument, "checkpoint must be the exact 32-character lowercase operation ID"), *jsonMode)
	}
	session, err := agent.OpenExisting(context.Background(), *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	result, err := session.DeliveryRestore(context.Background(), task, *checkpoint)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, result); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Restore status: %s · live workspace written: %t · applied parent: %s · restore operation: %s · receipt: %s · %s\n", result.Status, result.TargetWritten, result.OperationID, result.RestoreOperationID, result.ReceiptDigest, result.Reason)
		if result.Status == "UNKNOWN_EFFECT" && validDeliveryOperationID(result.RestoreOperationID) {
			fmt.Fprintf(out, "Reconcile this UNKNOWN restore with: delivery-reconcile --store <store> --checkpoint %s %s\n", result.RestoreOperationID, task)
		}
	}
	if result.Status == "RESTORED_EXACT_TOUCHED_FILES" || result.Status == "ALREADY_RESTORED" {
		return 0
	}
	if result.Status == "RESTORE_CONFLICT" || result.Status == "UNKNOWN_EFFECT_PENDING" || result.Status == "UNSUPPORTED_NO_APPLIED_RECEIPT" || result.Status == "RESTORE_REQUIRES_CHECKPOINT" {
		return 4
	}
	return 4
}

func validDeliveryOperationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
