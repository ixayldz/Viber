package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type RetentionOptions struct {
	CommandID        string `json:"command_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Mode             string `json:"mode"`
	Deadline         string `json:"deadline,omitempty"`
	Acknowledgement  string `json:"acknowledgement,omitempty"`
}

type privacyRetentionRecord struct {
	CommandID   string            `json:"command_id"`
	InputDigest string            `json:"input_digest"`
	Policy      c.RetentionPolicy `json:"policy"`
}

func validRetentionCommandID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && !strings.ContainsAny(id, "\x00\r\n")
}
func (r privacyRetentionRecord) validate() error {
	if !validRetentionCommandID(r.CommandID) || !c.ValidDigest(r.InputDigest) || r.Policy.Validate() != nil {
		return c.Fail(c.StoreIntegrityError, "invalid source-bound retention consent")
	}
	expected, err := retentionInputDigest(r.Policy.TaskID, RetentionOptions{CommandID: r.CommandID, ExpectedRevision: r.Policy.Revision - 1, Mode: r.Policy.Mode, Deadline: r.Policy.Deadline, Acknowledgement: r.Policy.Acknowledgement})
	if err != nil || expected != r.InputDigest {
		return c.Fail(c.StoreIntegrityError, "retention consent input digest mismatch")
	}
	return nil
}
func retentionInputDigest(task string, options RetentionOptions) (string, error) {
	return c.Digest(struct {
		Task    string           `json:"task"`
		Options RetentionOptions `json:"options"`
	}{task, options})
}

type RetentionStatus struct {
	SchemaVersion int                `json:"schema_version"`
	TaskID        string             `json:"task_id"`
	Policy        *c.RetentionPolicy `json:"policy,omitempty"`
	ObservedAt    string             `json:"observed_at"`
	Status        string             `json:"status"`
	Reason        string             `json:"reason,omitempty"`
	Deletion      *DeletionResult    `json:"deletion,omitempty"`
}
type RetentionRun struct {
	SchemaVersion int               `json:"schema_version"`
	Limit         int               `json:"limit"`
	HasMore       bool              `json:"has_more"`
	Items         []RetentionStatus `json:"items"`
}

func (s *Session) retentionView(ctx context.Context) (privacyView, error) {
	if err := ctx.Err(); err != nil {
		return privacyView{}, err
	}
	if s.privacy == nil {
		return privacyView{}, c.Fail(c.PolicyDenied, "existing privacy authority required")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return privacyView{}, err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return privacyView{}, err
	}
	defer lock.Close()
	return readPrivacy(root)
}

// Only the user command channel configures retention. Model tools never call it.
// The external family authority retains consent across owner loss and backups.
func (s *Session) SetRetention(ctx context.Context, task string, options RetentionOptions) (c.RetentionPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	var empty c.RetentionPolicy
	if !validRetentionCommandID(options.CommandID) || options.ExpectedRevision < 0 || options.ExpectedRevision >= 1_000_000_000 || options.Mode != "KEEP" && options.Mode != "EXPIRE" {
		return empty, c.Fail(c.InvalidArgument, "bounded retention command and expected revision required")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := s.Journal.Writable(); err != nil {
		return empty, err
	}
	if s.privacy == nil {
		return empty, c.Fail(c.PolicyDenied, "existing privacy authority required")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return empty, err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return empty, err
	}
	input, err := retentionInputDigest(task, options)
	if err != nil {
		return empty, err
	}
	if prior, exists := view.RetentionCommands[options.CommandID]; exists {
		if prior.InputDigest != input {
			return empty, c.Fail(c.CommandIDConflict, "retention retry must preserve task and options")
		}
		return prior.Policy, nil
	}
	if _, deleted := view.Deletions[task]; deleted {
		return empty, c.Fail(c.PolicyDenied, "retention cannot revise a durable deletion intent")
	}
	for _, deletion := range view.Deletions {
		for _, id := range deletionCommandIDs(deletion) {
			if id == options.CommandID {
				return empty, c.Fail(c.CommandIDConflict, "retention and deletion command namespace collision")
			}
		}
	}
	if _, _, _, found, err := s.Journal.CommandReceipt(ctx, options.CommandID); err != nil || found {
		if err != nil {
			return empty, err
		}
		return empty, c.Fail(c.CommandIDConflict, "retention command already used by kernel")
	}
	if options.ExpectedRevision != view.Retentions[task].Policy.Revision {
		return empty, c.Fail(c.StaleRequest, "retention revision changed; refresh status")
	}
	// Do not call Session.Load under the family lock (it reads this same registry).
	states, err := s.Journal.Replay(ctx, task)
	if err != nil {
		return empty, err
	}
	state, exists := states[task]
	if !exists || state.Deletion != nil {
		return empty, c.Fail(c.PolicyDenied, "available current task required")
	}
	raw, err := s.Archive.GetBytes(task, state.DocumentDigest)
	if err != nil {
		return empty, err
	}
	var document Document
	if c.DecodeStrict(raw, &document) != nil || document.TaskID != task || document.Spec.TaskID != task || document.Spec.Version != state.SpecVersion || document.Candidate.SnapshotDigest != state.CandidateDigest || document.Baseline.SnapshotDigest != state.BaselineDigest {
		return empty, c.Fail(c.StoreIntegrityError, "retention source document binding mismatch")
	}
	authorityDigest, _ := c.Digest(s.privacy)
	policy := c.RetentionPolicy{SchemaVersion: 1, TaskID: task, Revision: options.ExpectedRevision + 1, Actor: "USER", Scope: "MANAGED_FAMILY_TASK_CONTENT", Mode: options.Mode, Clock: "SYSTEM_UTC", ChangedAt: time.Now().UTC().Format(time.RFC3339Nano), Deadline: options.Deadline, Acknowledgement: options.Acknowledgement, AuthorityDigest: authorityDigest, SourcePhysicalRoot: s.instance.PhysicalRoot, SourceTaskSequence: state.TaskSeq, SourceDocumentDigest: state.DocumentDigest}
	if err = policy.Validate(); err != nil {
		return empty, err
	}
	record := privacyRetentionRecord{options.CommandID, input, policy}
	if err = appendPrivacy(root, view, privacyRecord{Type: "RETENTION_POLICY", Retention: &record}); err != nil {
		return empty, err
	}
	return policy, nil
}

func retentionStatus(task string, view privacyView) RetentionStatus {
	now := time.Now().UTC()
	result := RetentionStatus{SchemaVersion: 1, TaskID: task, ObservedAt: now.Format(time.RFC3339Nano), Status: "KEEP"}
	if record, exists := view.Retentions[task]; exists {
		policy := record.Policy
		result.Policy = &policy
		if policy.Mode == "EXPIRE" {
			deadline, _ := c.RetentionTime(policy.Deadline)
			result.Status = "NOT_DUE"
			if !now.Before(deadline) {
				result.Status = "DUE"
			}
		}
	}
	if deletion, exists := view.Deletions[task]; exists {
		result.Status = "PENDING"
		if deletion.Expiry == nil {
			result.Status, result.Reason = "BLOCKED", "USER_DELETION_REQUIRES_EXACT_MANUAL_RETRY"
		}
	}
	return result
}
func (s *Session) RetentionStatus(ctx context.Context, task string) (RetentionStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	view, err := s.retentionView(ctx)
	if err != nil {
		return RetentionStatus{}, err
	}
	result := retentionStatus(task, view)
	states, err := s.Journal.Replay(ctx, task)
	if err != nil {
		return result, err
	}
	state, exists := states[task]
	if !exists {
		return result, c.Fail(c.InvalidArgument, "task is not in this owner store")
	}
	if state.Deletion != nil && state.Deletion.Status == "PURGED" {
		result.Status = "PURGED"
	}
	return result, nil
}

// At most sixteen due task attempts per maintenance pass. Pin failures remain
// visible and retryable; expiry never cancels work, waives risk or replays a
// user deletion. A pending expiry uses its immutable original external command.
func (s *Session) RunRetention(ctx context.Context) (RetentionRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	result := RetentionRun{SchemaVersion: 1, Limit: 16, Items: []RetentionStatus{}}
	view, err := s.retentionView(ctx)
	if err != nil {
		return result, err
	}
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return result, err
	}
	tasks := []string{}
	for task, record := range view.Retentions {
		state, exists := states[task]
		if !exists || state.Deletion != nil && state.Deletion.Status == "PURGED" || record.Policy.Mode != "EXPIRE" {
			continue
		}
		status := retentionStatus(task, view)
		if status.Status != "NOT_DUE" {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, b := view.Retentions[tasks[i]].Policy.Deadline, view.Retentions[tasks[j]].Policy.Deadline
		ta, _ := c.RetentionTime(a)
		tb, _ := c.RetentionTime(b)
		if ta.Equal(tb) {
			return tasks[i] < tasks[j]
		}
		return ta.Before(tb)
	})
	result.HasMore = len(tasks) > result.Limit
	// Round-robin across due policies: permanently pinned early tasks cannot
	// starve later eligible tasks. This cursor is scheduling, never authority;
	// owner restart rescans the durable policies and immutable expiry intents.
	if len(tasks) > 0 && s.retentionCursorTask != "" {
		start := 0
		for i, task := range tasks {
			deadline, _ := c.RetentionTime(view.Retentions[task].Policy.Deadline)
			if deadline.After(s.retentionCursorDeadline) || deadline.Equal(s.retentionCursorDeadline) && task > s.retentionCursorTask {
				start = i
				break
			}
		}
		tasks = append(tasks[start:], tasks[:start]...)
	}
	if len(tasks) > result.Limit {
		tasks = tasks[:result.Limit]
	}
	for _, task := range tasks {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		status := retentionStatus(task, view)
		s.retentionCursorTask = task
		s.retentionCursorDeadline, _ = c.RetentionTime(view.Retentions[task].Policy.Deadline)
		if status.Status == "BLOCKED" {
			result.Items = append(result.Items, status)
			continue
		}
		policy := view.Retentions[task].Policy
		command, retry := view.Deletions[task]
		if !retry {
			plan, planErr := s.deletionPlanLocked(ctx, task)
			if planErr != nil {
				status.Status = "BLOCKED"
				status.Reason = retentionReason(planErr)
				result.Items = append(result.Items, status)
				continue
			}
			digest, _ := c.Digest(policy)
			command = DeletionCommand{CommandID: "retention-expiry-" + digest, Plan: plan, Expiry: &c.RetentionExpiry{Policy: policy, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
		}
		deletion, deleteErr := s.deleteContentLocked(ctx, command)
		status.Deletion = &deletion
		status.Status = deletion.Status
		if deleteErr != nil {
			status.Status = "BLOCKED"
			status.Reason = retentionReason(deleteErr)
		}
		result.Items = append(result.Items, status)
	}
	return result, nil
}
func retentionReason(err error) string {
	var typed *c.Error
	if errors.As(err, &typed) {
		return string(typed.Code)
	}
	return "RETENTION_ATTEMPT_FAILED"
}

func checkCurrentExpiry(command DeletionCommand, view privacyView) error {
	if command.Expiry == nil {
		return nil
	}
	current, exists := view.Retentions[command.Plan.TaskID]
	expected, _ := c.Digest(current.Policy)
	actual, _ := c.Digest(command.Expiry.Policy)
	deadline, err := c.RetentionTime(command.Expiry.Policy.Deadline)
	observed, observationErr := c.RetentionTime(command.Expiry.ObservedAt)
	now := time.Now().UTC()
	if !exists || expected != actual || err != nil || observationErr != nil || now.Before(deadline) || observed.After(now) {
		return c.Fail(c.StaleRequest, "retention consent or current UTC deadline is no longer admissible")
	}
	return nil
}
func bindRetentionTombstone(command DeletionCommand, tombstone *c.TaskDeletion) string {
	if command.Expiry == nil {
		return "user"
	}
	tombstone.RetentionDigest, _ = c.Digest(command.Expiry.Policy)
	tombstone.RetentionRevision = command.Expiry.Policy.Revision
	tombstone.RetentionDeadline = command.Expiry.Policy.Deadline
	return "retention"
}

// These identities are reserved by the immutable intent before any kernel
// receipt exists, including receipts in other restored stores in the family.
func deletionCommandIDs(command DeletionCommand) []string {
	ids := []string{command.CommandID, "privacy-purge-" + c.HashBytes([]byte(command.CommandID))}
	for _, bound := range command.Plan.Stores {
		id := "privacy-scope-" + c.HashBytes([]byte(command.CommandID+":"+bound.Scope.PhysicalRoot))
		ids = append(ids, id, id+"-purged")
	}
	return ids
}

func retentionDeletionCollision(view privacyView, command DeletionCommand) bool {
	for _, id := range deletionCommandIDs(command) {
		if _, exists := view.RetentionCommands[id]; exists {
			return true
		}
	}
	return false
}
