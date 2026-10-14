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
	"github.com/ixayldz/Viber/internal/auth"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/kernel"
	"github.com/ixayldz/Viber/internal/learning"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/owner"
	"github.com/ixayldz/Viber/internal/plan"
	"github.com/ixayldz/Viber/internal/verify"
)

func taskResult(out, errout io.Writer, state c.TaskState, doc agent.Document, jsonMode bool, protection *agent.ProtectionInfo, checks []agent.CheckSummary) int {
	if jsonMode {
		if err := jsonWrite(out, struct {
			Checks        []agent.CheckSummary  `json:"checks"`
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
			PRDCoverage   *c.PRDCoverage        `json:"prd_coverage,omitempty"`
			ReleaseReady  bool                  `json:"release_ready"`
		}{checks, doc.Runtime, doc.Plan, protection, doc.Context, 1, state, doc.Budget, doc.Blocker, doc.FinalSummary, doc.Candidate, doc.PRDCoverage, false}); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Task %s · %s / %s\nQuality %s · fulfillment %s · candidate %s\n", strconv.QuoteToASCII(state.TaskID), state.Execution, state.Outcome, state.Quality, state.Fulfillment, state.CandidateDigest)
		for _, check := range checks {
			fmt.Fprintf(out, "Check %s: %s · current %t · verification %s\n", strconv.QuoteToASCII(check.CheckID), check.Outcome, check.Current, check.Verification)
		}
		if doc.Blocker != "" {
			fmt.Fprintf(out, "Blocker %s\n", strconv.QuoteToASCII(doc.Blocker))
		}
		if doc.FinalReady {
			fmt.Fprintf(out, "Model summary %s\n", strconv.QuoteToASCII(doc.FinalSummary))
		}
		if doc.PRDCoverage != nil {
			fmt.Fprintf(out, "PRD coverage %s · required %d · planned %d\n", doc.PRDCoverage.Status, len(doc.PRDCoverage.RequiredRequirementIDs), len(doc.PRDCoverage.CoveredRequirementIDs))
		}
		if protection != nil {
			fmt.Fprintf(out, "Check protection %s · checks %d · scopes %d · strong verification unavailable\n", protection.ClosureCoverage, protection.CheckCount, protection.ProtectedScopes)
		}
		fmt.Fprintln(out, "Fixture/local/ChatGPT engineering profile · stable release gates remain closed")
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
	prdFile := f.String("prd", "", "exact project-relative captured PRD file to guide a planned implementation")
	userConfigFile := f.String("user-config", "", "secret-free global config; default OS user config directory")
	operatorPolicyFile := f.String("operator-policy", "", "absolute operator restriction profile outside project; revision changes invalidate task admissions")
	rankingDirectory := f.String("ranking-registry", "", "explicit local advisory ranking control outside source/store; disabled by default")
	skillsDirectory := f.String("skills-registry", "", "explicit externally reviewed procedural skill registry; disabled by default")
	skillsTrustFile := f.String("skills-trust-policy", "", "separate operator evaluator trust pins for advisory skills")
	extensionFile := f.String("extension-manifest", "", "explicit canonical MCP manifest outside source/store; each call requires approval")
	goGraphRuntimeFile := f.String("go-graph-runtime", "", "explicit pinned helper image manifest for hard-isolated Go graph analysis")
	directory := f.String("store", "", "private session store outside source")
	task := f.String("task", "", "task ID (default random)")
	fixtureFile := f.String("fixture", "", "offline model fixture JSON")
	taskKind := f.String("task-kind", "CODE", "CODE or read-only ANALYSIS")
	maxRepairs := f.Int("max-repairs", 2, "bounded completion repairs, 0..8; uses existing work budget")
	checkRuntimeFile := f.String("check-runtime", "", "operator JSON schema_version=1 and pinned offline runner profile")
	checkPlanFile := f.String("check-plan", "", "trusted operator check closure plan JSON; cannot grant VERIFIED")
	offline := f.Bool("offline", false, "offline fixture profile only")
	provider := f.String("provider", "", "ollama, chatgpt, openai or anthropic")
	stream := f.Bool("stream", false, "explicit bounded API/local stream protocol; ChatGPT streams implicitly")
	endpoint := f.String("endpoint", "http://127.0.0.1:11434", "literal loopback Ollama origin")
	modelID := f.String("model", "", "operator-selected model")
	allowRemote := f.Bool("allow-remote", false, "allow selected remote provider to receive this task context")
	authDirectory := f.String("auth-dir", "", "private ChatGPT credentials outside source/store")
	authProfile := f.String("auth-profile", "", "saved ChatGPT account profile ID; default current selection")
	localModel := f.Bool("local-model", false, "declare the endpoint/model executes locally without a cloud proxy")
	contextLimit := f.Int64("context-limit", 32768, "declared model context capacity; conservative byte preflight")
	outputLimit := f.Int64("output-limit", 512, "maximum model output tokens")
	timeoutMillis := f.Int64("model-timeout-ms", 120000, "bounded inference timeout, 1000..300000 ms")
	git := f.Bool("git", false, "native Git-aware capture")
	allow := f.Bool("allow-unverified", false, "explicitly allow a limited UNVERIFIED candidate-only result (exit 2)")
	autonomy := f.String("autonomy", "guided", "review, guided or auto native tools")
	combinedChecksFile := f.String("check-config", "", "bound operator plan/runtime JSON from check-config")
	goalReviewFile := f.String("goal-review", "", "explicit raw-goal/protected-dependency review JSON outside source")
	resourcePolicyFile := f.String("resource-policy", "", "immutable store resource limits and operator versioned model prices JSON")
	storeInput := f.Int64("store-input-tokens", 64<<20, "immutable store-wide input token work limit; first task only")
	storeOutput := f.Int64("store-output-tokens", 4<<20, "immutable store-wide output token work limit; first task only")
	maxInputTokens := f.Int64("max-input-tokens", 512<<10, "task input token work limit")
	maxOutputTokens := f.Int64("max-output-tokens", 0, "task output work limit; default 32768 local/fixture or 1048576 ChatGPT")
	steps := f.Int64("max-steps", 16, "bounded model turns")
	tools := f.Int64("max-tool-calls", 64, "bounded native tool calls")
	activeMillis := f.Int64("max-active-ms", 120000, "sealed active task time budget, 1000..900000 ms; resume does not reset it")
	detached := f.Bool("detach", false, "create task then run under a persistent background owner")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if prompt == "" && f.NArg() == 1 {
		prompt = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one raw task prompt required"), *jsonMode)
	}
	prdRequested := false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "prd" {
			prdRequested = true
		}
	})
	if prdRequested {
		if prompt == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "raw prompt required with --prd"), *jsonMode)
		}
		if err := validatePRDPath(*prdFile); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		workflowPrompt, promptErr := promptWithPRDWorkflow(prompt, *prdFile)
		if promptErr != nil {
			return report(out, errout, promptErr, *jsonMode)
		}
		prompt = workflowPrompt
	}
	if (*taskKind != "CODE" && *taskKind != "ANALYSIS") || *maxRepairs < 0 || *maxRepairs > 8 {
		return report(out, errout, c.Fail(c.InvalidArgument, "invalid task kind/repair limit"), *jsonMode)
	}
	configSource, configErr := filepath.Abs(*root)
	if configErr != nil {
		return report(out, errout, c.Fail(c.InvalidArgument, "source root unavailable"), *jsonMode)
	}
	resolvedConfig, configErr := loadRunConfigAuthority(configSource, *userConfigFile, *operatorPolicyFile)
	if configErr != nil {
		return report(out, errout, configErr, *jsonMode)
	}
	if resolvedConfig != nil {
		explicit := map[string]bool{}
		f.Visit(func(v *flag.Flag) { explicit[v.Name] = true })
		preferences := resolvedConfig.Preferences
		if !explicit["provider"] && !*offline && preferences.Provider != "" {
			*provider = preferences.Provider
		}
		if !explicit["model"] && !*offline && preferences.Model != "" {
			*modelID = preferences.Model
		}
		if !explicit["autonomy"] && preferences.Autonomy != "" {
			*autonomy = preferences.Autonomy
		}
		if !explicit["context-limit"] && preferences.ContextLimit != 0 {
			*contextLimit = preferences.ContextLimit
		}
		if !explicit["output-limit"] && preferences.OutputLimit != 0 {
			*outputLimit = preferences.OutputLimit
		}
		if *provider != "" {
			if err := resolvedConfig.AdmitProvider(*provider, *provider != "ollama"); err != nil {
				return report(out, errout, err, *jsonMode)
			}
		}
	}
	var resourcePolicy *c.ResourcePolicy
	if *resourcePolicyFile != "" {
		resourcePolicy = &c.ResourcePolicy{}
		if err := readJSON(*resourcePolicyFile, resourcePolicy); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if err := resourcePolicy.Validate(); err != nil {
			return report(out, errout, err, *jsonMode)
		}
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
	chatGPTCapFlag := false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "endpoint" || value.Name == "context-limit" || value.Name == "output-limit" {
			chatGPTCapFlag = true
		}
	})
	var runtime *agent.LocalRuntime
	if *provider != "chatgpt" && (*authDirectory != "" || *authProfile != "") || (*provider == "" || *provider == "ollama") && *allowRemote {
		return report(out, errout, c.Fail(c.InvalidArgument, "ChatGPT auth flags require --provider chatgpt"), *jsonMode)
	}
	if *provider == "" {
		if !*offline || *fixtureFile == "" {
			report(out, errout, c.Fail(c.UnsupportedCapability, "select --offline --fixture or declared local --provider ollama; or explicitly consented remote provider"), *jsonMode)
			return 3
		}
		if *modelID != "" || *localModel || localFlag || *stream {
			return report(out, errout, c.Fail(c.InvalidArgument, "fixture and local runtime flags are mutually exclusive"), *jsonMode)
		}
	} else if *provider == "openai" || *provider == "anthropic" {
		endpointFlag := false
		f.Visit(func(v *flag.Flag) {
			if v.Name == "endpoint" {
				endpointFlag = true
			}
		})
		if !*allowRemote || *localModel || *offline || *fixtureFile != "" || endpointFlag {
			return report(out, errout, c.Fail(c.PolicyDenied, "remote API runtime requires --allow-remote and cannot use local/fixture/custom endpoint flags"), *jsonMode)
		}
		origin, handle := "https://api.openai.com", "OPENAI_API_KEY"
		if *provider == "anthropic" {
			origin, handle = "https://api.anthropic.com", "ANTHROPIC_API_KEY"
		}
		value, found := os.LookupEnv(handle)
		if !found || value == "" || len(value) > 16384 || strings.ContainsAny(value, "\r\n\x00") {
			return report(out, errout, c.Fail(c.PolicyDenied, "selected provider credential handle unavailable"), *jsonMode)
		}
		runtime = &agent.LocalRuntime{SchemaVersion: 1, Provider: *provider, Stream: *stream, Endpoint: origin, Model: *modelID, AllowRemote: true, SecretHandle: handle, ContextLimit: *contextLimit, OutputLimit: *outputLimit, TimeoutMillis: *timeoutMillis}
		if err := runtime.Validate(); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	} else if *provider == "chatgpt" {
		if !*allowRemote || *localModel || *offline || *fixtureFile != "" || chatGPTCapFlag || *stream {
			return report(out, errout, c.Fail(c.PolicyDenied, "ChatGPT requires --allow-remote and cannot use local/fixture/endpoint/output-cap flags"), *jsonMode)
		}
		if *authDirectory == "" {
			var err error
			*authDirectory, err = defaultAuthDirectory()
			if err != nil {
				return report(out, errout, err, *jsonMode)
			}
		}
		absoluteAuth, err := filepath.Abs(*authDirectory)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		credentials, err := auth.Open(absoluteAuth, false)
		if err != nil {
			return report(out, errout, c.Fail(c.PolicyDenied, "ChatGPT sign-in required; run auth login"), *jsonMode)
		}
		found := false
		for _, profile := range credentials.View().Profiles {
			if (*authProfile == "" && profile.Active) || profile.ID == *authProfile {
				if profile.PlanUsage {
					*authProfile = profile.ID
					found = true
				}
				break
			}
		}
		credentials.Close()
		if !found {
			return report(out, errout, c.Fail(c.PolicyDenied, "select a signed-in ChatGPT profile with plan usage scope"), *jsonMode)
		}
		if *modelID == "" {
			*modelID = model.ChatGPTModel
		}
		runtime = &agent.LocalRuntime{SchemaVersion: 1, Provider: "chatgpt", Endpoint: "https://api.openai.com", Model: *modelID, AllowRemote: true, AuthDirectory: absoluteAuth, AuthProfile: *authProfile, ContextLimit: model.ChatGPTContextCapacity, OutputLimit: model.ChatGPTOutputCeiling, TimeoutMillis: *timeoutMillis}
		if err := runtime.Validate(); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	} else {
		if *provider != "ollama" {
			report(out, errout, c.Fail(c.UnsupportedCapability, "supported providers: ollama, chatgpt, openai, anthropic"), *jsonMode)
			return 3
		}
		if *offline || *fixtureFile != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "local runtime cannot be combined with offline fixture flags"), *jsonMode)
		}
		runtime = &agent.LocalRuntime{SchemaVersion: 1, Provider: *provider, Stream: *stream, Endpoint: *endpoint, Model: *modelID, DeclaredLocal: *localModel, ContextLimit: *contextLimit, OutputLimit: *outputLimit, TimeoutMillis: *timeoutMillis}
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
	if runtime != nil && runtime.Provider == "chatgpt" && (!fileguard.Disjoint(source, runtime.AuthDirectory) || !fileguard.Disjoint(target, runtime.AuthDirectory)) {
		return report(out, errout, c.Fail(c.PolicyDenied, "credentials must be outside source and task store"), *jsonMode)
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
	var checkRuntime *agent.CheckRuntime
	if *checkRuntimeFile != "" {
		checkRuntime = &agent.CheckRuntime{}
		if err = readJSON(*checkRuntimeFile, checkRuntime); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if checkRuntime.SchemaVersion != 1 || checkRuntime.Profile.Validate() != nil {
			return report(out, errout, c.Fail(c.InvalidArgument, "invalid check runtime"), *jsonMode)
		}
		p, _ := agent.ParseCheckPlan(checkPlan)
		profileDigest, _ := c.Digest(checkRuntime.Profile)
		if len(p.Checks) == 0 {
			return report(out, errout, c.Fail(c.InvalidArgument, "check runtime requires --check-plan"), *jsonMode)
		}
		for _, check := range p.Checks {
			if check.RunnerDigest != profileDigest {
				return report(out, errout, c.Fail(c.PolicyDenied, "check profile digest mismatch"), *jsonMode)
			}
		}
	}
	if *combinedChecksFile != "" {
		if *checkPlanFile != "" || *checkRuntimeFile != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "check-config cannot mix with check-plan/check-runtime"), *jsonMode)
		}
		combined, configErr := readOperatorChecks(*combinedChecksFile)
		if configErr != nil {
			return report(out, errout, configErr, *jsonMode)
		}
		checkPlan, _ = c.CanonicalV1(combined.Plan)
		checkRuntime = &combined.Runtime
		*checkRuntimeFile = *combinedChecksFile
	}
	var goalReview *verify.GoalReview
	if *goalReviewFile != "" {
		goalReview = &verify.GoalReview{}
		if err = readJSON(*goalReviewFile, goalReview); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if checkRuntime != nil && len(checkRuntime.ObserverSuites) > 0 {
		for _, inputFile := range []string{*checkRuntimeFile, *goalReviewFile} {
			if inputFile != "" {
				absolute, pathErr := filepath.Abs(inputFile)
				if pathErr != nil || !fileguard.Disjoint(source, absolute) {
					return report(out, errout, c.Fail(c.PolicyDenied, "observer/review configuration must be outside source"), *jsonMode)
				}
			}
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
	budget := agent.DefaultBudget()
	budget.MaxSteps = *steps
	budget.MaxToolCalls = *tools
	budget.MaxInputTokens = *maxInputTokens
	budget.MaxActiveMillis = *activeMillis
	if *maxOutputTokens != 0 {
		budget.MaxOutputTokens = *maxOutputTokens
	} else if runtime != nil && runtime.Provider == "chatgpt" {
		budget.MaxOutputTokens = 1 << 20
	}
	if err := budget.Validate(); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var ranking *learning.Binding
	var skillRegistry *agent.SkillRegistryRef
	var extension *agent.ExtensionRef
	var goGraphRuntime *agent.GoGraphRuntimeBinding
	if *goGraphRuntimeFile != "" {
		binding, e := agent.BindGoGraphRuntime(*goGraphRuntimeFile)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		goGraphRuntime = &binding
	}
	if *extensionFile != "" {
		binding, e := agent.BindExtensionManifest(*extensionFile)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		extension = &binding
	}
	if (*skillsDirectory == "") != (*skillsTrustFile == "") {
		return report(out, errout, c.Fail(c.InvalidArgument, "skills registry and separate operator trust policy required together"), *jsonMode)
	}
	if *skillsDirectory != "" {
		binding, e := agent.BindSkillRegistry(*skillsDirectory, *skillsTrustFile)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		skillRegistry = &binding
	}
	if *rankingDirectory != "" {
		registry, e := learning.Open(*rankingDirectory, false)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		binding, e := registry.Bind()
		registry.Close()
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		ranking = &binding
	}
	session, err := agent.Open(ctx, *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	prdPath := ""
	if prdRequested {
		prdPath = *prdFile
	}
	createdState, err := session.Create(ctx, agent.StartOptions{GoGraphRuntime: goGraphRuntime, Extension: extension, SkillRegistry: skillRegistry, Ranking: ranking, GoalReview: goalReview, Config: resolvedConfig, ResourcePolicy: resourcePolicy, TaskKind: *taskKind, PRDPath: prdPath, MaxRepairs: *maxRepairs, CheckRuntime: checkRuntime, StoreTokens: storeLimits, Runtime: runtime, CheckPlan: checkPlan, Root: source, Prompt: []byte(prompt), Git: *git, TaskID: *task, Budget: budget, Autonomy: *autonomy, AllowUnverified: *allow, Fixture: raw})
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if prdRequested {
		binding, bindErr := requireCapturedPRD(ctx, session, *task, *prdFile)
		if bindErr != nil {
			return report(out, errout, bindErr, *jsonMode)
		}
		if binding.Candidate != createdState.CandidateDigest {
			return report(out, errout, c.Fail(c.StaleBase, "selected PRD no longer belongs to the created candidate"), *jsonMode)
		}
		if binding.Entry.Path != *prdFile || binding.Entry.Hash == "" {
			return report(out, errout, c.Fail(c.StoreIntegrityError, "selected PRD path changed during capture"), *jsonMode)
		}
		_, createdDoc, loadErr := session.Load(ctx, *task)
		if loadErr != nil || createdDoc.Spec.PRD == nil || createdDoc.Spec.PRD.CandidateDigest != createdState.CandidateDigest || createdDoc.Spec.PRD.Path != binding.Entry.Path || createdDoc.Spec.PRD.Digest != binding.Entry.Hash || createdDoc.Spec.PRD.ByteLength != binding.Entry.Size {
			if loadErr != nil {
				return report(out, errout, loadErr, *jsonMode)
			}
			return report(out, errout, c.Fail(c.StoreIntegrityError, "durable PRD source binding was not retained"), *jsonMode)
		}
	}
	if *detached {
		if err = session.Close(); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		id, err := resolveCommandID("")
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		return runDetachedTask(ctx, *task, *directory, id, out, errout, *jsonMode)
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
