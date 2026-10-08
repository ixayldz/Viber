package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
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
	"github.com/ixayldz/Viber/internal/plan"
)

func taskResult(out, errout io.Writer, state c.TaskState, doc agent.Document, jsonMode bool, protection *agent.ProtectionInfo) int {
	if jsonMode {
		if err := jsonWrite(out, struct {
			Runtime       *agent.LocalRuntime   `json:"runtime,omitempty"`
			Plan          *plan.State           `json:"plan,omitempty"`
			Protection    *agent.ProtectionInfo `json:"check_protection,omitempty"`
			Context       *agent.ContextAudit   `json:"context,omitempty"`
			SchemaVersion int                   `json:"schema_version"`
			State         c.TaskState           `json:"state"`
			Budget        agent.Budget          `json:"budget"`
			Blocker       string                `json:"blocker"`
			Summary       string                `json:"untrusted_model_summary"`
			Candidate     artifact.Ref          `json:"candidate"`
			ReleaseReady  bool                  `json:"release_ready"`
		}{doc.Runtime, doc.Plan, protection, doc.Context, 1, state, doc.Budget, doc.Blocker, doc.FinalSummary, doc.Candidate, false}); err != nil {
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
		if protection != nil {
			fmt.Fprintf(out, "Check protection %s · checks %d · scopes %d · strong verification unavailable\n", protection.ClosureCoverage, protection.CheckCount, protection.ProtectedScopes)
		}
		fmt.Fprintln(out, "Fixture/local engineering profile · stable release gates remain closed")
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
	checkPlanFile := f.String("check-plan", "", "trusted operator check closure plan JSON; cannot grant VERIFIED")
	offline := f.Bool("offline", false, "offline fixture profile only")
	provider := f.String("provider", "", "ollama local runtime; remote providers remain unavailable")
	endpoint := f.String("endpoint", "http://127.0.0.1:11434", "literal loopback Ollama origin")
	modelID := f.String("model", "", "operator-selected local model")
	localModel := f.Bool("local-model", false, "declare the endpoint/model executes locally without a cloud proxy")
	contextLimit := f.Int64("context-limit", 32768, "declared model context capacity; conservative byte preflight")
	outputLimit := f.Int64("output-limit", 512, "maximum model output tokens")
	timeoutMillis := f.Int64("model-timeout-ms", 120000, "bounded local inference timeout")
	git := f.Bool("git", false, "native Git-aware capture")
	allow := f.Bool("allow-unverified", false, "explicitly allow a limited UNVERIFIED candidate-only result (exit 2)")
	autonomy := f.String("autonomy", "guided", "review, guided or auto native tools")
	storeInput := f.Int64("store-input-tokens", 64<<20, "immutable store-wide input token work limit; first task only")
	storeOutput := f.Int64("store-output-tokens", 4<<20, "immutable store-wide output token work limit; first task only")
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
	var storeLimits *c.TokenLimits
	f.Visit(func(value *flag.Flag) {
		if value.Name == "store-input-tokens" || value.Name == "store-output-tokens" {
			storeLimits = &c.TokenLimits{Input: *storeInput, Output: *storeOutput}
		}
	})
	if storeLimits != nil {
		if err := storeLimits.Validate(); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	localFlag := false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "endpoint" || value.Name == "context-limit" || value.Name == "output-limit" || value.Name == "model-timeout-ms" {
			localFlag = true
		}
	})
	var runtime *agent.LocalRuntime
	if *provider == "" {
		if !*offline || *fixtureFile == "" {
			report(out, errout, c.Fail(c.UnsupportedCapability, "select --offline --fixture or declared local --provider ollama; remote runtime remains unavailable"), *jsonMode)
			return 3
		}
		if *modelID != "" || *localModel || localFlag {
			return report(out, errout, c.Fail(c.InvalidArgument, "fixture and local runtime flags are mutually exclusive"), *jsonMode)
		}
	} else {
		if *provider != "ollama" {
			report(out, errout, c.Fail(c.UnsupportedCapability, "only local Ollama runtime is available"), *jsonMode)
			return 3
		}
		if *offline || *fixtureFile != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "local runtime cannot be combined with offline fixture flags"), *jsonMode)
		}
		runtime = &agent.LocalRuntime{SchemaVersion: 1, Provider: *provider, Endpoint: *endpoint, Model: *modelID, DeclaredLocal: *localModel, ContextLimit: *contextLimit, OutputLimit: *outputLimit, TimeoutMillis: *timeoutMillis}
		if err := runtime.Validate(); err != nil {
			return report(out, errout, err, *jsonMode)
		}
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
	var raw []byte
	if runtime == nil {
		file, err := os.Open(*fixtureFile)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		raw, err = io.ReadAll(io.LimitReader(file, (8<<20)+1))
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

	}
	var checkPlan []byte
	if *checkPlanFile != "" {
		path, readErr := filepath.Abs(*checkPlanFile)
		if readErr != nil {
			return report(out, errout, readErr, *jsonMode)
		}
		checkRoot, readErr := os.OpenRoot(filepath.Dir(path))
		if readErr != nil {
			return report(out, errout, readErr, *jsonMode)
		}
		checkPlan, readErr = fileguard.ReadRegular(checkRoot, filepath.Base(path), 1<<20)
		closeErr := checkRoot.Close()
		if readErr == nil {
			readErr = closeErr
		}
		if readErr == nil && len(checkPlan) == 0 {
			readErr = c.Fail(c.InvalidArgument, "explicit check plan file is empty")
		}
		if readErr == nil {
			_, readErr = agent.ParseCheckPlan(checkPlan)
		}
		if readErr != nil {
			return report(out, errout, readErr, *jsonMode)
		}
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
	_, err = session.Create(ctx, agent.StartOptions{StoreTokens: storeLimits, Runtime: runtime, CheckPlan: checkPlan, Root: source, Prompt: []byte(prompt), Git: *git, TaskID: *task, Budget: budget, Autonomy: *autonomy, AllowUnverified: *allow, Fixture: raw})
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
