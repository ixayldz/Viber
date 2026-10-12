package agent

import (
	"bytes"
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/skills"
)

const (
	projectMemoryMaxEventsPerTask = 256
	projectMemoryMaxTasks         = 8192
	projectMemoryMaxEvents        = 4096
	projectMemoryMaxBytes         = 32 << 20
	projectMemoryMaxDocumentBytes = 32 << 20
	projectMemoryMaxSourceBytes   = 32 << 20
	projectMemoryMaxSourceDocSize = 8 << 20
	projectMemoryEventMaxBytes    = 16 << 10
	projectMemoryTextMaxBytes     = 8 << 10
	projectMemoryMaxPage          = 200
)

var projectMemoryIDPattern = regexp.MustCompile(`^pm-[0-9a-f]{32}$`)
var projectMemoryKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// ProjectMemoryRef is a managed, task-scoped archive pointer. The memory
// record itself is immutable; the task journal binds this list in each
// document revision. No separate project index or authority is introduced.
type ProjectMemoryRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// ProjectMemorySource pins an explicit user statement to the task document,
// source snapshots, project scope, policy digest and policy epoch visible when
// the statement was recorded. These fields describe provenance only.
type ProjectMemorySource struct {
	TaskID          string `json:"task_id"`
	TaskSeq         int64  `json:"task_seq"`
	DocumentDigest  string `json:"document_digest"`
	BaselineDigest  string `json:"baseline_digest"`
	CandidateDigest string `json:"candidate_digest"`
	ProjectDigest   string `json:"project_digest"`
	PolicyEpoch     int64  `json:"policy_epoch"`
	PolicyDigest    string `json:"policy_digest"`
}

type projectMemoryPolicyProjection struct {
	Config          *config.Resolved `json:"config,omitempty"`
	SchemaVersion   int              `json:"schema_version"`
	Profile         string           `json:"profile"`
	Autonomy        string           `json:"autonomy"`
	AllowUnverified bool             `json:"allow_unverified"`
	Scope           string           `json:"scope"`
}

// ProjectMemoryEvent is an append-only assertion or an explicit user
// correction/retirement. It is advisory text and carries no evaluator result,
// policy effect, capability, verification verdict, or authorization.
type ProjectMemoryEvent struct {
	PreviousEventDigest string              `json:"previous_event_digest,omitempty"`
	TargetDigest        string              `json:"target_digest,omitempty"`
	RequestDigest       string              `json:"request_digest"`
	ContentDigest       string              `json:"content_digest,omitempty"`
	TargetID            string              `json:"target_id,omitempty"`
	CommandID           string              `json:"command_id"`
	ID                  string              `json:"id"`
	Action              string              `json:"action"`
	Kind                string              `json:"kind,omitempty"`
	Key                 string              `json:"key,omitempty"`
	Text                string              `json:"text,omitempty"`
	EvidenceClass       string              `json:"evidence_class"`
	Source              ProjectMemorySource `json:"source"`
	SchemaVersion       int                 `json:"schema_version"`
}

type ProjectMemoryMutationReceipt struct {
	Source        ProjectMemorySource `json:"source"`
	EventDigest   string              `json:"event_digest"`
	RequestDigest string              `json:"request_digest"`
	CommandID     string              `json:"command_id"`
	EventID       string              `json:"event_id"`
	ItemID        string              `json:"item_id"`
	TargetID      string              `json:"target_id,omitempty"`
	Operation     string              `json:"operation"`
	Status        string              `json:"status"`
	AdvisoryOnly  bool                `json:"advisory_only"`
	SchemaVersion int                 `json:"schema_version"`
}

type ProjectMemoryItem struct {
	Source        ProjectMemorySource `json:"source"`
	Digest        string              `json:"digest"`
	ContentDigest string              `json:"content_digest"`
	ID            string              `json:"id"`
	Kind          string              `json:"kind"`
	Key           string              `json:"key"`
	Text          string              `json:"text"`
	Status        string              `json:"status"`
	StatusReason  string              `json:"status_reason,omitempty"`
	RetiredBy     string              `json:"retired_by,omitempty"`
	SupersededBy  string              `json:"superseded_by,omitempty"`
	Redacted      bool                `json:"redacted,omitempty"`
	EvidenceClass string              `json:"evidence_class"`
	AdvisoryOnly  bool                `json:"advisory_only"`
	SchemaVersion int                 `json:"schema_version"`
}

type ProjectMemorySearchResult struct {
	SchemaVersion int                 `json:"schema_version"`
	ProjectDigest string              `json:"project_digest"`
	QuerySource   ProjectMemorySource `json:"query_source"`
	Query         string              `json:"query,omitempty"`
	Items         []ProjectMemoryItem `json:"items"`
	Offset        int                 `json:"offset"`
	Limit         int                 `json:"limit"`
	Total         int                 `json:"total"`
	HasMore       bool                `json:"has_more"`
}

type projectMemoryRequest struct {
	ProjectDigest string `json:"project_digest"`
	Action        string `json:"action"`
	Kind          string `json:"kind,omitempty"`
	Key           string `json:"key,omitempty"`
	Text          string `json:"text,omitempty"`
	TargetID      string `json:"target_id,omitempty"`
	TargetDigest  string `json:"target_digest,omitempty"`
}

type projectMemoryStored struct {
	TaskID string
	Event  ProjectMemoryEvent
	Digest string
}

type projectMemoryCurrent struct {
	State   c.TaskState
	Doc     Document
	Project string
}

type projectMemorySourceBudget struct {
	seen      map[string]bool
	validated map[string]ProjectMemorySource
	bytes     int64
}

func (e ProjectMemoryEvent) Validate(ref ProjectMemoryRef, ownerTask string) error {
	if e.SchemaVersion != 1 || ref.ID != e.ID || !projectMemoryIDPattern.MatchString(e.ID) || !c.ValidDigest(ref.Digest) ||
		!c.ValidDigest(e.RequestDigest) || !validProjectMemoryCommandID(e.CommandID) || e.EvidenceClass != "USER_ASSERTED" ||
		e.Source.TaskID != ownerTask || e.Source.TaskSeq < 1 || e.Source.TaskSeq > 1<<53 || e.Source.PolicyEpoch < 0 ||
		!c.ValidDigest(e.Source.DocumentDigest) || !c.ValidDigest(e.Source.BaselineDigest) || !c.ValidDigest(e.Source.CandidateDigest) ||
		!c.ValidDigest(e.Source.ProjectDigest) || !c.ValidDigest(e.Source.PolicyDigest) ||
		e.PreviousEventDigest != "" && !c.ValidDigest(e.PreviousEventDigest) {
		return c.Fail(c.StoreIntegrityError, "invalid source-bound project memory event")
	}
	switch e.Action {
	case "ASSERT", "CORRECT":
		if (e.Kind != "FACT" && e.Kind != "DECISION" && e.Kind != "CONVENTION") || !projectMemoryKeyPattern.MatchString(e.Key) ||
			!validProjectMemoryText(e.Text) || e.ContentDigest != c.HashBytes([]byte(e.Text)) {
			return c.Fail(c.StoreIntegrityError, "invalid project memory assertion")
		}
		if e.Action == "ASSERT" && (e.TargetID != "" || e.TargetDigest != "") || e.Action == "CORRECT" && (!projectMemoryIDPattern.MatchString(e.TargetID) || !c.ValidDigest(e.TargetDigest)) {
			return c.Fail(c.StoreIntegrityError, "invalid project memory lineage")
		}
	case "RETIRE":
		if !projectMemoryIDPattern.MatchString(e.TargetID) || !c.ValidDigest(e.TargetDigest) || e.Kind != "" || e.Key != "" || e.Text != "" || e.ContentDigest != "" {
			return c.Fail(c.StoreIntegrityError, "invalid project memory retirement")
		}
	default:
		return c.Fail(c.StoreIntegrityError, "unknown project memory action")
	}
	return nil
}

func validProjectMemoryCommandID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validProjectMemoryText(value string) bool {
	if len(value) < 1 || len(value) > projectMemoryTextMaxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if r == 0 || r == '\r' || r < 0x20 && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func projectMemoryRequestDigest(request projectMemoryRequest) (string, error) {
	return c.Digest(request)
}

func projectMemoryPolicyDigest(doc Document) (string, error) {
	projection := projectMemoryPolicyProjection{SchemaVersion: 1, Profile: "VIBER_RESOLVED_CONFIG_V1", Config: doc.Config,
		Autonomy: doc.Autonomy, AllowUnverified: doc.AllowUnverified, Scope: "EXACT_PROJECT_CANDIDATE_POLICY"}
	if doc.Config == nil {
		// No-config tasks use a named built-in policy projection. This keeps
		// their policy provenance explicit rather than treating it as unknown.
		projection.Profile = "VIBER_NO_CONFIG_V1"
	}
	return c.Digest(projection)
}

func (s *Session) projectMemorySource(ctx context.Context, state c.TaskState, doc Document) (ProjectMemorySource, error) {
	var source ProjectMemorySource
	if state.TaskSeq < 1 || state.DocumentDigest == "" {
		return source, c.Fail(c.StaleAuthority, "project memory requires a retained source document")
	}
	if doc.Config != nil {
		if err := doc.Config.CurrentAuthority(); err != nil {
			return source, err
		}
	}
	raw, err := s.Archive.GetBytes(state.TaskID, state.DocumentDigest)
	if err != nil {
		return source, err
	}
	if len(raw) > projectMemoryMaxSourceDocSize {
		return source, c.Fail(c.BudgetLimitReached, "project memory source document exceeds its bound")
	}
	projectDigest, err := s.projectMemoryProject(doc)
	if err != nil {
		return source, err
	}
	policyDigest, err := projectMemoryPolicyDigest(doc)
	if err != nil {
		return source, err
	}
	source = ProjectMemorySource{
		TaskID: state.TaskID, TaskSeq: state.TaskSeq, DocumentDigest: state.DocumentDigest,
		BaselineDigest: state.BaselineDigest, CandidateDigest: state.CandidateDigest,
		ProjectDigest: projectDigest, PolicyEpoch: state.PolicyEpoch, PolicyDigest: policyDigest,
	}
	return source, nil
}

func (s *Session) projectMemoryProject(doc Document) (string, error) {
	manifest, err := s.Archive.ReadManifest(doc.Baseline)
	if err != nil {
		return "", err
	}
	snapshot := manifest.Snapshot
	if snapshot.SchemaVersion != c.SchemaVersion || snapshot.Consistency != "BEST_EFFORT" || !c.ValidDigest(snapshot.Digest) {
		return "", c.Fail(c.StoreIntegrityError, "project memory baseline snapshot is invalid")
	}
	claimedDigest := snapshot.Digest
	snapshot.Digest = ""
	sort.Slice(snapshot.Entries, func(i, j int) bool { return snapshot.Entries[i].Path < snapshot.Entries[j].Path })
	sort.Strings(snapshot.Exclusions)
	sort.Strings(snapshot.Directories)
	actualDigest, err := c.Digest(snapshot)
	if err != nil || actualDigest != claimedDigest {
		return "", c.Fail(c.StoreIntegrityError, "project memory baseline snapshot digest mismatch")
	}
	project, err := skills.ProjectScope(snapshot.Root)
	if err != nil {
		return "", err
	}
	return project.Key, nil
}

// validateProjectMemoryRefs verifies every immutable record stored by this
// task. Source document and snapshot hashes are checked from the archive; the
// event remains user-authored advisory text and is never an authority result.
func (s *Session) validateProjectMemoryRefs(ctx context.Context, state c.TaskState, doc Document, sourceBudget *projectMemorySourceBudget) ([]ProjectMemoryEvent, error) {
	if len(doc.ProjectMemoryRefs) > projectMemoryMaxEventsPerTask {
		return nil, c.Fail(c.BudgetLimitReached, "project memory task event limit reached")
	}
	events := make([]ProjectMemoryEvent, 0, len(doc.ProjectMemoryRefs))
	seen := make(map[string]bool, len(doc.ProjectMemoryRefs))
	previous := ""
	var totalBytes int64
	for _, ref := range doc.ProjectMemoryRefs {
		if seen[ref.ID] {
			return nil, c.Fail(c.StoreIntegrityError, "duplicate project memory event reference")
		}
		seen[ref.ID] = true
		raw, err := s.Archive.GetBytes(state.TaskID, ref.Digest)
		if err != nil {
			return nil, err
		}
		totalBytes += int64(len(raw))
		if len(raw) > projectMemoryEventMaxBytes || totalBytes > projectMemoryMaxBytes {
			return nil, c.Fail(c.BudgetLimitReached, "project memory task event bytes exceed bound")
		}
		var event ProjectMemoryEvent
		if err = c.DecodeStrict(raw, &event); err != nil || event.Validate(ref, state.TaskID) != nil {
			return nil, c.Fail(c.StoreIntegrityError, "project memory record failed validation")
		}
		canonical, err := c.CanonicalV1(event)
		if err != nil || !bytes.Equal(raw, canonical) || c.HashBytes(raw) != ref.Digest || event.PreviousEventDigest != previous || event.Source.TaskSeq >= state.TaskSeq {
			return nil, c.Fail(c.StoreIntegrityError, "project memory record lineage or canonical bytes mismatch")
		}
		if err = s.validateProjectMemorySource(ctx, event.Source, sourceBudget); err != nil {
			return nil, err
		}
		previous = ref.Digest
		events = append(events, event)
	}
	return events, nil
}

func (s *Session) validateProjectMemorySource(ctx context.Context, source ProjectMemorySource, sourceBudget *projectMemorySourceBudget) error {
	documentKey := source.TaskID + "\x00" + source.DocumentDigest
	cacheKey := documentKey + "\x00" + strconv.FormatInt(source.TaskSeq, 10)
	if sourceBudget != nil {
		if sourceBudget.validated == nil {
			sourceBudget.validated = map[string]ProjectMemorySource{}
		}
		if cached, ok := sourceBudget.validated[cacheKey]; ok {
			if cached != source {
				return c.Fail(c.StoreIntegrityError, "project memory source binding changed for a retained document")
			}
			return ctx.Err()
		}
	}
	raw, err := s.Archive.GetBytes(source.TaskID, source.DocumentDigest)
	if err != nil {
		return c.Fail(c.StoreIntegrityError, "project memory source document is unavailable")
	}
	if len(raw) > projectMemoryMaxSourceDocSize {
		return c.Fail(c.BudgetLimitReached, "project memory source document exceeds its bound")
	}
	if sourceBudget != nil {
		if sourceBudget.seen == nil {
			sourceBudget.seen = map[string]bool{}
		}
		if !sourceBudget.seen[documentKey] {
			sourceBudget.seen[documentKey] = true
			sourceBudget.bytes += int64(len(raw))
			if sourceBudget.bytes > projectMemoryMaxSourceBytes {
				return c.Fail(c.BudgetLimitReached, "project memory source document inventory exceeds bound")
			}
		}
	}
	var doc Document
	if err = c.DecodeStrict(raw, &doc); err != nil || doc.SchemaVersion != 1 || doc.TaskID != source.TaskID ||
		doc.Baseline.TaskID != source.TaskID || doc.Candidate.TaskID != source.TaskID ||
		doc.Baseline.SnapshotDigest != source.BaselineDigest || doc.Candidate.SnapshotDigest != source.CandidateDigest {
		return c.Fail(c.StoreIntegrityError, "project memory source document binding mismatch")
	}
	canonical, err := c.CanonicalV1(doc)
	if err != nil || !bytes.Equal(raw, canonical) {
		return c.Fail(c.StoreIntegrityError, "project memory source document is not canonical")
	}
	if err = validateTaskConfig(doc); err != nil {
		return c.Fail(c.StoreIntegrityError, "project memory source policy is invalid")
	}
	policyDigest, err := projectMemoryPolicyDigest(doc)
	if err != nil || policyDigest != source.PolicyDigest {
		return c.Fail(c.StoreIntegrityError, "project memory source policy digest mismatch")
	}
	projectDigest, err := s.projectMemoryProject(doc)
	if err != nil {
		return err
	}
	if projectDigest != source.ProjectDigest {
		return c.Fail(c.StoreIntegrityError, "project memory source project binding mismatch")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if sourceBudget != nil {
		sourceBudget.validated[cacheKey] = source
	}
	return nil
}

func (s *Session) projectMemoryInventory(ctx context.Context, projectDigest string) ([]projectMemoryStored, map[string]projectMemoryCurrent, error) {
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	if len(states) > projectMemoryMaxTasks {
		return nil, nil, c.Fail(c.BudgetLimitReached, "project memory task inventory exceeds bound")
	}
	tasks := make([]string, 0, len(states))
	for task := range states {
		tasks = append(tasks, task)
	}
	sort.Strings(tasks)
	current := make(map[string]projectMemoryCurrent, len(states))
	stored := make([]projectMemoryStored, 0)
	var totalBytes int64
	var documentBytes int64
	sourceBudget := &projectMemorySourceBudget{}
	var privacy *privacyView
	if s.privacy != nil {
		view, viewErr := s.retentionView(ctx)
		if viewErr != nil {
			return nil, nil, viewErr
		}
		privacy = &view
	}
	for _, task := range tasks {
		state := states[task]
		if state.Deletion != nil {
			if state.Deletion.Status != "PURGED" {
				return nil, nil, c.Fail(c.PolicyDenied, "complete pending task-content deletion before project memory search")
			}
			continue
		}
		if privacy != nil {
			if _, exists := privacy.Deletions[task]; exists {
				return nil, nil, c.Fail(c.PolicyDenied, "task privacy deletion has not reached its journal-purged state")
			}
			revoked := false
			for _, attempt := range privacy.Attempts {
				if attempt.ChildTask == task {
					if _, deleted := privacy.Deletions[attempt.ParentTask]; deleted {
						revoked = true
						break
					}
				}
			}
			if revoked {
				continue
			}
		}
		raw, readErr := s.Archive.GetBytes(task, state.DocumentDigest)
		if readErr != nil {
			return nil, nil, readErr
		}
		documentBytes += int64(len(raw))
		if documentBytes > projectMemoryMaxDocumentBytes {
			return nil, nil, c.Fail(c.BudgetLimitReached, "project memory source document inventory exceeds bound")
		}
		var doc Document
		if readErr = c.DecodeStrict(raw, &doc); readErr != nil || doc.SchemaVersion != 1 || doc.TaskID != task || doc.Spec.TaskID != task ||
			doc.Baseline.TaskID != task || doc.Candidate.TaskID != task || doc.Baseline.SnapshotDigest != state.BaselineDigest || doc.Candidate.SnapshotDigest != state.CandidateDigest {
			return nil, nil, c.Fail(c.StoreIntegrityError, "project memory task document binding mismatch")
		}
		canonical, canonicalErr := c.CanonicalV1(doc)
		if canonicalErr != nil || !bytes.Equal(raw, canonical) {
			return nil, nil, c.Fail(c.StoreIntegrityError, "project memory task document is not canonical")
		}
		if readErr = validateTaskConfig(doc); readErr != nil {
			return nil, nil, readErr
		}
		currentProjectDigest, readErr := s.projectMemoryProject(doc)
		if readErr != nil {
			return nil, nil, readErr
		}
		current[task] = projectMemoryCurrent{State: state, Doc: doc, Project: currentProjectDigest}
		if projectDigest != "" && currentProjectDigest != projectDigest || len(doc.ProjectMemoryRefs) == 0 {
			continue
		}
		events, validateErr := s.validateProjectMemoryRefs(ctx, state, doc, sourceBudget)
		if validateErr != nil {
			return nil, nil, validateErr
		}
		for index, ref := range doc.ProjectMemoryRefs {
			encoded, encodeErr := c.CanonicalV1(events[index])
			if encodeErr != nil {
				return nil, nil, encodeErr
			}
			totalBytes += int64(len(encoded))
			if len(stored) >= projectMemoryMaxEvents || totalBytes > projectMemoryMaxBytes {
				return nil, nil, c.Fail(c.BudgetLimitReached, "project memory inventory exceeds bound")
			}
			stored = append(stored, projectMemoryStored{TaskID: task, Event: events[index], Digest: ref.Digest})
		}
	}
	return stored, current, nil
}

func projectMemoryBuildItems(stored []projectMemoryStored, current map[string]projectMemoryCurrent, querySource ProjectMemorySource) ([]ProjectMemoryItem, error) {
	events := make(map[string]projectMemoryStored, len(stored))
	for _, item := range stored {
		if _, exists := events[item.Event.ID]; exists {
			return nil, c.Fail(c.StoreIntegrityError, "duplicate project memory identity")
		}
		events[item.Event.ID] = item
	}
	retiredBy := map[string]string{}
	supersededBy := map[string]string{}
	for _, item := range stored {
		event := item.Event
		if event.Action != "RETIRE" && event.Action != "CORRECT" {
			continue
		}
		target, exists := events[event.TargetID]
		if !exists {
			return nil, c.Fail(c.StoreIntegrityError, "project memory lineage target is missing")
		}
		if target.Digest != event.TargetDigest || target.TaskID != item.TaskID || target.Event.Action == "RETIRE" {
			return nil, c.Fail(c.StoreIntegrityError, "project memory lineage target digest mismatch")
		}
		if target.Event.Source.ProjectDigest != event.Source.ProjectDigest {
			return nil, c.Fail(c.StoreIntegrityError, "project memory lineage crosses project scope")
		}
		if event.Action == "RETIRE" {
			if prior := retiredBy[event.TargetID]; prior != "" && prior != event.ID {
				return nil, c.Fail(c.StoreIntegrityError, "project memory fact retired more than once")
			}
			retiredBy[event.TargetID] = event.ID
		} else {
			if prior := supersededBy[event.TargetID]; prior != "" && prior != event.ID {
				return nil, c.Fail(c.StoreIntegrityError, "project memory fact corrected more than once")
			}
			supersededBy[event.TargetID] = event.ID
		}
	}
	items := make([]ProjectMemoryItem, 0, len(stored))
	for _, item := range stored {
		event := item.Event
		if event.Action == "RETIRE" {
			continue
		}
		out := ProjectMemoryItem{SchemaVersion: 1, Source: event.Source, Digest: item.Digest, ContentDigest: event.ContentDigest,
			ID: event.ID, Kind: event.Kind, Key: event.Key, Text: event.Text, EvidenceClass: event.EvidenceClass, AdvisoryOnly: true}
		if event.Source.PolicyDigest != querySource.PolicyDigest {
			out.Redacted = true
			out.Key, out.Text, out.ContentDigest = "", "", ""
		}
		if retiredBy[event.ID] != "" {
			out.Status, out.RetiredBy = "RETIRED", retiredBy[event.ID]
			out.StatusReason = "explicit user retirement"
		} else if supersededBy[event.ID] != "" {
			out.Status, out.SupersededBy = "SUPERSEDED", supersededBy[event.ID]
			out.StatusReason = "explicit user correction"
		} else if source, exists := current[event.Source.TaskID]; !exists || source.State.Deletion != nil {
			out.Status, out.StatusReason = "STALE", "source task is no longer available"
		} else if source.Project != event.Source.ProjectDigest || source.State.BaselineDigest != event.Source.BaselineDigest || source.State.CandidateDigest != event.Source.CandidateDigest || source.State.PolicyEpoch != event.Source.PolicyEpoch {
			out.Status, out.StatusReason = "STALE", "source candidate, baseline or policy epoch changed"
		} else {
			policyDigest, err := projectMemoryPolicyDigest(source.Doc)
			if err != nil {
				return nil, err
			}
			if policyDigest != event.Source.PolicyDigest {
				out.Status, out.StatusReason = "STALE", "source policy changed"
			} else if event.Source.BaselineDigest != querySource.BaselineDigest || event.Source.CandidateDigest != querySource.CandidateDigest ||
				event.Source.PolicyEpoch != querySource.PolicyEpoch || event.Source.PolicyDigest != querySource.PolicyDigest {
				out.Status, out.StatusReason = "STALE", "query task baseline, candidate or policy differs from recorded source"
			} else {
				out.Status = "ACTIVE"
			}
		}
		items = append(items, out)
	}
	conflicts := map[string]map[string]bool{}
	for _, item := range items {
		if item.Status != "ACTIVE" {
			continue
		}
		key := item.Source.ProjectDigest + "\x00" + item.Kind + "\x00" + item.Key
		if conflicts[key] == nil {
			conflicts[key] = map[string]bool{}
		}
		conflicts[key][item.ContentDigest] = true
	}
	for index := range items {
		if items[index].Status == "ACTIVE" && len(conflicts[items[index].Source.ProjectDigest+"\x00"+items[index].Kind+"\x00"+items[index].Key]) > 1 {
			items[index].Status, items[index].StatusReason = "CONFLICT", "different active user assertions share this key"
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Source.TaskID != items[j].Source.TaskID {
			return items[i].Source.TaskID < items[j].Source.TaskID
		}
		if items[i].Source.TaskSeq != items[j].Source.TaskSeq {
			return items[i].Source.TaskSeq < items[j].Source.TaskSeq
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func projectMemoryFilter(items []ProjectMemoryItem, query string, offset, limit int) (ProjectMemorySearchResult, error) {
	result := ProjectMemorySearchResult{SchemaVersion: 1, Items: []ProjectMemoryItem{}, Query: query, Offset: offset, Limit: limit}
	if offset < 0 || limit < 0 || limit > projectMemoryMaxPage || !utf8.ValidString(query) || len(query) > 1024 {
		return result, c.Fail(c.InvalidArgument, "bounded project memory query and page required")
	}
	if limit == 0 {
		limit = 50
		result.Limit = limit
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	filtered := make([]ProjectMemoryItem, 0, len(items))
	for _, item := range items {
		if needle == "" || strings.Contains(strings.ToLower(item.Key), needle) || strings.Contains(strings.ToLower(item.Text), needle) {
			filtered = append(filtered, item)
		}
	}
	result.Total = len(filtered)
	if offset >= len(filtered) {
		return result, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	result.Items = append(result.Items, filtered[offset:end]...)
	result.HasMore = end < len(filtered)
	return result, nil
}

func (s *Session) projectMemorySearchLocked(ctx context.Context, task, query string, offset, limit int) (ProjectMemorySearchResult, []projectMemoryStored, map[string]projectMemoryCurrent, error) {
	var empty ProjectMemorySearchResult
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return empty, nil, nil, err
	}
	querySource, err := s.projectMemorySource(ctx, state, doc)
	if err != nil {
		return empty, nil, nil, err
	}
	projectDigest := querySource.ProjectDigest
	stored, current, err := s.projectMemoryInventory(ctx, projectDigest)
	if err != nil {
		return empty, nil, nil, err
	}
	items, err := projectMemoryBuildItems(stored, current, querySource)
	if err != nil {
		return empty, nil, nil, err
	}
	result, err := projectMemoryFilter(items, query, offset, limit)
	result.ProjectDigest = projectDigest
	result.QuerySource = querySource
	return result, stored, current, err
}

// projectMemoryContextLocked is called from a Session mutation/compile section.
// It uses the exact task state/document already admitted for that run and does
// not acquire s.mu itself.
func (s *Session) projectMemoryContextLocked(ctx context.Context, state c.TaskState, doc Document) ([]ProjectMemoryItem, bool, error) {
	querySource, err := s.projectMemorySource(ctx, state, doc)
	if err != nil {
		return nil, false, err
	}
	stored, current, err := s.projectMemoryInventory(ctx, querySource.ProjectDigest)
	if err != nil {
		return nil, false, err
	}
	items, err := projectMemoryBuildItems(stored, current, querySource)
	if err != nil {
		return nil, false, err
	}
	selected := make([]ProjectMemoryItem, 0, 8)
	usedBytes := 0
	truncated := false
	for _, item := range items {
		if item.Status != "ACTIVE" {
			continue
		}
		// The compiled request is durably retained in the querying task. Until
		// there is an explicit privacy descendant registration, do not copy text
		// from another task into that request; cross-task search stays available.
		if item.Source.TaskID != state.TaskID {
			continue
		}
		encoded, encodeErr := c.CanonicalV1(item)
		if encodeErr != nil {
			return nil, false, encodeErr
		}
		itemBytes := len(encoded)
		if len(selected) == 8 {
			truncated = true
			continue
		}
		if usedBytes+itemBytes > 8<<10 {
			truncated = true
			continue
		}
		selected = append(selected, item)
		usedBytes += itemBytes
	}
	return selected, truncated, nil
}

// SearchProjectMemory returns current-project assertions with explicit
// ACTIVE, STALE, CONFLICT, SUPERSEDED or RETIRED status. Nothing is injected
// into model context or treated as authority.
func (s *Session) SearchProjectMemory(ctx context.Context, task, query string, offset, limit int) (ProjectMemorySearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, _, _, err := s.projectMemorySearchLocked(ctx, task, query, offset, limit)
	return result, err
}

func (s *Session) mutateProjectMemory(ctx context.Context, task, commandID, action, kind, key, text, targetID, targetDigest string) (ProjectMemoryMutationReceipt, error) {
	var receipt ProjectMemoryMutationReceipt
	if !validProjectMemoryCommandID(commandID) {
		return receipt, c.Fail(c.InvalidArgument, "stable project memory command ID required")
	}
	if action != "ASSERT" && action != "CORRECT" && action != "RETIRE" {
		return receipt, c.Fail(c.InvalidArgument, "project memory action must be record, correct or retire")
	}
	if action == "ASSERT" || action == "CORRECT" {
		kind = strings.ToUpper(strings.TrimSpace(kind))
		key = strings.ToLower(strings.TrimSpace(key))
		if (kind != "FACT" && kind != "DECISION" && kind != "CONVENTION") || !projectMemoryKeyPattern.MatchString(key) || !validProjectMemoryText(text) {
			return receipt, c.Fail(c.InvalidArgument, "bounded FACT/DECISION/CONVENTION kind, key and text required")
		}
	}
	if action == "RETIRE" || action == "CORRECT" {
		if !projectMemoryIDPattern.MatchString(targetID) || !c.ValidDigest(targetDigest) {
			return receipt, c.Fail(c.InvalidArgument, "exact project memory target ID and digest required")
		}
	}
	if action == "RETIRE" && (kind != "" || key != "" || text != "") {
		return receipt, c.Fail(c.InvalidArgument, "retirement takes only the exact target")
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if err := s.Journal.Writable(); err != nil {
		return receipt, err
	}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return receipt, err
	}
	if state.Execution == c.Running || state.Execution == c.Verifying || state.Execution == c.Delivering || state.Execution == c.Scoping || state.Execution == c.Pausing || state.Execution == c.Recovering {
		return receipt, c.Fail(c.Conflict, "project memory mutation requires a quiescent task")
	}
	source, err := s.projectMemorySource(ctx, state, doc)
	if err != nil {
		return receipt, err
	}
	request := projectMemoryRequest{ProjectDigest: source.ProjectDigest, Action: action, Kind: kind, Key: key, Text: text, TargetID: targetID, TargetDigest: targetDigest}
	requestDigest, err := projectMemoryRequestDigest(request)
	if err != nil {
		return receipt, err
	}
	stored, current, err := s.projectMemoryInventory(ctx, "")
	if err != nil {
		return receipt, err
	}
	for _, existing := range stored {
		if existing.Event.CommandID != commandID {
			continue
		}
		if existing.Event.RequestDigest != requestDigest || existing.Event.Source.ProjectDigest != source.ProjectDigest {
			return receipt, c.Fail(c.CommandIDConflict, "project memory command ID reused with different content")
		}
		itemID := existing.Event.ID
		status := "RECORDED"
		if action == "RETIRE" {
			itemID, status = existing.Event.TargetID, "RETIRED"
		} else if action == "CORRECT" {
			status = "CORRECTED"
		}
		return ProjectMemoryMutationReceipt{SchemaVersion: 1, Source: existing.Event.Source, EventDigest: existing.Digest, RequestDigest: requestDigest,
			CommandID: commandID, EventID: existing.Event.ID, ItemID: itemID, TargetID: existing.Event.TargetID, Operation: existing.Event.Action, Status: status, AdvisoryOnly: true}, nil
	}
	if len(stored) >= projectMemoryMaxEvents {
		return receipt, c.Fail(c.BudgetLimitReached, "project memory inventory is full; no records were discarded")
	}
	if len(doc.ProjectMemoryRefs) >= projectMemoryMaxEventsPerTask {
		return receipt, c.Fail(c.BudgetLimitReached, "task project memory event limit reached; no records were discarded")
	}
	if action == "RETIRE" || action == "CORRECT" {
		items, buildErr := projectMemoryBuildItems(stored, current, source)
		if buildErr != nil {
			return receipt, buildErr
		}
		found := false
		for _, item := range items {
			if item.ID == targetID && item.Digest == targetDigest {
				if item.Source.TaskID != task {
					return receipt, c.Fail(c.PolicyDenied, "correction and retirement must stay in the source task so its privacy deletion covers the full lineage")
				}
				if item.Source.ProjectDigest != source.ProjectDigest {
					return receipt, c.Fail(c.StaleBase, "project memory target belongs to another project")
				}
				found = true
				if item.Status == "RETIRED" || item.Status == "SUPERSEDED" {
					return receipt, c.Fail(c.StaleBase, "project memory target is already retired or superseded")
				}
				break
			}
		}
		if !found {
			return receipt, c.Fail(c.StaleBase, "project memory target is not present at the current project inventory")
		}
	}
	previous := ""
	if len(doc.ProjectMemoryRefs) > 0 {
		previous = doc.ProjectMemoryRefs[len(doc.ProjectMemoryRefs)-1].Digest
	}
	event := ProjectMemoryEvent{SchemaVersion: 1, ID: newID("pm-"), Action: action, Kind: kind, Key: key, Text: text,
		TargetID: targetID, TargetDigest: targetDigest, CommandID: commandID, RequestDigest: requestDigest,
		EvidenceClass: "USER_ASSERTED", Source: source, PreviousEventDigest: previous}
	if action == "ASSERT" || action == "CORRECT" {
		event.ContentDigest = c.HashBytes([]byte(text))
	}
	ref := ProjectMemoryRef{ID: event.ID}
	if err = event.Validate(ProjectMemoryRef{ID: event.ID, Digest: c.HashBytes([]byte("pending"))}, task); err != nil {
		// Digest is filled after canonical encoding; all other constraints are
		// validated by passing a syntactically valid placeholder here.
		return receipt, err
	}
	raw, err := c.CanonicalV1(event)
	if err != nil {
		return receipt, err
	}
	if len(raw) > projectMemoryEventMaxBytes {
		return receipt, c.Fail(c.BudgetLimitReached, "project memory event exceeds its byte bound")
	}
	ref.Digest, err = s.Archive.PutBytes(task, raw)
	if err != nil {
		return receipt, err
	}
	doc.ProjectMemoryRefs = append(doc.ProjectMemoryRefs, ref)
	if _, err = s.record(ctx, state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		return receipt, err
	}
	status := "RECORDED"
	itemID := event.ID
	if action == "RETIRE" {
		status, itemID = "RETIRED", targetID
	} else if action == "CORRECT" {
		status = "CORRECTED"
	}
	return ProjectMemoryMutationReceipt{SchemaVersion: 1, Source: source, EventDigest: ref.Digest, RequestDigest: requestDigest,
		CommandID: commandID, EventID: event.ID, ItemID: itemID, TargetID: targetID, Operation: action, Status: status, AdvisoryOnly: true}, nil
}

// RecordProjectMemory adds a user-authored fact, decision or convention.
func (s *Session) RecordProjectMemory(ctx context.Context, task, commandID, kind, key, text string) (ProjectMemoryMutationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateProjectMemory(ctx, task, commandID, "ASSERT", kind, key, text, "", "")
}

// RetireProjectMemory records explicit retirement of an exact current fact.
func (s *Session) RetireProjectMemory(ctx context.Context, task, commandID, targetID, targetDigest string) (ProjectMemoryMutationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateProjectMemory(ctx, task, commandID, "RETIRE", "", "", "", targetID, targetDigest)
}

// CorrectProjectMemory appends a replacement and links it to an exact prior
// entry; the previous text remains visible as SUPERSEDED until task deletion.
func (s *Session) CorrectProjectMemory(ctx context.Context, task, commandID, targetID, targetDigest, kind, key, text string) (ProjectMemoryMutationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateProjectMemory(ctx, task, commandID, "CORRECT", kind, key, text, targetID, targetDigest)
}
