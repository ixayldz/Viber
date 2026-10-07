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
	"strconv"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/kernel"
	"github.com/ixayldz/Viber/internal/owner"
)

func taskResult(out, errout io.Writer, state c.TaskState, doc agent.Document, jsonMode bool) int {
	if jsonMode {
		if err := jsonWrite(out, struct {
			Context       *agent.ContextAudit `json:"context,omitempty"`
			SchemaVersion int                 `json:"schema_version"`
			State         c.TaskState         `json:"state"`
			Budget        agent.Budget        `json:"budget"`
			Blocker       string              `json:"blocker"`
			Summary       string              `json:"untrusted_model_summary"`
			Candidate     artifact.Ref        `json:"candidate"`
			ReleaseReady  bool                `json:"release_ready"`
		}{doc.Context, 1, state, doc.Budget, doc.Blocker, doc.FinalSummary, doc.Candidate, false}); err != nil {
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
	_, err = session.Create(ctx, agent.StartOptions{Root: source, Prompt: []byte(prompt), Git: *git, TaskID: *task, Budget: budget, Autonomy: *autonomy, AllowUnverified: *allow, Fixture: raw})
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	host, err := owner.Open(context.Background(), *directory, session)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer host.Close()
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	view, err := host.Run(ctx, *task, id)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if exit := resultView(out, errout, view, *jsonMode); exit != 0 {
		return exit
	}
	return kernel.InvocationExit(view.State, ctx.Err() != nil)
}
func runTaskControl(command string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(command, errout)
	directory := f.String("store", "", "private session store")
	commandID := f.String("command-id", "", "stable command ID for reconciliation and deduplication")
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
		return report(out, errout, c.Fail(c.InvalidArgument, "task and explicit store required"), *jsonMode)
	}
	id, err := resolveCommandID(*commandID)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	raw, routed, err := ownerCall(ctx, *directory, task, command, id, nil)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result any
	if routed {
		if command == "diff" {
			var changes []owner.DiffChange
			err = c.DecodeStrict(raw, &changes)
			result = changes
		} else {
			var view owner.View
			err = c.DecodeStrict(raw, &view)
			result = view
		}
	} else {
		session, openErr := agent.OpenExisting(ctx, *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		host, openErr := owner.Open(context.Background(), *directory, session)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer host.Close()
		result, err = host.Handle(ctx, ipc.Request{ID: id, TaskID: task, Command: command, Payload: []byte("null")})
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if command == "diff" {
		changes := result.([]owner.DiffChange)
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
	view := result.(owner.View)
	if exit := resultView(out, errout, view, *jsonMode); exit != 0 {
		return exit
	}
	if command == "resume" {
		return kernel.InvocationExit(view.State, ctx.Err() != nil)
	}
	return 0
}
