package contracts

import (
	"strings"
)

type Integrity string

const (
	Intact  Integrity = "INTACT"
	Missing Integrity = "MISSING"
	Corrupt Integrity = "CORRUPT"
)

type ExecutionState string

const (
	Created         ExecutionState = "CREATED"
	Scoping         ExecutionState = "SCOPING"
	Ready           ExecutionState = "READY"
	Running         ExecutionState = "RUNNING"
	Verifying       ExecutionState = "VERIFYING"
	Delivering      ExecutionState = "DELIVERING"
	WaitingUser     ExecutionState = "WAITING_USER"
	WaitingResource ExecutionState = "WAITING_RESOURCE"
	Blocked         ExecutionState = "BLOCKED"
	Pausing         ExecutionState = "PAUSING"
	Paused          ExecutionState = "PAUSED"
	Recovering      ExecutionState = "RECOVERING"
	Terminated      ExecutionState = "TERMINATED"
)

type Outcome string

const (
	Finished        Outcome = "FINISHED"
	Cancelled       Outcome = "CANCELLED"
	Failed          Outcome = "FAILED"
	BudgetExhausted Outcome = "BUDGET_EXHAUSTED"
)

type Quality string

const (
	Unverified         Quality = "UNVERIFIED"
	Partial            Quality = "PARTIAL"
	Verified           Quality = "VERIFIED"
	QualityFailed      Quality = "FAILED"
	AcceptedWithWaiver Quality = "ACCEPTED_WITH_WAIVER"
)

type Fulfillment string

const (
	FulfillmentPending Fulfillment = "PENDING"
	Satisfied          Fulfillment = "SATISFIED"
	Conflicted         Fulfillment = "CONFLICTED"
	FulfillmentUnknown Fulfillment = "UNKNOWN"
	FulfillmentFailed  Fulfillment = "FAILED"
)

type Verdict string

const (
	Pending     Verdict = "PENDING"
	Pass        Verdict = "PASS"
	FailVerdict Verdict = "FAIL"
	Unknown     Verdict = "UNKNOWN"
	Waived      Verdict = "WAIVED"
)

type SourceSpan struct {
	InputID string `json:"input_id"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
}
type InputSource struct {
	ID         string    `json:"id"`
	PayloadRef string    `json:"payload_ref"`
	Digest     string    `json:"digest"`
	ByteLength int64     `json:"byte_length"`
	Integrity  Integrity `json:"integrity"`
}
type Requirement struct {
	ID                 string     `json:"id"`
	Source             SourceSpan `json:"source_span"`
	Required           bool       `json:"required"`
	Risk               string     `json:"risk"`
	VerificationMethod string     `json:"verification_method"`
}
type TaskSpec struct {
	SchemaVersion   int           `json:"schema_version"`
	TaskID          string        `json:"task_id"`
	Version         int64         `json:"version"`
	Goal            string        `json:"goal"`
	Inputs          []InputSource `json:"inputs"`
	Requirements    []Requirement `json:"requirements"`
	ProtectedOrigin string        `json:"protected_check_origin"`
	DeliveryPolicy  string        `json:"delivery_policy"`
}

func (s TaskSpec) Validate() error {
	if s.SchemaVersion != SchemaVersion || s.Version < 1 || strings.TrimSpace(s.TaskID) == "" || strings.TrimSpace(s.Goal) == "" {
		return Fail(InvalidArgument, "unsupported spec version or missing task/goal")
	}
	if s.DeliveryPolicy != "CANDIDATE_ONLY" && s.DeliveryPolicy != "LIVE_APPLY_REQUIRED" {
		return Fail(InvalidArgument, "invalid delivery policy")
	}
	inputs := map[string]InputSource{}
	for _, input := range s.Inputs {
		if input.ID == "" || input.PayloadRef == "" || !ValidDigest(input.Digest) || input.ByteLength <= 0 || input.Integrity != Intact {
			return Fail(InvalidArgument, "required raw intent is unavailable or invalid")
		}
		if _, ok := inputs[input.ID]; ok {
			return Fail(InvalidArgument, "duplicate input")
		}
		inputs[input.ID] = input
	}
	if len(inputs) == 0 {
		return Fail(InvalidArgument, "raw intent is required")
	}
	ids := map[string]bool{}
	required := 0
	for _, r := range s.Requirements {
		input, ok := inputs[r.Source.InputID]
		if r.ID == "" || ids[r.ID] || !ok || r.Source.Start < 0 || r.Source.End <= r.Source.Start || r.Source.End > input.ByteLength || r.VerificationMethod == "" {
			return Fail(InvalidArgument, "invalid requirement source or definition")
		}
		if r.Risk != "NORMAL" && r.Risk != "CRITICAL" {
			return Fail(InvalidArgument, "invalid requirement risk")
		}
		ids[r.ID] = true
		if r.Required {
			required++
		}
	}
	if required == 0 {
		return Fail(InvalidArgument, "at least one required criterion is mandatory")
	}
	if !ValidDigest(s.ProtectedOrigin) {
		return Fail(InvalidArgument, "protected check origin required")
	}
	return nil
}

type TaskState struct {
	DocumentDigest          string         `json:"document_digest,omitempty"`
	BaselineDigest          string         `json:"baseline_digest,omitempty"`
	CandidateDigest         string         `json:"candidate_digest,omitempty"`
	SchemaVersion           int            `json:"schema_version"`
	TaskID                  string         `json:"task_id"`
	SpecVersion             int64          `json:"spec_version"`
	Execution               ExecutionState `json:"execution_state"`
	Outcome                 Outcome        `json:"terminal_outcome"`
	Quality                 Quality        `json:"quality_verdict"`
	Fulfillment             Fulfillment    `json:"fulfillment_status"`
	TaskSeq                 int64          `json:"task_seq"`
	StoreSeq                int64          `json:"journal_seq"`
	KernelGeneration        int64          `json:"kernel_generation"`
	PolicyEpoch             int64          `json:"policy_epoch"`
	InputBarrier            bool           `json:"input_barrier"`
	PendingInputIDs         []string       `json:"pending_input_ids"`
	OpenRequiredObligations int            `json:"open_required_obligations"`
}
type Event struct {
	SchemaVersion    int    `json:"schema_version"`
	ID               string `json:"event_id"`
	StoreSeq         int64  `json:"store_seq"`
	TaskID           string `json:"task_id"`
	TaskSeq          int64  `json:"task_seq"`
	KernelGeneration int64  `json:"kernel_generation"`
	Actor            string `json:"actor"`
	CausationID      string `json:"causation_id"`
	Type             string `json:"type"`
	Timestamp        string `json:"timestamp"`
	PayloadDigest    string `json:"payload_digest"`
	PayloadRef       string `json:"payload_ref"`
}
type EventPayload struct {
	DocumentDigest      string         `json:"document_digest,omitempty"`
	SnapshotDigest      string         `json:"snapshot_digest,omitempty"`
	InputID             string         `json:"input_id,omitempty"`
	SpecVersion         int64          `json:"spec_version,omitempty"`
	State               ExecutionState `json:"state,omitempty"`
	Outcome             Outcome        `json:"outcome,omitempty"`
	Quality             Quality        `json:"quality,omitempty"`
	Fulfillment         Fulfillment    `json:"fulfillment,omitempty"`
	PolicyEpoch         int64          `json:"policy_epoch,omitempty"`
	RequiredObligations int            `json:"required_obligations,omitempty"`
	Reason              string         `json:"reason,omitempty"`
}
type Entry struct {
	Path string `json:"path"`
	Hash string `json:"blob_hash"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
}
type GitIndexEntry struct {
	Path        string `json:"path"`
	ObjectID    string `json:"object_id"`
	Mode        uint32 `json:"mode"`
	IntentToAdd bool   `json:"intent_to_add"`
}
type GitState struct {
	GitDir              string          `json:"git_dir"`
	CommonDir           string          `json:"common_dir"`
	HeadRef             string          `json:"head_ref"`
	HeadObject          string          `json:"head_object"`
	ObjectFormat        string          `json:"object_format"`
	IndexDigest         string          `json:"index_digest"`
	IndexPresent        bool            `json:"index_present"`
	Entries             []GitIndexEntry `json:"entries"`
	IgnoreSourcesDigest string          `json:"ignore_sources_digest"`
}
type Snapshot struct {
	Git           *GitState `json:"git,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	Digest        string    `json:"manifest_digest"`
	Root          string    `json:"canonical_root"`
	Consistency   string    `json:"capture_consistency"`
	Entries       []Entry   `json:"entries"`
	Exclusions    []string  `json:"exclusions"`
	Directories   []string  `json:"directories"`
}
type ReadCondition struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
}
type Change struct {
	Path         string `json:"path"`
	BeforeDigest string `json:"before_digest"`
	After        []byte `json:"after_bytes"`
	Delete       bool   `json:"delete"`
}
type Proposal struct {
	SchemaVersion    int             `json:"schema_version"`
	ID               string          `json:"id"`
	TaskID           string          `json:"task_id"`
	SpecVersion      int64           `json:"spec_version"`
	BaseSnapshot     string          `json:"base_snapshot"`
	PolicyEpoch      int64           `json:"policy_epoch"`
	KernelGeneration int64           `json:"kernel_generation"`
	ReadSet          []ReadCondition `json:"read_set"`
	Changes          []Change        `json:"changes"`
}
type Binding struct {
	TaskID            string `json:"task_id"`
	SpecVersion       int64  `json:"spec_version"`
	CandidateDigest   string `json:"candidate_digest"`
	CheckSetDigest    string `json:"check_set_digest"`
	EnvironmentDigest string `json:"environment_digest"`
	PolicyDigest      string `json:"policy_digest"`
}
type VerificationReceipt struct {
	ID                     string    `json:"id"`
	CheckKind              string    `json:"check_kind"`
	Binding                Binding   `json:"binding"`
	RequirementIDs         []string  `json:"requirement_ids"`
	Verdict                Verdict   `json:"verdict"`
	Integrity              Integrity `json:"integrity"`
	PolicyCompatible       bool      `json:"policy_compatible"`
	SourceReadOnly         bool      `json:"source_read_only"`
	ProtectedResultChannel bool      `json:"protected_result_channel"`
	ProcessTreeQuiescent   bool      `json:"process_tree_quiescent"`
	DiscoveryExpected      bool      `json:"discovery_expected"`
	ExecutedTests          int64     `json:"executed_test_count"`
	TrustedCompletion      bool      `json:"trusted_completion"`
	IndependentObserver    bool      `json:"independent_observer"`
	WaiverAuthorized       bool      `json:"waiver_authorized"`
}
