package kernel

import (
	"errors"
	"fmt"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func applyBudgetEvent(t *testing.T, state *c.TaskState, seq int64, kind, actor string, payload c.EventPayload) (c.TaskState, error) {
	t.Helper()
	eventID := fmt.Sprintf("budget-event-%s-%d", kind, seq)
	if kind == "GoalQueued" && payload.Goal != nil {
		eventID = payload.Goal.ID
	}
	event := c.Event{
		SchemaVersion:    c.SchemaVersion,
		ID:               eventID,
		TaskID:           "budget-parent",
		TaskSeq:          seq,
		StoreSeq:         seq,
		KernelGeneration: 1,
		Actor:            actor,
		Type:             kind,
	}
	return Reduce(state, event, payload)
}

func budgetGoal(state c.TaskState, id string, allocation c.BudgetAllocation) *c.GoalMutation {
	digest, _ := allocation.Digest()
	return &c.GoalMutation{
		ID: id, Digest: c.HashBytes([]byte("goal:" + id)),
		BudgetAllocation: allocation, BudgetAllocationDigest: digest,
		Bytes: 1, ParentTaskSeq: state.TaskSeq, ParentDocumentDigest: state.DocumentDigest,
		SourceSnapshotDigest: state.BaselineDigest, ChildTaskID: "child-" + id,
		Reason: c.HashBytes([]byte("reason:" + id)),
	}
}

func TestGoalQueuedCannotExceedSealedParentBudgetPool(t *testing.T) {
	limits := c.BudgetAllocation{MaxSteps: 16, MaxToolCalls: 64, MaxInputTokens: 512 << 10, MaxOutputTokens: 32 << 10, MaxActiveMillis: 120000}
	initial := c.TaskWorkBudget{Limits: limits}
	document := c.HashBytes([]byte("created-document"))
	snapshot := c.HashBytes([]byte("source-snapshot"))
	state, err := applyBudgetEvent(t, nil, 1, "TaskCreated", "kernel", c.EventPayload{SpecVersion: 1, DocumentDigest: document, SnapshotDigest: snapshot, WorkBudget: &initial})
	if err != nil {
		t.Fatal(err)
	}
	used := initial
	used.Steps, used.ToolCalls = 13, 20
	used.UsedInput, used.UsedOutput, used.ActiveMillis = 200000, 10000, 30000
	updatedDocument := c.HashBytes([]byte("updated-document"))
	state, err = applyBudgetEvent(t, &state, 2, "SessionRecorded", "kernel", c.EventPayload{DocumentDigest: updatedDocument, WorkBudget: &used})
	if err != nil {
		t.Fatal(err)
	}
	first := c.BudgetAllocation{MaxSteps: 3, MaxToolCalls: 10, MaxInputTokens: 100000, MaxOutputTokens: 7000, MaxActiveMillis: 30000}
	state, err = applyBudgetEvent(t, &state, 3, "GoalQueued", "user", c.EventPayload{Goal: budgetGoal(state, "first", first)})
	if err != nil {
		t.Fatalf("within-pool fixed allocation rejected: %v", err)
	}
	second := c.BudgetAllocation{MaxSteps: 1, MaxToolCalls: 1, MaxInputTokens: 4096, MaxOutputTokens: 512, MaxActiveMillis: 1000}
	_, err = applyBudgetEvent(t, &state, 4, "GoalQueued", "user", c.EventPayload{Goal: budgetGoal(state, "second", second)})
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
		t.Fatalf("forged child allocation exceeded the parent's remaining steps: %v", err)
	}
}

func TestLegacyTaskCannotMintAnUnwitnessedChildBudget(t *testing.T) {
	state, err := applyBudgetEvent(t, nil, 1, "TaskCreated", "kernel", c.EventPayload{SpecVersion: 1, DocumentDigest: c.HashBytes([]byte("old-document")), SnapshotDigest: c.HashBytes([]byte("old-snapshot"))})
	if err != nil {
		t.Fatal(err)
	}
	allocation := c.BudgetAllocation{MaxSteps: 1, MaxToolCalls: 1, MaxInputTokens: 4096, MaxOutputTokens: 512, MaxActiveMillis: 1000}
	_, err = applyBudgetEvent(t, &state, 2, "GoalQueued", "user", c.EventPayload{Goal: budgetGoal(state, "legacy-child", allocation)})
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
		t.Fatalf("legacy task admitted a child without a sealed pool: %v", err)
	}
}

func TestLegacyGoalScheduleReplayRemainsReadableButCannotEnterNewPool(t *testing.T) {
	newPool := c.TaskWorkBudget{Limits: c.BudgetAllocation{MaxSteps: 16, MaxToolCalls: 64, MaxInputTokens: 512 << 10, MaxOutputTokens: 32 << 10, MaxActiveMillis: 120000}}
	newState, err := applyBudgetEvent(t, nil, 1, "TaskCreated", "kernel", c.EventPayload{
		SpecVersion: 1, DocumentDigest: c.HashBytes([]byte("new-document")), SnapshotDigest: c.HashBytes([]byte("new-snapshot")), WorkBudget: &newPool,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyGoal := budgetGoal(newState, "legacy-shaped", c.BudgetAllocation{})
	legacyGoal.BudgetAllocation = c.BudgetAllocation{}
	legacyGoal.BudgetAllocationDigest = ""
	if _, err = applyBudgetEvent(t, &newState, 2, "GoalQueued", "user", c.EventPayload{Goal: legacyGoal}); err == nil {
		t.Fatal("unallocated legacy-shaped event entered a newly sealed pool")
	}

	oldState, err := applyBudgetEvent(t, nil, 1, "TaskCreated", "kernel", c.EventPayload{
		SpecVersion: 1, DocumentDigest: c.HashBytes([]byte("old-document")), SnapshotDigest: c.HashBytes([]byte("old-snapshot")),
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyGoal = budgetGoal(oldState, "legacy-replay", c.BudgetAllocation{})
	legacyGoal.BudgetAllocation = c.BudgetAllocation{}
	legacyGoal.BudgetAllocationDigest = ""
	oldState, err = applyBudgetEvent(t, &oldState, 2, "GoalQueued", "user", c.EventPayload{Goal: legacyGoal})
	if err != nil || len(oldState.GoalQueue) != 1 {
		t.Fatalf("legacy schedule replay should remain auditable: %#v, %v", oldState.GoalQueue, err)
	}
}
