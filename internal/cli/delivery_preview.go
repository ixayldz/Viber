package cli

import (
	"context"
	"fmt"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"io"
)

func runDeliveryPreview(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("delivery-preview", errout)
	directory := f.String("store", "", "private session store")
	output := f.String("output", "", "fresh private preview directory")
	jsonMode := f.Bool("json", false, "bound preview manifest")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" || *output == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task, store and fresh output required"), *jsonMode)
	}
	session, err := agent.OpenExisting(context.Background(), *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	manifest, err := session.DeliveryPreview(context.Background(), task, *output)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, manifest); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Delivery preview %s · merged candidate UNVERIFIED · live workspace written: false · %s\n", manifest.Preview.Status, *output)
	}
	if manifest.Preview.Status == "CONFLICT" {
		return 1
	}
	return 0
}
