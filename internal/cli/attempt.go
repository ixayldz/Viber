package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func runAttempt(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("attempt", errout)
	directory := f.String("store", "", "private existing store")
	newTask := f.String("new-task", "", "stable distinct new task ID")
	parentSeq := f.Int64("parent-seq", 0, "exact terminal parent task sequence from status")
	fixtureFile := f.String("fixture", "", "fresh fixture for fixture parent; omit for local parent")
	allow := f.Bool("allow-unverified", false, "allow limited UNVERIFIED result in the new attempt")
	jsonMode := f.Bool("json", false, "structured creation state")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one parent task required"), *jsonMode)
	}
	if task == "" || *directory == "" || *newTask == "" || *parentSeq < 1 {
		return report(out, errout, c.Fail(c.InvalidArgument, "parent task, store, new-task and exact parent-seq required"), *jsonMode)
	}
	var fixture []byte
	if *fixtureFile != "" {
		path, err := filepath.Abs(*fixtureFile)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		fixture, err = fileguard.ReadRegular(root, filepath.Base(path), 8<<20)
		closeErr := root.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if _, err = agent.ParseFixture(fixture); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	options := agent.AttemptOptions{ParentTask: task, NewTask: *newTask, ExpectedParentSequence: *parentSeq, Fixture: fixture, AllowUnverified: *allow}
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	raw, routed, err := ownerCall(context.Background(), *directory, task, "attempt", id, options)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var value any = json.RawMessage(raw)
	if !routed {
		s, err := agent.OpenExisting(context.Background(), *directory)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		defer s.Close()
		value, err = s.NewAttempt(context.Background(), options)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if err = jsonWrite(out, value); err != nil {
		return 4
	}
	return 0
}
