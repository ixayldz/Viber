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
	ctx := context.Background()
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	raw, routed, err := ownerCall(ctx, *directory, task, "delivery-export", id, struct {
		Output string `json:"output"`
	}{*output})
	var manifest agent.ExportManifest
	if err == nil && routed {
		err = c.DecodeStrict(raw, &manifest)
	} else if err == nil {
		session, openErr := agent.OpenExisting(ctx, *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		manifest, err = session.Export(ctx, task, *output)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if manifest.SchemaVersion != 1 || manifest.TaskID != task || manifest.State.TaskID != task || manifest.LiveWorkspaceWritten {
		return report(out, errout, c.Fail(c.StoreIntegrityError, "delivery response identity mismatch"), *jsonMode)
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
