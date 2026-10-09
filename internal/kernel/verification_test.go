package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"strings"
	"testing"
)

func strictFinalFixture() (c.TaskState, c.Event, c.EventPayload) {
	digest := strings.Repeat("a", 64)
	spec := c.TaskSpec{SchemaVersion: 1, TaskID: "task", Version: 1, Goal: c.VerificationProjectionGoal, Inputs: []c.InputSource{{ID: "input", PayloadRef: "blob://task/" + digest, Digest: digest, ByteLength: 4, Integrity: c.Intact}}, Requirements: []c.Requirement{{ID: "goal", Source: c.SourceSpan{InputID: "input", Start: 0, End: 4}, Required: true, Risk: "CRITICAL", VerificationMethod: "trusted observer"}}, ProtectedOrigin: digest, DeliveryPolicy: "CANDIDATE_ONLY"}
	binding := c.Binding{TaskID: "task", SpecVersion: 1, CandidateDigest: digest, CheckSetDigest: digest, EnvironmentDigest: digest, PolicyDigest: digest}
	receipt := c.VerificationReceipt{ID: "receipt", CheckKind: "TEST", Binding: binding, RequirementIDs: []string{"goal"}, Verdict: c.Pass, Integrity: c.Intact, PolicyCompatible: true, SourceReadOnly: true, ProtectedResultChannel: true, ProcessTreeQuiescent: true, DiscoveryExpected: true, ExecutedTests: 2, TrustedCompletion: true, IndependentObserver: true}
	guards := c.VerificationGuards{RawIntentAvailable: true, GoalCoverageReviewed: true, ManifestCoherent: true, ManifestIntact: true, ProtectedOriginIntact: true, SourceEnforcement: true, ProcessTreeQuiescent: true, NoRelevantUnknownEffects: true, NoStalePreconditions: true, NoPolicyViolation: true, RequiredGatesPass: true, NoPendingInput: true}
	state := c.TaskState{SchemaVersion: 1, TaskID: "task", SpecVersion: 1, TaskSeq: 5, StoreSeq: 5, KernelGeneration: 1, PolicyEpoch: 1, Execution: c.Delivering, Quality: c.Unverified, Fulfillment: c.FulfillmentPending, OpenRequiredObligations: 1, CandidateDigest: digest, DocumentDigest: digest}
	event := c.Event{ID: "final-event", SchemaVersion: 1, TaskID: "task", TaskSeq: 6, StoreSeq: 6, KernelGeneration: 1, Type: "VerifiedResultFinalized", Actor: "kernel"}
	payload := c.EventPayload{DocumentDigest: digest, SnapshotDigest: digest, Quality: c.Verified, Fulfillment: c.Satisfied, Reason: digest, Verification: &c.VerificationFinalization{SourceSpecDigest: digest, Spec: spec, Binding: binding, Receipts: []c.VerificationReceipt{receipt}, Guards: guards, PolicyEpoch: 1, ArtifactDigest: digest, ReportDigest: digest}}
	return state, event, payload
}
func TestVerifiedFinalRecomputesCompletePredicate(t *testing.T) {
	state, event, payload := strictFinalFixture()
	next, err := Reduce(&state, event, payload)
	if err != nil || !StrictSuccess(next) {
		t.Fatal(next, err)
	}
	mutations := map[string]func(*c.TaskState, *c.Event, *c.EventPayload){
		"model actor": func(s *c.TaskState, e *c.Event, p *c.EventPayload) { e.Actor = "model" },
		"raw intent":  func(s *c.TaskState, e *c.Event, p *c.EventPayload) { p.Verification.Guards.RawIntentAvailable = false },
		"goal coverage": func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			p.Verification.Guards.GoalCoverageReviewed = false
		},
		"zero discovery": func(s *c.TaskState, e *c.Event, p *c.EventPayload) { p.Verification.Receipts[0].ExecutedTests = 0 },
		"forged channel": func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			p.Verification.Receipts[0].ProtectedResultChannel = false
		},
		"self PASS": func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			p.Verification.Receipts[0].IndependentObserver = false
		},
		"failed attempt": func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			p.Verification.Receipts[0].Verdict = c.FailVerdict
		},
		"unknown effect": func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			p.Verification.Guards.NoRelevantUnknownEffects = false
		},
		"stale candidate": func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			p.Verification.Binding.CandidateDigest = strings.Repeat("b", 64)
		},
		"missing report": func(s *c.TaskState, e *c.Event, p *c.EventPayload) { p.Verification.ReportDigest = "" },
		"barrier": func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			s.InputBarrier = true
			s.PendingInputIDs = []string{"new"}
		},
		"outstanding criteria": func(s *c.TaskState, e *c.Event, p *c.EventPayload) { p.RequiredObligations = 1 },
		"live delivery unsupported": func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			p.Verification.Spec.DeliveryPolicy = "LIVE_APPLY_REQUIRED"
		},
		"wrong phase": func(s *c.TaskState, e *c.Event, p *c.EventPayload) { s.Execution = c.Running },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s, e, p := strictFinalFixture()
			mutate(&s, &e, &p)
			if _, err := Reduce(&s, e, p); err == nil {
				t.Fatal("invalid strict final accepted")
			}
		})
	}
}
