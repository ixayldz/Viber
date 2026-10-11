package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runDeliveryApply(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("apply", errout)
	directory := f.String("store", "", "private session store")
	output := f.String("output", "", "fresh review package directory")
	exclusiveNTFS := f.Bool("exclusive-ntfs", false, "opt into the Windows fixed-NTFS exclusive-handle apply profile")
	jsonMode := f.Bool("json", false, "structured apply preflight")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" || *output == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task, store and fresh review package output required"), *jsonMode)
	}
	session, err := agent.OpenExisting(context.Background(), *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	result, err := session.DeliveryApply(context.Background(), task, *output, agent.DeliveryApplyOptions{ExclusiveNTFS: *exclusiveNTFS})
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, result); err != nil {
			return 4
		}
	} else {
		if result.Status == "CONFLICT" {
			fmt.Fprintf(out, "Delivery apply blocked by conflicts · live workspace written: false · review package: %s · %s\n", result.PackageDirectory, result.ManifestDigest)
		} else if result.Status == "APPLIED_EXACT_TOUCHED_FILES" {
			fmt.Fprintf(out, "Applied exact touched files · workspace quality: %s · checkpoint: %s · receipt: %s\n", result.WorkspaceQuality, result.OperationID, result.ReceiptDigest)
		} else if result.Status == "UNKNOWN_EFFECT" {
			fmt.Fprintf(out, "Delivery outcome is UNKNOWN · do not edit or restore touched files until reconciled · checkpoint: %s · plan: %s\n", result.OperationID, result.PlanDirectory)
		} else {
			fmt.Fprintf(out, "Delivery status: %s · live workspace written: %t · fresh review package: %s · manifest: %s\n", result.Status, result.TargetWritten, result.PackageDirectory, result.ManifestDigest)
		}
	}
	if result.Status == "CONFLICT" || result.Status == "CONFLICT_STALE_TARGET" {
		return 1
	}
	if result.Status == "APPLIED_EXACT_TOUCHED_FILES" || result.Status == "NO_LIVE_CHANGES" {
		return 0
	}
	if result.Status == "UNKNOWN_EFFECT" || result.Status == "UNSUPPORTED_STRONG_EXCLUSIVITY" || result.Status == "UNSUPPORTED_NATIVE_CHANGESET" || result.Status == "UNSUPPORTED_NATIVE_PROFILE" || result.Status == "EXCLUSIVE_HANDLE_DENIED" {
		return 4
	}
	if result.Status != "UNSUPPORTED_STRONG_EXCLUSIVITY" || result.TargetWritten {
		return report(out, errout, c.Fail(c.StoreIntegrityError, "apply preflight returned an unexpected live-write state"), *jsonMode)
	}
	return 4
}
