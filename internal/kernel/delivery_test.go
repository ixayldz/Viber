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

func TestDeliveryReconcileAndAuthorityRebindAdvanceOnlyCurrentGeneration(t *testing.T) {
	state := c.TaskState{
		SchemaVersion: c.SchemaVersion, TaskID: "task", TaskSeq: 9, StoreSeq: 9,
		KernelGeneration: 3, SpecVersion: 2, PolicyEpoch: 4,
		DocumentDigest: deliveryDigest("document"), CandidateDigest: deliveryDigest("candidate"),
		Execution: c.Terminated, Outcome: c.Finished, Quality: c.Unverified, Fulfillment: c.Satisfied,
	}
	apply := c.DeliveryEffect{
		OperationID: "0123456789abcdef0123456789abcdef", Phase: c.DeliveryApplyPhase, Status: c.DeliveryUnknown,
		PlanDigest: deliveryDigest("plan"), SourceDocument: state.DocumentDigest,
		SourceTaskSeq: state.TaskSeq, SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch,
		Generation: state.KernelGeneration, CandidateDigest: state.CandidateDigest,
		TargetDigest: deliveryDigest("target"), ResultDigest: deliveryDigest("result"),
	}
	intentEvent, intentPayload := deliveryEvent(state, apply, state.DocumentDigest, state.TaskSeq)
	intentState, err := Reduce(&state, intentEvent, intentPayload)
	if err != nil {
		t.Fatal(err)
	}
	completed := apply
	completed.Status = c.DeliveryApplied
	completed.ReceiptDigest = deliveryDigest("receipt")
	reason, _ := c.Digest(completed)
	reconcileEvent := c.Event{SchemaVersion: c.SchemaVersion, ID: "delivery-reconcile", StoreSeq: intentState.StoreSeq + 1, TaskID: intentState.TaskID, TaskSeq: intentState.TaskSeq + 1, KernelGeneration: 4, Actor: "kernel", Type: "DeliveryEffectReconciled"}
	reconcilePayload := c.EventPayload{DocumentDigest: intentState.DocumentDigest, SnapshotDigest: completed.CandidateDigest, Reason: reason, DeliveryEffect: &completed}
	reconciled, err := Reduce(&intentState, reconcileEvent, reconcilePayload)
	if err != nil {
		t.Fatalf("read-only crash reconciliation did not advance generation: %v", err)
	}
	if reconciled.KernelGeneration != 4 || reconciled.DeliveryEffects[0].Status != c.DeliveryApplied || reconciled.Quality != state.Quality {
		t.Fatal("delivery reconciliation changed task verdict or lost its receipt")
	}

	current := c.TaskState{SchemaVersion: c.SchemaVersion, TaskID: "fresh", TaskSeq: 3, StoreSeq: 3, KernelGeneration: 7, SpecVersion: 1, PolicyEpoch: 1, DocumentDigest: deliveryDigest("fresh-document"), CandidateDigest: deliveryDigest("fresh-candidate"), Execution: c.Terminated, Outcome: c.Finished, Quality: c.Partial, Fulfillment: c.Satisfied}
	currentGeneration := int64(8)
	bind, err := c.DeliveryAuthorityRebindDigest(current, currentGeneration, nil)
	if err != nil {
		t.Fatal(err)
	}
	rebindEvent := c.Event{SchemaVersion: c.SchemaVersion, ID: "delivery-authority-rebind", StoreSeq: current.StoreSeq + 1, TaskID: current.TaskID, TaskSeq: current.TaskSeq + 1, KernelGeneration: currentGeneration, Actor: "kernel", Type: "DeliveryAuthorityRebound"}
	rebound, err := Reduce(&current, rebindEvent, c.EventPayload{DocumentDigest: current.DocumentDigest, SnapshotDigest: current.CandidateDigest, Reason: bind})
	if err != nil || rebound.KernelGeneration != currentGeneration || rebound.Quality != current.Quality || rebound.DocumentDigest != current.DocumentDigest {
		t.Fatalf("safe terminal authority rebind changed immutable task facts: %+v %v", rebound, err)
	}
}
