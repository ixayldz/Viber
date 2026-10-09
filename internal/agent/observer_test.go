package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/kernel"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/verify"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func observedFixture(t *testing.T, image string, reviewed bool, turns []Turn, configure ...func(*StartOptions)) (*Session, StartOptions) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "app.sh"), []byte("cat\n"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p := runner.DefaultProfile(image)
	p.TimeoutSeconds = 5
	p.MaxOutputBytes = 16 << 10
	suite := verify.StdioSuite{SchemaVersion: 1, CheckID: "echo", Level: "V4", Protocol: verify.ObserverProtocol, Repeats: 2, Cases: []verify.StdioCase{{ID: "bytes", Input: []byte("ö\r\n\x00"), Stdout: []byte("ö\r\n\x00")}, {ID: "empty", Input: []byte{}, Stdout: []byte{}}}}
	oracle, _ := c.Digest(suite)
	profile, _ := c.Digest(p)
	plan := verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{ID: "echo", Kind: "TEST", RequirementIDs: []string{"user-goal"}, ExpectedTests: verify.SuiteCaseIDs(suite), ObserverDigest: oracle, Argv: []string{"/bin/sh", "/workspace/app.sh"}, RunnerDigest: profile, Selection: "all", Closure: []verify.CheckScope{{Path: "checks", Recursive: true}}}}}
	planRaw, _ := c.CanonicalV1(plan)
	fixtureRaw, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	prompt := []byte("Echo each supplied byte exactly, with no stderr.")
	options := StartOptions{Root: source, TaskID: "observed", Prompt: prompt, Budget: DefaultBudget(), Autonomy: "guided", Fixture: fixtureRaw, AllowUnverified: !reviewed, CheckPlan: planRaw, CheckRuntime: &CheckRuntime{SchemaVersion: 1, Profile: p, ObserverSuites: []verify.StdioSuite{suite}}}
	if reviewed {
		options.GoalReview = &verify.GoalReview{SchemaVersion: 1, InputDigests: []string{c.HashBytes(prompt)}, Coverage: []verify.GoalMapping{{RequirementID: "user-goal", CheckIDs: []string{"echo"}}}, DependencyChecks: []string{"echo"}, Acknowledgement: verify.CoverageAcknowledgement}
	}
	for _, change := range configure {
		change(&options)
	}
	if _, err = s.Create(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	return s, options
}

type syntheticObserverRunner struct {
	calls     int
	failFirst bool
}

func (r *syntheticObserverRunner) Run(ctx context.Context, source string, p runner.Profile, inv runner.Invocation) (runner.Result, error) {
	r.calls++
	profile, _ := c.Digest(p)
	invocation, _ := c.Digest(inv)
	result := runner.Result{SchemaVersion: 1, CandidateDigest: inv.CandidateDigest, ProfileDigest: profile, InvocationDigest: invocation, ContainerID: c.HashBytes([]byte(fmt.Sprint(r.calls))), ExitCode: 0, SourceReadOnly: true, ProtectedResultChannel: true, ProcessTreeQuiescent: true, Stdout: bytes.Clone(*inv.Stdin)}
	if r.failFirst && r.calls == 5 {
		result.Stdout = []byte("PASS")
	}
	return result, nil
}
func retainSyntheticObserver(t *testing.T, s *Session, task string, subject *syntheticObserverRunner, id string) (c.TaskState, Document) {
	t.Helper()
	state, doc, err := s.Load(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	suite, check, _ := observerSuite(doc, "echo")
	base, err := verify.Observe(context.Background(), subject, "synthetic-baseline", doc.CheckRuntime.Profile, doc.Baseline.SnapshotDigest, check, suite)
	if err != nil {
		t.Fatal(err)
	}
	current, err := verify.Observe(context.Background(), subject, "synthetic-candidate", doc.CheckRuntime.Profile, doc.Candidate.SnapshotDigest, check, suite)
	if err != nil {
		t.Fatal(err)
	}
	e := &ObservedEvidence{SchemaVersion: 1, Binding: verificationBinding(doc, state), Baseline: &base, Current: &current}
	if doc.GoalCoverage != nil {
		e.GoalReviewDigest, _ = c.Digest(doc.GoalCoverage)
	}
	record := CheckRun{SchemaVersion: 1, TaskID: task, CallID: id, CheckID: "echo", SpecVersion: doc.Spec.Version, PolicyEpoch: state.PolicyEpoch, Generation: state.KernelGeneration, OriginDigest: doc.Spec.ProtectedOrigin, Discovery: verify.ObserverProtocol, Observer: e, Result: current.Attempts[0].Result}
	record.Outcome = observedOutcome(e)
	raw, _ := c.CanonicalV1(record)
	digest, err := s.Archive.PutBytes(task, raw)
	if err != nil {
		t.Fatal(err)
	}
	doc.CheckRuns = append(doc.CheckRuns, CheckRunRef{ID: id, Digest: digest})
	state, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{})
	if err != nil {
		t.Fatal(err)
	}
	return state, doc
}
func TestReviewedObserverGuardedFinalAndFreshRestore(t *testing.T) {
	// Synthetic runner facts validate wiring, not OS enforcement.
	s, options := observedFixture(t, "golang@sha256:"+strings.Repeat("a", 64), true, []Turn{{Text: "No changes required: exact echo already implemented.", UsageKnown: true}})
	retainSyntheticObserver(t, s, options.TaskID, &syntheticObserverRunner{}, "observed-run")
	state, err := s.Run(context.Background(), options.TaskID)
	if err != nil || !kernel.StrictSuccess(state) || kernel.InvocationExit(state, false) != 0 {
		t.Fatal(state, err)
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil || doc.VerificationReport == "" || doc.FinalArtifactDigest == "" {
		t.Fatal(doc, err)
	}
	report, err := s.deriveVerification(doc, state)
	if err != nil || report.Assessment.Quality != c.Verified || len(report.Receipts) != 1 {
		t.Fatal(report, err)
	}
	request, _, _, err := compileOfflineRequest(doc, state, s.layers(state, doc))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(request.Instructions, "expected_stdout") || strings.Contains(request.Instructions, "observer_suites") {
		t.Fatal("oracle entered model request")
	}
	if _, err = s.readCheckOutput(context.Background(), doc, checkOutputQuery{RunID: "observed-run", Stream: "stdout", Offset: 0, Limit: 16}, false); err == nil {
		t.Fatal("subject echoed oracle bytes into model tool")
	}
	if _, err = s.readCheckOutput(context.Background(), doc, checkOutputQuery{RunID: "observed-run", Stream: "stdout", Offset: 0, Limit: 16, CaseID: "bytes", Repeat: 2, Scope: "baseline"}, true); err != nil {
		t.Fatal("local operator cannot inspect exact retained repeat", err)
	}
	events, err := s.Journal.History(context.Background(), options.TaskID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events.Records {
		if event.Event.Type == "VerifiedResultFinalized" {
			raw, _ := json.Marshal(event.Payload)
			if bytes.Contains(raw, options.Prompt) {
				t.Fatal("raw goal duplicated into permanent finalization payload")
			}
		}
	}
	parent := t.TempDir()
	backup := filepath.Join(parent, "backup")
	restored := filepath.Join(parent, "restored")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreBackup(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	r, err := OpenExisting(context.Background(), restored)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _, err := r.Load(context.Background(), options.TaskID)
	if err != nil || !kernel.StrictSuccess(got) {
		t.Fatal(got, err)
	}
	refPath, _ := artifact.ScopedBlobPath(options.TaskID, doc.VerificationReport)
	if err = os.Remove(filepath.Join(restored, "artifacts", filepath.FromSlash(refPath))); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Load(context.Background(), options.TaskID); err == nil {
		t.Fatal("missing frozen report accepted")
	}
}
func TestObserverNeedsReviewAndCannotWashOutFailedAttempt(t *testing.T) {
	image := "golang@sha256:" + strings.Repeat("a", 64)
	s, options := observedFixture(t, image, false, []Turn{{Text: "Model claims VERIFIED.", UsageKnown: true}})
	state, doc := retainSyntheticObserver(t, s, options.TaskID, &syntheticObserverRunner{}, "unreviewed")
	report, err := s.deriveVerification(doc, state)
	if err != nil || report.Assessment.Quality == c.Verified {
		t.Fatal(report, err)
	}
	state, err = s.Run(context.Background(), options.TaskID)
	if err != nil || state.Quality != c.Unverified || kernel.StrictSuccess(state) {
		t.Fatal(state, err)
	}
	s, options = observedFixture(t, image, true, []Turn{{Text: "Model claims VERIFIED.", UsageKnown: true}})
	subject := &syntheticObserverRunner{failFirst: true}
	retainSyntheticObserver(t, s, options.TaskID, subject, "fail-then-pass")
	state, doc = retainSyntheticObserver(t, s, options.TaskID, subject, "passing-rerun")
	report, err = s.deriveVerification(doc, state)
	if err != nil || report.Assessment.Quality != c.QualityFailed {
		t.Fatal("failed attempt washed out", report, err)
	}
}
func TestObserverConfigCannotSwapOracleOrReviewSource(t *testing.T) {
	s, options := observedFixture(t, "golang@sha256:"+strings.Repeat("a", 64), true, []Turn{{Text: "done", UsageKnown: true}})
	state, doc := retainSyntheticObserver(t, s, options.TaskID, &syntheticObserverRunner{}, "bound")
	doc.CheckRuntime.ObserverSuites[0].Cases[0].Stdout = []byte("weaker")
	if err := s.validateChecks(doc); err == nil {
		t.Fatal("oracle weakening accepted")
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Spec.Version++
	state.SpecVersion++
	report, err := s.deriveVerification(doc, state)
	if err != nil || report.Guards.GoalCoverageReviewed || report.Assessment.Quality == c.Verified {
		t.Fatal("review survived scope revision", report, err)
	}
}
func TestActualDockerIndependentObserverCandidateFinal(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned real Docker image required")
	}
	turns := []Turn{{Calls: []model.Call{{ID: "actual-observer", Name: "check_run", Arguments: json.RawMessage(`{"check_id":"echo"}`)}}, UsageKnown: true, InputTokens: 10, OutputTokens: 10}, {Text: "Exact echo candidate.", UsageKnown: true, InputTokens: 10, OutputTokens: 10}}
	s, options := observedFixture(t, image, true, turns)
	state, err := s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(state, err)
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil || !kernel.StrictSuccess(state) {
		t.Fatal(state, doc.Blocker, err)
	}
	record, err := s.readCheckRun(doc, doc.CheckRuns[0])
	if err != nil {
		t.Fatal(err)
	}
	if record.Observer == nil || len(record.Observer.Current.Attempts) != 4 || record.Observer.Current.Verdict != c.Pass || record.Observer.Baseline.Verdict != c.Pass {
		t.Fatal(record)
	}
	live, err := os.ReadFile(filepath.Join(options.Root, "app.sh"))
	if err != nil || string(live) != "cat\n" {
		t.Fatal("source changed", err)
	}
}
