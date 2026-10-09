package agent

import (
	"context"
	"errors"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/verify"
	"time"
)

type ObservedEvidence struct {
	SchemaVersion    int                 `json:"schema_version"`
	Binding          c.Binding           `json:"binding"`
	GoalReviewDigest string              `json:"goal_review_digest,omitempty"`
	Baseline         *verify.ObservedRun `json:"baseline,omitempty"`
	Current          *verify.ObservedRun `json:"current,omitempty"`
}

func checkSetDigest(doc Document) string {
	var plan verify.CheckPlan
	if doc.Protection != nil {
		plan = doc.Protection.Plan
	}
	digest, _ := c.Digest(struct {
		Plan     verify.CheckPlan `json:"plan"`
		Runtime  *CheckRuntime    `json:"runtime,omitempty"`
		Protocol string           `json:"protocol"`
	}{plan, doc.CheckRuntime, verify.ObserverProtocol})
	return digest
}
func verificationBinding(doc Document, state c.TaskState) c.Binding {
	env := ""
	if doc.CheckRuntime != nil {
		env, _ = c.Digest(doc.CheckRuntime.Profile)
	}
	policy, _ := c.Digest((&Session{}).layers(state, doc))
	return c.Binding{TaskID: doc.TaskID, SpecVersion: doc.Spec.Version, CandidateDigest: doc.Candidate.SnapshotDigest, CheckSetDigest: checkSetDigest(doc), EnvironmentDigest: env, PolicyDigest: policy}
}
func observerSuite(doc Document, id string) (verify.StdioSuite, verify.CheckDefinition, bool) {
	if doc.CheckRuntime == nil || doc.Protection == nil {
		return verify.StdioSuite{}, verify.CheckDefinition{}, false
	}
	for _, suite := range doc.CheckRuntime.ObserverSuites {
		if suite.CheckID != id {
			continue
		}
		for _, check := range doc.Protection.Plan.Checks {
			if check.ID == id {
				return suite, check, true
			}
		}
	}
	return verify.StdioSuite{}, verify.CheckDefinition{}, false
}
func validateObserverConfig(doc Document) error {
	if doc.CheckRuntime == nil || doc.Protection == nil {
		return c.Fail(c.InvalidArgument, "observer requires protected check runtime")
	}
	if len(doc.CheckRuntime.ObserverSuites) > 128 {
		return c.Fail(c.InvalidArgument, "observer suite quota exceeded")
	}
	seen := map[string]bool{}
	for _, suite := range doc.CheckRuntime.ObserverSuites {
		if seen[suite.CheckID] {
			return c.Fail(c.InvalidArgument, "duplicate observer suite")
		}
		seen[suite.CheckID] = true
		found := false
		for _, check := range doc.Protection.Plan.Checks {
			if check.ID == suite.CheckID {
				found = true
				if err := suite.Validate(check, doc.CheckRuntime.Profile); err != nil {
					return err
				}
			}
		}
		if !found {
			return c.Fail(c.PolicyDenied, "oracle is outside protected check set")
		}
	}
	for _, check := range doc.Protection.Plan.Checks {
		if (check.ObserverDigest != "") != seen[check.ID] {
			return c.Fail(c.PolicyDenied, "protected oracle missing or silently downgraded")
		}
	}
	if doc.GoalCoverage != nil {
		if doc.GoalCoverage.Spec.TaskID != doc.TaskID || doc.GoalCoverage.Spec.Version > doc.Spec.Version {
			return c.Fail(c.StoreIntegrityError, "coverage task/version mismatch")
		}
		return verify.ValidateCoverage(*doc.GoalCoverage, doc.Protection.Plan, checkSetDigest(doc))
	}
	return nil
}
func observedOutcome(e *ObservedEvidence) string {
	if e == nil || e.Current == nil || !e.Current.Complete || e.Baseline == nil || !e.Baseline.Complete {
		return "OBSERVER_INCOMPLETE"
	}
	switch e.Current.Verdict {
	case c.Pass:
		return "OBSERVER_PASS"
	case c.FailVerdict:
		return "OBSERVER_FAIL"
	default:
		return "OBSERVER_UNKNOWN"
	}
}
func observedCurrent(doc Document, state c.TaskState, record CheckRun) bool {
	return record.Observer != nil && record.SpecVersion == doc.Spec.Version && record.PolicyEpoch == state.PolicyEpoch && record.Observer.Binding == verificationBinding(doc, state)
}
func (s *Session) validateObservedEvidence(doc Document, ref CheckRunRef, record CheckRun) error {
	suite, check, ok := observerSuite(doc, record.CheckID)
	e := record.Observer
	if !ok || e == nil || e.SchemaVersion != 1 || record.SchemaVersion != 1 || record.TaskID != doc.TaskID || record.CallID != ref.ID || record.CallID == "" || record.SpecVersion < 1 || record.SpecVersion > doc.Spec.Version || record.PolicyEpoch < 1 || record.Generation < 1 || record.OriginDigest != doc.Spec.ProtectedOrigin || record.Discovery != verify.ObserverProtocol || record.Outcome != observedOutcome(e) || e.Binding.TaskID != doc.TaskID || e.Binding.SpecVersion != record.SpecVersion || e.Binding.CheckSetDigest != checkSetDigest(doc) || !c.ValidDigest(e.Binding.CandidateDigest) || !c.ValidDigest(e.Binding.PolicyDigest) {
		return c.Fail(c.StoreIntegrityError, "protected observer receipt binding mismatch")
	}
	env, _ := c.Digest(doc.CheckRuntime.Profile)
	if e.Binding.EnvironmentDigest != env {
		return c.Fail(c.StoreIntegrityError, "observer environment changed")
	}
	if e.GoalReviewDigest != "" {
		digest, _ := c.Digest(doc.GoalCoverage)
		if doc.GoalCoverage == nil || digest != e.GoalReviewDigest {
			return c.Fail(c.StoreIntegrityError, "observer goal review changed")
		}
	}
	candidate, err := s.Archive.Get(artifact.Ref{SchemaVersion: 1, TaskID: doc.TaskID, SnapshotDigest: e.Binding.CandidateDigest})
	if err != nil {
		return err
	}
	if err = verify.GuardProtectedCandidate(*doc.Protection, candidate); err != nil {
		return err
	}
	if e.Baseline != nil {
		if err = verify.ValidateObservedRun(*e.Baseline, doc.CheckRuntime.Profile, doc.Baseline.SnapshotDigest, check, suite); err != nil {
			return err
		}
	}
	if e.Current != nil {
		if err = verify.ValidateObservedRun(*e.Current, doc.CheckRuntime.Profile, e.Binding.CandidateDigest, check, suite); err != nil {
			return err
		}
		if e.Baseline == nil || !e.Baseline.Complete {
			return c.Fail(c.StoreIntegrityError, "subject ran before baseline observer completed")
		}
	}
	containers := map[string]bool{}
	for _, run := range []*verify.ObservedRun{e.Baseline, e.Current} {
		if run != nil {
			for _, a := range run.Attempts {
				id := a.Result.ContainerID
				if !c.ValidDigest(id) {
					continue
				}
				if containers[id] {
					return c.Fail(c.StoreIntegrityError, "baseline/current observer reused subject identity")
				}
				containers[id] = true
			}
		}
	}
	var expected runner.Result
	if e.Current != nil && len(e.Current.Attempts) > 0 {
		expected = e.Current.Attempts[0].Result
	}
	expectedDigest, _ := c.Digest(expected)
	actualDigest, _ := c.Digest(record.Result)
	if expectedDigest != actualDigest {
		return c.Fail(c.StoreIntegrityError, "check output differs from observed subject artifact")
	}
	return nil
}
func (s *Session) executeObservedCheck(ctx context.Context, state c.TaskState, doc *Document, call model.Call, broker *runner.Docker, source string, check verify.CheckDefinition, suite verify.StdioSuite) (any, error) {
	remaining := max(int64(1), doc.Budget.MaxActiveMillis-doc.Budget.ActiveMillis)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(remaining)*time.Millisecond)
	defer cancel()
	baseline, err := s.Archive.Materialize(doc.Baseline)
	if err != nil {
		return nil, err
	}
	evidence := &ObservedEvidence{SchemaVersion: 1, Binding: verificationBinding(*doc, state)}
	if doc.GoalCoverage != nil {
		evidence.GoalReviewDigest, _ = c.Digest(doc.GoalCoverage)
	}
	base, runErr := verify.Observe(runCtx, broker, baseline.SourceDirectory, doc.CheckRuntime.Profile, doc.Baseline.SnapshotDigest, check, suite)
	evidence.Baseline = &base
	if runErr == nil {
		current, currentErr := verify.Observe(runCtx, broker, source, doc.CheckRuntime.Profile, doc.Candidate.SnapshotDigest, check, suite)
		evidence.Current = &current
		runErr = currentErr
	}
	if runErr != nil {
		var failure *runner.DispatchFailure
		// A missing broker acknowledgement never becomes PASS or a blind retry.
		possible := !errors.As(runErr, &failure) || failure.EffectPossible
		failedRun := evidence.Current
		if failedRun == nil {
			failedRun = evidence.Baseline
		}
		if failedRun != nil && len(failedRun.Attempts) > 0 && failedRun.Attempts[len(failedRun.Attempts)-1].Result.ProcessTreeQuiescent {
			possible = false
		}
		if possible {
			doc.UnknownEffect = true
			if doc.Pending != nil {
				doc.Pending.Status = "UNKNOWN"
			}
		}
	}
	if _, err = s.Archive.Materialize(doc.Baseline); err != nil {
		doc.UnknownEffect = true
		if doc.Pending != nil {
			doc.Pending.Status = "UNKNOWN"
		}
		return nil, err
	}
	if _, err = s.Archive.Materialize(doc.Candidate); err != nil {
		doc.UnknownEffect = true
		if doc.Pending != nil {
			doc.Pending.Status = "UNKNOWN"
		}
		return nil, err
	}
	record := CheckRun{SchemaVersion: 1, TaskID: doc.TaskID, CallID: call.ID, CheckID: check.ID, SpecVersion: doc.Spec.Version, PolicyEpoch: state.PolicyEpoch, Generation: state.KernelGeneration, OriginDigest: doc.Spec.ProtectedOrigin, Discovery: verify.ObserverProtocol, Observer: evidence}
	record.Outcome = observedOutcome(evidence)
	if evidence.Current != nil && len(evidence.Current.Attempts) > 0 {
		record.Result = evidence.Current.Attempts[0].Result
	}
	raw, err := c.CanonicalV1(record)
	if err != nil {
		doc.UnknownEffect = true
		if doc.Pending != nil {
			doc.Pending.Status = "UNKNOWN"
		}
		return nil, err
	}
	digest, err := s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		doc.UnknownEffect = true
		if doc.Pending != nil {
			doc.Pending.Status = "UNKNOWN"
		}
		return nil, err
	}
	doc.CheckRuns = append(doc.CheckRuns, CheckRunRef{ID: call.ID, Digest: digest})
	if runErr != nil {
		return nil, runErr
	}
	return struct {
		RunID         string    `json:"run_id"`
		CheckID       string    `json:"check_id"`
		ReceiptDigest string    `json:"receipt_digest"`
		Outcome       string    `json:"computation_outcome"`
		Verdict       c.Verdict `json:"criterion_verdict"`
		Flaky         bool      `json:"flaky"`
		Executed      int       `json:"executed_attempts"`
	}{call.ID, check.ID, digest, record.Outcome, evidence.Current.Verdict, evidence.Current.Flaky, len(evidence.Current.Attempts)}, nil
}

func checkCandidate(record CheckRun) string {
	if record.Observer != nil {
		return record.Observer.Binding.CandidateDigest
	}
	return record.Result.CandidateDigest
}
