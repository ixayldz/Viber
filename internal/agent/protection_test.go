package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/verify"
)

func operatorPlan(t *testing.T, scopes ...verify.CheckScope) []byte {
	t.Helper()
	raw, err := json.Marshal(verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{ID: "protected-test", Kind: "TEST", RequirementIDs: []string{"user-goal"}, Argv: []string{"trusted-runner"}, RunnerDigest: c.HashBytes([]byte("runner-v1")), Selection: "all", ExpectedTests: []string{"required-behavior"}, Closure: scopes}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func protectionSession(t *testing.T, autonomy string, plan []byte) (*Session, string, StartOptions) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("before\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "check.txt"), []byte("must deny reuse"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "VERIFIED", UsageKnown: true}}})
	session, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := StartOptions{Root: root, Prompt: []byte("change application"), TaskID: "protected", Budget: DefaultBudget(), Autonomy: autonomy, AllowUnverified: true, Fixture: raw, CheckPlan: plan}
	if _, err = session.Create(context.Background(), opts); err != nil {
		session.Close()
		t.Fatal(err)
	}
	return session, root, opts
}
func TestF26ProtectedProposalDeniedInGuidedAndReviewWithoutApprovalBypass(t *testing.T) {
	for _, autonomy := range []string{"guided", "review", "auto"} {
		t.Run(autonomy, func(t *testing.T) {
			session, root, opts := protectionSession(t, autonomy, operatorPlan(t, verify.CheckScope{Path: "a.txt"}))
			defer session.Close()
			initial, doc, err := session.Load(context.Background(), opts.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Protection == nil || doc.Protection.ClosureCoverage != "EXPLICIT_UNREVIEWED" {
				t.Fatal("origin not recorded before mutation")
			}
			state, err := session.Run(context.Background(), opts.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			_, doc, err = session.Load(context.Background(), opts.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if state.CandidateDigest != initial.CandidateDigest || state.Quality == c.Verified || len(doc.Requests) != 0 {
				t.Fatal("protected edit or review bypass", state, doc.Requests)
			}
			found := false
			for _, msg := range doc.Messages {
				for _, reply := range msg.Replies {
					if reply.CallID == "write-one" && reply.IsError {
						found = true
					}
				}
			}
			if !found {
				t.Fatal("denial was not durably returned to worker")
			}
			raw, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			if string(raw) != "before\r\n" {
				t.Fatal("live source changed")
			}
		})
	}
}
func TestProtectedProductionChangeSurvivesRestartBackupAndFreshRestore(t *testing.T) {
	session, _, opts := protectionSession(t, "guided", operatorPlan(t, verify.CheckScope{Path: "check.txt"}, verify.CheckScope{Path: "new-check.txt"}))
	state, err := session.Run(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := session.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CandidateDigest == doc.Baseline.SnapshotDigest || state.Quality != c.Unverified {
		t.Fatal("production change rejected or fake verification")
	}
	digest := doc.Spec.ProtectedOrigin
	snapshot := doc.Candidate.SnapshotDigest
	directory := session.directory
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = session.Backup(context.Background(), backup); err != nil {
		session.Close()
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(context.Background(), backup, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	recovered, doc, err := restored.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Spec.ProtectedOrigin != digest || doc.Candidate.SnapshotDigest != snapshot || recovered.Quality != c.Unverified || doc.Protection == nil {
		t.Fatal("protected origin or quality lost in restore")
	}
	info := CheckProtection(doc)
	if info.CheckCount != 1 || info.ProtectedScopes != 2 || info.StrongVerificationAvailable {
		t.Fatal("misleading capability", info)
	}
}
func TestOriginCannotBeRemovedAndLegacySessionsAreNotUpgraded(t *testing.T) {
	session, _, opts := protectionSession(t, "guided", nil)
	defer session.Close()
	_, doc, err := session.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Protection.ClosureCoverage != "UNRESOLVED" {
		t.Fatal("empty closure pretended reviewed")
	}
	doc.Protection = nil
	if err := session.validateProtection(doc); err == nil {
		t.Fatal("new origin removed into legacy path")
	}
	legacy, _ := c.Digest(struct {
		Baseline string
		Profile  string
	}{doc.Baseline.SnapshotDigest, "offline-fixture-no-protected-checks-v1"})
	doc.Spec.ProtectedOrigin = legacy
	if err := session.validateProtection(doc); err != nil {
		t.Fatal("legacy offline session refused", err)
	}
	if CheckProtection(doc) != nil {
		t.Fatal("legacy upgraded to protected assurance")
	}
}
func TestScopeRevisionPreservesProtectedOriginAndRejectsOldCoverage(t *testing.T) {
	session, _, opts := protectionSession(t, "guided", operatorPlan(t, verify.CheckScope{Path: "check.txt"}))
	defer session.Close()
	state, doc, err := session.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	digest := doc.Spec.ProtectedOrigin
	input := SteeringInput{CommandID: "steer-protection", TaskID: opts.TaskID, Text: "Keep original protected checks; add a second criterion."}
	if _, err = session.RecordSteering(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	state, doc, err = session.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Controls(context.Background(), opts.TaskID, "pause"); err != nil {
		t.Fatal(err)
	}
	state, doc, err = session.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	revision := ScopeRevision{CommandID: "revise-protection", TaskID: opts.TaskID, InputID: input.CommandID, ExpectedSpecVersion: state.SpecVersion, ExpectedPolicyEpoch: state.PolicyEpoch, ExpectedCandidate: state.CandidateDigest, Fixture: opts.Fixture}
	if _, err = session.Revise(context.Background(), revision); err != nil {
		t.Fatal(err)
	}
	_, doc, err = session.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Spec.ProtectedOrigin != digest || doc.Protection == nil {
		t.Fatal("revision replaced origin")
	}
	base, err := session.Archive.Get(doc.Baseline)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := session.Archive.Get(doc.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verify.FreezeChecks(doc.Spec, *doc.Protection, base, candidate, c.HashBytes([]byte("env")), c.HashBytes([]byte("policy"))); err == nil {
		t.Fatal("new criterion inherited old coverage")
	}
}
func TestCheckPlanStrictBoundaryAndUnknownRequirementCannotCreateTask(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"schema_version":1,"checks":[],"grant_verified":true}`), []byte(`{"schema_version":1,"schema_version":1,"checks":[]}`), []byte(`{"schema_version":2,"checks":[]}`)} {
		if _, err := ParseCheckPlan(raw); err == nil {
			t.Fatal("invalid check plan admitted")
		}
	}
	session, _, opts := protectionSession(t, "guided", nil)
	defer session.Close()
	plan, err := ParseCheckPlan(operatorPlan(t, verify.CheckScope{Path: "check.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	plan.Checks[0].RequirementIDs = []string{"unknown-goal"}
	opts.CheckPlan, _ = json.Marshal(plan)
	opts.TaskID = "rejected"
	if _, err = session.Create(context.Background(), opts); err == nil {
		t.Fatal("foreign criterion admitted")
	}
	states, err := session.Journal.Replay(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := states[opts.TaskID]; ok {
		t.Fatal("failed preflight published task")
	}
}
