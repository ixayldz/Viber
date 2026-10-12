package contracts

import (
	"bytes"
	"encoding/json"
	"testing"
)

// This is the TaskState/GoalSchedule wire shape immediately before fixed
// child allocations were added. Checkpoint digests hash this projection, so a
// new zero-value struct field must remain absent when old JSON is re-encoded.
type legacyGoalSchedule struct {
	ID                   string `json:"id"`
	Digest               string `json:"digest"`
	Bytes                int64  `json:"bytes"`
	ParentTaskSeq        int64  `json:"parent_task_seq"`
	ParentDocumentDigest string `json:"parent_document_digest"`
	SourceSnapshotDigest string `json:"source_snapshot_digest"`
	ChildTaskID          string `json:"child_task_id"`
	Status               string `json:"status"`
	ActivationCommandID  string `json:"activation_command_id,omitempty"`
	ChildTaskSeq         int64  `json:"child_task_seq,omitempty"`
	ChildDocumentDigest  string `json:"child_document_digest,omitempty"`
}

type legacyTaskState struct {
	Deletion                *TaskDeletion        `json:"content_deletion,omitempty"`
	DeliveryEffects         []DeliveryEffect     `json:"delivery_effects,omitempty"`
	GoalQueue               []legacyGoalSchedule `json:"goal_queue,omitempty"`
	PromptQueue             []PromptRef          `json:"prompt_queue,omitempty"`
	Resources               *ResourceAccount     `json:"resource_account,omitempty"`
	Tokens                  *TokenAccount        `json:"token_account,omitempty"`
	DocumentDigest          string               `json:"document_digest,omitempty"`
	BaselineDigest          string               `json:"baseline_digest,omitempty"`
	CandidateDigest         string               `json:"candidate_digest,omitempty"`
	SchemaVersion           int                  `json:"schema_version"`
	TaskID                  string               `json:"task_id"`
	SpecVersion             int64                `json:"spec_version"`
	Execution               ExecutionState       `json:"execution_state"`
	Outcome                 Outcome              `json:"terminal_outcome"`
	Quality                 Quality              `json:"quality_verdict"`
	Fulfillment             Fulfillment          `json:"fulfillment_status"`
	TaskSeq                 int64                `json:"task_seq"`
	StoreSeq                int64                `json:"journal_seq"`
	KernelGeneration        int64                `json:"kernel_generation"`
	PolicyEpoch             int64                `json:"policy_epoch"`
	InputBarrier            bool                 `json:"input_barrier"`
	PendingInputIDs         []string             `json:"pending_input_ids"`
	OpenRequiredObligations int                  `json:"open_required_obligations"`
}

func TestPreAllocationCheckpointStateCanonicalBytesRemainStable(t *testing.T) {
	legacy := legacyTaskState{
		GoalQueue: []legacyGoalSchedule{{
			ID: "goal-old", Digest: HashBytes([]byte("goal")), Bytes: 4, ParentTaskSeq: 2,
			ParentDocumentDigest: HashBytes([]byte("parent")), SourceSnapshotDigest: HashBytes([]byte("source")),
			ChildTaskID: "child-old", Status: "QUEUED",
		}},
		SchemaVersion: SchemaVersion, TaskID: "old-parent", SpecVersion: 1,
		Execution: Ready, Quality: Unverified, Fulfillment: FulfillmentPending,
		TaskSeq: 2, StoreSeq: 2, KernelGeneration: 1, PolicyEpoch: 1,
	}
	oldJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TaskState
	if err = DecodeStrict(oldJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	oldCanonical, err := CanonicalV1(legacy)
	if err != nil {
		t.Fatal(err)
	}
	newCanonical, err := CanonicalV1(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oldCanonical, newCanonical) {
		t.Fatalf("pre-allocation checkpoint bytes changed after decode/re-encode:\nold %s\nnew %s", oldCanonical, newCanonical)
	}
	oldDigest, err := Digest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	newDigest, err := Digest(decoded)
	if err != nil || oldDigest != newDigest {
		t.Fatalf("pre-allocation checkpoint digest changed: old=%s new=%s err=%v", oldDigest, newDigest, err)
	}
}

func TestPreAllocationGoalMutationCanonicalBytesRemainStable(t *testing.T) {
	type legacyMutation struct {
		ID                   string `json:"id"`
		Digest               string `json:"digest,omitempty"`
		Bytes                int64  `json:"bytes,omitempty"`
		ParentTaskSeq        int64  `json:"parent_task_seq,omitempty"`
		ParentDocumentDigest string `json:"parent_document_digest,omitempty"`
		SourceSnapshotDigest string `json:"source_snapshot_digest,omitempty"`
		ChildTaskID          string `json:"child_task_id,omitempty"`
		ActivationCommandID  string `json:"activation_command_id,omitempty"`
		ChildTaskSeq         int64  `json:"child_task_seq,omitempty"`
		ChildDocumentDigest  string `json:"child_document_digest,omitempty"`
		Reason               string `json:"reason"`
	}
	legacy := legacyMutation{ID: "goal-old", Digest: HashBytes([]byte("goal")), Bytes: 4,
		ParentTaskSeq: 2, ParentDocumentDigest: HashBytes([]byte("parent")),
		SourceSnapshotDigest: HashBytes([]byte("source")), ChildTaskID: "child-old", Reason: HashBytes([]byte("command")),
	}
	oldJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded GoalMutation
	if err = DecodeStrict(oldJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	oldCanonical, err := CanonicalV1(legacy)
	if err != nil {
		t.Fatal(err)
	}
	newCanonical, err := CanonicalV1(decoded)
	if err != nil || !bytes.Equal(oldCanonical, newCanonical) {
		t.Fatalf("pre-allocation goal mutation bytes changed after decode/re-encode: old=%s new=%s err=%v", oldCanonical, newCanonical, err)
	}
}
