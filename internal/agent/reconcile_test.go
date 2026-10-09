package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"

	"path/filepath"
	"reflect"
	"testing"
)

func unknownModelBoundary(t *testing.T) (*Session, StartOptions, c.TaskState, Document) {
	t.Helper()
	s, opts, _ := startFixture(t, []Turn{{Text: "must never be accepted", UsageKnown: false}, {Text: "fresh observed response", UsageKnown: true, InputTokens: 2, OutputTokens: 1}}, false)
	t.Cleanup(func() { s.Close() })
	st, err := s.Run(context.Background(), opts.TaskID)
	if err != nil || st.Execution != c.Blocked {
		t.Fatal(st, err)
	}
	st, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return s, opts, st, doc
}
func riskCommand(st c.TaskState, doc Document) ModelRiskCommand {
	return ModelRiskCommand{CommandID: "risk-accounting", TaskID: st.TaskID, ExpectedTaskSeq: st.TaskSeq, ReservationID: doc.Pending.ID, RequestDigest: doc.Pending.ArgumentsDigest, ProfileDigest: tokenProfile(doc), Decision: "ACCOUNT_FULL_UPPER_BOUND_WITHOUT_OUTPUT"}
}
func TestModelRiskReconciliationChargesAllExposureWithoutAcceptingLostOutput(t *testing.T) {
	s, opts, before, doc := unknownModelBoundary(t)
	command := riskCommand(before, doc)
	state, err := s.ReconcileModelRisk(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	state, after, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	tokens := state.Tokens.Reservations[0]
	resources := state.Resources.Reservations[0]
	expected := resources.Upper
	expected.Children = 0
	if tokens.Status != "SETTLED" || tokens.Used != tokens.Upper || tokens.UsageSource != "OPERATOR_ASSUMED_UPPER_BOUND" || resources.Used != expected || resources.Meter != "OPERATOR_ASSUMED_UPPER_BOUND" || after.Pending != nil || after.UnknownEffect || !reflect.DeepEqual(after.Messages, doc.Messages) || after.Candidate != doc.Candidate || !reflect.DeepEqual(after.Spec, doc.Spec) || state.Quality != c.Unverified || state.Execution != c.Blocked {
		t.Fatal("accounting changed output/authority")
	}
	retry, err := s.ReconcileModelRisk(context.Background(), command)
	if err != nil || retry.TaskSeq != state.TaskSeq {
		t.Fatal("dedup", err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(context.Background(), backup); err != nil {
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
	state, err = restored.Run(context.Background(), opts.TaskID)
	if err != nil || state.Execution != c.WaitingUser {
		t.Fatal("new inference did not resume", state, err)
	}
	_, loaded, err := restored.Load(context.Background(), opts.TaskID)
	if err != nil || len(state.Tokens.Reservations) != 2 || loaded.FinalSummary != "fresh observed response" {
		t.Fatal("lost response was accepted", err)
	}
	command.Decision = "RELEASE_ZERO"
	if _, err = s.ReconcileModelRisk(context.Background(), command); err == nil {
		t.Fatal("risk decision changed")
	}
}
func TestModelRiskRejectsStaleReducedOrNativeExposure(t *testing.T) {
	for _, kind := range []string{"stale", "wrong-request", "zero", "pending-input"} {
		t.Run(kind, func(t *testing.T) {
			s, opts, state, doc := unknownModelBoundary(t)
			command := riskCommand(state, doc)
			switch kind {
			case "stale":
				command.ExpectedTaskSeq--
			case "wrong-request":
				command.RequestDigest = c.HashBytes([]byte("other"))
			case "zero":
				command.Decision = "RELEASE_ZERO"
			case "pending-input":
				_, err := s.RecordSteering(context.Background(), SteeringInput{CommandID: "input", TaskID: opts.TaskID, Text: "change intent"})
				if err != nil {
					t.Fatal(err)
				}
				state, _, err = s.Load(context.Background(), opts.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				command.ExpectedTaskSeq = state.TaskSeq

			}
			if _, err := s.ReconcileModelRisk(context.Background(), command); err == nil {
				t.Fatal("unsafe reconciliation", kind)
			}
			actual, after, err := s.Load(context.Background(), opts.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Pending == nil || !after.UnknownEffect || actual.Tokens.Reservations[0].Status != "UNKNOWN" {
				t.Fatal("failed admission erased unknown risk")
			}
		})
	}
}
func TestCancelledUnknownModelCanBeAccountedAfterOwnerRestart(t *testing.T) {
	s, opts, state, doc := unknownModelBoundary(t)
	state, err := s.transition(context.Background(), state, c.Terminated, c.Cancelled, "test cancellation")
	if err != nil {
		t.Fatal(err)
	}
	path := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err = reopened.ReconcileModelRisk(context.Background(), riskCommand(state, doc))
	if err != nil || state.Outcome != c.Cancelled || state.Execution != c.Terminated || state.KernelGeneration != reopened.Journal.Generation() {
		t.Fatal(state, err)
	}
	if _, err = reopened.Run(context.Background(), opts.TaskID); err == nil {
		t.Fatal("cancelled task resumed")
	}
}
func TestOperatorRiskProofCannotTurnAnOutputIntoVerifiedEvidence(t *testing.T) {
	s, opts, state, doc := unknownModelBoundary(t)
	state, err := s.ReconcileModelRisk(context.Background(), riskCommand(state, doc))
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err = s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	r := state.Tokens.Reservations[0]
	raw, err := s.Archive.GetBytes(opts.TaskID, r.ResponseDigest)
	if err != nil {
		t.Fatal(err)
	}
	var proof ModelRiskReceipt
	if c.DecodeStrict(raw, &proof) != nil {
		t.Fatal("proof")
	}
	proof.AcceptedModelOutput = true
	raw, _ = c.CanonicalV1(proof)
	if err = s.validateRiskReceipt(doc, r, raw); err == nil {
		t.Fatal("operator estimate became model output")
	}

}

func TestNativeUnknownRiskCannotBeReleasedByModelAccounting(t *testing.T) {
	s, opts := resourceFixture(t, c.DefaultResourcePolicy(), []Turn{{Text: "done", UsageKnown: true}})
	state, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.transition(context.Background(), state, c.Running, "", "test native intent")
	if err != nil {
		t.Fatal(err)
	}
	doc.Pending = &Pending{ID: "native", Kind: "NATIVE_TOOL", Candidate: doc.Candidate.SnapshotDigest, ArgumentsDigest: c.HashBytes([]byte("{}")), Generation: state.KernelGeneration, Epoch: state.PolicyEpoch, Status: "ADMITTED"}
	state, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{})
	if err != nil {
		t.Fatal(err)
	}
	doc.UnknownEffect = true
	doc.Pending.Status = "UNKNOWN"
	state, err = s.stop(context.Background(), state, doc, "native unknown", c.Blocked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReconcileModelRisk(context.Background(), riskCommand(state, doc)); err == nil {
		t.Fatal("native process risk released by model accounting")
	}
	state, after, err := s.Load(context.Background(), opts.TaskID)
	if err != nil || !after.UnknownEffect || after.Pending == nil || state.Resources.Reservations[0].Status != "UNKNOWN" {
		t.Fatal("native risk erased", err)
	}
}
