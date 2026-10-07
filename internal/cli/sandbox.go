package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/runner"
)

// sandbox-run is a trusted operator engineering surface, not an agent bypass.
// The supplied policy/state files must come from the trusted session owner.
func runSandbox(args []string, out, errout io.Writer) int {
	f := flags("sandbox-run", errout)
	directory := f.String("store", "", "private archive")
	task := f.String("task", "", "task scope")
	digest := f.String("snapshot", "", "snapshot digest")
	profileFile := f.String("profile", "", "pinned offline runner profile")
	policyFile := f.String("policy", "", "trusted restriction layers")
	authorityFile := f.String("authority", "", "trusted current task state")
	jsonMode := f.Bool("json", false, "JSON computation outcome")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if *directory == "" || *task == "" || *digest == "" || *profileFile == "" || *policyFile == "" || *authorityFile == "" || f.NArg() == 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "store, task, snapshot, profile, policy, authority and -- argv required"), *jsonMode)
	}
	var profile runner.Profile
	var layers []policy.Policy
	var authority c.TaskState
	for _, entry := range []struct {
		path   string
		target any
	}{{*profileFile, &profile}, {*policyFile, &layers}, {*authorityFile, &authority}} {
		if err := readJSON(entry.path, entry.target); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if authority.SchemaVersion != 1 || authority.TaskID != *task || authority.Execution != c.Running && authority.Execution != c.Verifying {
		return report(out, errout, c.Fail(c.StaleAuthority, "sandbox invocation requires active task authority"), *jsonMode)
	}
	if err := policy.Admit(layers, policy.Action{Epoch: authority.PolicyEpoch, Generation: authority.KernelGeneration, InputBarrier: authority.InputBarrier, Effect: "process.run"}); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	archive, err := artifact.Open(*directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer archive.Close()
	ref := artifact.Ref{SchemaVersion: 1, TaskID: *task, SnapshotDigest: *digest}
	materialized, err := archive.Materialize(ref)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	broker, err := runner.OpenDocker()
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer broker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(profile.TimeoutSeconds+60)*time.Second)
	defer cancel()
	result, err := broker.Run(ctx, materialized.SourceDirectory, profile, runner.Invocation{CandidateDigest: *digest, Argv: f.Args()})
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	// Reopening/revalidating the frozen export supplements the enforced readonly
	// mount. It does not replace the backend enforcement or produce a PASS receipt.
	if _, err = archive.Materialize(ref); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, result); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Sandbox exit %d · readonly %t · quiescent %t\nstdout %s\nstderr %s\n", result.ExitCode, result.SourceReadOnly, result.ProcessTreeQuiescent, strconv.QuoteToASCII(string(result.Stdout)), strconv.QuoteToASCII(string(result.Stderr)))
	}
	if result.TimedOut || result.Cancelled || result.OOMKilled || result.OutputTruncated || result.ExitCode != 0 {
		return 4
	}
	return 0
}
