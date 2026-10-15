package contracts

import (
	"strconv"
	"strings"
	"unicode/utf8"
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

// PRDSource is an immutable binding to the exact PRD file that was present in
// the captured project candidate.  It is part of the task specification so a
// later model turn, resume, or export cannot silently switch to a live file or
// to a different path with similar contents.
type PRDSource struct {
	SchemaVersion           int          `json:"schema_version"`
	CandidateDigest         string       `json:"candidate_digest"`
	Path                    string       `json:"path"`
	InputID                 string       `json:"input_id"`
	Digest                  string       `json:"digest"`
	ByteLength              int64        `json:"byte_length"`
	SectionExtraction       string       `json:"section_extraction"`
	HeadingCount            int          `json:"heading_count"`
	OmittedHeadingCount     int          `json:"omitted_heading_count"`
	UnsupportedHeadingCount int          `json:"unsupported_heading_count"`
	Sections                []PRDSection `json:"sections"`
}

// PRDSection is a deterministic byte interval for one extracted Markdown
// heading. It identifies source material only; the heading text is never a
// semantic interpretation of the requirement.
type PRDSection struct {
	ID      string `json:"id"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Level   int    `json:"level"`
	Heading string `json:"heading"`
}

type PRDReadInterval struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// PRDCoverage is a durable, deliberately modest coverage signal.  It records
// whether the source-bound requirements have an admitted plan; it never
// claims implementation or verification merely because a plan exists.
type PRDCoverage struct {
	SchemaVersion          int               `json:"schema_version"`
	PRDSourceDigest        string            `json:"prd_source_digest"`
	Status                 string            `json:"status"`
	SectionStatus          string            `json:"section_status"`
	PlanDigest             string            `json:"plan_digest,omitempty"`
	RequiredRequirementIDs []string          `json:"required_requirement_ids"`
	CoveredRequirementIDs  []string          `json:"covered_requirement_ids"`
	ReadCandidateDigest    string            `json:"read_candidate_digest,omitempty"`
	ReadDigest             string            `json:"read_digest,omitempty"`
	ReadTotalBytes         int64             `json:"read_total_bytes,omitempty"`
	ReadBytes              int64             `json:"read_bytes,omitempty"`
	ReadStatus             string            `json:"read_status"`
	ReadIntervals          []PRDReadInterval `json:"read_intervals"`
	CoveredSectionIDs      []string          `json:"covered_section_ids"`
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
	PRD             *PRDSource    `json:"prd_source,omitempty"`
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
	if s.PRD != nil {
		prd := s.PRD
		if prd.SchemaVersion != SchemaVersion || !ValidDigest(prd.CandidateDigest) || !ValidDigest(prd.Digest) || prd.InputID == "" || prd.ByteLength <= 0 || len(prd.Path) == 0 || len(prd.Path) > 256 || !validRelativePath(prd.Path) || prd.HeadingCount < 0 || prd.OmittedHeadingCount < 0 || prd.UnsupportedHeadingCount < 0 || len(prd.Sections) > 128 {
			return Fail(InvalidArgument, "invalid PRD source binding")
		}
		switch prd.SectionExtraction {
		case "":
			// PRD bindings written by the first durable-binding release had no
			// section metadata. Keep them loadable, but treat their section
			// coverage as requiring review rather than claiming no headings.
			if len(prd.Sections) != 0 || prd.HeadingCount != 0 || prd.OmittedHeadingCount != 0 || prd.UnsupportedHeadingCount != 0 {
				return Fail(InvalidArgument, "invalid legacy PRD section metadata")
			}
		case "NO_HEADINGS":
			if prd.HeadingCount != 0 || prd.OmittedHeadingCount != 0 || prd.UnsupportedHeadingCount != 0 || len(prd.Sections) != 0 {
				return Fail(InvalidArgument, "invalid empty PRD section extraction")
			}
		case "COMPLETE":
			if prd.OmittedHeadingCount != 0 || prd.UnsupportedHeadingCount != 0 || prd.HeadingCount != len(prd.Sections) {
				return Fail(InvalidArgument, "incomplete PRD section extraction marked complete")
			}
		case "REVIEW_REQUIRED":
			if prd.OmittedHeadingCount == 0 && prd.UnsupportedHeadingCount == 0 && prd.HeadingCount == len(prd.Sections) {
				return Fail(InvalidArgument, "PRD review state lacks an explicit omitted or unsupported heading")
			}
		default:
			return Fail(InvalidArgument, "invalid PRD section extraction status")
		}
		input, ok := inputs[prd.InputID]
		if !ok || input.Digest != prd.Digest || input.ByteLength != prd.ByteLength || input.Integrity != Intact {
			return Fail(InvalidArgument, "PRD source input binding mismatch")
		}
		lastEnd := int64(-1)
		for i, section := range prd.Sections {
			if section.ID != "prd-section-"+formatOrdinal(i+1) || section.Start < 0 || section.End <= section.Start || section.End > prd.ByteLength || section.Start < lastEnd || section.Level < 1 || section.Level > 6 || len(section.Heading) > 2048 || !utf8.ValidString(section.Heading) {
				return Fail(InvalidArgument, "invalid PRD section span")
			}
			lastEnd = section.End
		}
	}
	ids := map[string]bool{}
	requirements := map[string]Requirement{}
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
		requirements[r.ID] = r
		if r.Required {
			required++
		}
	}
	if required == 0 {
		return Fail(InvalidArgument, "at least one required criterion is mandatory")
	}
	if s.PRD != nil {
		r, ok := requirements["prd-source"]
		if !ok || !r.Required || r.Source.InputID != s.PRD.InputID || r.Source.Start != 0 || r.Source.End != s.PRD.ByteLength || r.VerificationMethod != "PRD_REQUIREMENT_COVERAGE" {
			return Fail(InvalidArgument, "PRD source requirement is missing or unbound")
		}
		for _, section := range s.PRD.Sections {
			r, ok := requirements[section.ID]
			if !ok || !r.Required || r.Source.InputID != s.PRD.InputID || r.Source.Start != section.Start || r.Source.End != section.End || r.VerificationMethod != "PRD_SECTION_COVERAGE" {
				return Fail(InvalidArgument, "PRD section criterion is missing or unbound")
			}
		}
	}
	if !ValidDigest(s.ProtectedOrigin) {
		return Fail(InvalidArgument, "protected check origin required")
	}
	return nil
}

func validRelativePath(path string) bool {
	if path == "." || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") || strings.Contains(path, "\\") || strings.ContainsRune(path, 0) {
		return false
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func formatOrdinal(value int) string {
	if value < 10 {
		return "000" + strconv.Itoa(value)
	}
	if value < 100 {
		return "00" + strconv.Itoa(value)
	}
	if value < 1000 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

type PromptRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

// BudgetAllocation is a fixed upper bound reserved from a parent task's local
// work budget for one separately scheduled child. Store-wide token and
// physical-resource ledgers remain independent and are deliberately not
// duplicated here.
type BudgetAllocation struct {
	MaxSteps        int64 `json:"max_steps"`
	MaxToolCalls    int64 `json:"max_tool_calls"`
	MaxInputTokens  int64 `json:"max_input_tokens"`
	MaxOutputTokens int64 `json:"max_output_tokens"`
	MaxActiveMillis int64 `json:"max_active_millis"`
}

// GoalSchedule is a durable request to create a separate child task. It is
// intentionally distinct from PromptQueue: queued prompts become steering
// input on this task, while goals create a separately scheduled task from the
// immutable source state captured when the goal was queued.
type GoalSchedule struct {
	ID                     string           `json:"id"`
	Digest                 string           `json:"digest"`
	BudgetAllocation       BudgetAllocation `json:"budget_allocation,omitempty,omitzero"`
	BudgetAllocationDigest string           `json:"budget_allocation_digest,omitempty"`
	Bytes                  int64            `json:"bytes"`
	ParentTaskSeq          int64            `json:"parent_task_seq"`
	ParentDocumentDigest   string           `json:"parent_document_digest"`
	SourceSnapshotDigest   string           `json:"source_snapshot_digest"`
	ChildTaskID            string           `json:"child_task_id"`
	Role                   string           `json:"role,omitempty"`
	ReadWorkerLease        ReadWorkerLease  `json:"read_worker_lease,omitempty,omitzero"`
	// UNKNOWN is used after an active read-worker cancellation, including a
	// receipt-free ACTIVATING intent. It is a durable retained-allocation marker
	// because child creation may precede the activation receipt. Only a worker
	// that is still QUEUED can become refundable CANCELLED.
	Status              string `json:"status"`
	ActivationCommandID string `json:"activation_command_id,omitempty"`
	ChildTaskSeq        int64  `json:"child_task_seq,omitempty"`
	ChildDocumentDigest string `json:"child_document_digest,omitempty"`
}

// TaskWorkBudget is the parent task's authoritative local work pool. Limits
// are sealed when TaskCreated is reduced; counters are updated only by
// document-recording kernel events. Child goal allocations are reserved from
// this pool without duplicating the store-wide token/resource ledgers.
type TaskWorkBudget struct {
	Limits         BudgetAllocation `json:"limits"`
	Steps          int64            `json:"steps"`
	ToolCalls      int64            `json:"tool_calls"`
	UsedInput      int64            `json:"used_input"`
	UsedOutput     int64            `json:"used_output"`
	ReservedInput  int64            `json:"reserved_input"`
	ReservedOutput int64            `json:"reserved_output"`
	ActiveMillis   int64            `json:"active_millis"`
	EffectPending  bool             `json:"effect_pending,omitempty"`
}

type TaskState struct {
	Deletion                *TaskDeletion         `json:"content_deletion,omitempty"`
	WorkBudget              *TaskWorkBudget       `json:"work_budget,omitempty"`
	ProjectMemoryRevision   int64                 `json:"project_memory_revision,omitempty"`
	ProjectMemoryCopyCount  int64                 `json:"project_memory_copy_count,omitempty"`
	ReadWorkerAdmissions    []ReadWorkerAdmission `json:"read_worker_admissions,omitempty"`
	DeliveryEffects         []DeliveryEffect      `json:"delivery_effects,omitempty"`
	GoalQueue               []GoalSchedule        `json:"goal_queue,omitempty"`
	PromptQueue             []PromptRef           `json:"prompt_queue,omitempty"`
	Resources               *ResourceAccount      `json:"resource_account,omitempty"`
	Tokens                  *TokenAccount         `json:"token_account,omitempty"`
	DocumentDigest          string                `json:"document_digest,omitempty"`
	BaselineDigest          string                `json:"baseline_digest,omitempty"`
	CandidateDigest         string                `json:"candidate_digest,omitempty"`
	SchemaVersion           int                   `json:"schema_version"`
	TaskID                  string                `json:"task_id"`
	SpecVersion             int64                 `json:"spec_version"`
	Execution               ExecutionState        `json:"execution_state"`
	Outcome                 Outcome               `json:"terminal_outcome"`
	Quality                 Quality               `json:"quality_verdict"`
	Fulfillment             Fulfillment           `json:"fulfillment_status"`
	TaskSeq                 int64                 `json:"task_seq"`
	StoreSeq                int64                 `json:"journal_seq"`
	KernelGeneration        int64                 `json:"kernel_generation"`
	PolicyEpoch             int64                 `json:"policy_epoch"`
	InputBarrier            bool                  `json:"input_barrier"`
	PendingInputIDs         []string              `json:"pending_input_ids"`
	OpenRequiredObligations int                   `json:"open_required_obligations"`
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
	Deletion              *TaskDeletion             `json:"content_deletion,omitempty"`
	WorkBudget            *TaskWorkBudget           `json:"work_budget,omitempty"`
	ProjectMemoryMutation *ProjectMemoryMutation    `json:"project_memory_mutation,omitempty"`
	ReadWorkerAdmission   *ReadWorkerAdmission      `json:"read_worker_admission,omitempty"`
	DeliveryEffect        *DeliveryEffect           `json:"delivery_effect,omitempty"`
	Goal                  *GoalMutation             `json:"goal,omitempty"`
	Verification          *VerificationFinalization `json:"verification,omitempty"`
	QueueID               string                    `json:"queue_id,omitempty"`
	Resources             *ResourceMutation         `json:"resource_mutation,omitempty"`
	Tokens                *TokenMutation            `json:"token_mutation,omitempty"`
	CommandError          *Error                    `json:"command_error,omitempty"`
	DocumentDigest        string                    `json:"document_digest,omitempty"`
	SnapshotDigest        string                    `json:"snapshot_digest,omitempty"`
	InputDigest           string                    `json:"input_digest,omitempty"`
	InputBytes            int64                     `json:"input_bytes,omitempty"`
	InputID               string                    `json:"input_id,omitempty"`
	SpecVersion           int64                     `json:"spec_version,omitempty"`
	State                 ExecutionState            `json:"state,omitempty"`
	Outcome               Outcome                   `json:"outcome,omitempty"`
	Quality               Quality                   `json:"quality,omitempty"`
	Fulfillment           Fulfillment               `json:"fulfillment,omitempty"`
	PolicyEpoch           int64                     `json:"policy_epoch,omitempty"`
	RequiredObligations   int                       `json:"required_obligations,omitempty"`
	Reason                string                    `json:"reason,omitempty"`
}
type GoalMutation struct {
	ID                     string           `json:"id"`
	Digest                 string           `json:"digest,omitempty"`
	BudgetAllocation       BudgetAllocation `json:"budget_allocation,omitempty,omitzero"`
	BudgetAllocationDigest string           `json:"budget_allocation_digest,omitempty"`
	Bytes                  int64            `json:"bytes,omitempty"`
	ParentTaskSeq          int64            `json:"parent_task_seq,omitempty"`
	ParentDocumentDigest   string           `json:"parent_document_digest,omitempty"`
	SourceSnapshotDigest   string           `json:"source_snapshot_digest,omitempty"`
	ChildTaskID            string           `json:"child_task_id,omitempty"`
	Role                   string           `json:"role,omitempty"`
	ReadWorkerLease        ReadWorkerLease  `json:"read_worker_lease,omitempty,omitzero"`
	ActivationCommandID    string           `json:"activation_command_id,omitempty"`
	ChildTaskSeq           int64            `json:"child_task_seq,omitempty"`
	ChildDocumentDigest    string           `json:"child_document_digest,omitempty"`
	Reason                 string           `json:"reason,omitempty"`
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
