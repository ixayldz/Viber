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
  viber [start]                                       interactive first-task wizard
  viber repo-blueprint --root SOURCE --blueprint SPEC_JSON [--git] [--json]
  viber repo-generate --root SOURCE --blueprint MANIFEST_JSON --blueprint-digest SHA --files JSON --output NEW_DIR [--git] [--check-config FILE --task ID --input-sha SHA] [--json]
  viber skills source|propose|evaluate|activate|rollback|revoke|expire|status|load --registry PATH --trust-policy FILE [--store PATH --task ID] [--definition FILE --acknowledge-external-copy ACK] [--evaluation FILE --id ID --version N] [--scope-kind KIND --scope-key KEY --approve] [--json]
  viber rank-policy init|status|train|evaluate|select|disable --registry PATH [--id ID --version N --license LICENSE] [--definition FILE --dataset FILE --dataset-sha SHA] [--definition-sha SHA --evaluation-sha SHA --mode SHADOW|CANARY|ACTIVE --approve ACK] [--json]
  viber package install|status|rollback|deactivate|uninstall|run --prefix PATH [--archive FILE --source-sha FULL_SHA --gh ABSOLUTE_GH] [--revision SHA] [--store PATH] [--json] [-- ARGS...]
  viber run "TASK" --offline --fixture FILE --root PATH --store PATH [--check-plan FILE --check-runtime FILE] [--task-kind CODE|ANALYSIS] [--max-repairs N] [--allow-unverified] [--detach] [--json]
  viber run "TASK" --provider ollama --model ID --local-model --root PATH --store PATH [--endpoint ORIGIN] [--context-limit N] [--output-limit N] [--stream] [--model-timeout-ms N] [--json]
  viber run "TASK" --provider chatgpt --allow-remote --root PATH --store PATH [--model gpt-6.1-sol] [--auth-profile ID] [--json]
  viber run "TASK" --provider openai|anthropic --allow-remote --model ID --root PATH --store PATH [--context-limit N] [--output-limit N] [--stream] [--json]
    Optional local advisory controls: [--ranking-registry DIR] [--skills-registry DIR --skills-trust-policy FILE]
    Optional isolated MCP tools: [--extension-manifest CANONICAL_FILE] (explicit approval per call)
  viber auth login|status|profiles|select|models|logout|migrate-storage [--provider chatgpt] [--profile ID] [--auth-dir PATH] [--json]
  viber status|diff|pause|cancel|resume TASK --store PATH [--json]
  viber check-config --runtime FILE [--plan FILE] [--json]
  viber check-prepare --file RECIPE --output FRESH_FILE
  viber check-discover --root PATH [--json]
  viber repo-history --root PATH --allow-local-history --cutoff UTC [--json]
  viber repo-graph --root PATH [--goos OS --goarch ARCH --compiler gc --go-version go1.27 --tags TAG --tags-complete --cgo DISABLED] [--changed PATH] [--cooperative-types] [--json]
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
  viber eval-register --store PATH --protocol FILE --dataset DIR --assets FILE --output NEW_PATH [--json]
  viber eval-registration-inspect --registration DIR [--json]
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
  viber project-memory search|record|correct|retire --task TASK --store PATH [--kind FACT|DECISION|CONVENTION --key KEY --text TEXT] [--id ID --digest SHA --command-id ID] [--query QUERY --offset N --limit N] [--json]
  viber project-memory copy --store PATH --from SOURCE_TASK --to TARGET_TASK --id ID --digest SHA --command-id ID [--json]
  viber retrieval-report TASK --store PATH --call-id ID [--json]
  viber read-workers TASK --store PATH --candidate SHA [--workers inventory,requirements --query TEXT --timeout-ms N] [--json]
  viber queue TASK --store PATH [--command-file FILE] [--json]
  viber goal-queue TASK --store PATH [--command-file FILE] [--json]
  viber support --store PATH [--output NEW_PATH] [--json]
  viber privacy-capacity --store PATH [--replenish-control-reserve | --retire-operation-leases] [--json]
  viber privacy-checkpoint --store PATH [--json]
  viber retention [TASK] --store PATH [--keep | --expires-at UTC --acknowledge CONSENT] [--revision N --command-id ID] [--run-due] [--json]
  viber export TASK --store PATH --output NEW_PATH [--json]
  viber training-export TASK --store PATH --output NEW_PATH --consent FILE --samples FILE [--json]
  viber delivery-preview TASK --store PATH --output NEW_PATH [--json]
  viber delivery-reverify TASK --store PATH --command-file FILE [--json]
  viber apply TASK --store PATH --output NEW_PATH [--exclusive-ntfs] [--json]
  viber restore TASK --store PATH --checkpoint ID [--json]
  viber delivery-reconcile TASK --store PATH --checkpoint ID [--json]
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
	if len(args) == 0 {
		if interactiveOnboarding(out) {
			return runOnboarding(nil, out, errout)
		}
		fmt.Fprint(out, help)
		return 0
	}
	if args[0] == "start" {
		return runOnboarding(args[1:], out, errout)
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, help)
		return 0
	}

	if args[0] == "rank-policy" {
		return runRanking(args[1:], out, errout)
	}
	if args[0] == "skills" {
		return runSkills(args[1:], out, errout)
	}
	if args[0] == "repo-blueprint" || args[0] == "repo-generate" {
		return runRepoGeneration(args[0], args[1:], out, errout)
	}
	switch args[0] {
	case "training-export":
		return runTrainingExport(args[1:], out, errout)
	case "package":
		return runPackage(args[1:], out, errout)
	case "privacy-checkpoint":
		return runPrivacyCheckpoint(args[1:], out, errout)
	case "eval-register":
		return runEvaluationRegister(args[1:], out, errout)
	case "eval-registration-inspect":
		return runEvaluationRegistrationInspect(args[1:], out, errout)
	case "check-discover":
		return runCheckDiscover(args[1:], out, errout)
	case "repo-history":
		return runRepoHistory(args[1:], out, errout)
	case "repo-graph":
		return runRepoGraph(args[1:], out, errout)
	case "goal-queue":
		return runGoalQueue(args[1:], out, errout)
	case "project-memory":
		return runProjectMemory(args[1:], out, errout)
	case "read-workers":
		return runReadWorkers(args[1:], out, errout)
	case "apply", "delivery-apply":
		return runDeliveryApply(args[1:], out, errout)
	case "restore", "delivery-restore":
		return runDeliveryRestore(args[1:], out, errout)
	case "delivery-reconcile":
		return runDeliveryReconcile(args[1:], out, errout)
	case "queue":
		return runQueue(args[1:], out, errout)
	case "ui":
		return runUI(args[1:], out, errout)
	case "support":
		return runSupport(args[1:], out, errout)
	case "retrieval-report":
		return runRetrievalReport(args[1:], out, errout)
	case "privacy-capacity":
		return runPrivacyCapacity(args[1:], out, errout)
	case "retention":
		return runRetention(args[1:], out, errout)
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
	d := doctor{Continuity: "SOURCE_BOUND_HISTORY_PIN_LOCKED_PROVIDER_SWITCH", ResourceAccounting: "CONSERVATIVE_GLOBAL_VECTOR; PHYSICAL_CONTROL_RESERVE", ArchiveGC: "TYPED_ROOTS_ORPHAN_GC; TASK_CONTENT_DELETE_WITH_EXTERNAL_WATERMARK; EXPLICIT_MANAGED_CONTENT_EXPIRY; METADATA_AUDIT_RETENTION_PENDING", Supervisor: "DETACHED_OWNER_CURSOR_ATTACH; HOSTILE_FENCING_ACCEPTANCE_PENDING", SupportReport: "NUMERIC_ALLOWLIST_SUPPORT_V1", CredentialStorage: "WINDOWS_DPAPI_OR_UNIX_OS_VAULT; NATIVE_UNIX_ROUNDTRIP_CI_TESTED; HOST_AVAILABILITY_NOT_PROBED", SchemaVersion: c.SchemaVersion, Version: Version, Platform: runtime.GOOS + "/" + runtime.GOARCH, GitDiscovered: gitErr == nil, SnapshotPreview: true, ProposalPreview: true, ProcessSandbox: "DEVELOPER_OFFLINE_V1_CONFORMANCE_PENDING", ChatGPTAuth: "OFFICIAL_SIWC; OFFLINE_SECURITY_TESTED; LIVE_ACCEPTANCE_PENDING", ChatGPTInference: "STATELESS_SSE; GPT_6_1_SOL_PUBLISHED_CEILING; LIVE_ACCEPTANCE_PENDING", RemoteProviders: "API_KEY_RUNTIME; JSON_SSE_OFFLINE_TESTED; LIVE_ACCEPTANCE_PENDING", LocalProvider: "DECLARED_LOCAL_RUNTIME_CONFORMANCE_PENDING", LiveApply: "UNSUPPORTED", CaptureConsistency: "BEST_EFFORT"}
	if runtime.GOOS == "windows" {
		d.LiveApply = "EXPLICIT_FIXED_NTFS_PROFILE; HOST_NOT_PROBED; GENERAL_UNSUPPORTED"
	}
	if *jsonMode {
		if err := jsonWrite(out, d); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Viber %s · %s\nSnapshot/proposal: developer preview · BEST_EFFORT\nOffline sandbox: developer profile · providers: fixture conformance\nLive apply: %s\nStable release: gates closed · telemetry/training: off\n", d.Version, d.Platform, d.LiveApply)
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
