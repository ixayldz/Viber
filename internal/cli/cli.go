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

const Version = "0.6.0-dev"
const help = `Viber 0.6.0-dev — offline engineering agent (stable release gates closed)

Usage:
  viber run "TASK" --offline --fixture FILE --root PATH --store PATH [--check-plan FILE] [--allow-unverified] [--json]
  viber status|diff|pause|cancel|resume TASK --store PATH [--json]
  viber store-backup --store PATH --output NEW_PATH [--json]
  viber store-restore --backup PATH --store NEW_PATH [--json]
  viber store-migrate --store PATH --backup-output PATH --command-id ID [--json]
  viber store-migration-status --store PATH [--json]
  viber requests TASK --store PATH [--json]
  viber respond TASK --store PATH --request ID --response-file FILE [--json]
  viber inspect TASK --store PATH [--at TASK_SEQ] [--json]
  viber replay TASK --store PATH [--until TASK_SEQ] [--json]
  viber events TASK --store PATH [--after TASK_SEQ] [--limit N] [--json]
  viber export TASK --store PATH --output NEW_PATH [--json]
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
never produce VERIFIED; limited delivery requires --allow-unverified and exits 2.
The implementation/release plan is in docs/IMPLEMENTATION_PLAN.md.
`

type doctor struct {
	SchemaVersion      int    `json:"schema_version"`
	Version            string `json:"version"`
	Platform           string `json:"platform"`
	GitDiscovered      bool   `json:"git_discovered"`
	SnapshotPreview    bool   `json:"snapshot_preview"`
	ProposalPreview    bool   `json:"proposal_preview"`
	ProcessSandbox     string `json:"process_sandbox"`
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
	case "run":
		return runTask(args[1:], out, errout)
	case "inspect", "replay", "events":
		return runHistory(args[0], args[1:], out, errout)
	case "resume", "pause", "cancel", "status", "diff":
		return runTaskControl(args[0], args[1:], out, errout)
	case "steer", "revise":
		return runSteering(args[0], args[1:], out, errout)
	case "apply", "restore", "attach", "delete":
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
	d := doctor{SchemaVersion: c.SchemaVersion, Version: Version, Platform: runtime.GOOS + "/" + runtime.GOARCH, GitDiscovered: gitErr == nil, SnapshotPreview: true, ProposalPreview: true, ProcessSandbox: "DEVELOPER_OFFLINE_V1_CONFORMANCE_PENDING", RemoteProviders: "OFFLINE_PROTOCOL_FIXTURES", LocalProvider: "LOOPBACK_PROTOCOL_FIXTURES", LiveApply: "UNSUPPORTED", CaptureConsistency: "BEST_EFFORT"}
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
