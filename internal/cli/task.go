package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/kernel"
)

func taskResult(out, errout io.Writer, state c.TaskState, doc agent.Document, jsonMode bool) int {
	if jsonMode {
		if err := jsonWrite(out, struct {
			SchemaVersion int          `json:"schema_version"`
			State         c.TaskState  `json:"state"`
			Budget        agent.Budget `json:"budget"`
			Blocker       string       `json:"blocker"`
			Summary       string       `json:"untrusted_model_summary"`
			Candidate     artifact.Ref `json:"candidate"`
			ReleaseReady  bool         `json:"release_ready"`
		}{1, state, doc.Budget, doc.Blocker, doc.FinalSummary, doc.Candidate, false}); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Task %s · %s / %s\nQuality %s · fulfillment %s · candidate %s\n", strconv.QuoteToASCII(state.TaskID), state.Execution, state.Outcome, state.Quality, state.Fulfillment, state.CandidateDigest)
		if doc.Blocker != "" {
			fmt.Fprintf(out, "Blocker %s\n", strconv.QuoteToASCII(doc.Blocker))
		}
		if doc.FinalReady {
			fmt.Fprintf(out, "Model summary %s\n", strconv.QuoteToASCII(doc.FinalSummary))
		}
		fmt.Fprintln(out, "Offline fixture engineering profile · stable release gates remain closed")
	}
	return 0
}
func promptFirst(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}
func runTask(args []string, out, errout io.Writer) int {
	prompt, args := promptFirst(args)
	f := flags("run", errout)
	root := f.String("root", ".", "live source root")
	directory := f.String("store", "", "private session store outside source")
	task := f.String("task", "", "task ID (default random)")
	fixtureFile := f.String("fixture", "", "offline model fixture JSON")
	offline := f.Bool("offline", false, "offline fixture profile only")
	git := f.Bool("git", false, "native Git-aware capture")
	allow := f.Bool("allow-unverified", false, "explicitly allow a limited UNVERIFIED candidate-only result (exit 2)")
	autonomy := f.String("autonomy", "guided", "review, guided or auto native tools")
	steps := f.Int64("max-steps", 16, "bounded model turns")
	tools := f.Int64("max-tool-calls", 64, "bounded native tool calls")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if prompt == "" && f.NArg() == 1 {
		prompt = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one raw task prompt required"), *jsonMode)
	}
	if !*offline || *fixtureFile == "" {
		report(out, errout, c.Fail(c.UnsupportedCapability, "only explicit --offline --fixture profile is available; production backend/provider acceptance is pending"), *jsonMode)
		return 3
	}
	if prompt == "" || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "raw prompt and session store required"), *jsonMode)
	}
	source, err := filepath.Abs(*root)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	target, err := filepath.Abs(*directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if !fileguard.Disjoint(source, target) {
		return report(out, errout, c.Fail(c.PolicyDenied, "session store must not overlap source"), *jsonMode)
	}
	file, err := os.Open(*fixtureFile)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	raw, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if _, err = agent.ParseFixture(raw); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *task == "" {
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		*task = "task-" + hex.EncodeToString(nonce[:])
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	session, err := agent.Open(ctx, *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	budget := agent.DefaultBudget()
	budget.MaxSteps = *steps
	budget.MaxToolCalls = *tools
	state, err := session.Create(ctx, agent.StartOptions{Root: source, Prompt: []byte(prompt), Git: *git, TaskID: *task, Budget: budget, Autonomy: *autonomy, AllowUnverified: *allow, Fixture: raw})
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	state, err = session.Run(ctx, *task)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	_, doc, err := session.Load(context.Background(), *task)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if exit := taskResult(out, errout, state, doc, *jsonMode); exit != 0 {
		return exit
	}
	return kernel.InvocationExit(state, ctx.Err() != nil)
}
func runTaskControl(command string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(command, errout)
	directory := f.String("store", "", "private session store")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task ID required"), *jsonMode)
	}
	if task == "" || *directory == "" {
		report(out, errout, c.Fail(c.UnsupportedCapability, "task and explicit --store required for offline engineering sessions"), *jsonMode)
		if command == "resume" {
			return 3
		}
		return 4
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	session, err := agent.OpenExisting(ctx, *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	var state c.TaskState
	switch command {
	case "resume":
		state, err = session.Run(ctx, task)
	case "pause", "cancel":
		state, err = session.Controls(ctx, task, command)
	default:
		state, err = session.State(ctx, task)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	_, doc, err := session.Load(context.Background(), task)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if command == "diff" {
		before, err := session.Archive.Get(doc.Baseline)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		after, err := session.Archive.Get(doc.Candidate)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		type change struct {
			Path    string `json:"path"`
			Before  string `json:"before_digest"`
			After   string `json:"after_digest"`
			Deleted bool   `json:"deleted"`
		}
		paths := map[string]bool{}
		for name := range before.Contents {
			paths[name] = true
		}
		for name := range after.Contents {
			paths[name] = true
		}
		names := []string{}
		for name := range paths {
			names = append(names, name)
		}
		sort.Strings(names)
		changes := []change{}
		for _, name := range names {
			old, oldOK := before.Contents[name]
			next, nextOK := after.Contents[name]
			oldHash, nextHash := "", ""
			if oldOK {
				oldHash = c.HashBytes(old)
			}
			if nextOK {
				nextHash = c.HashBytes(next)
			}
			if oldOK == nextOK && oldHash == nextHash {
				continue
			}
			changes = append(changes, change{Path: name, Before: oldHash, After: nextHash, Deleted: !nextOK})
		}
		if *jsonMode {
			if err = jsonWrite(out, changes); err != nil {
				return 4
			}
		} else {
			for _, change := range changes {
				fmt.Fprintf(out, "%s %s -> %s\n", strconv.QuoteToASCII(change.Path), change.Before, change.After)
			}
		}
		return 0
	}
	if exit := taskResult(out, errout, state, doc, *jsonMode); exit != 0 {
		return exit
	}
	if command == "resume" {
		return kernel.InvocationExit(state, ctx.Err() != nil)
	}
	return 0
}
