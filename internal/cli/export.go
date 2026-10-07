package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runExport(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("export", errout)
	directory := f.String("store", "", "private session store")
	output := f.String("output", "", "new changeset directory with existing parent")
	jsonMode := f.Bool("json", false, "manifest result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" || *output == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task, store and new output directory required"), *jsonMode)
	}
	session, err := agent.OpenExisting(context.Background(), *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	manifest, err := session.Export(context.Background(), task, *output)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, manifest); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Export complete · %d changes · text patch complete: %t · quality %s\n", len(manifest.Changeset.Changes), manifest.Changeset.PatchComplete, manifest.State.Quality)
	}
	return 0
}
