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
	"github.com/ixayldz/Viber/internal/plan"
	"github.com/ixayldz/Viber/internal/workspace"
)

func planFixture(t *testing.T, complete bool) (*Session, StartOptions, string) {
	t.Helper()
	session, options, source := startFixture(t, []Turn{{Text: "placeholder", UsageKnown: true}}, true)
	t.Cleanup(func() { session.Close() })
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	d := plan.Definition{SchemaVersion: 1, ID: "single-writer-plan", SpecVersion: 1, PolicyEpoch: state.PolicyEpoch, BaseCandidate: doc.Candidate.SnapshotDigest, Nodes: []plan.Node{{ID: "edit", Goal: "change greeting", InputContract: "exact before bytes", OutputContract: "after bytes and explanation", ReadScope: []string{"a.txt"}, WriteScope: []string{"a.txt"}, RequirementIDs: []string{"user-goal"}, RequiredChecks: []string{}, Mutex: []string{"greeting"}}}, Relations: []plan.Relation{}}
	raw, _ := json.Marshal(d)
	turns := []Turn{{Calls: []model.Call{{ID: "plan-1", Name: "plan_propose", Arguments: raw}}, UsageKnown: true, InputTokens: 10, OutputTokens: 10}, {Calls: []model.Call{{ID: "next-1", Name: "plan_next", Arguments: json.RawMessage(`{}`)}}, UsageKnown: true, InputTokens: 10, OutputTokens: 10}, {Calls: []model.Call{patchCall()}, UsageKnown: true, InputTokens: 10, OutputTokens: 10}}
	if complete {
		base, err := session.Archive.Get(doc.Candidate)
		if err != nil {
			t.Fatal(err)
		}
		var args struct {
			ReadSet []c.ReadCondition `json:"read_set"`
			Changes []c.Change        `json:"changes"`
		}
		if err = c.DecodeStrict(patchCall().Arguments, &args); err != nil {
			t.Fatal(err)
		}
		previewState := state
		previewState.Execution = c.Running
		changed, err := workspace.Preview(base, base, c.Proposal{SchemaVersion: 1, ID: "calculate", TaskID: options.TaskID, SpecVersion: 1, BaseSnapshot: base.Snapshot.Digest, PolicyEpoch: state.PolicyEpoch, KernelGeneration: state.KernelGeneration, ReadSet: args.ReadSet, Changes: args.Changes}, session.layers(previewState, doc), previewState)
		if err != nil {
			t.Fatal(err)
		}
		finish, _ := json.Marshal(map[string]any{"node_id": "edit", "candidate": changed.Snapshot.Digest, "output": "Changed exact greeting bytes; checks remain UNKNOWN."})
		turns = append(turns, Turn{Calls: []model.Call{{ID: "finish-1", Name: "plan_finish", Arguments: finish}}, UsageKnown: true, InputTokens: 10, OutputTokens: 10})
	}
	turns = append(turns, Turn{Text: "Plan implemented; model claims VERIFIED", UsageKnown: true, InputTokens: 10, OutputTokens: 10})
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	doc.FixtureDigest, err = session.Archive.PutBytes(options.TaskID, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	return session, options, source
}
func TestNativePlanLoopEnforcesContractAndRetainsUnknownQualityAcrossRestore(t *testing.T) {
	s, options, source := planFixture(t, true)
	state, err := s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Plan.Complete() || state.Quality != c.Unverified || state.OpenRequiredObligations != 1 || state.Execution != c.Terminated {
		t.Fatal("plan became quality authority", state, doc.Plan)
	}
	if raw, _ := os.ReadFile(filepath.Join(source, "a.txt")); string(raw) != "before\r\n" {
		t.Fatal("live source changed")
	}
	parent := t.TempDir()
	backup := filepath.Join(parent, "backup")
	restored := filepath.Join(parent, "restored")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreBackup(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(context.Background(), restored)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, restoredDoc, err := reopened.Load(context.Background(), options.TaskID)
	if err != nil || actual.Quality != c.Unverified || restoredDoc.Plan.DefinitionDigest != doc.Plan.DefinitionDigest || !restoredDoc.Plan.Complete() {
		t.Fatal("restored plan differs", err)
	}
	output := restoredDoc.Plan.Progress[0].OutputDigest
	if err = os.Remove(filepath.Join(restored, "artifacts", "tasks", options.TaskID, "blobs", output[:2], output)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = reopened.Load(context.Background(), options.TaskID); err == nil {
		t.Fatal("missing output accepted")
	}
	if _, err = reopened.Backup(context.Background(), filepath.Join(parent, "broken-backup")); err == nil {
		t.Fatal("missing output backed up")
	}
}
func TestUnfinishedNativePlanCannotFinalizeLimitedResult(t *testing.T) {
	s, options, _ := planFixture(t, false)
	defer s.Close()
	state, err := s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.WaitingUser || doc.Blocker != "PLAN_NODES_PENDING" || state.Quality != c.Unverified {
		t.Fatal(state, doc.Blocker, err)
	}
}
func TestActivePlanScopesCannotBeBypassedByReviewApproval(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	d := plan.Definition{SchemaVersion: 1, ID: "restricted", SpecVersion: 1, PolicyEpoch: state.PolicyEpoch, BaseCandidate: doc.Candidate.SnapshotDigest, Nodes: []plan.Node{{ID: "read", Goal: "inspect", InputContract: "source", OutputContract: "report", ReadScope: []string{"other.txt"}, WriteScope: []string{"other.txt"}, RequirementIDs: []string{"user-goal"}}}}
	doc.Plan, err = plan.New(d, doc.Spec, nil, doc.Candidate.SnapshotDigest, state.PolicyEpoch)
	if err != nil {
		t.Fatal(err)
	}
	doc.Plan.Next()
	doc.Autonomy = "review"
	if err = s.validateReviewCall(state, doc, patchCall()); err == nil {
		t.Fatal("out-of-contract approval request")
	}
	_, _, err = s.executeTool(context.Background(), state, &doc, model.Call{ID: "read", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)})
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatal("read scope bypass", err)
	}
}
