package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestParentGoalBudgetPoolHoldsEveryActivationState(t *testing.T) {
	parent := DefaultBudget()
	allocation := c.BudgetAllocation{
		MaxSteps: parent.MaxSteps / 4, MaxToolCalls: parent.MaxToolCalls / 4,
		MaxInputTokens: parent.MaxInputTokens / 4, MaxOutputTokens: parent.MaxOutputTokens / 4,
		MaxActiveMillis: parent.MaxActiveMillis / 4,
	}
	digest, err := allocation.Digest()
	if err != nil {
		t.Fatal(err)
	}
	statuses := []string{"QUEUED", "ACTIVATING", "UNKNOWN", "ACTIVATED"}
	for _, status := range statuses {
		item := c.GoalSchedule{ID: "goal", BudgetAllocation: allocation, BudgetAllocationDigest: digest, Status: status}
		headroom, err := budgetHeadroom(parent, []c.GoalSchedule{item}, false)
		if err != nil || headroom.MaxSteps != parent.MaxSteps-allocation.MaxSteps || headroom.MaxToolCalls != parent.MaxToolCalls-allocation.MaxToolCalls || headroom.MaxInputTokens != parent.MaxInputTokens-allocation.MaxInputTokens || headroom.MaxOutputTokens != parent.MaxOutputTokens-allocation.MaxOutputTokens || headroom.MaxActiveMillis != parent.MaxActiveMillis-allocation.MaxActiveMillis {
			t.Fatalf("%s did not retain its fixed parent hold: %#v, %v", status, headroom, err)
		}
	}
	cancelled := c.GoalSchedule{ID: "goal", BudgetAllocation: allocation, BudgetAllocationDigest: digest, Status: "CANCELLED"}
	full, err := budgetHeadroom(parent, []c.GoalSchedule{cancelled}, false)
	if err != nil || full.MaxSteps != parent.MaxSteps || full.MaxInputTokens != parent.MaxInputTokens || full.MaxActiveMillis != parent.MaxActiveMillis {
		t.Fatalf("only cancellation of an unactivated goal may release its hold: %#v, %v", full, err)
	}
	if _, err = allocateGoalBudget(parent, nil, true); err == nil {
		t.Fatal("pending parent effect was allowed to allocate active-time capacity")
	}
	legacy := c.GoalSchedule{ID: "legacy", Status: "ACTIVATING"}
	if _, err = budgetHeadroom(parent, []c.GoalSchedule{legacy}, false); err == nil {
		t.Fatal("legacy activation without a fixed allocation escaped the pool")
	}
}

func TestPreAllocationGoalOriginCanonicalBytesRemainStable(t *testing.T) {
	type legacyOrigin struct {
		ParentTask           string `json:"parent_task"`
		ParentTaskSeq        int64  `json:"parent_task_seq"`
		ParentDocumentDigest string `json:"parent_document_digest"`
		GoalID               string `json:"goal_id"`
		GoalDigest           string `json:"goal_digest"`
		SourceSnapshotDigest string `json:"source_snapshot_digest"`
		ChildTaskID          string `json:"child_task_id"`
	}
	legacy := legacyOrigin{
		ParentTask: "old-parent", ParentTaskSeq: 3,
		ParentDocumentDigest: c.HashBytes([]byte("parent-document")),
		GoalID:               "old-goal", GoalDigest: c.HashBytes([]byte("goal")),
		SourceSnapshotDigest: c.HashBytes([]byte("source")), ChildTaskID: "old-child",
	}
	oldJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded GoalOrigin
	if err = c.DecodeStrict(oldJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	oldCanonical, err := c.CanonicalV1(legacy)
	if err != nil {
		t.Fatal(err)
	}
	newCanonical, err := c.CanonicalV1(decoded)
	if err != nil || !bytes.Equal(oldCanonical, newCanonical) {
		t.Fatalf("pre-allocation goal origin bytes changed after decode/re-encode: old=%s new=%s err=%v", oldCanonical, newCanonical, err)
	}
	oldDigest, err := c.Digest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	newDigest, err := c.Digest(decoded)
	if err != nil || oldDigest != newDigest {
		t.Fatalf("pre-allocation goal origin digest changed: old=%s new=%s err=%v", oldDigest, newDigest, err)
	}
}
