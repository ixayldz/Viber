// Package cli exposes safe engineering surfaces while runtime conformance is pending.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

const Version = "0.9.0-dev"
const help = `Viber 0.9.0-dev — fixture/local/ChatGPT engineering agent (stable release gates closed)

Usage:
  viber run "TASK" --offline --fixture FILE --root PATH --store PATH [--check-plan FILE --check-runtime FILE] [--task-kind CODE|ANALYSIS] [--max-repairs N] [--allow-unverified] [--detach] [--json]
  viber run "TASK" --provider ollama --model ID --local-model --root PATH --store PATH [--endpoint ORIGIN] [--context-limit N] [--output-limit N] [--stream] [--model-timeout-ms N] [--json]
  viber run "TASK" --provider chatgpt --allow-remote --root PATH --store PATH [--model gpt-6.1-sol] [--auth-profile ID] [--json]
  viber run "TASK" --provider openai|anthropic --allow-remote --model ID --root PATH --store PATH [--context-limit N] [--output-limit N] [--stream] [--json]
  viber auth login|status|profiles|select|models|logout|migrate-storage [--provider chatgpt] [--profile ID] [--auth-dir PATH] [--json]
  viber status|diff|pause|cancel|resume TASK --store PATH [--json]
  viber check-config --runtime FILE [--plan FILE] [--json]
  viber check-prepare --file RECIPE --output FRESH_FILE
  viber context-why|checks|verification|report TASK --store PATH [--json]
  viber context-page TASK --store PATH [--offset N] [--limit N] [--json]
  viber check-output TASK --store PATH --run-id ID [--stream stdout|stderr] [--offset N] [--limit N] [--json]
  viber attempt TASK --store PATH --new-task ID --parent-seq N [--fixture FILE] [--allow-unverified] [--detach] [--json]
  viber store-gc-preview --store PATH [--json]
  viber delete-preview TASK --store PATH [--json]
  viber delete TASK --store PATH --command-file FILE [--json]
  viber store-gc --store PATH --command-file FILE [--json]
  viber store-backup --store PATH --output NEW_PATH [--json]
  viber store-restore --backup PATH --store NEW_PATH [--json]
  viber store-migrate --store PATH --backup-output PATH --command-id ID [--json]
  viber store-migration-status --store PATH [--json]
  viber budget TASK --store PATH [--json]
  viber resources TASK --store PATH [--json]
  viber continuity-info TASK --store PATH [--json]
  viber context-edit TASK --store PATH --command-file FILE [--json]
  viber model-switch TASK --store PATH --command-file FILE [--json]
  viber reconcile-model-risk TASK --store PATH --command-file FILE [--json]
  viber eval-candidate TASK --store PATH --recipe BOUND_HIDDEN_FILE --output NEW_PATH [--json]
  viber eval-inspect --bundle PATH [--json]
  viber eval-report-inspect --bundle PATH [--json]
  viber eval-report --protocol FILE --observations JSONL --output NEW_PATH [--json]
  viber runtime-info TASK --store PATH [--json]
  viber reconcile-native-risk TASK --store PATH --command-file FILE [--json]
  viber history-page TASK --store PATH --history-digest SHA256 --offset N --limit N [--json]
  viber requests TASK --store PATH [--json]
  viber respond TASK --store PATH --request ID --response-file FILE [--json]
  viber inspect TASK --store PATH [--at TASK_SEQ] [--json]
  viber replay TASK --store PATH [--until TASK_SEQ] [--json]
  viber events TASK --store PATH [--after TASK_SEQ] [--limit N] [--json]
  viber ui TASK --store PATH
  viber queue TASK --store PATH [--command-file FILE] [--json]
  viber support --store PATH [--output NEW_PATH] [--json]
  viber privacy-capacity --store PATH [--replenish-control-reserve | --retire-operation-leases] [--json]
  viber export TASK --store PATH --output NEW_PATH [--json]
  viber delivery-preview TASK --store PATH --output NEW_PATH [--json]
  viber delivery-reverify TASK --store PATH --command-file FILE [--json]
  viber detach TASK --store PATH [--command-id ID] [--json]
  viber attach TASK --store PATH [--after TASK_SEQ] [--limit N] [--follow] [--poll-ms N] [--json]
  viber serve-background|owner-status|owner-stop --store PATH [--json]
  viber serve --store PATH [--json]
  viber steer TASK "RAW INSTRUCTION" --store PATH [--command-id ID] [--json]
  viber revise TASK --store PATH --revision-file FILE [--json]
  viber version
  viber doctor [--json]
  viber snapshot [--root PATH] [--json]
  viber sandbox-run --store PATH --task ID --snapshot DIGEST --profile FILE --policy FILE --authority FILE [--json] -- ARGV...
  viber snapshot-save --root PATH --store PATH --task ID [--git] [--json]
  viber snapshot-list --store PATH --task ID [--json]
  viber proposal-materialize --store PATH --base DIGEST --proposal FILE --policy FILE --authority FILE [--json]
  viber proposal-check --root PATH --proposal FILE --policy FILE --authority FILE [--json]

Snapshot and proposal-check are developer previews. They do not write source.
Offline Docker broker and provider protocol fixtures are engineering surfaces.
Production backend/provider conformance is pending. Offline fixture sessions
Default fixtures remain UNVERIFIED; limited delivery exits 2. Reviewed external V4
STDIO evidence can issue VERIFIED candidate-only results. ReleaseReady stays false.
The implementation/release plan is in docs/IMPLEMENTATION_PLAN.md.
`

type doctor struct {
	Continuity         string `json:"continuity"`
	ResourceAccounting string `json:"resource_accounting"`
	ArchiveGC          string `json:"archive_gc"`
	Supervisor         string `json:"supervisor"`
	SupportReport      string `json:"support_report"`
	CredentialStorage  string `json:"credential_storage"`
	SchemaVersion      int    `json:"schema_version"`
	Version            string `json:"version"`
	Platform           string `json:"platform"`
	GitDiscovered      bool   `json:"git_discovered"`
	SnapshotPreview    bool   `json:"snapshot_preview"`
	ProposalPreview    bool   `json:"proposal_preview"`
	ProcessSandbox     string `json:"process_sandbox"`
	ChatGPTAuth        string `json:"chatgpt_auth"`
	ChatGPTInference   string `json:"chatgpt_inference"`
	RemoteProviders    string `json:"remote_providers"`
	LocalProvider      string `json:"local_provider"`
	LiveApply          string `json:"live_apply"`
	CaptureConsistency string `json:"capture_consistency"`
	Telemetry          bool   `json:"telemetry"`
	TrainingExport     bool   `json:"training_export"`
	ReleaseReady       bool   `json:"release_ready"`
}

func jsonWrite(out io.Writer, value any) error {
	e := json.NewEncoder(out)
	e.SetEscapeHTML(true)
	return e.Encode(value)
}
func report(out, errout io.Writer, err error, jsonMode bool) int {
	var typed *c.Error
	if !errors.As(err, &typed) {
		typed = &c.Error{Code: c.InvalidArgument, Message: err.Error()}
	}
	if jsonMode {
		_ = jsonWrite(out, struct {
			SchemaVersion int      `json:"schema_version"`
			Error         *c.Error `json:"error"`
		}{c.SchemaVersion, typed})
	} else {
		fmt.Fprintf(errout, "%s: %s\n", typed.Code, strconv.QuoteToASCII(typed.Message))
	}
	return 4
}
func Execute(args []string, out, errout io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, help)
		return 0
	}
	switch args[0] {
	case "queue":
		return runQueue(args[1:], out, errout)
	case "ui":
		return runUI(args[1:], out, errout)
	case "support":
		return runSupport(args[1:], out, errout)
	case "privacy-capacity":
		return runPrivacyCapacity(args[1:], out, errout)
	case "serve-background", "owner-status", "owner-stop":
		return runSupervisor(args[0], args[1:], out, errout)
	case "detach", "attach":
		return runAttachment(args[0], args[1:], out, errout)
	case "store-gc-preview", "store-gc":
		return runGC(args[0], args[1:], out, errout)
	case "context-edit", "model-switch", "reconcile-model-risk", "reconcile-native-risk":
		return runContinuityEdit(args[0], args[1:], out, errout)
	case "eval-candidate", "eval-inspect":
		return runEvaluationCandidate(args[0], args[1:], out, errout)
	case "eval-report-inspect":
		return runEvaluationReportInspect(args[1:], out, errout)
	case "eval-report":
		return runEvaluationReport(args[1:], out, errout)
	case "check-config":
		return runCheckConfig(args[1:], out, errout)
	case "check-prepare":
		return runCheckPrepare(args[1:], out, errout)
	case "auth":
		return runAuth(args[1:], out, errout)
	case "attempt":
		return runAttempt(args[1:], out, errout)
	case "context-why", "context-page", "checks", "check-output", "verification", "report", "history-page", "continuity-info", "resources", "runtime-info":
		return runObservation(args[0], args[1:], out, errout)
	case "budget":
		return runTokenLedger(args[1:], out, errout)
	case "serve":
		return runServe(args[1:], out, errout)
	case "store-migrate", "store-migration-status":
		return runStoreMigration(args[0], args[1:], out, errout)
	case "store-backup", "store-restore":
		return runStoreBackup(args[0], args[1:], out, errout)
	case "version":
		if len(args) != 1 {
			return report(out, errout, c.Fail(c.InvalidArgument, "version takes no arguments"), false)
		}
		fmt.Fprintln(out, Version)
		return 0
	case "doctor":
		return runDoctor(args[1:], out, errout)
	case "snapshot":
		return runSnapshot(args[1:], out, errout)
	case "sandbox-run":
		return runSandbox(args[1:], out, errout)
	case "snapshot-save":
		return runSnapshotSave(args[1:], out, errout)
	case "snapshot-list":
		return runSnapshotList(args[1:], out, errout)
	case "proposal-materialize":
		return runProposalMaterialize(args[1:], out, errout)
	case "proposal-check":
		return runProposal(args[1:], out, errout)
	case "requests", "respond":
		return runRequests(args[0], args[1:], out, errout)
	case "export":
		return runExport(args[1:], out, errout)
	case "delivery-preview":
		return runDeliveryPreview(args[1:], out, errout)
	case "delivery-reverify":
		return runDeliveryReverification(args[1:], out, errout)
	case "run":
		return runTask(args[1:], out, errout)
	case "inspect", "replay", "events":
		return runHistory(args[0], args[1:], out, errout)
	case "resume", "pause", "cancel", "status", "diff":
		return runTaskControl(args[0], args[1:], out, errout)
	case "steer", "revise":
		return runSteering(args[0], args[1:], out, errout)
	case "delete", "delete-preview":
		return runPrivacy(args[0], args[1:], out, errout)
	case "apply", "restore":
		jsonMode := false
		for _, arg := range args {
			if arg == "--json" {
				jsonMode = true
			}
		}
		err := c.Fail(c.UnsupportedCapability, "runtime command requires unfinished sandbox/provider/IPC contracts; see implementation plan")
		report(out, errout, err, jsonMode)
		if args[0] == "run" || args[0] == "resume" {
			return 3
		}
		return 4
	default:
		return report(out, errout, c.Fail(c.InvalidArgument, "unknown command; use viber --help"), false)
	}
}
func flags(name string, errout io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(errout)
	return f
}
func runDoctor(args []string, out, errout io.Writer) int {
	f := flags("doctor", errout)
	jsonMode := f.Bool("json", false, "JSON result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "unexpected arguments"), *jsonMode)
	}
	_, gitErr := exec.LookPath("git")
	d := doctor{Continuity: "SOURCE_BOUND_HISTORY_PIN_LOCKED_PROVIDER_SWITCH", ResourceAccounting: "CONSERVATIVE_GLOBAL_VECTOR; PHYSICAL_CONTROL_RESERVE", ArchiveGC: "TYPED_ROOTS_ORPHAN_GC; TASK_CONTENT_DELETE_WITH_EXTERNAL_WATERMARK; RETENTION_PENDING", Supervisor: "DETACHED_OWNER_CURSOR_ATTACH; HOSTILE_FENCING_ACCEPTANCE_PENDING", SupportReport: "NUMERIC_ALLOWLIST_SUPPORT_V1", CredentialStorage: "WINDOWS_DPAPI_OR_UNIX_OS_VAULT; NATIVE_UNIX_ROUNDTRIP_CI_TESTED; HOST_AVAILABILITY_NOT_PROBED", SchemaVersion: c.SchemaVersion, Version: Version, Platform: runtime.GOOS + "/" + runtime.GOARCH, GitDiscovered: gitErr == nil, SnapshotPreview: true, ProposalPreview: true, ProcessSandbox: "DEVELOPER_OFFLINE_V1_CONFORMANCE_PENDING", ChatGPTAuth: "OFFICIAL_SIWC; OFFLINE_SECURITY_TESTED; LIVE_ACCEPTANCE_PENDING", ChatGPTInference: "STATELESS_SSE; GPT_6_1_SOL_PUBLISHED_CEILING; LIVE_ACCEPTANCE_PENDING", RemoteProviders: "API_KEY_RUNTIME; JSON_SSE_OFFLINE_TESTED; LIVE_ACCEPTANCE_PENDING", LocalProvider: "DECLARED_LOCAL_RUNTIME_CONFORMANCE_PENDING", LiveApply: "UNSUPPORTED", CaptureConsistency: "BEST_EFFORT"}
	if *jsonMode {
		if err := jsonWrite(out, d); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Viber %s · %s\nSnapshot/proposal: developer preview · BEST_EFFORT\nOffline sandbox: developer profile · providers: fixture conformance · live apply: unavailable\nStable release: gates closed · telemetry/training: off\n", d.Version, d.Platform)
	}
	return 0
}
func runSnapshot(args []string, out, errout io.Writer) int {
	f := flags("snapshot", errout)
	root := f.String("root", ".", "directory to inspect")
	jsonMode := f.Bool("json", false, "JSON manifest")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "unexpected arguments"), *jsonMode)
	}
	capture, err := workspace.CaptureDirectory(*root, workspace.DefaultLimits())
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, capture.Snapshot); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Snapshot %s\nRoot %s\nFiles %d · excluded %d · BEST_EFFORT\n", capture.Snapshot.Digest, strconv.QuoteToASCII(capture.Snapshot.Root), len(capture.Snapshot.Entries), len(capture.Snapshot.Exclusions))
		for _, e := range capture.Snapshot.Entries {
			fmt.Fprintf(out, "%s %d %s\n", e.Hash, e.Size, strconv.QuoteToASCII(e.Path))
		}
	}
	return 0
}
func readJSON(path string, target any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return err
	}
	return c.DecodeStrict(data, target)
}
func runProposal(args []string, out, errout io.Writer) int {
	f := flags("proposal-check", errout)
	root := f.String("root", ".", "source directory")
	file := f.String("proposal", "", "versioned proposal JSON")
	policyFile := f.String("policy", "", "trusted restriction layers JSON")
	authorityFile := f.String("authority", "", "trusted current task state JSON")
	jsonMode := f.Bool("json", false, "JSON candidate manifest")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *file == "" || *policyFile == "" || *authorityFile == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "proposal, policy and authority files required"), *jsonMode)
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
	base, err := workspace.CaptureDirectory(*root, workspace.DefaultLimits())
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	next, err := workspace.Preview(base, base, p, layers, authority)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, next.Snapshot); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Proposal validated · candidate %s · %d changes · source unchanged\n", next.Snapshot.Digest, len(p.Changes))
	}
	return 0
}
