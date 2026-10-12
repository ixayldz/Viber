package kernel

import (
	"errors"
	"fmt"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func readWorkerTestTask(t *testing.T) c.TaskState {
	t.Helper()
	limits := c.BudgetAllocation{MaxSteps: 16, MaxToolCalls: 64, MaxInputTokens: 512 << 10, MaxOutputTokens: 32 << 10, MaxActiveMillis: 120000}
	budget := c.TaskWorkBudget{Limits: limits}
	state, err := Reduce(nil, c.Event{SchemaVersion: c.SchemaVersion, ID: "read-worker-create", TaskID: "budget-parent", TaskSeq: 1, StoreSeq: 1, KernelGeneration: 1, Actor: "kernel", Type: "TaskCreated"}, c.EventPayload{SpecVersion: 1, DocumentDigest: c.HashBytes([]byte("read-worker-document")), SnapshotDigest: c.HashBytes([]byte("read-worker-candidate")), WorkBudget: &budget})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func readWorkerTestAdmission(state c.TaskState, operation string) c.ReadWorkerAdmission {
	a := c.ReadWorkerAdmission{
		OperationID: operation, RequestDigest: c.HashBytes([]byte("request:" + operation)),
		SourceDocumentDigest: state.DocumentDigest, CandidateDigest: state.CandidateDigest,
		PolicyDigest: c.HashBytes([]byte("policy")), SourceTaskSeq: state.TaskSeq, SpecVersion: state.SpecVersion,
		PolicyEpoch: state.PolicyEpoch, KernelGeneration: state.KernelGeneration, WorkerCount: 2,
		ChargedSteps: 2, ChargedToolCalls: 2, ChargedActiveMillis: 5000, Status: c.ReadWorkerAdmissionCharged,
	}
	a.AdmissionDigest, _ = a.Digest()
	return a
}

func readWorkerReadyState(t *testing.T) c.TaskState {
	t.Helper()
	state := readWorkerTestTask(t)
	for i, execution := range []c.ExecutionState{c.Scoping, c.Ready} {
		event := c.Event{SchemaVersion: c.SchemaVersion, ID: fmt.Sprintf("read-worker-ready-%d", i+1), TaskID: state.TaskID, TaskSeq: state.TaskSeq + 1, StoreSeq: state.StoreSeq + 1, KernelGeneration: state.KernelGeneration, Actor: "kernel", Type: "StateTransitioned"}
		next, err := Reduce(&state, event, c.EventPayload{State: execution})
		if err != nil {
			t.Fatalf("cannot prepare quiescent worker task: %v", err)
		}
		state = next
	}
	return state
}

func reduceReadWorkerCharge(state c.TaskState, admission c.ReadWorkerAdmission, stepDelta int64) (c.TaskState, error) {
	updated := *state.WorkBudget
	updated.Steps += stepDelta
	updated.ToolCalls += admission.ChargedToolCalls
	updated.ActiveMillis += admission.ChargedActiveMillis
	newDocument := c.HashBytes([]byte(fmt.Sprintf("read-worker-after:%d", state.TaskSeq)))
	event := c.Event{SchemaVersion: c.SchemaVersion, ID: fmt.Sprintf("read-worker-%d", state.TaskSeq+1), TaskID: state.TaskID, TaskSeq: state.TaskSeq + 1, StoreSeq: state.StoreSeq + 1, KernelGeneration: admission.KernelGeneration, Actor: "kernel", Type: "ReadWorkersAdmitted"}
	return Reduce(&state, event, c.EventPayload{DocumentDigest: newDocument, WorkBudget: &updated, ReadWorkerAdmission: &admission})
}

func TestReadWorkersAdmissionChargesExactBudgetAndRejectsReuse(t *testing.T) {
	state := readWorkerTestTask(t)
	admission := readWorkerTestAdmission(state, "0123456789abcdef0123456789abcdef")
	next, err := reduceReadWorkerCharge(state, admission, 2)
	if err != nil {
		t.Fatalf("exact read-worker charge rejected: %v", err)
	}
	if next.WorkBudget.Steps != 2 || next.WorkBudget.ToolCalls != 2 || next.WorkBudget.ActiveMillis != 5000 || len(next.ReadWorkerAdmissions) != 1 {
		t.Fatalf("admission did not charge the sealed work pool: %#v", next)
	}
	duplicate := readWorkerTestAdmission(next, admission.OperationID)
	if _, err = reduceReadWorkerCharge(next, duplicate, 2); err == nil {
		t.Fatal("already charged operation ID was admitted again")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.CommandIDConflict {
			t.Fatalf("duplicate operation returned the wrong error: %v", err)
		}
	}
}

func TestReadWorkersAdmissionRejectsForgedDeltaAndChildHoldOverrun(t *testing.T) {
	state := readWorkerTestTask(t)
	admission := readWorkerTestAdmission(state, "1123456789abcdef0123456789abcdef")
	if _, err := reduceReadWorkerCharge(state, admission, 3); err == nil {
		t.Fatal("forged overcharge was accepted")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StoreIntegrityError {
			t.Fatalf("forged delta returned the wrong error: %v", err)
		}
	}

	allocation := c.BudgetAllocation{MaxSteps: 15, MaxToolCalls: 63, MaxInputTokens: 4096, MaxOutputTokens: 512, MaxActiveMillis: 116000}
	digest, _ := allocation.Digest()
	state.GoalQueue = []c.GoalSchedule{{ID: "held-child", BudgetAllocation: allocation, BudgetAllocationDigest: digest, Status: "ACTIVATED"}}
	admission = readWorkerTestAdmission(state, "2123456789abcdef0123456789abcdef")
	if _, err := reduceReadWorkerCharge(state, admission, 2); err == nil {
		t.Fatal("read-worker charge exceeded the parent pool after child holds")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
			t.Fatalf("child-held budget returned the wrong error: %v", err)
		}
	}
}

func TestReadWorkersAdmissionRebindsOnlyQuiescentOwnerGeneration(t *testing.T) {
	state := readWorkerReadyState(t)
	oldGeneration, sourceSeq, sourceDoc := state.KernelGeneration, state.TaskSeq, state.DocumentDigest
	currentGeneration := oldGeneration + 1
	admission := readWorkerTestAdmission(state, "3123456789abcdef0123456789abcdef")
	admission.KernelGeneration = currentGeneration
	admission.AdmissionDigest, _ = admission.Digest()
	next, err := reduceReadWorkerCharge(state, admission, 2)
	if err != nil {
		t.Fatalf("quiescent read-worker admission should bind the current owner without changing the source cursor: %v", err)
	}
	if next.KernelGeneration != currentGeneration || next.TaskSeq != sourceSeq+1 || next.DocumentDigest == sourceDoc || len(next.ReadWorkerAdmissions) != 1 || next.ReadWorkerAdmissions[0].SourceTaskSeq != sourceSeq || next.ReadWorkerAdmissions[0].SourceDocumentDigest != sourceDoc {
		t.Fatalf("generation-bound admission lost its original source cursor: %#v", next)
	}

	blocked := state
	blocked.Tokens = &c.TokenAccount{SchemaVersion: 1, Limits: c.DefaultTokenLimits(), Reservations: []c.TokenReservation{{ID: "unknown-model-call", Status: "UNKNOWN"}}}
	blockedAdmission := readWorkerTestAdmission(blocked, "4123456789abcdef0123456789abcdef")
	blockedAdmission.KernelGeneration = currentGeneration
	blockedAdmission.AdmissionDigest, _ = blockedAdmission.Digest()
	if _, err = reduceReadWorkerCharge(blocked, blockedAdmission, 2); err == nil {
		t.Fatal("owner generation rebind was allowed while token exposure remained UNKNOWN")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StaleAuthority {
			t.Fatalf("unsettled exposure rebind returned the wrong error: %v", err)
		}
	}
}
