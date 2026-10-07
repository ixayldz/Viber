package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

func runSnapshotSave(args []string, out, errout io.Writer) int {
	f := flags("snapshot-save", errout)
	root := f.String("root", ".", "source root")
	directory := f.String("store", "", "private archive outside source")
	task := f.String("task", "", "task scope")
	git := f.Bool("git", false, "native Git capture including index and ignore rules")
	jsonMode := f.Bool("json", false, "JSON reference")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" || *task == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "store and task required"), *jsonMode)
	}
	var capture workspace.Capture
	var err error
	if *git {
		capture, err = workspace.CaptureRepository(*root, workspace.DefaultLimits())
	} else {
		capture, err = workspace.CaptureDirectory(*root, workspace.DefaultLimits())
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	absolute, err := filepath.Abs(*directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if !fileguard.Disjoint(absolute, capture.Snapshot.Root) {
		return report(out, errout, c.Fail(c.PolicyDenied, "store must be outside source"), *jsonMode)
	}
	archive, err := artifact.Open(*directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer archive.Close()
	ref, err := archive.Put(*task, capture)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, ref); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Saved %s · task %s · %d files · BEST_EFFORT\n", ref.SnapshotDigest, strconv.QuoteToASCII(ref.TaskID), len(capture.Snapshot.Entries))
	}
	return 0
}
func runSnapshotList(args []string, out, errout io.Writer) int {
	f := flags("snapshot-list", errout)
	directory := f.String("store", "", "private archive")
	task := f.String("task", "", "task scope")
	jsonMode := f.Bool("json", false, "JSON references")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" || *task == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "store and task required"), *jsonMode)
	}
	archive, err := artifact.Open(*directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer archive.Close()
	refs, err := archive.List(*task)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, refs); err != nil {
			return 4
		}
	} else {
		for _, ref := range refs {
			fmt.Fprintln(out, ref.SnapshotDigest)
		}
	}
	return 0
}
func runProposalMaterialize(args []string, out, errout io.Writer) int {
	f := flags("proposal-materialize", errout)
	directory := f.String("store", "", "private archive")
	baseDigest := f.String("base", "", "saved baseline digest")
	file := f.String("proposal", "", "proposal JSON")
	policyFile := f.String("policy", "", "trusted policy layers")
	authorityFile := f.String("authority", "", "current trusted state")
	jsonMode := f.Bool("json", false, "JSON candidate")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" || *baseDigest == "" || *file == "" || *policyFile == "" || *authorityFile == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "store, base, proposal, policy and authority required"), *jsonMode)
	}
	var p c.Proposal
	var layers []policy.Policy
	var authority c.TaskState
	if err := readJSON(*file, &p); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err := readJSON(*policyFile, &layers); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err := readJSON(*authorityFile, &authority); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	archive, err := artifact.Open(*directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer archive.Close()
	base, err := archive.Get(artifact.Ref{SchemaVersion: c.SchemaVersion, TaskID: p.TaskID, SnapshotDigest: *baseDigest})
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var current workspace.Capture
	if base.Snapshot.Git != nil {
		current, err = workspace.CaptureRepository(base.Snapshot.Root, workspace.DefaultLimits())
	} else {
		current, err = workspace.CaptureDirectory(base.Snapshot.Root, workspace.DefaultLimits())
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	next, err := workspace.Preview(base, current, p, layers, authority)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	ref, err := archive.Put(p.TaskID, next)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	materialized, err := archive.Materialize(ref)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, materialized); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Candidate %s\nSource %s\nLive workspace unchanged · runtime readonly enforcement pending\n", ref.SnapshotDigest, strconv.QuoteToASCII(materialized.SourceDirectory))
	}
	return 0
}
