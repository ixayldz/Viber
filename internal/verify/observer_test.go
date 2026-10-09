package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/runner"
	"strings"
	"testing"
)

type fakeSubject struct {
	calls   int
	mutate  func(int, *runner.Result)
	failure error
}

func (s *fakeSubject) Run(ctx context.Context, source string, p runner.Profile, inv runner.Invocation) (runner.Result, error) {
	s.calls++
	profile, _ := c.Digest(p)
	invocation, _ := c.Digest(inv)
	r := runner.Result{SchemaVersion: 1, CandidateDigest: inv.CandidateDigest, ProfileDigest: profile, InvocationDigest: invocation, ContainerID: c.HashBytes([]byte(fmt.Sprint(s.calls))), ExitCode: 0, SourceReadOnly: true, ProtectedResultChannel: true, ProcessTreeQuiescent: true, Stdout: bytes.Clone(*inv.Stdin)}
	if s.mutate != nil {
		s.mutate(s.calls, &r)
	}
	return r, s.failure
}
func observerFixture() (runner.Profile, CheckDefinition, StdioSuite) {
	p := runner.DefaultProfile("golang@sha256:" + strings.Repeat("a", 64))
	p.MaxOutputBytes = 16 << 10
	suite := StdioSuite{SchemaVersion: 1, CheckID: "exact", Level: "V4", Protocol: ObserverProtocol, Repeats: 2, Cases: []StdioCase{{ID: "unicode", Input: []byte("ö\r\n\x00"), Stdout: []byte("ö\r\n\x00")}, {ID: "empty", Input: []byte{}, Stdout: []byte{}}}}
	suiteDigest, _ := c.Digest(suite)
	profile, _ := c.Digest(p)
	check := CheckDefinition{ID: "exact", Kind: "TEST", RequirementIDs: []string{"r"}, ExpectedTests: SuiteCaseIDs(suite), ObserverDigest: suiteDigest, Argv: []string{"/bin/cat"}, Selection: "all", RunnerDigest: profile, Closure: []CheckScope{{Path: "checks", Recursive: true}}}
	return p, check, suite
}
func TestProtectedObserverExactBytesAndAllAttempts(t *testing.T) {
	p, check, suite := observerFixture()
	subject := &fakeSubject{}
	run, err := Observe(context.Background(), subject, "immutable", p, c.HashBytes([]byte("candidate")), check, suite)
	if err != nil || run.Verdict != c.Pass || !run.Complete || run.Flaky || len(run.Attempts) != 4 || subject.calls != 4 {
		t.Fatal(run, err)
	}
	mutations := map[string]func(*ObservedRun){
		"candidate":            func(r *ObservedRun) { r.Candidate = c.HashBytes([]byte("different")) },
		"zero discovery":       func(r *ObservedRun) { r.Discovered = nil },
		"missing last attempt": func(r *ObservedRun) { r.Attempts = r.Attempts[:3] },
		"reused container":     func(r *ObservedRun) { r.Attempts[1].Result.ContainerID = r.Attempts[0].Result.ContainerID },
		"forged PASS":          func(r *ObservedRun) { r.Attempts[0].Result.Stdout = []byte("PASS") },
		"wrong stdin":          func(r *ObservedRun) { r.Attempts[0].Result.InvocationDigest = c.HashBytes([]byte("wrong-input")) },
		"incomplete PASS":      func(r *ObservedRun) { r.Complete = false },
		"skip":                 func(r *ObservedRun) { r.Attempts[0].Repeat = 2 },
		"missing readonly":     func(r *ObservedRun) { r.Attempts[0].Result.SourceReadOnly = false },
		"oracle swap":          func(r *ObservedRun) { r.SuiteDigest = c.HashBytes([]byte("weakened")) },
		"fake completion":      func(r *ObservedRun) { r.Attempts[0].BrokerError = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			raw, _ := c.CanonicalV1(run)
			var bad ObservedRun
			c.DecodeStrict(raw, &bad)
			mutate(&bad)
			if ValidateObservedRun(bad, p, run.Candidate, check, suite) == nil {
				t.Fatal("forged protected observer accepted")
			}
		})
	}
}
func TestObserverDoesNotWashOutFailOrUnknown(t *testing.T) {
	p, check, suite := observerFixture()
	scenarios := []struct {
		name    string
		mutate  func(int, *runner.Result)
		verdict c.Verdict
		flaky   bool
	}{
		{"fake PASS", func(n int, r *runner.Result) { r.Stdout = []byte("PASS") }, c.FailVerdict, false},
		{"pass after fail", func(n int, r *runner.Result) {
			if n == 1 {
				r.ExitCode = 9
			}
		}, c.FailVerdict, true},
		{"output limit", func(n int, r *runner.Result) { r.OutputTruncated = true }, c.Unknown, false},
		{"timeout", func(n int, r *runner.Result) { r.TimedOut = true }, c.Unknown, false},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			subject := &fakeSubject{mutate: scenario.mutate}
			run, err := Observe(context.Background(), subject, "immutable", p, c.HashBytes([]byte("candidate")), check, suite)
			if err != nil || run.Verdict != scenario.verdict || run.Flaky != scenario.flaky || len(run.Attempts) != 4 {
				t.Fatal(run, err)
			}
		})
	}
}
func TestObserverLostBrokerResponseAndCancelKeepEffectClass(t *testing.T) {
	p, check, suite := observerFixture()
	failure := &runner.DispatchFailure{Cause: errors.New("lost response"), EffectPossible: true}
	subject := &fakeSubject{failure: failure, mutate: func(n int, r *runner.Result) { *r = runner.Result{} }}
	run, err := Observe(context.Background(), subject, "immutable", p, c.HashBytes([]byte("candidate")), check, suite)
	var actual *runner.DispatchFailure
	if !errors.As(err, &actual) || !actual.EffectPossible || run.Complete || run.Verdict != c.Unknown || len(run.Attempts) != 1 || !run.Attempts[0].BrokerError {
		t.Fatal(run, err)
	}
	if err = ValidateObservedRun(run, p, run.Candidate, check, suite); err != nil {
		t.Fatal("incomplete retention rejected", err)
	}
	run.Attempts[0].CaseID = "forged"
	if ValidateObservedRun(run, p, run.Candidate, check, suite) == nil {
		t.Fatal("partial receipt lost case binding")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	subject = &fakeSubject{}
	run, err = Observe(ctx, subject, "immutable", p, c.HashBytes([]byte("candidate")), check, suite)
	if !errors.As(err, &actual) || actual.EffectPossible || subject.calls != 0 || run.Complete {
		t.Fatal(run, err)
	}
}
func TestObserverRefusesWeakenedOrUnboundedOracle(t *testing.T) {
	p, check, suite := observerFixture()
	suite.Cases = nil
	if suite.Validate(check, p) == nil {
		t.Fatal("zero discovery accepted")
	}
	_, _, suite = observerFixture()
	suite.Repeats = 1
	if suite.Validate(check, p) == nil {
		t.Fatal("single run accepted")
	}
	_, _, suite = observerFixture()
	suite.Cases[0].Stdout = []byte("weakened")
	if suite.Validate(check, p) == nil {
		t.Fatal("unregistered oracle accepted")
	}
}
func TestGoalCoverageRequiresEverySourceAndProtectedDependency(t *testing.T) {
	p, check, _ := observerFixture()
	_ = p
	input := c.HashBytes([]byte("do it"))
	spec := c.TaskSpec{SchemaVersion: 1, TaskID: "t", Version: 1, Goal: "do it", Inputs: []c.InputSource{{ID: "initial", PayloadRef: "blob://t/" + input, Digest: input, ByteLength: 5, Integrity: c.Intact}}, Requirements: []c.Requirement{{ID: "r", Source: c.SourceSpan{InputID: "initial", Start: 0, End: 5}, Required: true, Risk: "CRITICAL", VerificationMethod: "external exact observer"}}, ProtectedOrigin: c.HashBytes([]byte("origin")), DeliveryPolicy: "CANDIDATE_ONLY"}
	plan := CheckPlan{SchemaVersion: 1, Checks: []CheckDefinition{check}}
	digest := c.HashBytes([]byte("checkset"))
	review := GoalReview{SchemaVersion: 1, InputDigests: []string{input}, Coverage: []GoalMapping{{RequirementID: "r", CheckIDs: []string{"exact"}}}, DependencyChecks: []string{"exact"}, Acknowledgement: CoverageAcknowledgement}
	record, err := ReviewCoverage(spec, plan, digest, review)
	if err != nil {
		t.Fatal(err)
	}
	if !CoverageCurrent(&record, spec, digest) {
		t.Fatal("review not current")
	}
	spec.Version++
	if CoverageCurrent(&record, spec, digest) {
		t.Fatal("scope revision reused review")
	}
	review.InputDigests[0] = c.HashBytes([]byte("tampered"))
	if record.Review.InputDigests[0] != input {
		t.Fatal("review alias edited admitted authority")
	}
	if _, err = ReviewCoverage(record.Spec, plan, digest, review); err == nil {
		t.Fatal("wrong input reviewed")
	}
	review = record.Review
	review.DependencyChecks = nil
	if _, err = ReviewCoverage(record.Spec, plan, digest, review); err == nil {
		t.Fatal("incomplete closure reviewed")
	}
}
