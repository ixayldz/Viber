package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func revisionFor(t *testing.T, s *Session, task, input, id string) ScopeRevision {
	t.Helper()
	state, err := s.State(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{UsageKnown: true, Text: "fresh scope final", InputTokens: 3, OutputTokens: 1}}})
	return ScopeRevision{CommandID: id, TaskID: task, InputID: input, ExpectedSpecVersion: state.SpecVersion, ExpectedPolicyEpoch: state.PolicyEpoch, ExpectedCandidate: state.CandidateDigest, Fixture: fixture}
}
func TestSteeringBarrierRevisionInvalidatesApprovalAndPreservesBudgetOrigin(t *testing.T) {
	s, options, source := startFixture(t, []Turn{{UsageKnown: true, Text: "done", InputTokens: 7, OutputTokens: 2}}, false)
	defer s.Close()
	if _, err := s.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	oldRequest := pending(t, s, options.TaskID, "LIMITED_UNVERIFIED_DELIVERY")
	if _, err := s.Respond(context.Background(), ResponseTemplate(oldRequest, "old-approval", "approve")); err != nil {
		t.Fatal(err)
	}
	_, before, _ := s.Load(context.Background(), options.TaskID)
	input := SteeringInput{CommandID: "steer-one", TaskID: options.TaskID, Text: "Önceki gereksinimleri koru. CRLF ve kullanıcı verisini değiştirme.\r\n"}
	recorded, err := s.RecordSteering(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.RecordSteering(context.Background(), input)
	if err != nil || !reflect.DeepEqual(recorded, duplicate) {
		t.Fatal("raw input not deduplicated", err)
	}
	if !recorded.InputBarrier || len(recorded.PendingInputIDs) != 1 {
		t.Fatal("missing durable barrier", recorded)
	}
	if _, err = s.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	state, blocked, _ := s.Load(context.Background(), options.TaskID)
	if state.Execution != c.WaitingUser || blocked.FixtureCursor != before.FixtureCursor || blocked.Budget != before.Budget {
		t.Fatal("input admitted work before resolution")
	}
	if _, err = s.Controls(context.Background(), options.TaskID, "pause"); err != nil {
		t.Fatal(err)
	}
	revision := revisionFor(t, s, options.TaskID, input.CommandID, "revise-one")
	revised, err := s.Revise(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err = s.Revise(context.Background(), revision)
	if err != nil || !reflect.DeepEqual(revised, duplicate) {
		t.Fatal("revision not deduplicated", err)
	}
	_, after, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if revised.SpecVersion != 2 || revised.PolicyEpoch != 2 || revised.InputBarrier || revised.Execution != c.Paused || revised.Quality != c.Unverified || revised.OpenRequiredObligations != 2 {
		t.Fatal(revised)
	}
	if after.Budget != before.Budget || after.Spec.ProtectedOrigin != before.Spec.ProtectedOrigin || after.Candidate != before.Candidate || after.AllowUnverified || after.Context != nil || len(after.Spec.Requirements) != 2 || after.Requests[0].Status != "STALE" {
		t.Fatal("revision widened authority or discarded original scope")
	}
	raw, err := s.Archive.GetBytes(options.TaskID, after.Spec.Inputs[1].Digest)
	if err != nil || string(raw) != input.Text {
		t.Fatal("raw bytes lost", err)
	}
	if _, err = s.Respond(context.Background(), ResponseTemplate(oldRequest, "stale-approval", "approve")); err == nil {
		t.Fatal("old action approved revised scope")
	}
	next, err := s.Run(context.Background(), options.TaskID)
	if err != nil || next.Execution != c.WaitingUser {
		t.Fatal("revision inherited delivery approval", err, next)
	}
	newRequest := pending(t, s, options.TaskID, "LIMITED_UNVERIFIED_DELIVERY")
	if newRequest.SpecVersion != 2 || newRequest.ID == oldRequest.ID {
		t.Fatal("old request reused", newRequest)
	}
	live, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(live) != "before\r\n" {
		t.Fatal("source changed")
	}
}
func TestPendingInputsSurviveBackupRestoreAndResolveInOrder(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{UsageKnown: true, Text: "done"}}, true)
	defer s.Close()
	for _, input := range []SteeringInput{{"first", options.TaskID, "first raw instruction"}, {"second", options.TaskID, "second raw instruction"}} {
		if _, err := s.RecordSteering(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Controls(context.Background(), options.TaskID, "pause"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackup(context.Background(), backup, directory); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.Revise(context.Background(), revisionFor(t, restored, options.TaskID, "second", "out-of-order")); err == nil {
		t.Fatal("input ordering bypassed")
	}
	first, err := restored.Revise(context.Background(), revisionFor(t, restored, options.TaskID, "first", "rev-first"))
	if err != nil || !first.InputBarrier {
		t.Fatal("first input cleared whole queue", err, first)
	}
	second, err := restored.Revise(context.Background(), revisionFor(t, restored, options.TaskID, "second", "rev-second"))
	if err != nil || second.InputBarrier || second.SpecVersion != 3 || second.PolicyEpoch != 3 || second.OpenRequiredObligations != 3 {
		t.Fatal(err, second)
	}
	_, doc, err := restored.Load(context.Background(), options.TaskID)
	if err != nil || len(doc.Spec.Inputs) != 3 || len(doc.Messages) != 3 {
		t.Fatal("source provenance missing", err)
	}
}
func TestMissingRawInputPreventsLoadBackupAndRevision(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{UsageKnown: true, Text: "done"}}, true)
	defer s.Close()
	input := SteeringInput{"missing-raw", options.TaskID, "retain these exact bytes"}
	if _, err := s.RecordSteering(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	digest := c.HashBytes([]byte(input.Text))
	file := filepath.Join(s.directory, "artifacts", "tasks", options.TaskID, "blobs", digest[:2], digest)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load(context.Background(), options.TaskID); err == nil {
		t.Fatal("missing raw input loaded")
	}
	if _, err := s.Backup(context.Background(), filepath.Join(t.TempDir(), "backup")); err == nil {
		t.Fatal("missing raw input backed up")
	}
	if _, err := s.Revise(context.Background(), revisionFor(t, s, options.TaskID, input.CommandID, "revision")); err == nil {
		t.Fatal("missing raw input revised")
	}
}
func TestRevisionCannotReconcileUnknownUsageOrResetIt(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{UsageKnown: false, Text: "unknown usage"}}, true)
	defer s.Close()
	if _, err := s.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	_, before, _ := s.Load(context.Background(), options.TaskID)
	if _, err := s.RecordSteering(context.Background(), SteeringInput{"steer", options.TaskID, "keep reserved costs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revise(context.Background(), revisionFor(t, s, options.TaskID, "steer", "revision")); err == nil {
		t.Fatal("scope reset unknown effects")
	}
	_, after, _ := s.Load(context.Background(), options.TaskID)
	if after.Budget != before.Budget || !after.UnknownEffect || after.Pending == nil {
		t.Fatal("reservation disappeared")
	}
}
