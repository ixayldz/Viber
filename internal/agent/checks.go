package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ixayldz/Viber/internal/verify"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/runner"
)

// CheckRuntime is operator input. Models select IDs, never command lines,
// images, profiles or observer authority.
type CheckRuntime struct {
	SchemaVersion int            `json:"schema_version"`
	Profile       runner.Profile `json:"profile"`
}
type CheckRunRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}
type CheckRun struct {
	SchemaVersion int           `json:"schema_version"`
	TaskID        string        `json:"task_id"`
	CallID        string        `json:"call_id"`
	CheckID       string        `json:"check_id"`
	SpecVersion   int64         `json:"spec_version"`
	PolicyEpoch   int64         `json:"policy_epoch"`
	Generation    int64         `json:"generation"`
	OriginDigest  string        `json:"origin_digest"`
	Outcome       string        `json:"computation_outcome"`
	Discovery     string        `json:"discovery_authority"`
	Result        runner.Result `json:"result"`
}
type CheckSummary struct {
	CheckID      string `json:"check_id"`
	RunID        string `json:"run_id,omitempty"`
	Digest       string `json:"digest,omitempty"`
	Candidate    string `json:"candidate,omitempty"`
	Current      bool   `json:"current"`
	Outcome      string `json:"computation_outcome"`
	Verification string `json:"verification"`
}

func checkTools() []model.Tool {
	return []model.Tool{
		{Name: "check_run", Description: "Run one registered operator check ID in its pinned offline readonly Docker environment. No host fallback. Exit zero is computation evidence only; discovery and goal verification remain UNKNOWN.", Parameters: json.RawMessage(`{"type":"object","properties":{"check_id":{"type":"string"}},"required":["check_id"],"additionalProperties":false}`)},
		{Name: "check_output", Description: "Read an exact-byte page of retained untrusted check stdout or stderr by run ID. Offset is a byte position; missing pages are not proof of absence.", Parameters: json.RawMessage(`{"type":"object","properties":{"run_id":{"type":"string"},"stream":{"type":"string","enum":["stdout","stderr"]},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":16384}},"required":["run_id","stream","offset","limit"],"additionalProperties":false}`)},
	}
}
func validateCheckRuntime(doc Document) error {
	if doc.CheckRuntime == nil {
		if len(doc.CheckRuns) != 0 {
			return c.Fail(c.StoreIntegrityError, "check receipts without operator runtime")
		}
		return nil
	}
	if doc.CheckRuntime.SchemaVersion != 1 || doc.CheckRuntime.Profile.Validate() != nil || doc.Protection == nil || len(doc.Protection.Plan.Checks) == 0 || len(doc.CheckRuns) > 256 {
		return c.Fail(c.InvalidArgument, "registered protected checks and valid bounded runtime required")
	}
	digest, err := c.Digest(doc.CheckRuntime.Profile)
	if err != nil {
		return err
	}
	for _, check := range doc.Protection.Plan.Checks {
		if check.RunnerDigest != digest {
			return c.Fail(c.PolicyDenied, "check runner digest differs from operator profile")
		}
	}
	return nil
}
func checkOutcome(r runner.Result) string {
	switch {
	case !r.ProcessTreeQuiescent || !r.SourceReadOnly || !r.ProtectedResultChannel || r.ExitCode < 0:
		return "UNKNOWN"
	case r.OutputTruncated:
		return "OUTPUT_LIMIT"
	case r.OOMKilled:
		return "OOM"
	case r.TimedOut:
		return "TIMEOUT"
	case r.Cancelled:
		return "CANCELLED"
	case r.ExitCode != 0:
		return "EXIT_NONZERO"
	default:
		return "EXIT_ZERO"
	}
}
func (s *Session) readCheckRun(doc Document, ref CheckRunRef) (CheckRun, error) {
	var record CheckRun
	raw, err := s.Archive.GetBytes(doc.TaskID, ref.Digest)
	if err != nil {
		return record, c.Fail(c.StoreIntegrityError, "retained check receipt unavailable")
	}
	if err = c.DecodeStrict(raw, &record); err != nil {
		return record, err
	}
	if doc.CheckRuntime == nil || doc.Protection == nil || record.SchemaVersion != 1 || record.TaskID != doc.TaskID || record.CallID != ref.ID || record.CallID == "" || record.SpecVersion < 1 || record.SpecVersion > doc.Spec.Version || record.PolicyEpoch < 1 || record.Generation < 1 || record.OriginDigest != doc.Spec.ProtectedOrigin || record.Discovery != "UNTRUSTED_UNRESOLVED" || record.Outcome != checkOutcome(record.Result) || record.Result.SchemaVersion != 1 || record.Result.DurationMillis < 0 || !c.ValidDigest(record.Result.ContainerID) {
		return record, c.Fail(c.StoreIntegrityError, "check receipt authority binding mismatch")
	}
	profileDigest, _ := c.Digest(doc.CheckRuntime.Profile)
	var argv []string
	for _, check := range doc.Protection.Plan.Checks {
		if check.ID == record.CheckID {
			argv = check.Argv
		}
	}
	invocationDigest, _ := c.Digest(runner.Invocation{CandidateDigest: record.Result.CandidateDigest, Argv: argv})
	if argv == nil || record.Result.ProfileDigest != profileDigest || record.Result.InvocationDigest != invocationDigest || int64(len(record.Result.Stdout)) > doc.CheckRuntime.Profile.MaxOutputBytes || int64(len(record.Result.Stderr)) > doc.CheckRuntime.Profile.MaxOutputBytes {
		return record, c.Fail(c.StoreIntegrityError, "check receipt command/profile/output binding mismatch")
	}
	candidate, err := s.Archive.Get(artifact.Ref{SchemaVersion: 1, TaskID: doc.TaskID, SnapshotDigest: record.Result.CandidateDigest})
	if err != nil {
		return record, err
	}
	if err = verify.GuardProtectedCandidate(*doc.Protection, candidate); err != nil {
		return record, err
	}
	return record, nil
}
func (s *Session) validateChecks(doc Document) error {
	if err := validateCheckRuntime(doc); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ref := range doc.CheckRuns {
		if ref.ID == "" || seen[ref.ID] || !c.ValidDigest(ref.Digest) {
			return c.Fail(c.StoreIntegrityError, "duplicate or invalid check receipt reference")
		}
		seen[ref.ID] = true
		if _, err := s.readCheckRun(doc, ref); err != nil {
			return err
		}
	}
	return nil
}
func (s *Session) CheckSummaries(doc Document, state c.TaskState) ([]CheckSummary, error) {
	result := []CheckSummary{}
	if doc.Protection == nil {
		return result, nil
	}
	for _, check := range doc.Protection.Plan.Checks {
		summary := CheckSummary{CheckID: check.ID, Outcome: "NOT_RUN", Verification: "UNKNOWN"}
		for _, ref := range doc.CheckRuns {
			record, err := s.readCheckRun(doc, ref)
			if err != nil {
				return nil, err
			}
			if record.CheckID != check.ID {
				continue
			}
			summary = CheckSummary{CheckID: check.ID, RunID: ref.ID, Digest: ref.Digest, Candidate: record.Result.CandidateDigest, Current: record.Result.CandidateDigest == doc.Candidate.SnapshotDigest && record.SpecVersion == doc.Spec.Version && record.PolicyEpoch == state.PolicyEpoch, Outcome: record.Outcome, Verification: "UNKNOWN"}
		}
		result = append(result, summary)
	}
	return result, nil
}
func (s *Session) executeCheck(ctx context.Context, state c.TaskState, doc *Document, call model.Call) (any, error) {
	if call.Name == "check_output" {
		var args struct {
			RunID  string `json:"run_id"`
			Stream string `json:"stream"`
			Offset int64  `json:"offset"`
			Limit  int64  `json:"limit"`
		}
		if err := c.DecodeStrict(call.Arguments, &args); err != nil {
			return nil, err
		}
		if args.Offset < 0 || args.Limit < 1 || args.Limit > 16384 || args.Stream != "stdout" && args.Stream != "stderr" {
			return nil, c.Fail(c.InvalidArgument, "invalid check output byte page")
		}
		for _, ref := range doc.CheckRuns {
			if ref.ID != args.RunID {
				continue
			}
			record, err := s.readCheckRun(*doc, ref)
			if err != nil {
				return nil, err
			}
			data := record.Result.Stdout
			if args.Stream == "stderr" {
				data = record.Result.Stderr
			}
			if args.Offset > int64(len(data)) {
				return nil, c.Fail(c.InvalidArgument, "output offset outside retained bytes")
			}
			end := min(args.Offset+args.Limit, int64(len(data)))
			return struct {
				RunID    string `json:"run_id"`
				Stream   string `json:"stream"`
				Bytes    []byte `json:"untrusted_exact_bytes"`
				Total    int64  `json:"total_bytes"`
				Next     int64  `json:"next_offset"`
				Complete bool   `json:"complete"`
			}{args.RunID, args.Stream, data[args.Offset:end], int64(len(data)), end, end == int64(len(data))}, nil
		}
		return nil, c.Fail(c.InvalidArgument, "run ID unavailable in this task")
	}
	var args struct {
		CheckID string `json:"check_id"`
	}
	if err := c.DecodeStrict(call.Arguments, &args); err != nil {
		return nil, err
	}
	if doc.CheckRuntime == nil {
		return nil, c.Fail(c.UnsupportedCapability, "operator --check-runtime configuration required; no host execution fallback")
	}
	if err := validateCheckRuntime(*doc); err != nil {
		return nil, err
	}
	if len(doc.CheckRuns) >= 256 {
		return nil, c.Fail(c.BudgetLimitReached, "retained check attempt quota exhausted")
	}
	var argv []string
	for _, check := range doc.Protection.Plan.Checks {
		if check.ID == args.CheckID {
			argv = check.Argv
		}
	}
	if argv == nil {
		return nil, c.Fail(c.PolicyDenied, "check is not registered by the operator")
	}
	if node := doc.Plan.Active(); node != nil {
		allowed := false
		for _, id := range node.RequiredChecks {
			allowed = allowed || id == args.CheckID
		}
		if !allowed {
			return nil, c.Fail(c.PolicyDenied, "check is outside active plan node")
		}
	}
	if err := s.validateProtection(*doc); err != nil {
		return nil, err
	}
	materialized, err := s.Archive.Materialize(doc.Candidate)
	if err != nil {
		return nil, err
	}
	broker, err := runner.OpenDocker()
	if err != nil {
		return nil, err
	}
	defer broker.Close()
	if err = broker.Check(ctx); err != nil {
		return nil, err
	}
	fresh, err := s.State(ctx, doc.TaskID)
	if err != nil {
		return nil, err
	}
	if fresh.TaskSeq != state.TaskSeq || fresh.Execution != c.Running || fresh.SpecVersion != doc.Spec.Version || fresh.CandidateDigest != doc.Candidate.SnapshotDigest || fresh.KernelGeneration != s.Journal.Generation() {
		return nil, c.Fail(c.StaleAuthority, "check authority changed before process dispatch")
	}
	if err = policy.Admit(s.layers(fresh, *doc), policy.Action{Epoch: fresh.PolicyEpoch, Generation: fresh.KernelGeneration, InputBarrier: fresh.InputBarrier, Effect: "check.run"}); err != nil {
		return nil, err
	}
	result, runErr := broker.Run(ctx, materialized.SourceDirectory, doc.CheckRuntime.Profile, runner.Invocation{CandidateDigest: doc.Candidate.SnapshotDigest, Argv: argv})
	if runErr != nil {
		// The broker may have created a process before losing its reply.
		// Never turn missing cleanup proof into a retryable model tool error.
		var failure *runner.DispatchFailure
		if !result.ProcessTreeQuiescent && (!errors.As(runErr, &failure) || failure.EffectPossible) {
			doc.UnknownEffect = true
			doc.Pending.Status = "UNKNOWN"
		}
		return nil, runErr
	}
	if _, err = s.Archive.Materialize(doc.Candidate); err != nil {
		doc.UnknownEffect = true
		doc.Pending.Status = "UNKNOWN"
		return nil, err
	}
	record := CheckRun{SchemaVersion: 1, TaskID: doc.TaskID, CallID: call.ID, CheckID: args.CheckID, SpecVersion: doc.Spec.Version, PolicyEpoch: state.PolicyEpoch, Generation: state.KernelGeneration, OriginDigest: doc.Spec.ProtectedOrigin, Outcome: checkOutcome(result), Discovery: "UNTRUSTED_UNRESOLVED", Result: result}
	raw, err := c.CanonicalV1(record)
	if err != nil {
		doc.UnknownEffect = true
		return nil, err
	}
	digest, err := s.Archive.PutBytes(doc.TaskID, raw)
	if err != nil {
		doc.UnknownEffect = true
		return nil, err
	}
	doc.CheckRuns = append(doc.CheckRuns, CheckRunRef{ID: call.ID, Digest: digest})
	if record.Outcome == "UNKNOWN" {
		doc.UnknownEffect = true
		doc.Pending.Status = "UNKNOWN"
	}
	return struct {
		RunID   string `json:"run_id"`
		CheckID string `json:"check_id"`
		Digest  string `json:"receipt_digest"`
		Outcome string `json:"computation_outcome"`
		Quality string `json:"verification"`
		Output  string `json:"output_access"`
	}{call.ID, args.CheckID, digest, record.Outcome, "UNKNOWN", fmt.Sprintf("check_output run_id=%s", call.ID)}, nil
}
