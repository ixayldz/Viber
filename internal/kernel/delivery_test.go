package kernel

import (
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func deliveryDigest(label string) string { return c.HashBytes([]byte(label)) }

func deliveryEvent(state c.TaskState, effect c.DeliveryEffect, document string, seq int64) (c.Event, c.EventPayload) {
	event := c.Event{
		SchemaVersion: c.SchemaVersion, ID: "delivery-event", StoreSeq: state.StoreSeq + 1,
		TaskID: state.TaskID, TaskSeq: state.TaskSeq + 1, KernelGeneration: state.KernelGeneration,
		Actor: "kernel", Type: "DeliveryEffectRecorded",
	}
	effect.SourceTaskSeq = seq
	encodedDigest, _ := c.Digest(effect)
	return event, c.EventPayload{DeliveryEffect: &effect, DocumentDigest: document, SnapshotDigest: effect.CandidateDigest, Reason: encodedDigest}
}

func TestDeliveryEffectUnknownThenOwnedApplyAndRestore(t *testing.T) {
	state := c.TaskState{
		SchemaVersion: c.SchemaVersion, TaskID: "task", TaskSeq: 7, StoreSeq: 7,
		KernelGeneration: 3, SpecVersion: 2, PolicyEpoch: 4,
		DocumentDigest: deliveryDigest("document-before-apply"), CandidateDigest: deliveryDigest("candidate"),
		Execution: c.Terminated, Outcome: c.Finished, Quality: c.Verified, Fulfillment: c.Satisfied,
	}
	apply := c.DeliveryEffect{
		OperationID: "0123456789abcdef0123456789abcdef", Phase: c.DeliveryApplyPhase, Status: c.DeliveryUnknown,
		PlanDigest: deliveryDigest("apply-plan"), SourceDocument: state.DocumentDigest,
		SourceTaskSeq: state.TaskSeq, SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch,
		Generation: state.KernelGeneration, CandidateDigest: state.CandidateDigest,
		TargetDigest: deliveryDigest("target"), ResultDigest: deliveryDigest("result"),
	}
	firstEvent, firstPayload := deliveryEvent(state, apply, deliveryDigest("apply-intent-document"), state.TaskSeq)
	intentState, err := Reduce(&state, firstEvent, firstPayload)
	if err != nil {
		t.Fatalf("record apply intent: %v", err)
	}
	if StrictSuccess(intentState) {
		t.Fatal("strict success survived an unresolved live effect")
	}

	completedApply := apply
	completedApply.Status = c.DeliveryApplied
	completedApply.ReceiptDigest = deliveryDigest("apply-receipt")
	applyEvent, applyPayload := deliveryEvent(intentState, completedApply, deliveryDigest("apply-receipt-document"), apply.SourceTaskSeq)
	appliedState, err := Reduce(&intentState, applyEvent, applyPayload)
	if err != nil {
		t.Fatalf("record apply receipt: %v", err)
	}
	if appliedState.Quality != state.Quality || appliedState.Outcome != state.Outcome || appliedState.Fulfillment != state.Fulfillment || !StrictSuccess(appliedState) {
		t.Fatal("delivery receipt changed the terminal task verdict")
	}

	restore := c.DeliveryEffect{
		OperationID: "abcdef0123456789abcdef0123456789", ParentOperationID: apply.OperationID,
		Phase: c.DeliveryRestorePhase, Status: c.DeliveryUnknown,
		PlanDigest: deliveryDigest("restore-plan"), SourceDocument: appliedState.DocumentDigest,
		SourceTaskSeq: appliedState.TaskSeq, SpecVersion: appliedState.SpecVersion,
		PolicyEpoch: appliedState.PolicyEpoch, Generation: appliedState.KernelGeneration,
		CandidateDigest: appliedState.CandidateDigest, TargetDigest: apply.TargetDigest, ResultDigest: apply.ResultDigest,
	}
	restoreIntentEvent, restoreIntentPayload := deliveryEvent(appliedState, restore, deliveryDigest("restore-intent-document"), appliedState.TaskSeq)
	restoreIntentState, err := Reduce(&appliedState, restoreIntentEvent, restoreIntentPayload)
	if err != nil {
		t.Fatalf("record restore intent: %v", err)
	}
	if StrictSuccess(restoreIntentState) {
		t.Fatal("strict success survived an unresolved restore")
	}

	completedRestore := restore
	completedRestore.Status = c.DeliveryRestored
	completedRestore.ReceiptDigest = deliveryDigest("restore-receipt")
	restoreEvent, restorePayload := deliveryEvent(restoreIntentState, completedRestore, deliveryDigest("restore-receipt-document"), restore.SourceTaskSeq)
	finalState, err := Reduce(&restoreIntentState, restoreEvent, restorePayload)
	if err != nil {
		t.Fatalf("record restore receipt: %v", err)
	}
	if finalState.DeliveryEffects[0].Status != c.DeliveryApplied || finalState.DeliveryEffects[1].Status != c.DeliveryRestored || !StrictSuccess(finalState) {
		t.Fatal("restore did not preserve the apply record and resolve its own cursor")
	}
}

func TestDeliveryEffectCannotPromoteQualityOrSkipItsBinding(t *testing.T) {
	state := c.TaskState{
		SchemaVersion: c.SchemaVersion, TaskID: "task", TaskSeq: 1, StoreSeq: 1,
		KernelGeneration: 1, SpecVersion: 1, PolicyEpoch: 1,
		DocumentDigest: deliveryDigest("document"), CandidateDigest: deliveryDigest("candidate"),
		Execution: c.Terminated, Outcome: c.Finished, Quality: c.Partial, Fulfillment: c.Satisfied,
	}
	effect := c.DeliveryEffect{
		OperationID: "0123456789abcdef0123456789abcdef", Phase: c.DeliveryApplyPhase, Status: c.DeliveryUnknown,
		PlanDigest: deliveryDigest("plan"), SourceDocument: state.DocumentDigest,
		SourceTaskSeq: state.TaskSeq, SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch,
		Generation: state.KernelGeneration, CandidateDigest: state.CandidateDigest,
		TargetDigest: deliveryDigest("target"), ResultDigest: deliveryDigest("result"),
	}
	event, payload := deliveryEvent(state, effect, deliveryDigest("intent"), state.TaskSeq)
	payload.Quality = c.Verified
	if _, err := Reduce(&state, event, payload); err == nil {
		t.Fatal("delivery event promoted terminal quality")
	}

	_, payload = deliveryEvent(state, effect, deliveryDigest("intent"), state.TaskSeq)
	effect.CandidateDigest = deliveryDigest("different-candidate")
	encodedDigest, _ := c.Digest(effect)
	payload.DeliveryEffect = &effect
	payload.SnapshotDigest = effect.CandidateDigest
	payload.Reason = encodedDigest
	if _, err := Reduce(&state, event, payload); err == nil {
		t.Fatal("delivery intent with stale candidate binding accepted")
	}
}
