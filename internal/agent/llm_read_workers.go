package agent

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

// LLMReadWorkerCommand is the owner-facing command for the durable Explore /
// Review child path. The command is intentionally small: source, runtime,
// provider, budget and lease are all taken from the parent task.
type LLMReadWorkerCommand struct {
	CommandID   string `json:"command_id"`
	TaskID      string `json:"task_id"`
	Action      string `json:"action"`
	Prompt      string `json:"prompt,omitempty"`
	GoalID      string `json:"goal_id,omitempty"`
	ChildTaskID string `json:"child_task_id,omitempty"`
}

// ReadWorkerCommand is kept as a short public alias for CLI and integrations.
type ReadWorkerCommand = LLMReadWorkerCommand

const (
	ReadWorkerActionExplore = "explore"
	ReadWorkerActionReview  = "review"
	ReadWorkerActionStatus  = "status"
	ReadWorkerActionResult  = "result"
	ReadWorkerActionCancel  = "cancel"
)

type LLMReadWorkerResult struct {
	SchemaVersion          int               `json:"schema_version"`
	ParentTask             string            `json:"parent_task"`
	GoalID                 string            `json:"goal_id"`
	ChildTaskID            string            `json:"child_task_id"`
	Role                   string            `json:"role"`
	Status                 string            `json:"status"`
	Advisory               bool              `json:"advisory"`
	StaleSource            bool              `json:"stale_source"`
	SourceCandidateDigest  string            `json:"source_candidate_digest"`
	CurrentCandidateDigest string            `json:"current_candidate_digest"`
	SourceDocumentDigest   string            `json:"source_document_digest"`
	ChildTaskSeq           int64             `json:"child_task_seq"`
	ChildDocumentDigest    string            `json:"child_document_digest"`
	ResultDigest           string            `json:"result_digest,omitempty"`
	Summary                string            `json:"untrusted_model_summary,omitempty"`
	Trust                  string            `json:"trust"`
	Lease                  c.ReadWorkerLease `json:"lease"`
	Parent                 c.TaskState       `json:"parent_state"`
	Schedule               c.GoalSchedule    `json:"schedule"`
	Child                  *c.TaskState      `json:"child_state,omitempty"`
	Failure                *c.Error          `json:"failure,omitempty"`
}

// ReadWorkerLease returns the current typed lease for a parent-bound worker.
// It is useful to coordinators that need to display the exact admission
// without exposing any live source path or provider credential.
func (s *Session) ReadWorkerLease(ctx context.Context, task, goalID string) (c.ReadWorkerLease, error) {
	_, schedule, err := s.readWorkerSchedule(ctx, task, goalID)
	if err != nil {
		return c.ReadWorkerLease{}, err
	}
	if !c.ValidReadWorkerRole(schedule.Role) {
		return c.ReadWorkerLease{}, c.Fail(c.InvalidArgument, "goal is not an LLM read-worker schedule")
	}
	return schedule.ReadWorkerLease, nil
}

func readWorkerRoleForAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ReadWorkerActionExplore:
		return c.ReadWorkerRoleExplore
	case ReadWorkerActionReview:
		return c.ReadWorkerRoleReview
	default:
		return ""
	}
}

func readWorkerChildID(commandID string) string {
	return "read-worker-" + c.HashBytes([]byte(commandID))[:40]
}

func (s *Session) newReadWorkerLease(state c.TaskState, doc Document, role, child string, outputBudget int64) (c.ReadWorkerLease, error) {
	if !c.ValidReadWorkerRole(role) || state.TaskID == "" || child == "" || !c.ValidDigest(state.DocumentDigest) || !c.ValidDigest(doc.Candidate.SnapshotDigest) {
		return c.ReadWorkerLease{}, c.Fail(c.InvalidArgument, "read-worker lease requires a current parent candidate")
	}
	profile, err := readWorkerContextProfile(doc)
	if err != nil {
		return c.ReadWorkerLease{}, err
	}
	issued := time.Since(s.clockOrigin).Milliseconds()
	if issued < 0 {
		issued = 0
	}
	ttl := doc.Budget.MaxActiveMillis
	if ttl < 1000 {
		ttl = 1000
	}
	if ttl > c.ReadWorkerLeaseMaxTTLMillis {
		ttl = c.ReadWorkerLeaseMaxTTLMillis
	}
	lease := c.ReadWorkerLease{
		SchemaVersion: c.ReadWorkerLeaseSchemaVersion, ParentTask: state.TaskID,
		ParentTaskSeq: state.TaskSeq, ParentDocumentDigest: state.DocumentDigest,
		ParentCandidateDigest: doc.Candidate.SnapshotDigest, ChildTaskID: child,
		Role: role, SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch,
		KernelGeneration: state.KernelGeneration, ContextProfileDigest: profile,
		OutputBudget: outputBudget, ClockDomain: s.clockDomain, IssuedMillis: issued,
		DeadlineMillis: issued + ttl,
	}
	if err := lease.Validate(); err != nil {
		return c.ReadWorkerLease{}, err
	}
	return lease, nil
}

func readWorkerContextProfile(doc Document) (string, error) {
	profile := struct {
		Provider      string        `json:"provider"`
		Model         string        `json:"model"`
		ContextLimit  int64         `json:"context_limit"`
		OutputLimit   int64         `json:"output_limit"`
		RuntimeSchema int           `json:"runtime_schema"`
		Runtime       *LocalRuntime `json:"runtime,omitempty"`
	}{Provider: "fixture", RuntimeSchema: 1}
	if doc.Runtime != nil {
		profile.Provider = doc.Runtime.Provider
		profile.Model = doc.Runtime.Model
		profile.ContextLimit = doc.Runtime.ContextLimit
		profile.OutputLimit = doc.Runtime.OutputLimit
		profile.RuntimeSchema = doc.Runtime.SchemaVersion
		// Keep the complete declared runtime profile in the lease digest. This
		// binds endpoint, timeout, stream and operator credential/profile
		// selectors along with provider/model/context limits without retaining a
		// credential value itself.
		profile.Runtime = doc.Runtime
	}
	raw, err := c.CanonicalV1(profile)
	if err != nil {
		return "", err
	}
	return c.HashBytes(raw), nil
}

// ScheduleLLMReadWorker binds a worker to the parent's exact candidate and
// durable work pool. Activation remains a separate durable intent, performed by
// the owner coordinator after this admission returns.
func (s *Session) ScheduleLLMReadWorker(ctx context.Context, command LLMReadWorkerCommand) (c.TaskState, c.GoalSchedule, error) {
	if command.CommandID == "" || len(command.CommandID) > 128 || command.TaskID == "" || command.Action != ReadWorkerActionExplore && command.Action != ReadWorkerActionReview || command.GoalID != "" || !utf8.ValidString(command.Prompt) || strings.TrimSpace(command.Prompt) == "" || len(command.Prompt) > 64<<10 {
		return c.TaskState{}, c.GoalSchedule{}, c.Fail(c.InvalidArgument, "bounded read-worker command and prompt required")
	}
	role := readWorkerRoleForAction(command.Action)
	child := command.ChildTaskID
	if child == "" {
		child = readWorkerChildID(command.CommandID)
	}
	if child == command.TaskID || len(child) > 128 || strings.HasPrefix(child, "eval-") {
		return c.TaskState{}, c.GoalSchedule{}, c.Fail(c.InvalidArgument, "stable distinct read-worker child task ID required")
	}
	state, doc, err := s.Load(ctx, command.TaskID)
	if err != nil {
		return state, c.GoalSchedule{}, err
	}
	if state.Deletion != nil || state.InputBarrier || len(state.PendingInputIDs) != 0 || doc.Pending != nil || doc.UnknownEffect || state.Execution == c.Terminated && state.Outcome == c.Cancelled {
		return state, c.GoalSchedule{}, c.Fail(c.PolicyDenied, "read-worker parent is cancelled, deleted, blocked, or awaiting input")
	}
	queued, err := s.GoalControl(ctx, GoalCommand{CommandID: command.CommandID, TaskID: command.TaskID, Action: "add", ChildTaskID: child, Role: role, SourceSnapshotDigest: doc.Candidate.SnapshotDigest, Text: command.Prompt})
	if err != nil {
		return queued, c.GoalSchedule{}, err
	}
	schedule, found := goalItem(queued, command.CommandID)
	if !found {
		return queued, c.GoalSchedule{}, c.Fail(c.StoreIntegrityError, "read-worker schedule missing after durable admission")
	}
	return queued, schedule, nil
}

// StartReadWorker is a convenience wrapper for callers that already have a
// role and prompt but do not need to construct the wire command themselves.
func (s *Session) StartReadWorker(ctx context.Context, task, role, prompt, commandID, childTask string) (c.TaskState, c.GoalSchedule, error) {
	normalized := strings.ToUpper(strings.TrimSpace(role))
	var action string
	switch normalized {
	case c.ReadWorkerRoleExplore:
		action = ReadWorkerActionExplore
	case c.ReadWorkerRoleReview:
		action = ReadWorkerActionReview
	default:
		return c.TaskState{}, c.GoalSchedule{}, c.Fail(c.InvalidArgument, "read-worker role must be EXPLORE or REVIEW")
	}
	return s.ScheduleLLMReadWorker(ctx, LLMReadWorkerCommand{CommandID: commandID, TaskID: task, Action: action, Prompt: prompt, ChildTaskID: childTask})
}

func (s *Session) readWorkerSchedule(ctx context.Context, task, goalID string) (c.TaskState, c.GoalSchedule, error) {
	state, err := s.State(ctx, task)
	if err != nil {
		return state, c.GoalSchedule{}, err
	}
	var selected c.GoalSchedule
	found := false
	for _, item := range state.GoalQueue {
		if !c.ValidReadWorkerRole(item.Role) || goalID != "" && item.ID != goalID {
			continue
		}
		if goalID == "" || item.ID == goalID {
			selected, found = item, true
			if goalID != "" {
				break
			}
		}
	}
	if !found {
		return state, c.GoalSchedule{}, c.Fail(c.StaleRequest, "read-worker schedule is unavailable")
	}
	return state, selected, nil
}

func (s *Session) ReadWorkerResult(ctx context.Context, task, goalID string) (LLMReadWorkerResult, error) {
	parent, schedule, err := s.readWorkerSchedule(ctx, task, goalID)
	if err != nil {
		return LLMReadWorkerResult{}, err
	}
	result := LLMReadWorkerResult{SchemaVersion: 1, ParentTask: task, GoalID: schedule.ID, ChildTaskID: schedule.ChildTaskID, Role: schedule.Role, Status: strings.ToUpper(schedule.Status), Advisory: true, SourceCandidateDigest: schedule.SourceSnapshotDigest, SourceDocumentDigest: schedule.ParentDocumentDigest, Lease: schedule.ReadWorkerLease, Parent: parent, Schedule: schedule, Trust: "MODEL_AUTHORED_UNREVIEWED_ADVISORY"}
	staleLease := schedule.ReadWorkerLease.Validate() != nil || time.Since(s.clockOrigin).Milliseconds() >= schedule.ReadWorkerLease.DeadlineMillis || s.clockDomain != schedule.ReadWorkerLease.ClockDomain || parent.SpecVersion != schedule.ReadWorkerLease.SpecVersion || parent.PolicyEpoch != schedule.ReadWorkerLease.PolicyEpoch || parent.KernelGeneration != schedule.ReadWorkerLease.KernelGeneration || parent.InputBarrier || len(parent.PendingInputIDs) != 0 || parent.Deletion != nil || parent.Execution == c.Terminated && parent.Outcome == c.Cancelled
	if _, doc, loadErr := s.Load(ctx, task); loadErr == nil {
		result.CurrentCandidateDigest = doc.Candidate.SnapshotDigest
		result.StaleSource = result.CurrentCandidateDigest != schedule.SourceSnapshotDigest
		if err := s.validateReadWorkerParentCapture(ctx, schedule.ReadWorkerLease, parent, doc); err != nil {
			staleLease = true
		}
	} else {
		return result, loadErr
	}
	childState, childDoc, childErr := s.Load(ctx, schedule.ChildTaskID)
	if childErr != nil {
		// ACTIVATING is a durable intent whose child creation and activation
		// receipt may still be in flight. Preserve that inspectable state rather
		// than turning a successful admission into a transient CLI error.
		switch schedule.Status {
		case "QUEUED", "ACTIVATING", "CANCELLED":
			result.Status = schedule.Status
		case "UNKNOWN":
			result.Status = "UNKNOWN_OUTCOME"
		default:
			return result, childErr
		}
		return result, nil
	}
	result.ChildTaskSeq, result.ChildDocumentDigest = childState.TaskSeq, childState.DocumentDigest
	result.Child = &childState
	result.ResultDigest = childDoc.FinalArtifactDigest
	if !c.ValidDigest(result.ResultDigest) {
		result.ResultDigest = childDoc.LastResponseBlob
	}
	result.Summary = childDoc.FinalSummary
	if schedule.Status == "CANCELLED" {
		result.Status = "CANCELLED"
	} else if schedule.Status == "UNKNOWN" {
		// UNKNOWN is also the durable marker for cancellation after a child
		// receipt. Its allocation remains held because the child may have
		// consumed budget or the provider outcome may still be unresolved.
		result.Status = "UNKNOWN_OUTCOME"
	} else if result.StaleSource {
		result.Status = "STALE_SOURCE"
	} else if staleLease {
		result.Status = "STALE_LEASE"
	} else {
		result.Status = readWorkerChildStatus(childState, childDoc)
	}
	if err := readWorkerChildMatchesLease(childState, childDoc, schedule.ReadWorkerLease); err != nil {
		result.Status = "STALE_LEASE"
	}
	return result, nil
}

func readWorkerChildStatus(state c.TaskState, doc Document) string {
	if doc.UnknownEffect || doc.Pending != nil || state.Execution == c.Blocked {
		return "UNKNOWN_OUTCOME"
	}
	switch state.Execution {
	case c.Terminated:
		if state.Outcome == c.Cancelled {
			return "CANCELLED"
		}
		if doc.FinalArtifactDigest != "" || doc.FinalSummary != "" {
			return "COMPLETE"
		}
		return "TERMINATED"
	case c.Running:
		return "RUNNING"
	case c.WaitingUser:
		return "WAITING_USER"
	case c.Paused, c.Pausing, c.Recovering:
		return "PAUSED"
	default:
		return strings.ToUpper(string(state.Execution))
	}
}

// CancelReadWorker durably cancels the child invocation and records the parent
// outcome. Only a still-queued worker releases its tranche; every activation
// or unknown outcome remains UNKNOWN with its full allocation held, including
// a receipt-free ACTIVATING intent whose child may already have been created.
func (s *Session) CancelReadWorker(ctx context.Context, command LLMReadWorkerCommand) (c.TaskState, error) {
	if command.CommandID == "" || command.TaskID == "" || command.Action != ReadWorkerActionCancel || command.GoalID == "" || command.Prompt != "" || command.ChildTaskID != "" {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "read-worker cancellation requires a bound goal ID")
	}
	parent, item, err := s.readWorkerSchedule(ctx, command.TaskID, command.GoalID)
	if err != nil {
		return parent, err
	}
	if item.Status == "QUEUED" {
		return s.GoalControl(ctx, GoalCommand{CommandID: command.CommandID, TaskID: command.TaskID, Action: "cancel", GoalID: item.ID})
	}
	if item.Status == "CANCELLED" {
		return parent, nil
	}
	if item.Status == "UNKNOWN" {
		// UNKNOWN is the durable retained-allocation marker. A repeated
		// cancellation must not submit another journal mutation that could
		// accidentally turn an unresolved child into a refundable one.
		return parent, nil
	}
	child, childErr := s.State(ctx, item.ChildTaskID)
	if childErr != nil {
		if item.Status == "ACTIVATING" {
			// A missing child state does not prove that CreateGoalTask never
			// created a child; preserve the activation intent as UNKNOWN.
			return s.cancelReadWorkerSchedule(ctx, command, item)
		}
		return parent, childErr
	}
	if child.TaskID != item.ChildTaskID {
		if item.Status == "ACTIVATING" {
			return s.cancelReadWorkerSchedule(ctx, command, item)
		}
		return parent, c.Fail(c.StaleAuthority, "read-worker child task is unavailable")
	}
	if child.Execution == c.Terminated && child.Outcome == c.Finished {
		return parent, c.Fail(c.StaleRequest, "read-worker result is already complete")
	}
	if child.Execution != c.Terminated {
		if _, childErr = s.ControlsWithID(ctx, item.ChildTaskID, "cancel", command.CommandID+"-child"); childErr != nil {
			return parent, childErr
		}
	}
	return s.cancelReadWorkerSchedule(ctx, command, item)
}

func (s *Session) cancelReadWorkerSchedule(ctx context.Context, command LLMReadWorkerCommand, item c.GoalSchedule) (c.TaskState, error) {
	s.publicationMu.RLock()
	defer s.publicationMu.RUnlock()
	digest, err := c.Digest(GoalCommand{CommandID: command.CommandID, TaskID: command.TaskID, Action: ReadWorkerActionCancel, GoalID: item.ID})
	if err != nil {
		return c.TaskState{}, err
	}
	mutation := &c.GoalMutation{ID: item.ID, Role: item.Role, ChildTaskID: item.ChildTaskID, ActivationCommandID: item.ActivationCommandID, ChildTaskSeq: item.ChildTaskSeq, ChildDocumentDigest: item.ChildDocumentDigest, Reason: digest}
	for i := 0; i < 32; i++ {
		state, readErr := s.State(ctx, command.TaskID)
		if readErr != nil {
			return state, readErr
		}
		result, execErr := s.Journal.Execute(ctx, store.Command{ID: command.CommandID, TaskID: command.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "GoalCancelled", Payload: c.EventPayload{Goal: mutation}})
		if execErr == nil {
			return result, nil
		}
		var typed *c.Error
		if !errors.As(execErr, &typed) || typed.Code != c.Conflict && typed.Code != c.StaleBase {
			return result, execErr
		}
	}
	return c.TaskState{}, c.Fail(c.Conflict, "read-worker cancellation could not reach a journal boundary")
}

// validateReadWorkerParentCapture verifies the historical parent cursor sealed
// into the lease. The current parent document is intentionally not compared to
// ParentDocumentDigest: queue admission and activation legitimately append
// parent events and therefore change that current digest while the captured
// candidate remains the same.
func (s *Session) validateReadWorkerParentCapture(ctx context.Context, lease c.ReadWorkerLease, current c.TaskState, currentDoc Document) error {
	historical, err := s.Journal.ReplayAt(ctx, lease.ParentTask, lease.ParentTaskSeq)
	if err != nil {
		return err
	}
	if historical.TaskID != lease.ParentTask || historical.DocumentDigest != lease.ParentDocumentDigest || historical.SpecVersion != lease.SpecVersion || historical.PolicyEpoch != lease.PolicyEpoch || historical.KernelGeneration != lease.KernelGeneration || historical.CandidateDigest != lease.ParentCandidateDigest {
		return c.Fail(c.StaleAuthority, "read-worker lease no longer matches its historical parent authority")
	}
	if current.TaskID != lease.ParentTask || current.SpecVersion != lease.SpecVersion || current.PolicyEpoch != lease.PolicyEpoch || current.KernelGeneration != lease.KernelGeneration {
		return c.Fail(c.StaleAuthority, "read-worker parent authority changed")
	}
	profile, err := readWorkerContextProfile(currentDoc)
	if err != nil {
		return err
	}
	if profile != lease.ContextProfileDigest || currentDoc.TaskID != lease.ParentTask || currentDoc.Spec.TaskID != lease.ParentTask || currentDoc.Spec.Version != lease.SpecVersion || currentDoc.Candidate.SnapshotDigest != lease.ParentCandidateDigest {
		return c.Fail(c.StaleAuthority, "read-worker parent runtime or candidate profile changed")
	}
	raw, err := s.Archive.GetBytes(lease.ParentTask, lease.ParentDocumentDigest)
	if err != nil {
		return err
	}
	if c.HashBytes(raw) != lease.ParentDocumentDigest {
		return c.Fail(c.StoreIntegrityError, "read-worker historical parent document checksum changed")
	}
	var historicalDoc Document
	if err := c.DecodeStrict(raw, &historicalDoc); err != nil {
		return err
	}
	if historicalDoc.SchemaVersion != 1 || historicalDoc.TaskID != lease.ParentTask || historicalDoc.Spec.TaskID != lease.ParentTask || historicalDoc.Spec.Version != lease.SpecVersion || historicalDoc.Candidate.SnapshotDigest != lease.ParentCandidateDigest {
		return c.Fail(c.StaleAuthority, "read-worker historical parent capture changed")
	}
	historicalProfile, err := readWorkerContextProfile(historicalDoc)
	if err != nil {
		return err
	}
	if historicalProfile != lease.ContextProfileDigest {
		return c.Fail(c.StaleAuthority, "read-worker historical parent runtime profile changed")
	}
	return nil
}

// readWorkerChildMatchesLease checks the child-owned part of a lease. Child
// tasks are independent documents and begin at their own version-one spec and
// policy epoch; comparing them to the parent's versions would reject a valid
// worker whenever the parent had been revised before scheduling.
func readWorkerChildMatchesLease(state c.TaskState, doc Document, lease c.ReadWorkerLease) error {
	if err := lease.Validate(); err != nil || state.TaskID != lease.ChildTaskID || state.SpecVersion != 1 || state.PolicyEpoch != 1 || state.KernelGeneration != lease.KernelGeneration || doc.TaskID != lease.ChildTaskID || doc.Spec.TaskID != lease.ChildTaskID || doc.Spec.Version != 1 || doc.Budget.MaxOutputTokens != lease.OutputBudget {
		return c.Fail(c.StaleAuthority, "read-worker child no longer matches its independent lease context")
	}
	profile, err := readWorkerContextProfile(doc)
	if err != nil {
		return err
	}
	if profile != lease.ContextProfileDigest {
		return c.Fail(c.StaleAuthority, "read-worker child runtime profile changed")
	}
	return nil
}

// validateReadWorkerLease is called by the runtime/model and native-tool
// admission hooks. It deliberately reloads the parent authority each time;
// cached child summaries never grant a new model or tool permission.
func (s *Session) validateReadWorkerLease(ctx context.Context, state c.TaskState, doc Document) error {
	if doc.GoalOrigin == nil || !c.ValidReadWorkerRole(doc.GoalOrigin.Role) {
		return nil
	}
	origin := doc.GoalOrigin
	lease := origin.ReadWorkerLease
	if err := lease.Validate(); err != nil || origin.ChildTaskID != doc.TaskID || doc.Baseline.SnapshotDigest != lease.ParentCandidateDigest || state.SpecVersion != 1 || state.PolicyEpoch != 1 || state.KernelGeneration != lease.KernelGeneration || doc.Spec.Version != 1 || doc.Budget.MaxOutputTokens != lease.OutputBudget || s.Journal.Generation() != lease.KernelGeneration || s.clockDomain != lease.ClockDomain {
		return c.Fail(c.StaleAuthority, "read-worker lease is stale for the current child owner")
	}
	childProfile, err := readWorkerContextProfile(doc)
	if err != nil || childProfile != lease.ContextProfileDigest {
		return c.Fail(c.StaleAuthority, "read-worker child runtime profile changed")
	}
	if time.Since(s.clockOrigin).Milliseconds() >= lease.DeadlineMillis {
		return c.Fail(c.StaleAuthority, "read-worker lease expired")
	}
	parent, parentDoc, err := s.Load(ctx, lease.ParentTask)
	if err != nil {
		return err
	}
	if err = s.validateReadWorkerParentCapture(ctx, lease, parent, parentDoc); err != nil {
		return err
	}
	goalActive := false
	for _, item := range parent.GoalQueue {
		if item.ID == origin.GoalID && item.ChildTaskID == doc.TaskID {
			goalActive = item.Status == "ACTIVATING" || item.Status == "ACTIVATED" || item.Status == "UNKNOWN"
			break
		}
	}
	parentProfile, profileErr := readWorkerContextProfile(parentDoc)
	if profileErr != nil || parentProfile != lease.ContextProfileDigest {
		return c.Fail(c.StaleAuthority, "read-worker parent runtime profile changed")
	}
	if parent.Deletion != nil || parent.InputBarrier || len(parent.PendingInputIDs) != 0 || parent.CandidateDigest != lease.ParentCandidateDigest || parentDoc.Candidate.SnapshotDigest != lease.ParentCandidateDigest || parentDoc.Pending != nil || parentDoc.UnknownEffect || parent.Execution == c.Terminated && parent.Outcome == c.Cancelled || !goalActive {
		return c.Fail(c.StaleAuthority, "read-worker parent authority changed")
	}
	return nil
}

// ReadWorkerAdmission exposes the same dynamic lease fence used by model and
// native-tool admission. Owners call it immediately before starting a child;
// it does not mutate either task.
func (s *Session) ReadWorkerAdmission(ctx context.Context, task string) error {
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return err
	}
	return s.validateReadWorkerLease(ctx, state, doc)
}
