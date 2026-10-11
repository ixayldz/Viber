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
	result, err := session.DeliveryApply(context.Background(), task, *output)
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
		} else {
			fmt.Fprintf(out, "Live apply unsupported · live workspace written: false · fresh review package: %s · manifest: %s\n", result.PackageDirectory, result.ManifestDigest)
		}
	}
	if result.Status == "CONFLICT" {
		return 1
	}
	if result.Status != "UNSUPPORTED_STRONG_EXCLUSIVITY" || result.TargetWritten {
		return report(out, errout, c.Fail(c.StoreIntegrityError, "apply preflight returned an unexpected live-write state"), *jsonMode)
	}
	return 4
}
