package contracts

import "strings"

// ReadWorker roles are deliberately separate from the ordinary goal role. An
// empty GoalSchedule.Role retains the original manual child semantics.
const (
	ReadWorkerRoleExplore = "EXPLORE"
	ReadWorkerRoleReview  = "REVIEW"
)

const (
	ReadWorkerLeaseSchemaVersion = 1
	ReadWorkerLeaseMaxTTLMillis  = int64(15 * 60 * 1000)
)

// ReadWorkerLease is the immutable parent-side admission for one LLM child.
// SpecVersion, PolicyEpoch and KernelGeneration describe the parent authority
// that admitted the child. They are deliberately not the child's own spec or
// policy counters: every newly created child starts its independent document
// at spec/policy version one. DeadlineMillis is relative to ClockDomain's
// monotonic origin; it is never a persisted wall-clock timestamp. A reopened
// owner must obtain a new activation/admission because the clock domain and
// kernel generation change.
type ReadWorkerLease struct {
	SchemaVersion         int    `json:"schema_version"`
	ParentTask            string `json:"parent_task"`
	ParentTaskSeq         int64  `json:"parent_task_seq"`
	ParentDocumentDigest  string `json:"parent_document_digest"`
	ParentCandidateDigest string `json:"parent_candidate_digest"`
	ChildTaskID           string `json:"child_task_id"`
	Role                  string `json:"role"`
	SpecVersion           int64  `json:"spec_version"`
	PolicyEpoch           int64  `json:"policy_epoch"`
	KernelGeneration      int64  `json:"kernel_generation"`
	// ContextProfileDigest binds the complete declared runtime/context profile;
	// it is a digest only and never contains provider credential bytes.
	ContextProfileDigest string `json:"context_profile_digest"`
	OutputBudget         int64  `json:"output_budget"`
	ClockDomain          string `json:"clock_domain"`
	IssuedMillis         int64  `json:"issued_millis"`
	DeadlineMillis       int64  `json:"deadline_millis"`
}

func ValidReadWorkerRole(role string) bool {
	return role == ReadWorkerRoleExplore || role == ReadWorkerRoleReview
}

func ValidReadWorkerClockDomain(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (l ReadWorkerLease) Digest() (string, error) {
	return Digest(l)
}

func (l ReadWorkerLease) Validate() error {
	if l.SchemaVersion != ReadWorkerLeaseSchemaVersion || l.ParentTask == "" || len(l.ParentTask) > 128 ||
		l.ParentTaskSeq < 1 || !ValidDigest(l.ParentDocumentDigest) || !ValidDigest(l.ParentCandidateDigest) ||
		l.ChildTaskID == "" || len(l.ChildTaskID) > 128 || l.ChildTaskID == l.ParentTask ||
		!ValidReadWorkerRole(l.Role) || l.SpecVersion < 1 || l.PolicyEpoch < 1 || l.KernelGeneration < 1 ||
		!ValidDigest(l.ContextProfileDigest) || l.OutputBudget < 512 || l.OutputBudget > 1<<20 ||
		!ValidReadWorkerClockDomain(l.ClockDomain) || l.IssuedMillis < 0 || l.DeadlineMillis <= l.IssuedMillis ||
		l.DeadlineMillis-l.IssuedMillis > ReadWorkerLeaseMaxTTLMillis {
		return Fail(InvalidArgument, "invalid parent-bound read-worker lease")
	}
	return nil
}
