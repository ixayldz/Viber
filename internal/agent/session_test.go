package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/kernel"
	"github.com/ixayldz/Viber/internal/model"
)

func startFixture(t *testing.T, turns []Turn, allow bool) (*Session, StartOptions, string) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("before\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	if err != nil {
		t.Fatal(err)
	}
	options := StartOptions{Root: source, Prompt: []byte("Change a.txt without altering the live workspace."), TaskID: "task-one", Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: allow, Fixture: raw}
	if _, err = session.Create(context.Background(), options); err != nil {
		session.Close()
		t.Fatal(err)
	}
	return session, options, source
}
func patchCall() model.Call {
	raw, _ := json.Marshal(struct {
		Read    []c.ReadCondition `json:"read_set"`
		Changes []c.Change        `json:"changes"`
	}{[]c.ReadCondition{{Path: "a.txt", Kind: "FILE", Digest: c.HashBytes([]byte("before\r\n"))}}, []c.Change{{Path: "a.txt", BeforeDigest: c.HashBytes([]byte("before\r\n")), After: []byte("after\r\n")}}})
	return model.Call{ID: "write-one", Name: "candidate_propose", Arguments: raw}
}
func TestOfflineDurableLoopChangesCandidateAndCannotClaimVerified(t *testing.T) {
	turns := []Turn{{Calls: []model.Call{{ID: "read-one", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}, UsageKnown: true, InputTokens: 10, OutputTokens: 2}, {Calls: []model.Call{patchCall()}, UsageKnown: true, InputTokens: 10, OutputTokens: 2}, {Text: "Model says everything is VERIFIED", UsageKnown: true, InputTokens: 10, OutputTokens: 2}}
	session, options, source := startFixture(t, turns, true)
	defer session.Close()
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.Terminated || state.Outcome != c.Finished || state.Quality != c.Unverified || state.Fulfillment != c.Satisfied || kernel.InvocationExit(state, false) != 2 || state.OpenRequiredObligations != 1 {
		t.Fatal("false success or bad final transaction", state)
	}
	_, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := session.Archive.Get(doc.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if string(candidate.Contents["a.txt"]) != "after\r\n" {
		t.Fatal("candidate not changed")
	}
	live, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(live) != "before\r\n" {
		t.Fatal("live source changed")
	}
	if doc.Budget.Steps != 3 || doc.Budget.ToolCalls != 2 || doc.Budget.UsedInput != 30 || doc.Budget.ReservedInput != 0 || doc.Pending != nil {
		t.Fatal("ledger mismatch", doc.Budget)
	}
}
func TestMissingTrustedVerificationWaitsAndResumeDoesNotApproveIt(t *testing.T) {
	session, options, _ := startFixture(t, []Turn{{Text: "finished", UsageKnown: true}}, false)
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.WaitingUser || state.Quality == c.Verified {
		t.Fatal("verification bypass", state)
	}
	directory := session.directory
	session.Close()
	session, err = Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.WaitingUser {
		t.Fatal("resume became approval", state)
	}
}
func TestUnknownUsagePreservesReservationAcrossRestartAndNeverReplaysModel(t *testing.T) {
	session, options, _ := startFixture(t, []Turn{{Text: "lost usage", UsageKnown: false}}, true)
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.Blocked {
		t.Fatal(state)
	}
	_, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Budget.ReservedInput == 0 || doc.Budget.ReservedOutput == 0 || !doc.UnknownEffect || doc.Pending == nil {
		t.Fatal("unknown reservation released")
	}
	directory := session.directory
	session.Close()
	session, err = Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, recovered, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.FixtureCursor != 1 || recovered.Budget.ReservedInput != doc.Budget.ReservedInput || state.Execution != c.Blocked {
		t.Fatal("unknown replayed or reservation lost")
	}
}
func TestResumeRejectsChangedLiveSourceAndPendingIntent(t *testing.T) {
	session, options, source := startFixture(t, []Turn{{Text: "final", UsageKnown: true}}, true)
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.WaitingUser {
		t.Fatal("stale base accepted")
	}
	raw, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(raw) != "user edit" {
		t.Fatal("user data lost")
	}
	session.Close()
	session, options, _ = startFixture(t, []Turn{{Text: "final", UsageKnown: true}}, true)
	defer session.Close()
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = session.transition(context.Background(), state, c.Running, "", "crash admission fixture")
	if err != nil {
		t.Fatal(err)
	}
	request, encoded, manifest, err := compileOfflineRequest(doc, state, session.layers(state, doc))
	if err != nil {
		t.Fatal(err)
	}
	requestDigest, err := session.Archive.PutBytes(doc.TaskID, encoded)
	if err != nil {
		t.Fatal(err)
	}
	doc.Context = &ContextAudit{SchemaVersion: 1, Profile: manifest.Estimator, RequestDigest: requestDigest, Manifest: manifest}
	doc.Pending = &Pending{ID: request.ID, Kind: "MODEL", Candidate: doc.Candidate.SnapshotDigest, Generation: state.KernelGeneration, Epoch: state.PolicyEpoch, ArgumentsDigest: c.HashBytes(encoded), Status: "ADMITTED"}
	doc.Budget.ReservedInput = manifest.InputTokens + 4096
	doc.Budget.ReservedOutput = request.MaxOutputTokens
	if _, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.Blocked {
		t.Fatal("admitted intent replayed")
	}
}
func TestBudgetAndReviewPolicyStopCandidateMutation(t *testing.T) {
	session, options, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "summary", UsageKnown: true}}, true)
	defer session.Close()
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Autonomy = "review"
	doc.Budget.MaxSteps = 1
	if _, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Outcome != c.BudgetExhausted || state.CandidateDigest != state.BaselineDigest {
		t.Fatal("review/budget bypass", state)
	}
}
func TestDurableCompletedToolBoundaryReconcilesWithoutRepeatingWrite(t *testing.T) {
	session, options, _ := startFixture(t, []Turn{{Text: "final", UsageKnown: true}}, true)
	defer session.Close()
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after a tool receipt committed but before its user-facing
	// protocol reply batch was appended. Resume should only close that boundary.
	doc.Messages = append(doc.Messages, model.Message{Role: "assistant", Calls: []model.Call{{ID: "read-one", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}})
	doc.ToolCursor = 1
	doc.PendingReplies = []model.Reply{{CallID: "read-one", Content: "already read"}}
	doc.Budget.ToolCalls = 1
	if _, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err = session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Outcome != c.Finished || doc.Budget.ToolCalls != 1 {
		t.Fatal("completed tool repeated", state, doc.Budget)
	}
}
