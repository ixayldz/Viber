package agent

import (
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/store"
	"strings"
	"unicode/utf8"
)

type QueueCommand struct {
	CommandID string `json:"command_id"`
	TaskID    string `json:"task_id"`
	Action    string `json:"action"`
	QueueID   string `json:"queue_id,omitempty"`
	Text      string `json:"text,omitempty"`
}
type QueuedPrompt struct {
	Ref  c.PromptRef `json:"ref"`
	Text string      `json:"text"`
}

type GoalCommand struct {
	CommandID            string `json:"command_id"`
	TaskID               string `json:"task_id"`
	Action               string `json:"action"`
	GoalID               string `json:"goal_id,omitempty"`
	ChildTaskID          string `json:"child_task_id,omitempty"`
	Role                 string `json:"role,omitempty"`
	SourceSnapshotDigest string `json:"source_snapshot_digest,omitempty"`
	Text                 string `json:"text,omitempty"`
}

type ScheduledGoal struct {
	Schedule c.GoalSchedule `json:"schedule"`
	Text     string         `json:"text"`
}

// GoalOrigin binds a created child task to the durable schedule that admitted
// it. This is checked whenever the child document is loaded, including after
// restart, so a reused child ID cannot silently dispatch a different goal.
type GoalOrigin struct {
	ParentTask             string             `json:"parent_task"`
	ParentTaskSeq          int64              `json:"parent_task_seq"`
	ParentDocumentDigest   string             `json:"parent_document_digest"`
	GoalID                 string             `json:"goal_id"`
	GoalDigest             string             `json:"goal_digest"`
	BudgetAllocation       c.BudgetAllocation `json:"budget_allocation,omitempty,omitzero"`
	BudgetAllocationDigest string             `json:"budget_allocation_digest,omitempty"`
	SourceSnapshotDigest   string             `json:"source_snapshot_digest"`
	ChildTaskID            string             `json:"child_task_id"`
	Role                   string             `json:"role,omitempty"`
	ReadWorkerLease        c.ReadWorkerLease  `json:"read_worker_lease,omitempty,omitzero"`
}

func (s *Session) queueFromState(state c.TaskState) ([]QueuedPrompt, error) {
	if err := s.contentAvailable(state.TaskID); err != nil {
		return nil, err
	}
	result := []QueuedPrompt{}
	if len(state.PromptQueue) > 64 {
		return nil, c.Fail(c.StoreIntegrityError, "prompt queue quota exceeded")
	}
	seen := map[string]bool{}
	for _, ref := range state.PromptQueue {
		if ref.ID == "" || seen[ref.ID] || !c.ValidDigest(ref.Digest) || ref.Bytes < 1 || ref.Bytes > 64<<10 {
			return nil, c.Fail(c.StoreIntegrityError, "invalid queued prompt binding")
		}
		raw, err := s.Archive.GetBytes(state.TaskID, ref.Digest)
		if err != nil || int64(len(raw)) != ref.Bytes || !utf8.Valid(raw) {
			return nil, c.Fail(c.StoreIntegrityError, "queued prompt payload unavailable")
		}
		seen[ref.ID] = true
		result = append(result, QueuedPrompt{ref, string(raw)})
	}
	return result, nil
}
func (s *Session) PromptQueue(ctx context.Context, task string) ([]QueuedPrompt, error) {
	state, err := s.State(ctx, task)
	if err != nil {
		return nil, err
	}
	return s.queueFromState(state)
}

// QueueControl uses journal optimistic admission, not the work-loop mutex.
// Queueing cannot alter the running spec, candidate, policy or input barrier.
func (s *Session) QueueControl(ctx context.Context, command QueueCommand) (c.TaskState, error) {
	s.publicationMu.RLock()
	defer s.publicationMu.RUnlock()
	if command.CommandID == "" || len(command.CommandID) > 128 || command.TaskID == "" ||
		(command.Action != "add" && command.Action != "remove") ||
		command.Action == "add" && (command.QueueID != "" || len(command.Text) < 1 || len(command.Text) > 64<<10 || !utf8.ValidString(command.Text)) ||
		command.Action == "remove" && (command.QueueID == "" || command.Text != "") {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "bounded queue command required")
	}
	if err := s.contentAvailable(command.TaskID); err != nil {
		return c.TaskState{}, err
	}
	digest, _ := c.Digest(command)
	kind := "PromptQueued"
	if command.Action == "remove" {
		kind = "PromptQueueRemoved"
	}
	prior, event, payload, found, err := s.Journal.CommandReceipt(ctx, command.CommandID)
	if err != nil {
		return prior, err
	}
	if found {
		if event.TaskID != command.TaskID || event.Actor != "user" || event.Type != kind || payload.Reason != digest {
			return prior, c.Fail(c.CommandIDConflict, "queue command ID conflict")
		}
		return prior, nil
	}
	current, err := s.State(ctx, command.TaskID)
	if err != nil {
		return current, err
	}
	if current.Deletion != nil || current.Execution == c.Terminated {
		return current, c.Fail(c.PolicyDenied, "terminal or deleted task cannot publish new queue content")
	}
	extra := c.EventPayload{InputID: command.QueueID, Reason: digest}
	if command.Action == "add" {
		blob, err := s.Archive.PutBytes(command.TaskID, []byte(command.Text))
		if err != nil {
			return prior, err
		}
		extra.InputID = command.CommandID
		extra.InputDigest = blob
		extra.InputBytes = int64(len(command.Text))
	}
	for i := 0; i < 32; i++ {
		state, err := s.State(ctx, command.TaskID)
		if err != nil {
			return state, err
		}
		if state.KernelGeneration != s.Journal.Generation() {
			state, err = s.recover(ctx, state)
			if err != nil {
				return state, err
			}
		}
		result, err := s.Journal.Execute(ctx, store.Command{ID: command.CommandID, TaskID: command.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: kind, Payload: extra})
		if err == nil {
			return result, nil
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.Conflict && typed.Code != c.StaleBase {
			return result, err
		}
	}
	return c.TaskState{}, c.Fail(c.Conflict, "queue admission could not reach a journal boundary")
}

func queueIndependentState(state c.TaskState) string {
	state.PromptQueue = nil
	// Bind the held pool, but normalize activation status and receipts: QUEUED,
	// ACTIVATING, UNKNOWN and ACTIVATED all reserve the same immutable tranche.
	// Cancellation removes only an unactivated hold, so it changes this digest.
	held := make([]c.GoalSchedule, 0, len(state.GoalQueue))
	for _, item := range state.GoalQueue {
		if item.Status == "CANCELLED" {
			continue
		}
		item.Status = "HELD"
		item.ActivationCommandID = ""
		item.ChildTaskSeq = 0
		item.ChildDocumentDigest = ""
		held = append(held, item)
	}
	state.GoalQueue = held
	state.TaskSeq = 0
	state.StoreSeq = 0
	digest, _ := c.Digest(state)
	return digest
}

// A concurrent enqueue changes only cursor/queue metadata. This narrow retry
// cannot cross steering barriers, resource settlements or candidate/spec changes.
func (s *Session) executeAroundQueue(ctx context.Context, state c.TaskState, command store.Command) (c.TaskState, error) {
	binding := queueIndependentState(state)
	for i := 0; i < 32; i++ {
		result, err := s.Journal.Execute(ctx, command)
		if err == nil {
			return result, nil
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StaleBase {
			return result, err
		}
		current, readErr := s.State(ctx, state.TaskID)
		if readErr != nil {
			return current, readErr
		}
		if queueIndependentState(current) != binding {
			return result, err
		}
		command.ExpectedTaskSeq = current.TaskSeq
	}
	return state, c.Fail(c.Conflict, "queue traffic exceeded bounded publication retries")
}

func goalItem(state c.TaskState, id string) (c.GoalSchedule, bool) {
	for _, item := range state.GoalQueue {
		if item.ID == id {
			return item, true
		}
	}
	return c.GoalSchedule{}, false
}

func validGoalItem(item c.GoalSchedule) bool {
	legacyAllocation := item.BudgetAllocation == (c.BudgetAllocation{}) && item.BudgetAllocationDigest == ""
	allocationValid := legacyAllocation
	if !legacyAllocation && item.BudgetAllocation.Validate() == nil && c.ValidDigest(item.BudgetAllocationDigest) {
		digest, err := item.BudgetAllocation.Digest()
		allocationValid = err == nil && digest == item.BudgetAllocationDigest
	}
	workerBindingValid := item.Role == "" && item.ReadWorkerLease == (c.ReadWorkerLease{}) || c.ValidReadWorkerRole(item.Role) && item.ReadWorkerLease.Validate() == nil && item.ReadWorkerLease.Role == item.Role && item.ReadWorkerLease.ParentTaskSeq == item.ParentTaskSeq && item.ReadWorkerLease.ParentDocumentDigest == item.ParentDocumentDigest && item.ReadWorkerLease.ParentCandidateDigest == item.SourceSnapshotDigest && item.ReadWorkerLease.ChildTaskID == item.ChildTaskID && item.ReadWorkerLease.OutputBudget == item.BudgetAllocation.MaxOutputTokens
	return allocationValid && workerBindingValid && item.ID != "" && len(item.ID) <= 128 && c.ValidDigest(item.Digest) && item.Bytes > 0 && item.Bytes <= 64<<10 &&
		item.ParentTaskSeq > 0 && c.ValidDigest(item.ParentDocumentDigest) && c.ValidDigest(item.SourceSnapshotDigest) &&
		item.ChildTaskID != "" && len(item.ChildTaskID) <= 128 &&
		(item.Status == "QUEUED" && item.ActivationCommandID == "" && item.ChildTaskSeq == 0 && item.ChildDocumentDigest == "" ||
			item.Status == "ACTIVATING" && item.ActivationCommandID != "" && item.ChildTaskSeq == 0 && item.ChildDocumentDigest == "" ||
			item.Status == "UNKNOWN" && item.ActivationCommandID != "" && (item.ChildTaskSeq == 0 && item.ChildDocumentDigest == "" || item.ChildTaskSeq > 0 && c.ValidDigest(item.ChildDocumentDigest)) ||
			item.Status == "ACTIVATED" && item.ActivationCommandID != "" && item.ChildTaskSeq > 0 && c.ValidDigest(item.ChildDocumentDigest) ||
			item.Status == "CANCELLED" && item.ActivationCommandID == "" && item.ChildTaskSeq == 0 && item.ChildDocumentDigest == "")
}

func (s *Session) GoalQueue(ctx context.Context, task string) ([]ScheduledGoal, error) {
	state, err := s.State(ctx, task)
	if err != nil {
		return nil, err
	}
	if len(state.GoalQueue) > 64 {
		return nil, c.Fail(c.StoreIntegrityError, "goal queue quota exceeded")
	}
	result := make([]ScheduledGoal, 0, len(state.GoalQueue))
	seen := map[string]bool{}
	for _, item := range state.GoalQueue {
		if !validGoalItem(item) || seen[item.ID] {
			return nil, c.Fail(c.StoreIntegrityError, "invalid durable goal queue entry")
		}
		seen[item.ID] = true
		raw, err := s.Archive.GetBytes(task, item.Digest)
		if err != nil || int64(len(raw)) != item.Bytes || !utf8.Valid(raw) {
			return nil, c.Fail(c.StoreIntegrityError, "scheduled goal payload unavailable")
		}
		result = append(result, ScheduledGoal{Schedule: item, Text: string(raw)})
	}
	return result, nil
}

// GoalControl persists a separate child-task schedule. Unlike QueueControl,
// this never writes steering input to the current task.
func (s *Session) GoalControl(ctx context.Context, command GoalCommand) (c.TaskState, error) {
	s.publicationMu.RLock()
	defer s.publicationMu.RUnlock()
	if command.CommandID == "" || len(command.CommandID) > 128 || command.TaskID == "" {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "bound goal queue command required")
	}
	digest, err := c.Digest(command)
	if err != nil {
		return c.TaskState{}, err
	}
	if command.Action == "add" {
		if command.GoalID != "" || command.ChildTaskID == "" || len(command.ChildTaskID) > 128 || command.ChildTaskID == command.TaskID || strings.HasPrefix(command.ChildTaskID, "eval-") || len(command.Text) < 1 || len(command.Text) > 64<<10 || !utf8.ValidString(command.Text) || command.Role != "" && !c.ValidReadWorkerRole(command.Role) || command.Role == "" && command.SourceSnapshotDigest != "" || command.Role != "" && !c.ValidDigest(command.SourceSnapshotDigest) {
			return c.TaskState{}, c.Fail(c.InvalidArgument, "bounded new-goal text and stable child task ID required")
		}
	} else if command.Action == "cancel" {
		if command.GoalID == "" || command.ChildTaskID != "" || command.Text != "" || command.Role != "" || command.SourceSnapshotDigest != "" {
			return c.TaskState{}, c.Fail(c.InvalidArgument, "goal cancellation needs only a queued goal ID")
		}
	} else {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "goal action must be add or cancel")
	}
	prior, event, payload, found, err := s.Journal.CommandReceipt(ctx, command.CommandID)
	if err != nil {
		return prior, err
	}
	if found {
		expectedType := "GoalQueued"
		if command.Action == "cancel" {
			expectedType = "GoalCancelled"
		}
		g := payload.Goal
		legacyAllocation := g != nil && g.BudgetAllocation == (c.BudgetAllocation{}) && g.BudgetAllocationDigest == ""
		allocationReceiptValid := legacyAllocation || g != nil && g.BudgetAllocation.Validate() == nil && c.ValidDigest(g.BudgetAllocationDigest) && allocationDigestMatches(g.BudgetAllocation, g.BudgetAllocationDigest)
		if event.TaskID != command.TaskID || event.Actor != "user" || event.Type != expectedType || g == nil || g.Reason != digest ||
			command.Action == "add" && (g.ID != command.CommandID || g.Digest != c.HashBytes([]byte(command.Text)) || g.Bytes != int64(len(command.Text)) || g.ChildTaskID != command.ChildTaskID || g.Role != command.Role || g.SourceSnapshotDigest != command.SourceSnapshotDigest || !allocationReceiptValid) ||
			command.Action == "cancel" && g.ID != command.GoalID {
			return prior, c.Fail(c.CommandIDConflict, "goal command ID reused with different input")
		}
		return prior, nil
	}
	if command.Action == "add" {
		state, doc, err := s.Load(ctx, command.TaskID)
		if err != nil {
			return state, err
		}
		if state.Deletion != nil {
			return state, c.Fail(c.PolicyDenied, "deleted task cannot schedule new goals")
		}
		if state.WorkBudget == nil {
			return state, c.Fail(c.UnsupportedCapability, "legacy task has no sealed parent work budget pool for child allocations")
		}
		if doc.Pending != nil || doc.UnknownEffect {
			return state, c.Fail(c.BudgetLimitReached, "a pending or unknown parent effect reserves the remaining child work window")
		}
		if _, exists := goalItem(state, command.CommandID); exists {
			return state, c.Fail(c.CommandIDConflict, "goal ID already exists")
		}
		for _, item := range state.GoalQueue {
			if item.ChildTaskID == command.ChildTaskID {
				return state, c.Fail(c.CommandIDConflict, "child task ID already scheduled")
			}
		}
		sourceSnapshot := doc.Baseline.SnapshotDigest
		if c.ValidReadWorkerRole(command.Role) {
			sourceSnapshot = doc.Candidate.SnapshotDigest
			if command.SourceSnapshotDigest != sourceSnapshot {
				return state, c.Fail(c.StaleBase, "read-worker source candidate changed before durable admission")
			}
		}
		allocation, err := allocateGoalBudget(doc.Budget, state.GoalQueue, false)
		if err != nil {
			return state, err
		}
		if doc.Runtime != nil && doc.Runtime.Provider == "chatgpt" {
			headroom, headroomErr := budgetHeadroom(doc.Budget, state.GoalQueue, false)
			if headroomErr != nil {
				return state, headroomErr
			}
			if allocation.MaxOutputTokens < model.ChatGPTOutputCeiling || headroom.MaxOutputTokens-allocation.MaxOutputTokens < model.ChatGPTOutputCeiling {
				return state, c.Fail(c.BudgetLimitReached, "ChatGPT requires its full published output ceiling; parent and child reservations cannot shrink it")
			}
		}
		allocationDigest, err := allocation.Digest()
		if err != nil {
			return state, err
		}
		blob, err := s.Archive.PutBytes(command.TaskID, []byte(command.Text))
		if err != nil {
			return state, err
		}
		lease := c.ReadWorkerLease{}
		if c.ValidReadWorkerRole(command.Role) {
			lease, err = s.newReadWorkerLease(state, doc, command.Role, command.ChildTaskID, allocation.MaxOutputTokens)
			if err != nil {
				return state, err
			}
		}
		mutation := &c.GoalMutation{ID: command.CommandID, Digest: blob, BudgetAllocation: allocation, BudgetAllocationDigest: allocationDigest, Bytes: int64(len(command.Text)), ParentTaskSeq: state.TaskSeq, ParentDocumentDigest: state.DocumentDigest, SourceSnapshotDigest: sourceSnapshot, ChildTaskID: command.ChildTaskID, Role: command.Role, ReadWorkerLease: lease, Reason: digest}
		return s.executeAroundQueue(ctx, state, store.Command{ID: command.CommandID, TaskID: command.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "GoalQueued", Payload: c.EventPayload{Goal: mutation}})
	}
	state, err := s.State(ctx, command.TaskID)
	if err != nil {
		return state, err
	}
	item, exists := goalItem(state, command.GoalID)
	if !exists || item.Status != "QUEUED" {
		return state, c.Fail(c.StaleRequest, "only a queued goal can be cancelled")
	}
	mutation := &c.GoalMutation{ID: command.GoalID, Reason: digest}
	return s.executeAroundQueue(ctx, state, store.Command{ID: command.CommandID, TaskID: command.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "GoalCancelled", Payload: c.EventPayload{Goal: mutation}})
}

func (s *Session) BeginGoalActivation(ctx context.Context, command GoalCommand) (c.TaskState, c.GoalSchedule, error) {
	if command.TaskID == "" || command.GoalID == "" || command.CommandID == "" || len(command.CommandID) > 128 || command.Action != "activate" && command.Action != "recover" || command.Text != "" || command.ChildTaskID != "" {
		return c.TaskState{}, c.GoalSchedule{}, c.Fail(c.InvalidArgument, "bound goal activation or recovery command required")
	}
	state, err := s.State(ctx, command.TaskID)
	if err != nil {
		return state, c.GoalSchedule{}, err
	}
	item, found := goalItem(state, command.GoalID)
	if !found {
		return state, c.GoalSchedule{}, c.Fail(c.StaleRequest, "scheduled goal unavailable")
	}
	if item.Status == "UNKNOWN" && command.Action == "activate" {
		return state, c.GoalSchedule{}, c.Fail(c.StaleRequest, "unknown goal activation requires explicit recovery")
	}
	if item.Status == "QUEUED" && (item.BudgetAllocation.Validate() != nil || !c.ValidDigest(item.BudgetAllocationDigest) || !allocationDigestMatches(item.BudgetAllocation, item.BudgetAllocationDigest)) {
		return state, c.GoalSchedule{}, c.Fail(c.UnsupportedCapability, "legacy goal has no fixed child budget allocation and cannot activate")
	}
	if item.Status == "ACTIVATING" || item.Status == "ACTIVATED" || item.Status == "UNKNOWN" {
		if command.Action == "activate" {
			prior, event, payload, receipt, receiptErr := s.Journal.CommandReceipt(ctx, command.CommandID)
			if receiptErr != nil {
				return state, c.GoalSchedule{}, receiptErr
			}
			if receipt && (event.TaskID != command.TaskID || event.Actor != "user" || event.Type != "GoalActivationIntent" || payload.Goal == nil || payload.Goal.ID != command.GoalID || payload.Goal.Reason != goalCommandDigest(command)) {
				return prior, c.GoalSchedule{}, c.Fail(c.CommandIDConflict, "goal activation command ID conflict")
			}
		}
		return state, item, nil
	}
	if command.Action == "recover" || item.Status != "QUEUED" {
		return state, c.GoalSchedule{}, c.Fail(c.StaleRequest, "goal is not awaiting activation recovery")
	}
	digest := goalCommandDigest(command)
	prior, event, payload, receipt, err := s.Journal.CommandReceipt(ctx, command.CommandID)
	if err != nil {
		return state, c.GoalSchedule{}, err
	}
	if receipt {
		if event.TaskID != command.TaskID || event.Actor != "user" || event.Type != "GoalActivationIntent" || payload.Goal == nil || payload.Goal.ID != command.GoalID || payload.Goal.Reason != digest {
			return prior, c.GoalSchedule{}, c.Fail(c.CommandIDConflict, "goal activation command ID conflict")
		}
		state, err = s.State(ctx, command.TaskID)
		if err != nil {
			return state, c.GoalSchedule{}, err
		}
		item, _ = goalItem(state, command.GoalID)
		return state, item, nil
	}
	state, err = s.executeAroundQueue(ctx, state, store.Command{ID: command.CommandID, TaskID: command.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "GoalActivationIntent", Payload: c.EventPayload{Goal: &c.GoalMutation{ID: command.GoalID, ActivationCommandID: command.CommandID, Reason: digest}}})
	if err != nil {
		return state, c.GoalSchedule{}, err
	}
	item, found = goalItem(state, command.GoalID)
	if !found {
		return state, c.GoalSchedule{}, c.Fail(c.StoreIntegrityError, "activation intent lost its scheduled goal")
	}
	return state, item, nil
}

func goalCommandDigest(command GoalCommand) string {
	digest, _ := c.Digest(command)
	return digest
}

func goalActivationReceiptID(parent, goal string) string {
	digest := c.HashBytes([]byte(parent + "\x00" + goal))
	return "goal-activated-" + digest[:40]
}

func (s *Session) CommitGoalActivation(ctx context.Context, parent string, item c.GoalSchedule, child c.TaskState) (c.TaskState, error) {
	if !validGoalItem(item) || item.Status != "ACTIVATING" && item.Status != "ACTIVATED" && item.Status != "UNKNOWN" || child.TaskID != item.ChildTaskID || child.TaskSeq < 1 || !c.ValidDigest(child.DocumentDigest) {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "verified child activation receipt required")
	}
	id := goalActivationReceiptID(parent, item.ID)
	mutation := &c.GoalMutation{ID: item.ID, ChildTaskID: item.ChildTaskID, ActivationCommandID: item.ActivationCommandID, ChildTaskSeq: child.TaskSeq, ChildDocumentDigest: child.DocumentDigest}
	prior, event, payload, found, err := s.Journal.CommandReceipt(ctx, id)
	if err != nil {
		return prior, err
	}
	if found {
		g := payload.Goal
		if event.TaskID != parent || event.Actor != "kernel" || event.Type != "GoalActivated" || g == nil || g.ID != item.ID || g.ChildTaskID != item.ChildTaskID || g.ActivationCommandID != item.ActivationCommandID || g.ChildTaskSeq < 1 || !c.ValidDigest(g.ChildDocumentDigest) {
			return prior, c.Fail(c.StoreIntegrityError, "child activation receipt conflicts with durable goal")
		}
		childAtReceipt, replayErr := s.Journal.ReplayAt(ctx, item.ChildTaskID, g.ChildTaskSeq)
		if replayErr != nil || childAtReceipt.DocumentDigest != g.ChildDocumentDigest {
			return prior, c.Fail(c.StoreIntegrityError, "activated child no longer matches its receipt")
		}
		return s.State(ctx, parent)
	}
	if item.Status != "ACTIVATING" && item.Status != "UNKNOWN" {
		return prior, c.Fail(c.StoreIntegrityError, "activated goal is missing its durable child receipt")
	}
	state, err := s.State(ctx, parent)
	if err != nil {
		return state, err
	}
	current, ok := goalItem(state, item.ID)
	if !ok || current.Status != "ACTIVATING" && current.Status != "UNKNOWN" || current.ChildTaskID != child.TaskID || current.ActivationCommandID != item.ActivationCommandID {
		return state, c.Fail(c.StaleRequest, "goal activation intent changed before child receipt")
	}
	return s.executeAroundQueue(ctx, state, store.Command{ID: id, TaskID: parent, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "GoalActivated", Payload: c.EventPayload{Goal: mutation}})
}

func (s *Session) CreateGoalTask(ctx context.Context, parent string, item c.GoalSchedule) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if parent == "" || !validGoalItem(item) || item.Status != "ACTIVATING" && item.Status != "ACTIVATED" && item.Status != "UNKNOWN" {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "recoverable durable goal activation required")
	}
	if item.BudgetAllocation == (c.BudgetAllocation{}) && item.BudgetAllocationDigest == "" {
		return c.TaskState{}, c.Fail(c.UnsupportedCapability, "legacy scheduled goal has no fixed child budget; activation is read-only")
	}
	origin := GoalOrigin{ParentTask: parent, ParentTaskSeq: item.ParentTaskSeq, ParentDocumentDigest: item.ParentDocumentDigest, GoalID: item.ID, GoalDigest: item.Digest, BudgetAllocation: item.BudgetAllocation, BudgetAllocationDigest: item.BudgetAllocationDigest, SourceSnapshotDigest: item.SourceSnapshotDigest, ChildTaskID: item.ChildTaskID, Role: item.Role, ReadWorkerLease: item.ReadWorkerLease}
	if err := origin.validate(); err != nil {
		return c.TaskState{}, err
	}
	parentAtOrigin, err := s.Journal.ReplayAt(ctx, parent, item.ParentTaskSeq)
	if err != nil || parentAtOrigin.TaskID != parent || parentAtOrigin.DocumentDigest != item.ParentDocumentDigest || parentAtOrigin.Deletion != nil {
		return c.TaskState{}, c.Fail(c.StaleAuthority, "scheduled parent snapshot is unavailable or changed")
	}
	var parentDoc Document
	parentAtOrigin, parentDoc, err = s.loadDocument(parentAtOrigin)
	if err != nil {
		return parentAtOrigin, err
	}
	if item.Role == "" && parentDoc.Baseline.SnapshotDigest != item.SourceSnapshotDigest {
		return parentAtOrigin, c.Fail(c.StaleAuthority, "scheduled source snapshot changed")
	}
	if c.ValidReadWorkerRole(item.Role) && parentDoc.Candidate.SnapshotDigest != item.SourceSnapshotDigest {
		return parentAtOrigin, c.Fail(c.StaleAuthority, "scheduled read-worker candidate capture is unavailable")
	}
	currentParent, err := s.Journal.Replay(ctx, parent)
	if err != nil {
		return parentAtOrigin, err
	}
	currentItem, found := goalItem(currentParent[parent], item.ID)
	if !found || currentItem.Status != "ACTIVATING" && currentItem.Status != "ACTIVATED" && currentItem.Status != "UNKNOWN" || currentItem.ChildTaskID != item.ChildTaskID || currentItem.Digest != item.Digest || currentItem.ParentDocumentDigest != item.ParentDocumentDigest || currentItem.SourceSnapshotDigest != item.SourceSnapshotDigest || currentItem.Role != item.Role || currentItem.ReadWorkerLease != item.ReadWorkerLease || currentItem.BudgetAllocation != item.BudgetAllocation || currentItem.BudgetAllocationDigest != item.BudgetAllocationDigest {
		return parentAtOrigin, c.Fail(c.StaleAuthority, "goal no longer has a matching durable activation intent")
	}
	goalRaw, err := s.Archive.GetBytes(parent, item.Digest)
	if err != nil || int64(len(goalRaw)) != item.Bytes || !utf8.Valid(goalRaw) {
		return parentAtOrigin, c.Fail(c.StoreIntegrityError, "scheduled goal payload unavailable")
	}
	// The child inherits frozen parent metadata/source references. Bind that
	// derivation before its first private copy, including interrupted retries.
	originDigest, err := c.Digest(origin)
	if err != nil {
		return parentAtOrigin, err
	}
	if err = s.registerAttemptLineage(ctx, AttemptOptions{ParentTask: parent, NewTask: item.ChildTaskID}, parentAtOrigin, originDigest); err != nil {
		return parentAtOrigin, err
	}
	if states, replayErr := s.Journal.Replay(ctx, ""); replayErr != nil {
		return parentAtOrigin, replayErr
	} else if existing, exists := states[item.ChildTaskID]; exists {
		child, childDoc, loadErr := s.loadDocument(existing)
		if loadErr != nil {
			return existing, loadErr
		}
		if childDoc.GoalOrigin == nil || *childDoc.GoalOrigin != origin || !budgetMatchesAllocation(childDoc.Budget, item.BudgetAllocation) || childDoc.Spec.Goal != string(goalRaw) || childDoc.Baseline.SnapshotDigest != item.SourceSnapshotDigest {
			return child, c.Fail(c.CommandIDConflict, "stable child ID belongs to a different task or goal")
		}
		return s.finishGoalCreation(ctx, child)
	}
	if item.Status == "ACTIVATED" {
		return parentAtOrigin, c.Fail(c.StoreIntegrityError, "durable activated goal is missing its child task")
	}
	baselineRef := parentDoc.Baseline
	if c.ValidReadWorkerRole(item.Role) {
		baselineRef = parentDoc.Candidate
	}
	baseline, err := s.Archive.Get(baselineRef)
	if err != nil {
		return parentAtOrigin, err
	}
	fixture := []byte(nil)
	if parentDoc.Runtime == nil {
		fixture, err = s.Archive.GetBytes(parent, parentDoc.FixtureDigest)
		if err != nil {
			return parentAtOrigin, c.Fail(c.StoreIntegrityError, "fixture goal cannot recover its retained fixture")
		}
	}
	var checkPlan []byte
	if parentDoc.Protection != nil {
		checkPlan, err = c.CanonicalV1(parentDoc.Protection.Plan)
		if err != nil {
			return parentAtOrigin, err
		}
	}
	budget := budgetFromAllocation(item.BudgetAllocation)
	taskKindForChild := taskKind(parentDoc)
	allowUnverified := parentDoc.AllowUnverified
	if c.ValidReadWorkerRole(item.Role) {
		taskKindForChild = "ANALYSIS"
		// Read-worker output is an internal advisory artifact, never a delivery
		// claim. Let the child close its bounded summary without waiting for the
		// parent's limited-delivery approval request.
		allowUnverified = true
		// A read worker receives an advisory prompt over an immutable candidate.
		// Registered checks are deliberately not inherited: an LLM summary cannot
		// mint verification evidence or alter the protected source origin.
		checkPlan = nil
		parentDoc.CheckRuntime = nil
	}
	childOptions := StartOptions{GoGraphRuntime: parentDoc.GoGraphRuntime, GoalOrigin: &origin, Config: parentDoc.Config, ResourcePolicy: parentDoc.ResourcePolicy, StoreTokens: parentDoc.StoreTokens, Root: baseline.Snapshot.Root, Git: baseline.Snapshot.Git != nil, Prompt: goalRaw, TaskID: item.ChildTaskID, Budget: budget, Autonomy: parentDoc.Autonomy, AllowUnverified: allowUnverified, Runtime: parentDoc.Runtime, Fixture: fixture, CheckPlan: checkPlan, CheckRuntime: parentDoc.CheckRuntime, TaskKind: taskKindForChild, MaxRepairs: parentDoc.MaxRepairs}
	if c.ValidReadWorkerRole(item.Role) {
		childOptions.evaluationCapture = &baseline
	}
	child, err := s.createLocked(ctx, childOptions)
	if err != nil {
		return child, err
	}
	return child, nil
}

func (origin GoalOrigin) validate() error {
	if err := origin.validateIdentity(); err != nil {
		return err
	}
	if origin.Role == "" && origin.ReadWorkerLease != (c.ReadWorkerLease{}) {
		return c.Fail(c.InvalidArgument, "ordinary scheduled goal cannot carry a read-worker lease")
	}
	if c.ValidReadWorkerRole(origin.Role) {
		lease := origin.ReadWorkerLease
		if err := lease.Validate(); err != nil || lease.ParentTask != origin.ParentTask || lease.ParentTaskSeq != origin.ParentTaskSeq || lease.ParentDocumentDigest != origin.ParentDocumentDigest || lease.ParentCandidateDigest != origin.SourceSnapshotDigest || lease.ChildTaskID != origin.ChildTaskID || lease.Role != origin.Role || lease.OutputBudget != origin.BudgetAllocation.MaxOutputTokens {
			return c.Fail(c.InvalidArgument, "read-worker origin and parent lease do not match")
		}
	} else if origin.Role != "" {
		return c.Fail(c.InvalidArgument, "unsupported scheduled-goal role")
	}
	if origin.hasLegacyBudget() || origin.BudgetAllocation.Validate() != nil || !c.ValidDigest(origin.BudgetAllocationDigest) || !allocationDigestMatches(origin.BudgetAllocation, origin.BudgetAllocationDigest) {
		return c.Fail(c.InvalidArgument, "scheduled-goal child requires an exact fixed budget allocation")
	}
	return nil
}

func (origin GoalOrigin) validateIdentity() error {
	if origin.ParentTask == "" || len(origin.ParentTask) > 128 || origin.ParentTask == origin.ChildTaskID || origin.ParentTaskSeq < 1 ||
		!c.ValidDigest(origin.ParentDocumentDigest) || origin.GoalID == "" || len(origin.GoalID) > 128 || !c.ValidDigest(origin.GoalDigest) ||
		!c.ValidDigest(origin.SourceSnapshotDigest) || origin.ChildTaskID == "" || len(origin.ChildTaskID) > 128 || strings.HasPrefix(origin.ChildTaskID, "eval-") {
		return c.Fail(c.InvalidArgument, "invalid immutable scheduled-goal origin")
	}
	return nil
}

func (origin GoalOrigin) hasLegacyBudget() bool {
	return origin.BudgetAllocation == (c.BudgetAllocation{}) && origin.BudgetAllocationDigest == ""
}

func allocationDigestMatches(allocation c.BudgetAllocation, expected string) bool {
	digest, err := allocation.Digest()
	return err == nil && digest == expected
}

func (s *Session) validateGoalOrigin(ctx context.Context, doc Document) error {
	if doc.GoalOrigin == nil {
		return nil
	}
	origin := *doc.GoalOrigin
	legacyBudget := origin.hasLegacyBudget()
	if err := origin.validateIdentity(); err != nil || !legacyBudget && origin.validate() != nil || origin.ChildTaskID != doc.TaskID || doc.Baseline.SnapshotDigest != origin.SourceSnapshotDigest || doc.Spec.Goal == "" || len(doc.Spec.Inputs) != 1 || doc.Spec.Inputs[0].Digest != origin.GoalDigest || doc.Spec.Inputs[0].ByteLength <= 0 || doc.Spec.Inputs[0].ByteLength > 64<<10 {
		return c.Fail(c.StoreIntegrityError, "scheduled child lost immutable goal origin")
	}
	goal, err := s.Archive.GetBytes(doc.TaskID, origin.GoalDigest)
	if err != nil || !utf8.Valid(goal) || string(goal) != doc.Spec.Goal || int64(len(goal)) != doc.Spec.Inputs[0].ByteLength {
		return c.Fail(c.StoreIntegrityError, "scheduled child goal bytes changed")
	}
	states, err := s.Journal.Replay(ctx, origin.ParentTask)
	if err != nil {
		return err
	}
	if state := states[origin.ParentTask]; state.TaskID != origin.ParentTask {
		return c.Fail(c.StoreIntegrityError, "scheduled child parent is unavailable")
	}
	var schedule *c.GoalSchedule
	for i := range states[origin.ParentTask].GoalQueue {
		if states[origin.ParentTask].GoalQueue[i].ID == origin.GoalID {
			schedule = &states[origin.ParentTask].GoalQueue[i]
			break
		}
	}
	allocationMatches := schedule != nil && schedule.BudgetAllocation == origin.BudgetAllocation && schedule.BudgetAllocationDigest == origin.BudgetAllocationDigest
	if legacyBudget {
		allocationMatches = schedule != nil && schedule.BudgetAllocation == (c.BudgetAllocation{}) && schedule.BudgetAllocationDigest == ""
	} else {
		allocationMatches = allocationMatches && budgetMatchesAllocation(doc.Budget, origin.BudgetAllocation)
	}
	activeSchedule := schedule != nil && (schedule.Status == "ACTIVATING" || schedule.Status == "ACTIVATED" || schedule.Status == "UNKNOWN" || c.ValidReadWorkerRole(origin.Role) && schedule.Status == "CANCELLED")
	if schedule == nil || schedule.ParentTaskSeq != origin.ParentTaskSeq || schedule.ParentDocumentDigest != origin.ParentDocumentDigest || schedule.SourceSnapshotDigest != origin.SourceSnapshotDigest || schedule.ChildTaskID != origin.ChildTaskID || schedule.Role != origin.Role || schedule.ReadWorkerLease != origin.ReadWorkerLease || schedule.Digest != origin.GoalDigest || !allocationMatches || !activeSchedule {
		return c.Fail(c.StoreIntegrityError, "scheduled child does not match its parent journal")
	}
	parentAtOrigin, err := s.Journal.ReplayAt(ctx, origin.ParentTask, origin.ParentTaskSeq)
	if err != nil || parentAtOrigin.DocumentDigest != origin.ParentDocumentDigest {
		return c.Fail(c.StoreIntegrityError, "scheduled child origin cursor does not match parent history")
	}
	return nil
}

func (s *Session) finishGoalCreation(ctx context.Context, state c.TaskState) (c.TaskState, error) {
	if state.Execution == c.Created {
		next, err := s.transition(ctx, state, c.Scoping, "", "reconcile scheduled child creation")
		if err != nil {
			return next, err
		}
		state = next
	}
	if state.Execution == c.Scoping || state.Execution == c.Recovering {
		return s.transition(ctx, state, c.Ready, "", "scheduled child admission ready after recovery")
	}
	return state, nil
}
