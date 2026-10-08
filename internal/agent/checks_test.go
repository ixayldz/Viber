package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/verify"
)

func checkFixture(t *testing.T, image string, turns []Turn) (*Session, StartOptions) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("before\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	profile := runner.DefaultProfile(image)
	digest, _ := c.Digest(profile)
	checkPlan := verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{ID: "registered", Kind: "STATIC", RequirementIDs: []string{"user-goal"}, Argv: []string{"/bin/sh", "-c", "test \"$(cat /workspace/a.txt)\" = before; test \"$(id -u)\" = 65532; test \"$(ls /sys/class/net)\" = lo; if touch /workspace/forbidden 2>/dev/null; then exit 21; fi; printf 'fake PASS\\n' "}, RunnerDigest: digest, Selection: "full", Closure: []verify.CheckScope{{Path: "checks", Recursive: true}}}}}
	planRaw, _ := json.Marshal(checkPlan)
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	options := StartOptions{Root: source, Prompt: []byte("Inspect the captured file."), TaskID: "checks-task", Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture, CheckPlan: planRaw, CheckRuntime: &CheckRuntime{SchemaVersion: 1, Profile: profile}}
	if _, err = s.Create(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	return s, options
}
func syntheticCheck(t *testing.T, s *Session, options StartOptions) (c.TaskState, Document) {
	t.Helper()
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	profileDigest, _ := c.Digest(doc.CheckRuntime.Profile)
	invDigest, _ := c.Digest(runner.Invocation{CandidateDigest: doc.Candidate.SnapshotDigest, Argv: doc.Protection.Plan.Checks[0].Argv})
	// Synthetic broker facts test storage contracts only, never OS conformance.
	result := runner.Result{SchemaVersion: 1, CandidateDigest: doc.Candidate.SnapshotDigest, ProfileDigest: profileDigest, InvocationDigest: invDigest, ContainerID: strings.Repeat("a", 64), ExitCode: 0, SourceReadOnly: true, ProtectedResultChannel: true, ProcessTreeQuiescent: true, Stdout: []byte{0xff, 'P', 'A', 'S', 'S', 0}}
	record := CheckRun{SchemaVersion: 1, TaskID: doc.TaskID, CallID: "unit-run", CheckID: "registered", SpecVersion: 1, PolicyEpoch: state.PolicyEpoch, Generation: state.KernelGeneration, OriginDigest: doc.Spec.ProtectedOrigin, Outcome: "EXIT_ZERO", Discovery: "UNTRUSTED_UNRESOLVED", Result: result}
	raw, _ := c.CanonicalV1(record)
	digest, err := s.Archive.PutBytes(doc.TaskID, raw)
	if err != nil {
		t.Fatal(err)
	}
	doc.CheckRuns = []CheckRunRef{{ID: record.CallID, Digest: digest}}
	state, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{})
	if err != nil {
		t.Fatal(err)
	}
	return state, doc
}
func TestCheckReceiptPagingStalenessAndFreshRestore(t *testing.T) {
	s, options := checkFixture(t, "golang@sha256:"+strings.Repeat("a", 64), []Turn{{Text: "done", UsageKnown: true}})
	state, doc := syntheticCheck(t, s, options)
	summaries, err := s.CheckSummaries(doc, state)
	if err != nil || len(summaries) != 1 || !summaries[0].Current || summaries[0].Verification != "UNKNOWN" || state.Quality != c.Unverified {
		t.Fatal(summaries, err)
	}
	page, err := s.executeCheck(context.Background(), state, &doc, model.Call{ID: "page", Name: "check_output", Arguments: json.RawMessage(`{"run_id":"unit-run","stream":"stdout","offset":0,"limit":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page)
	if !strings.Contains(string(raw), `"complete":false`) || !strings.Contains(string(raw), `"total_bytes":6`) {
		t.Fatal(string(raw))
	}
	stale := state
	stale.PolicyEpoch++
	summaries, err = s.CheckSummaries(doc, stale)
	if err != nil || summaries[0].Current {
		t.Fatal("old epoch reused", err)
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
	if _, _, err = r.Load(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	digest := doc.CheckRuns[0].Digest
	if err = os.Remove(filepath.Join(restored, "artifacts", "tasks", doc.TaskID, "blobs", digest[:2], digest)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Load(context.Background(), options.TaskID); err == nil {
		t.Fatal("missing receipt accepted")
	}
}
func TestCheckCannotSelectCommandOrUnknownCheck(t *testing.T) {
	s, options := checkFixture(t, "golang@sha256:"+strings.Repeat("a", 64), []Turn{{Text: "done", UsageKnown: true}})
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"check_id":"registered","argv":["cmd"]}`, `{"check_id":"attacker"}`} {
		if _, err = s.executeCheck(context.Background(), state, &doc, model.Call{Name: "check_run", Arguments: json.RawMessage(raw)}); err == nil || doc.UnknownEffect {
			t.Fatal("unregistered dispatch", err)
		}
	}
	_, doc = syntheticCheck(t, s, options)
	doc.CheckRuns = append(doc.CheckRuns, doc.CheckRuns[0])
	if err = s.validateChecks(doc); err == nil {
		t.Fatal("duplicate attempt accepted")
	}
	doc.CheckRuns = doc.CheckRuns[:1]
	doc.CheckRuntime.Profile.CPUs++
	if err = s.validateChecks(doc); err == nil {
		t.Fatal("profile drift accepted")
	}
}
func TestCheckLostOperationRemainsBlockedWithoutReplay(t *testing.T) {
	s, options := checkFixture(t, "golang@sha256:"+strings.Repeat("a", 64), []Turn{{Text: "done", UsageKnown: true}})
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Pending = &Pending{ID: "lost-check", Kind: "NATIVE_TOOL", Candidate: doc.Candidate.SnapshotDigest, Generation: state.KernelGeneration, Epoch: state.PolicyEpoch, ArgumentsDigest: c.HashBytes([]byte("{}")), Status: "ADMITTED"}
	if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = s.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.Blocked {
		t.Fatal(state, err)
	}
	_, doc, err = s.Load(context.Background(), options.TaskID)
	if err != nil || !doc.UnknownEffect || doc.FixtureCursor != 0 || doc.Pending.Status != "UNKNOWN" {
		t.Fatal(doc.Pending, err)
	}
}
func TestActualDockerAgentCheckRetainsEvidenceWithoutFalseVerification(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned real Docker image required")
	}
	turns := []Turn{{Calls: []model.Call{{ID: "actual-check", Name: "check_run", Arguments: json.RawMessage(`{"check_id":"registered"}`)}}, UsageKnown: true, InputTokens: 10, OutputTokens: 10}, {Text: "model says VERIFIED", UsageKnown: true, InputTokens: 10, OutputTokens: 10}}
	s, options := checkFixture(t, image, turns)
	state, err := s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.CheckRuns) != 1 || state.Quality != c.Unverified || state.Execution != c.Terminated {
		t.Fatal(state, doc.Blocker, doc.CheckRuns)
	}
	record, err := s.readCheckRun(doc, doc.CheckRuns[0])
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != "EXIT_ZERO" || !record.Result.ProcessTreeQuiescent || !record.Result.SourceReadOnly || !strings.Contains(string(record.Result.Stdout), "fake PASS") {
		t.Fatal(record)
	}
	live, err := os.ReadFile(filepath.Join(options.Root, "a.txt"))
	if err != nil || string(live) != "before\r\n" {
		t.Fatal("source changed", err)
	}
}
