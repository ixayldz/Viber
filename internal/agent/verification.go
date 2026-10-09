package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/verify"
)

type BaselineComparison struct {
	RunID     string    `json:"run_id"`
	Candidate string    `json:"baseline_candidate"`
	Verdict   c.Verdict `json:"verdict"`
	Flaky     bool      `json:"flaky"`
	Attempts  int       `json:"attempts"`
}
type FrozenVerification struct {
	Baselines        []BaselineComparison    `json:"baseline_comparisons"`
	SchemaVersion    int                     `json:"schema_version"`
	SpecDigest       string                  `json:"spec_digest"`
	GoalReviewDigest string                  `json:"goal_review_digest,omitempty"`
	PolicyEpoch      int64                   `json:"policy_epoch"`
	Generation       int64                   `json:"generation"`
	ArtifactDigest   string                  `json:"artifact_digest"`
	Binding          c.Binding               `json:"binding"`
	RunDigests       []string                `json:"run_digests"`
	Receipts         []c.VerificationReceipt `json:"receipts"`
	Guards           verify.Guards           `json:"guards"`
	Assessment       verify.Assessment       `json:"assessment"`
	Scope            string                  `json:"scope"`
}

func observerFacts(run *verify.ObservedRun) bool {
	if run == nil || !run.Complete || len(run.Attempts) == 0 {
		return false
	}
	for _, a := range run.Attempts {
		r := a.Result
		if a.BrokerError || r.SchemaVersion != 1 || !r.SourceReadOnly || !r.ProtectedResultChannel || !r.ProcessTreeQuiescent || r.ExitCode < 0 || r.OutputTruncated || r.Cancelled || r.TimedOut || r.OOMKilled {
			return false
		}
	}
	return true
}
func (s *Session) deriveVerification(doc Document, state c.TaskState) (FrozenVerification, error) {
	report := FrozenVerification{Baselines: []BaselineComparison{}, SchemaVersion: 1, PolicyEpoch: state.PolicyEpoch, Generation: state.KernelGeneration, ArtifactDigest: doc.FinalArtifactDigest, Binding: verificationBinding(doc, state), RunDigests: []string{}, Receipts: []c.VerificationReceipt{}, Scope: "OPERATOR_REVIEWED_EXACT_STDIO_CASES; IMMUTABLE_CANDIDATE_ONLY"}
	report.SpecDigest, _ = c.Digest(doc.Spec)
	if doc.GoalCoverage != nil {
		report.GoalReviewDigest, _ = c.Digest(doc.GoalCoverage)
	}
	g := verify.Guards{RawIntentAvailable: true, GoalCoverageReviewed: verify.CoverageCurrent(doc.GoalCoverage, doc.Spec, checkSetDigest(doc)), ManifestCoherent: doc.FinalReady, ManifestIntact: true, ProtectedOriginIntact: true, SourceEnforcement: true, ProcessTreeQuiescent: true, NoRelevantUnknownEffects: !doc.UnknownEffect && doc.Pending == nil, NoStalePreconditions: true, NoPolicyViolation: true, RequiredGatesPass: doc.Protection != nil && doc.CheckRuntime != nil && len(registeredChecks(doc)) > 0 && doc.Plan.Complete(), NoPendingInput: !state.InputBarrier && len(state.PendingInputIDs) == 0}
	for _, input := range doc.Spec.Inputs {
		raw, err := s.Archive.GetBytes(doc.TaskID, input.Digest)
		if err != nil || int64(len(raw)) != input.ByteLength {
			return report, c.Fail(c.StoreIntegrityError, "verification raw intent unavailable")
		}
	}
	if _, err := s.Archive.Get(doc.Candidate); err != nil {
		return report, err
	}
	if err := s.validateProtection(doc); err != nil {
		return report, err
	}
	for _, r := range []bool{unresolvedResource(state) == nil, tokensQuiescent(state)} {
		g.NoRelevantUnknownEffects = g.NoRelevantUnknownEffects && r
	}
	currentChecks := map[string]bool{}
	for _, ref := range doc.CheckRuns {
		record, err := s.readCheckRun(doc, ref)
		if err != nil {
			return report, err
		}
		if !observedCurrent(doc, state, record) {
			continue
		}
		e := record.Observer
		if e.Baseline != nil {
			report.Baselines = append(report.Baselines, BaselineComparison{RunID: ref.ID, Candidate: e.Baseline.Candidate, Verdict: e.Baseline.Verdict, Flaky: e.Baseline.Flaky, Attempts: len(e.Baseline.Attempts)})
		}
		_, check, ok := observerSuite(doc, record.CheckID)
		if !ok {
			return report, c.Fail(c.StoreIntegrityError, "current observer not registered")
		}
		complete := observerFacts(e.Current) && observerFacts(e.Baseline)
		g.SourceEnforcement = g.SourceEnforcement && complete
		g.ProcessTreeQuiescent = g.ProcessTreeQuiescent && complete
		currentChecks[record.CheckID] = complete && e.Current.Verdict == c.Pass && !e.Current.Flaky && !e.Baseline.Flaky
		report.RunDigests = append(report.RunDigests, ref.Digest)
		verdict := c.Unknown
		executed := int64(0)
		if e.Current != nil && e.Current.Complete {
			verdict = e.Current.Verdict
			executed = int64(len(e.Current.Attempts))
		}
		receipt := c.VerificationReceipt{ID: ref.ID, CheckKind: "TEST", Binding: e.Binding, RequirementIDs: append([]string{}, check.RequirementIDs...), Verdict: verdict, Integrity: c.Intact, PolicyCompatible: true, SourceReadOnly: complete, ProtectedResultChannel: complete, ProcessTreeQuiescent: complete, DiscoveryExpected: complete, ExecutedTests: executed, TrustedCompletion: complete, IndependentObserver: true}
		report.Receipts = append(report.Receipts, receipt)
	}
	for _, check := range registeredChecks(doc) {
		g.RequiredGatesPass = g.RequiredGatesPass && currentChecks[check.ID]
	}
	if len(report.Receipts) == 0 {
		g.SourceEnforcement = false
		g.ProcessTreeQuiescent = false
	}
	report.Guards = g
	report.Assessment = verify.Assess(doc.Spec, report.Binding, report.Receipts, g)
	return report, nil
}
func (s *Session) validateVerificationReport(state c.TaskState, doc Document) error {
	if doc.VerificationReport == "" {
		return nil
	}
	if !doc.FinalReady || !c.ValidDigest(doc.FinalArtifactDigest) || !c.ValidDigest(doc.VerificationReport) {
		return c.Fail(c.StoreIntegrityError, "verification report without frozen final artifact")
	}
	raw, err := s.Archive.GetBytes(doc.TaskID, doc.VerificationReport)
	if err != nil {
		return err
	}
	var actual FrozenVerification
	if err = c.DecodeStrict(raw, &actual); err != nil {
		return err
	}
	expected, err := s.deriveVerification(doc, state)
	if err != nil {
		return err
	}
	expectedDigest, _ := c.Digest(expected)
	actualDigest, _ := c.Digest(actual)
	if expectedDigest != actualDigest || c.HashBytes(raw) != doc.VerificationReport {
		return c.Fail(c.StoreIntegrityError, "frozen verification/report authority mismatch")
	}
	return nil
}
func (s *Session) tryVerifiedFinal(ctx context.Context, state c.TaskState, doc Document) (c.TaskState, bool, error) {
	report, err := s.deriveVerification(doc, state)
	if err != nil {
		return state, false, err
	}
	if report.Assessment.Quality != c.Verified {
		return state, false, nil
	}
	if taskKind(doc) == "ANALYSIS" {
		return state, false, nil
	} // Stdio cannot validate report semantics.
	if doc.FinalArtifactDigest == "" {
		raw, err := c.CanonicalV1(finalArtifact(doc))
		if err != nil {
			return state, false, err
		}
		doc.FinalArtifactDigest, err = s.Archive.PutControlBytes(doc.TaskID, raw, true)
		if err != nil {
			return state, false, err
		}
	}
	report, err = s.deriveVerification(doc, state)
	if err != nil {
		return state, false, err
	}
	raw, err := c.CanonicalV1(report)
	if err != nil {
		return state, false, err
	}
	doc.VerificationReport, err = s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		return state, false, err
	}
	state, err = s.record(ctx, state, doc, "SessionRecorded", c.EventPayload{})
	if err != nil {
		return state, false, err
	}
	state, err = s.transition(ctx, state, c.Verifying, "", "frozen candidate; independent operator observer evidence")
	if err != nil {
		return state, false, err
	}
	if _, err = s.Archive.Materialize(doc.Candidate); err != nil {
		return state, false, err
	}
	if err = s.validateVerificationReport(state, doc); err != nil {
		return state, false, err
	}
	state, err = s.transition(ctx, state, c.Delivering, "", "verified immutable candidate-only publication")
	if err != nil {
		return state, false, err
	}
	proof := &c.VerificationFinalization{SourceSpecDigest: report.SpecDigest, Spec: verificationSpecProjection(doc.Spec), Binding: report.Binding, Receipts: report.Receipts, Guards: report.Guards, PolicyEpoch: state.PolicyEpoch, ArtifactDigest: doc.FinalArtifactDigest, ReportDigest: doc.VerificationReport}
	state, err = s.record(ctx, state, doc, "VerifiedResultFinalized", c.EventPayload{SnapshotDigest: doc.Candidate.SnapshotDigest, Verification: proof, Quality: c.Verified, Fulfillment: c.Satisfied, Reason: doc.VerificationReport})
	return state, true, err
}

func tokensQuiescent(state c.TaskState) bool {
	if state.Tokens != nil {
		for _, r := range state.Tokens.Reservations {
			if r.Status != "SETTLED" {
				return false
			}
		}
	}
	return true
}

func verificationSpecProjection(spec c.TaskSpec) c.TaskSpec {
	spec.Goal = c.VerificationProjectionGoal
	spec.Requirements = append([]c.Requirement{}, spec.Requirements...)
	for i := range spec.Requirements {
		spec.Requirements[i].VerificationMethod = "SOURCE_BOUND_PROTECTED_CHECKS_V1"
	}
	return spec
}
