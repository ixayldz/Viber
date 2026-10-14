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
	"github.com/ixayldz/Viber/internal/store"
)

const (
	projectMemoryMaxEventsPerTask = 256
	projectMemoryMaxCopiesPerTask = 8
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
var projectMemoryCopyIDPattern = regexp.MustCompile(`^pmcopy-[0-9a-f]{32}$`)
var projectMemoryKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// ProjectMemoryRef is a managed, task-scoped archive pointer. The memory
// record itself is immutable; the task journal binds this list in each
// document revision. No separate project index or authority is introduced.
type ProjectMemoryRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Type   string `json:"type,omitempty"`
}

// ProjectMemoryCopy is a distinct user-selected transfer of one active
// assertion into another task. It preserves both the original source cursor
// and the target task cursor; the content remains advisory and grants no
// policy or verification authority.
type ProjectMemoryCopy struct {
	Origin               ProjectMemorySource `json:"origin"`
	Target               ProjectMemorySource `json:"target"`
	TransferPolicyDigest string              `json:"transfer_policy_digest"`
	RequestDigest        string              `json:"request_digest"`
	ContentDigest        string              `json:"content_digest"`
	PreviousRef          string              `json:"previous_ref_digest,omitempty"`
	SourceItemID         string              `json:"source_item_id"`
	SourceItemDigest     string              `json:"source_item_digest"`
	CommandID            string              `json:"command_id"`
	ID                   string              `json:"id"`
	Kind                 string              `json:"kind"`
	Key                  string              `json:"key"`
	Text                 string              `json:"text"`
	EvidenceClass        string              `json:"evidence_class"`
	SchemaVersion        int                 `json:"schema_version"`
}

type ProjectMemoryCopyReceipt struct {
	SchemaVersion    int    `json:"schema_version"`
	SourceTask       string `json:"source_task"`
	TargetTask       string `json:"target_task"`
	SourceItemID     string `json:"source_item_id"`
	SourceItemDigest string `json:"source_item_digest"`
	CopyID           string `json:"copy_id"`
	CopyDigest       string `json:"copy_digest"`
	RequestDigest    string `json:"request_digest"`
	Status           string `json:"status"`
	AdvisoryOnly     bool   `json:"advisory_only"`
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
	Source               ProjectMemorySource  `json:"source"`
	Origin               *ProjectMemorySource `json:"origin,omitempty"`
	TransferPolicyDigest string               `json:"transfer_policy_digest,omitempty"`
	SourceItemID         string               `json:"source_item_id,omitempty"`
	SourceItemDigest     string               `json:"source_item_digest,omitempty"`
	Digest               string               `json:"digest"`
	ContentDigest        string               `json:"content_digest"`
	ID                   string               `json:"id"`
	Kind                 string               `json:"kind"`
	Key                  string               `json:"key"`
	Text                 string               `json:"text"`
	Status               string               `json:"status"`
	StatusReason         string               `json:"status_reason,omitempty"`
	RetiredBy            string               `json:"retired_by,omitempty"`
	SupersededBy         string               `json:"superseded_by,omitempty"`
	Redacted             bool                 `json:"redacted,omitempty"`
	EvidenceClass        string               `json:"evidence_class"`
	AdvisoryOnly         bool                 `json:"advisory_only"`
	SchemaVersion        int                  `json:"schema_version"`
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
	TaskID  string
	Event   ProjectMemoryEvent
	Copy    *ProjectMemoryCopy
	CopyPin *privacyMemoryCopy
	Digest  string
}

type projectMemoryCurrent struct {
	State    c.TaskState
	Doc      Document
	Project  string
	Readable bool
}

type projectMemorySourceBudget struct {
	seen             map[string]bool
	validated        map[string]ProjectMemorySource
	privacy          *privacyView
	copyPins         map[string]privacyMemoryCopy
	copyHydration    map[string]bool
	copyHydrationSet map[string]bool
	copyOrigins      map[string]projectMemoryCurrent
	copyOriginSet    map[string]bool
	bytes            int64
}

type projectMemoryConflictIndexError struct {
	incomplete bool
	cause      error
}

func (e *projectMemoryConflictIndexError) Error() string {
	if e.cause != nil {
		return "project memory conflict index unavailable: " + e.cause.Error()
	}
	return "project memory conflict index incomplete"
}

func (e *projectMemoryConflictIndexError) Unwrap() error { return e.cause }

func (copy ProjectMemoryCopy) Validate(ref ProjectMemoryRef, targetTask string) error {
	if copy.SchemaVersion != 1 || ref.Type != "PROJECT_MEMORY_COPY" || ref.ID != copy.ID || !projectMemoryCopyIDPattern.MatchString(copy.ID) ||
		!c.ValidDigest(ref.Digest) || !c.ValidDigest(copy.RequestDigest) || !c.ValidDigest(copy.ContentDigest) ||
		!projectMemoryIDPattern.MatchString(copy.SourceItemID) || !c.ValidDigest(copy.SourceItemDigest) ||
		!validProjectMemoryCommandID(copy.CommandID) || copy.Target.TaskID != targetTask || copy.Origin.TaskID == "" || copy.Origin.TaskID == targetTask ||
		copy.Origin.ProjectDigest != copy.Target.ProjectDigest || copy.Origin.PolicyEpoch < 0 || copy.Target.PolicyEpoch < 0 ||
		copy.Target.TaskSeq < 1 || copy.Target.TaskSeq > 1<<53 || !c.ValidDigest(copy.Target.DocumentDigest) ||
		!c.ValidDigest(copy.Target.BaselineDigest) || !c.ValidDigest(copy.Target.CandidateDigest) || !c.ValidDigest(copy.Target.ProjectDigest) || !c.ValidDigest(copy.Target.PolicyDigest) ||
		copy.Origin.TaskSeq < 1 || copy.Origin.TaskSeq > 1<<53 || !c.ValidDigest(copy.Origin.DocumentDigest) ||
		!c.ValidDigest(copy.Origin.BaselineDigest) || !c.ValidDigest(copy.Origin.CandidateDigest) || !c.ValidDigest(copy.Origin.ProjectDigest) || !c.ValidDigest(copy.Origin.PolicyDigest) ||
		copy.Origin.PolicyDigest != copy.Target.PolicyDigest || copy.Origin.PolicyEpoch != copy.Target.PolicyEpoch || !c.ValidDigest(copy.TransferPolicyDigest) ||
		(copy.Kind != "FACT" && copy.Kind != "DECISION" && copy.Kind != "CONVENTION") || !projectMemoryKeyPattern.MatchString(copy.Key) ||
		!validProjectMemoryText(copy.Text) || c.HashBytes([]byte(copy.Text)) != copy.ContentDigest || copy.EvidenceClass != "USER_COPIED" ||
		copy.PreviousRef != "" && !c.ValidDigest(copy.PreviousRef) {
		return c.Fail(c.StoreIntegrityError, "invalid source-bound project memory copy")
	}
	return nil
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

// projectMemoryEffectivePolicyDigest binds the retained task profile and the
// live operator restrictions. Cross-task copies require exact equality at
// publication and recheck it before presenting copied text.
func projectMemoryEffectivePolicyDigest(doc Document) (string, error) {
	retained, err := projectMemoryPolicyDigest(doc)
	if err != nil {
		return "", err
	}
	var operator *config.Authority
	if doc.Config != nil && doc.Config.Authority != nil {
		current, currentErr := doc.Config.Authority.Current()
		if currentErr != nil {
			return "", currentErr
		}
		operator = &current
	}
	return c.Digest(struct {
		Retained string            `json:"retained_policy_digest"`
		Operator *config.Authority `json:"current_operator_policy,omitempty"`
	}{Retained: retained, Operator: operator})
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
func (s *Session) validateProjectMemoryRefs(ctx context.Context, state c.TaskState, doc Document, sourceBudget *projectMemorySourceBudget) ([]projectMemoryStored, error) {
	if len(doc.ProjectMemoryRefs) > projectMemoryMaxEventsPerTask {
		return nil, c.Fail(c.BudgetLimitReached, "project memory task event limit reached")
	}
	if state.ProjectMemoryRevision < 0 || state.ProjectMemoryRevision > int64(len(doc.ProjectMemoryRefs)) ||
		state.ProjectMemoryCopyCount < 0 || state.ProjectMemoryCopyCount > int64(len(doc.ProjectMemoryRefs)) {
		return nil, c.Fail(c.StoreIntegrityError, "project memory revision exceeds its retained event history")
	}
	if sourceBudget == nil {
		sourceBudget = &projectMemorySourceBudget{}
	}
	stored := make([]projectMemoryStored, 0, len(doc.ProjectMemoryRefs))
	seen := make(map[string]bool, len(doc.ProjectMemoryRefs))
	previous := ""
	copyCount := int64(0)
	var totalBytes int64
	for _, ref := range doc.ProjectMemoryRefs {
		if seen[ref.ID] {
			return nil, c.Fail(c.StoreIntegrityError, "duplicate project memory event reference")
		}
		seen[ref.ID] = true
		if ref.Type == "PROJECT_MEMORY_COPY" {
			pin, mayHydrate, pinErr := s.projectMemoryCopyHydrationAllowed(ctx, state, doc, ref, sourceBudget)
			if pinErr != nil {
				return nil, pinErr
			}
			if !mayHydrate {
				totalBytes += pin.CopyBytes
				if totalBytes > projectMemoryMaxBytes {
					return nil, c.Fail(c.BudgetLimitReached, "project memory task event bytes exceed bound")
				}
				stored = append(stored, projectMemoryStored{TaskID: state.TaskID, CopyPin: &pin, Digest: ref.Digest})
				copyCount++
				if copyCount > projectMemoryMaxCopiesPerTask {
					return nil, c.Fail(c.BudgetLimitReached, "task project memory copy limit reached")
				}
				previous = ref.Digest
				continue
			}
		}
		raw, err := s.Archive.GetBytes(state.TaskID, ref.Digest)
		if err != nil {
			return nil, err
		}
		totalBytes += int64(len(raw))
		if len(raw) > projectMemoryEventMaxBytes || totalBytes > projectMemoryMaxBytes {
			return nil, c.Fail(c.BudgetLimitReached, "project memory task event bytes exceed bound")
		}
		if c.HashBytes(raw) != ref.Digest {
			return nil, c.Fail(c.StoreIntegrityError, "project memory record digest mismatch")
		}
		if ref.Type == "" {
			var event ProjectMemoryEvent
			if err = c.DecodeStrict(raw, &event); err != nil || event.Validate(ref, state.TaskID) != nil {
				return nil, c.Fail(c.StoreIntegrityError, "project memory record failed validation")
			}
			canonical, encodeErr := c.CanonicalV1(event)
			if encodeErr != nil || !bytes.Equal(raw, canonical) || event.PreviousEventDigest != previous || event.Source.TaskSeq >= state.TaskSeq {
				return nil, c.Fail(c.StoreIntegrityError, "project memory record lineage or canonical bytes mismatch")
			}
			if err = s.validateProjectMemorySource(ctx, event.Source, sourceBudget); err != nil {
				return nil, err
			}
			stored = append(stored, projectMemoryStored{TaskID: state.TaskID, Event: event, Digest: ref.Digest})
		} else if ref.Type == "PROJECT_MEMORY_COPY" {
			var copy ProjectMemoryCopy
			if err = c.DecodeStrict(raw, &copy); err != nil || copy.Validate(ref, state.TaskID) != nil {
				return nil, c.Fail(c.StoreIntegrityError, "project memory copy failed validation")
			}
			canonical, encodeErr := c.CanonicalV1(copy)
			if encodeErr != nil || !bytes.Equal(raw, canonical) || copy.PreviousRef != previous || copy.Target.TaskSeq >= state.TaskSeq {
				return nil, c.Fail(c.StoreIntegrityError, "project memory copy lineage or canonical bytes mismatch")
			}
			if err = s.validateProjectMemorySource(ctx, copy.Origin, sourceBudget); err != nil {
				return nil, err
			}
			if err = s.validateProjectMemorySource(ctx, copy.Target, sourceBudget); err != nil {
				return nil, err
			}
			if err = s.validateProjectMemoryCopyPin(ctx, state, copy, ref, sourceBudget); err != nil {
				return nil, err
			}
			stored = append(stored, projectMemoryStored{TaskID: state.TaskID, Copy: &copy, Digest: ref.Digest})
			copyCount++
			if copyCount > projectMemoryMaxCopiesPerTask {
				return nil, c.Fail(c.BudgetLimitReached, "task project memory copy limit reached")
			}
		} else {
			return nil, c.Fail(c.StoreIntegrityError, "unknown project memory reference type")
		}
		previous = ref.Digest
	}
	if copyCount != state.ProjectMemoryCopyCount {
		return nil, c.Fail(c.StoreIntegrityError, "project memory copy count differs from retained typed references")
	}
	return stored, nil
}

func (s *Session) validateProjectMemorySource(ctx context.Context, source ProjectMemorySource, sourceBudget *projectMemorySourceBudget) error {
	if source.TaskID == "" || source.TaskSeq < 1 || !c.ValidDigest(source.DocumentDigest) {
		return c.Fail(c.StoreIntegrityError, "project memory source cursor is invalid")
	}
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
	sourceState, err := s.Journal.ReplayAt(ctx, source.TaskID, source.TaskSeq)
	if err != nil || sourceState.TaskID != source.TaskID || sourceState.TaskSeq != source.TaskSeq ||
		sourceState.DocumentDigest != source.DocumentDigest || sourceState.BaselineDigest != source.BaselineDigest ||
		sourceState.CandidateDigest != source.CandidateDigest || sourceState.PolicyEpoch != source.PolicyEpoch {
		return c.Fail(c.StoreIntegrityError, "project memory source does not match its exact journal cursor")
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

func privacyMemoryCopyOwnerScopeMatches(view privacyView, scopes []privacyScope, originScope, current privacyMemoryScope, targetTask string, pin *store.PrivacyCheckpointPin) bool {
	originRegistered, currentOwnerFound := false, false
	var currentOwner privacyScope
	for _, scope := range scopes {
		memoryScope := privacyMemoryScopeFromOwner(scope)
		if memoryScope == originScope {
			originRegistered = true
		}
		if memoryScope == current {
			currentOwner, currentOwnerFound = scope, true
		}
	}
	if !originRegistered {
		return false
	}
	if originScope == current {
		return true
	}
	// An owner whose whole lineage never took a privacy checkpoint carries no
	// anchor: that state is only admissible when no pin exists anywhere.
	anchorless := false
	if currentOwnerFound {
		if currentOwner.CheckpointAnchor == "" {
			if currentOwner.RestoreBinding != "" || pin != nil {
				return false
			}
			anchorless = true
		} else if !c.ValidDigest(currentOwner.CheckpointAnchor) || !c.ValidDigest(currentOwner.RestoreBinding) {
			return false
		}
	}
	for _, restore := range view.Copies {
		if restore.Kind != "RESTORE_STAGE" || restore.RestoreInstanceID != current.InstanceID || restore.PhysicalRoot != current.PhysicalRoot ||
			!c.ValidDigest(restore.RestoreSourcePhysicalRoot) || !privacyManagedCopyHasTask(restore, targetTask) {
			continue
		}
		if currentOwnerFound && restoreCopyIsCurrentOwner(restore, scopes) && restore.Directory == currentOwner.Directory && restore.RestoreTarget == currentOwner.Directory {
			if anchorless && restore.RestoreAnchor == "" && restore.RestorePinnedAnchor == "" && restore.RestorePinnedBinding == "" &&
				pin == nil && c.ValidDigest(restore.RestoreBinding) {
				return true
			}
			if !anchorless && restore.RestoreAnchor == currentOwner.CheckpointAnchor && restore.RestoreBinding == currentOwner.RestoreBinding &&
				restore.RestorePinnedAnchor == currentOwner.CheckpointAnchor && restore.RestorePinnedBinding == currentOwner.RestoreBinding && pin != nil &&
				pin.InstanceID == current.InstanceID && pin.PhysicalRoot == current.PhysicalRoot && pin.AnchorDigest == currentOwner.CheckpointAnchor &&
				pin.RestoreBinding == currentOwner.RestoreBinding {
				return true
			}
		}
		// During Restore's private stage, the owner scope is intentionally not
		// published yet. The exact registered stage allocation and its rebased
		// checkpoint pin still bind the copied database before final rename.
		if !currentOwnerFound && restore.Directory == current.Directory && restore.RestoreStageDirectory == current.Directory && restore.RestoreTarget != current.Directory &&
			restore.RestoreInstanceID == current.InstanceID && restore.PhysicalRoot == current.PhysicalRoot &&
			c.ValidDigest(restore.RestoreSourcePhysicalRoot) && privacyManagedCopyHasTask(restore, targetTask) {
			if restore.RestoreAnchor == "" && restore.RestorePinnedAnchor == "" && restore.RestorePinnedBinding == "" &&
				pin == nil && c.ValidDigest(restore.RestoreBinding) {
				return true
			}
			if restore.RestoreAnchor != "" && restore.RestoreBinding != "" && restore.RestorePinnedAnchor == restore.RestoreAnchor &&
				restore.RestorePinnedBinding == restore.RestoreBinding && pin != nil && pin.InstanceID == current.InstanceID &&
				pin.PhysicalRoot == current.PhysicalRoot && pin.AnchorDigest == restore.RestoreAnchor && pin.RestoreBinding == restore.RestoreBinding {
				return true
			}
		}
	}
	return false
}

func privacyManagedCopyHasTask(copy ManagedCopy, task string) bool {
	for _, candidate := range copy.Tasks {
		if candidate == task {
			return true
		}
	}
	return false
}

func (s *Session) projectMemoryCopyPinForRef(ctx context.Context, targetState c.TaskState, ref ProjectMemoryRef, sourceBudget *projectMemorySourceBudget) (privacyMemoryCopy, error) {
	var empty privacyMemoryCopy
	if s.privacy == nil || s.privacyOwner == nil {
		return empty, c.Fail(c.UnsupportedCapability, "project memory copy requires the current external privacy authority")
	}
	view := privacyView{}
	if sourceBudget != nil && sourceBudget.privacy != nil {
		view = *sourceBudget.privacy
	} else {
		var err error
		view, err = s.retentionView(ctx)
		if err != nil {
			return empty, err
		}
		if sourceBudget != nil {
			sourceBudget.privacy = &view
		}
	}
	expectedScope, err := s.currentPrivacyMemoryScope()
	if err != nil {
		return empty, err
	}
	registeredScopes, _, err := readPrivacyScopes(s.privacyOwner.root)
	if err != nil {
		return empty, err
	}
	currentPin, err := s.Journal.PrivacyCheckpointPin(ctx)
	if err != nil {
		return empty, err
	}
	var matched *privacyMemoryCopy
	for i := range view.MemoryCopies {
		candidate := &view.MemoryCopies[i]
		if candidate.CopyID != ref.ID {
			continue
		}
		matched = candidate
		break
	}
	if matched == nil {
		return empty, c.Fail(c.StaleAuthority, "project memory copy has no exact active current-authority lineage")
	}
	if matched.Status != "ACTIVE" {
		return empty, c.Fail(c.StaleAuthority, "project memory copy has no exact active current-authority lineage")
	}
	if !privacyMemoryCopyOwnerScopeMatches(view, registeredScopes, matched.Scope, expectedScope, targetState.TaskID, currentPin) {
		return empty, c.Fail(c.StaleAuthority, "project memory copy owner scope is not bound to the current or exact registered restore")
	}
	if matched.CopyDigest != ref.Digest || matched.Target.TaskID != targetState.TaskID || matched.CopyID != ref.ID || matched.TargetTaskSequence > targetState.TaskSeq {
		return empty, c.Fail(c.StaleAuthority, "project memory copy reference differs from its active authority pin")
	}
	historical, err := s.Journal.ReplayAt(ctx, targetState.TaskID, matched.TargetTaskSequence)
	if err != nil || historical.DocumentDigest != matched.TargetDocument || historical.ProjectMemoryCopyCount < 1 || historical.TaskSeq != matched.TargetTaskSequence {
		return empty, c.Fail(c.StoreIntegrityError, "project memory copy target journal receipt is missing or changed")
	}
	if sourceBudget != nil {
		if sourceBudget.copyPins == nil {
			sourceBudget.copyPins = map[string]privacyMemoryCopy{}
		}
		sourceBudget.copyPins[matched.CopyID] = *matched
	}
	return *matched, nil
}

func (s *Session) validateProjectMemoryCopyPin(ctx context.Context, targetState c.TaskState, copy ProjectMemoryCopy, ref ProjectMemoryRef, sourceBudget *projectMemorySourceBudget) error {
	matched, err := s.projectMemoryCopyPinForRef(ctx, targetState, ref, sourceBudget)
	if err != nil {
		return err
	}
	copyRaw, err := c.CanonicalV1(copy)
	if err != nil {
		return err
	}
	copyDigest := c.HashBytes(copyRaw)
	if matched.CopyDigest != copyDigest || matched.SourceItemID != copy.SourceItemID || matched.SourceItemDigest != copy.SourceItemDigest ||
		matched.RequestDigest != copy.RequestDigest || matched.CommandID != copy.CommandID || matched.Target != copy.Target || matched.Origin != copy.Origin {
		return c.Fail(c.StoreIntegrityError, "project memory copy authority binding differs from its immutable event")
	}
	return nil
}

// projectMemoryCopyHydrationAllowed checks the current source policy before it
// opens a copied-memory blob. A stale/denied source returns only its registered
// pin so callers can show a redacted status without exposing old text or key.
func (s *Session) projectMemoryCopyHydrationAllowed(ctx context.Context, targetState c.TaskState, targetDoc Document, ref ProjectMemoryRef, budget *projectMemorySourceBudget) (privacyMemoryCopy, bool, error) {
	pin, err := s.projectMemoryCopyPinForRef(ctx, targetState, ref, budget)
	if err != nil {
		return privacyMemoryCopy{}, false, err
	}
	if budget != nil && budget.copyHydrationSet[pin.CopyID] {
		return pin, budget.copyHydration[pin.CopyID], nil
	}
	allowed := false
	targetPolicy, targetPolicyErr := projectMemoryEffectivePolicyDigest(targetDoc)
	if targetPolicyErr != nil || targetPolicy != pin.TransferPolicyDigest {
		return s.cacheProjectMemoryCopyHydration(budget, pin, false), false, nil
	}
	if budget != nil && budget.privacy != nil {
		if _, deleted := budget.privacy.DeletionSequences[pin.Origin.TaskID]; deleted {
			return s.cacheProjectMemoryCopyHydration(budget, pin, false), false, nil
		}
	}
	if err = s.validateProjectMemorySource(ctx, pin.Origin, budget); err != nil {
		return privacyMemoryCopy{}, false, err
	}
	origin := projectMemoryCurrent{}
	if budget != nil && budget.copyOriginSet[pin.Origin.TaskID] {
		origin = budget.copyOrigins[pin.Origin.TaskID]
	} else {
		states, replayErr := s.Journal.Replay(ctx, pin.Origin.TaskID)
		if replayErr != nil {
			return privacyMemoryCopy{}, false, replayErr
		}
		originState, found := states[pin.Origin.TaskID]
		if !found || originState.Deletion != nil {
			origin = projectMemoryCurrent{State: originState, Project: pin.Origin.ProjectDigest, Readable: false}
		} else {
			originRaw, readErr := s.Archive.GetBytes(pin.Origin.TaskID, originState.DocumentDigest)
			if readErr != nil {
				return privacyMemoryCopy{}, false, readErr
			}
			if len(originRaw) > projectMemoryMaxSourceDocSize {
				return privacyMemoryCopy{}, false, c.Fail(c.BudgetLimitReached, "project memory current source document exceeds its bound")
			}
			if budget != nil {
				if budget.seen == nil {
					budget.seen = map[string]bool{}
				}
				key := pin.Origin.TaskID + "\x00" + originState.DocumentDigest
				if !budget.seen[key] {
					budget.seen[key] = true
					budget.bytes += int64(len(originRaw))
					if budget.bytes > projectMemoryMaxSourceBytes {
						return privacyMemoryCopy{}, false, c.Fail(c.BudgetLimitReached, "project memory current source inventory exceeds bound")
					}
				}
			}
			var originDoc Document
			if c.DecodeStrict(originRaw, &originDoc) != nil || originDoc.SchemaVersion != 1 || originDoc.TaskID != pin.Origin.TaskID ||
				originDoc.Spec.TaskID != pin.Origin.TaskID || originDoc.Baseline.TaskID != pin.Origin.TaskID || originDoc.Candidate.TaskID != pin.Origin.TaskID ||
				originDoc.Baseline.SnapshotDigest != originState.BaselineDigest || originDoc.Candidate.SnapshotDigest != originState.CandidateDigest {
				return privacyMemoryCopy{}, false, c.Fail(c.StoreIntegrityError, "project memory current source document binding mismatch")
			}
			canonical, canonicalErr := c.CanonicalV1(originDoc)
			if canonicalErr != nil || !bytes.Equal(originRaw, canonical) {
				return privacyMemoryCopy{}, false, c.Fail(c.StoreIntegrityError, "project memory current source document is not canonical")
			}
			if configErr := validateTaskConfig(originDoc); configErr != nil {
				return privacyMemoryCopy{}, false, configErr
			}
			originProject, projectErr := s.projectMemoryProject(originDoc)
			if projectErr != nil {
				return privacyMemoryCopy{}, false, projectErr
			}
			origin = projectMemoryCurrent{State: originState, Doc: originDoc, Project: originProject, Readable: currentOperatorEffect(originDoc, "context.read") == nil}
		}
		if budget != nil {
			if budget.copyOrigins == nil {
				budget.copyOrigins = map[string]projectMemoryCurrent{}
			}
			if budget.copyOriginSet == nil {
				budget.copyOriginSet = map[string]bool{}
			}
			budget.copyOrigins[pin.Origin.TaskID] = origin
			budget.copyOriginSet[pin.Origin.TaskID] = true
		}
	}
	if !origin.Readable || origin.State.Deletion != nil || origin.Project != pin.Origin.ProjectDigest ||
		origin.State.BaselineDigest != pin.Origin.BaselineDigest || origin.State.CandidateDigest != pin.Origin.CandidateDigest || origin.State.PolicyEpoch != pin.Origin.PolicyEpoch {
		return s.cacheProjectMemoryCopyHydration(budget, pin, false), false, nil
	}
	originPolicy, originPolicyErr := projectMemoryEffectivePolicyDigest(origin.Doc)
	if originPolicyErr != nil || originPolicy != pin.TransferPolicyDigest {
		return s.cacheProjectMemoryCopyHydration(budget, pin, false), false, nil
	}
	allowed = true
	return s.cacheProjectMemoryCopyHydration(budget, pin, allowed), allowed, nil
}

func (s *Session) cacheProjectMemoryCopyHydration(budget *projectMemorySourceBudget, pin privacyMemoryCopy, allowed bool) privacyMemoryCopy {
	if budget != nil {
		if budget.copyPins == nil {
			budget.copyPins = map[string]privacyMemoryCopy{}
		}
		if budget.copyHydration == nil {
			budget.copyHydration = map[string]bool{}
		}
		if budget.copyHydrationSet == nil {
			budget.copyHydrationSet = map[string]bool{}
		}
		budget.copyPins[pin.CopyID] = pin
		budget.copyHydration[pin.CopyID] = allowed
		budget.copyHydrationSet[pin.CopyID] = true
	}
	return pin
}

func (s *Session) projectMemoryInventory(ctx context.Context, projectDigest string) ([]projectMemoryStored, map[string]projectMemoryCurrent, error) {
	return s.projectMemoryInventoryScoped(ctx, projectDigest, "")
}

// projectMemoryInventoryScoped narrows automatic context to one task before
// loading any task document or project-memory event. Manual search keeps the
// whole-project inventory through projectMemoryInventory.
func (s *Session) projectMemoryInventoryScoped(ctx context.Context, projectDigest, taskFilter string) ([]projectMemoryStored, map[string]projectMemoryCurrent, error) {
	states, err := s.Journal.Replay(ctx, taskFilter)
	if err != nil {
		return nil, nil, err
	}
	if len(states) > projectMemoryMaxTasks {
		return nil, nil, c.Fail(c.BudgetLimitReached, "project memory task inventory exceeds bound")
	}
	tasks := make([]string, 0, len(states))
	for task := range states {
		if taskFilter != "" && task != taskFilter {
			continue
		}
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
		sourceBudget.privacy = privacy
	}
	var selectedCopies []ProjectMemoryCopy
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
		readable := currentOperatorEffect(doc, "context.read") == nil
		current[task] = projectMemoryCurrent{State: state, Doc: doc, Project: currentProjectDigest, Readable: readable}
		if !readable {
			// Do not open this task's memory bodies when its current policy denies
			// source reads. Metadata can still be represented as unavailable.
			continue
		}
		if projectDigest != "" && currentProjectDigest != projectDigest || len(doc.ProjectMemoryRefs) == 0 {
			continue
		}
		refs, validateErr := s.validateProjectMemoryRefs(ctx, state, doc, sourceBudget)
		if validateErr != nil {
			return nil, nil, validateErr
		}
		for index, ref := range doc.ProjectMemoryRefs {
			encodedSize := int64(0)
			if refs[index].CopyPin != nil {
				encodedSize = refs[index].CopyPin.CopyBytes
			} else {
				encoded, encodeErr := s.Archive.GetBytes(task, ref.Digest)
				if encodeErr != nil {
					return nil, nil, encodeErr
				}
				encodedSize = int64(len(encoded))
			}
			totalBytes += encodedSize
			if len(stored) >= projectMemoryMaxEvents || totalBytes > projectMemoryMaxBytes {
				return nil, nil, c.Fail(c.BudgetLimitReached, "project memory inventory exceeds bound")
			}
			stored = append(stored, refs[index])
			if taskFilter != "" && refs[index].Copy != nil {
				selectedCopies = append(selectedCopies, *refs[index].Copy)
			}
		}
	}
	if taskFilter != "" && len(selectedCopies) > 0 {
		loadedOrigins := map[string]bool{}
		for _, copy := range selectedCopies {
			if loadedOrigins[copy.Origin.TaskID] {
				continue
			}
			loadedOrigins[copy.Origin.TaskID] = true
			originStates, replayErr := s.Journal.Replay(ctx, copy.Origin.TaskID)
			if replayErr != nil {
				return nil, nil, replayErr
			}
			originState, ok := originStates[copy.Origin.TaskID]
			privacyDeleted := false
			if privacy != nil {
				_, privacyDeleted = privacy.DeletionSequences[copy.Origin.TaskID]
			}
			if !ok || originState.Deletion != nil || privacy != nil && privacyDeleted {
				current[copy.Origin.TaskID] = projectMemoryCurrent{State: originState, Project: copy.Origin.ProjectDigest, Readable: false}
				continue
			}
			originRaw, originReadErr := s.Archive.GetBytes(copy.Origin.TaskID, originState.DocumentDigest)
			if originReadErr != nil {
				return nil, nil, originReadErr
			}
			documentBytes += int64(len(originRaw))
			if documentBytes > projectMemoryMaxDocumentBytes {
				return nil, nil, c.Fail(c.BudgetLimitReached, "project memory source document inventory exceeds bound")
			}
			var originDoc Document
			if originReadErr = c.DecodeStrict(originRaw, &originDoc); originReadErr != nil || originDoc.SchemaVersion != 1 || originDoc.TaskID != copy.Origin.TaskID ||
				originDoc.Spec.TaskID != copy.Origin.TaskID || originDoc.Baseline.TaskID != copy.Origin.TaskID || originDoc.Candidate.TaskID != copy.Origin.TaskID ||
				originDoc.Baseline.SnapshotDigest != originState.BaselineDigest || originDoc.Candidate.SnapshotDigest != originState.CandidateDigest {
				return nil, nil, c.Fail(c.StoreIntegrityError, "project memory copy origin task document binding mismatch")
			}
			canonical, canonicalErr := c.CanonicalV1(originDoc)
			if canonicalErr != nil || !bytes.Equal(originRaw, canonical) {
				return nil, nil, c.Fail(c.StoreIntegrityError, "project memory copy origin task document is not canonical")
			}
			originProject, projectErr := s.projectMemoryProject(originDoc)
			if projectErr != nil || originProject != copy.Origin.ProjectDigest {
				return nil, nil, c.Fail(c.StaleAuthority, "project memory copy origin project changed")
			}
			if configErr := validateTaskConfig(originDoc); configErr != nil {
				return nil, nil, configErr
			}
			readable := currentOperatorEffect(originDoc, "context.read") == nil
			current[copy.Origin.TaskID] = projectMemoryCurrent{State: originState, Doc: originDoc, Project: originProject, Readable: readable}
			if !readable || len(originDoc.ProjectMemoryRefs) == 0 {
				continue
			}
			originRefs, validateErr := s.validateProjectMemoryRefs(ctx, originState, originDoc, sourceBudget)
			if validateErr != nil {
				return nil, nil, validateErr
			}
			for index, originRef := range originDoc.ProjectMemoryRefs {
				originSize := int64(0)
				if originRefs[index].CopyPin != nil {
					originSize = originRefs[index].CopyPin.CopyBytes
				} else {
					originBytes, readErr := s.Archive.GetBytes(copy.Origin.TaskID, originRef.Digest)
					if readErr != nil {
						return nil, nil, readErr
					}
					originSize = int64(len(originBytes))
				}
				totalBytes += originSize
				if len(stored) >= projectMemoryMaxEvents || totalBytes > projectMemoryMaxBytes {
					return nil, nil, c.Fail(c.BudgetLimitReached, "project memory inventory exceeds bound")
				}
				stored = append(stored, originRefs[index])
			}
		}
	}
	return stored, current, nil
}

func projectMemoryEventLineage(stored []projectMemoryStored) (map[string]projectMemoryStored, map[string]string, map[string]string, error) {
	events := make(map[string]projectMemoryStored, len(stored))
	for _, item := range stored {
		if item.Copy != nil || item.CopyPin != nil {
			continue
		}
		if _, exists := events[item.Event.ID]; exists {
			return nil, nil, nil, c.Fail(c.StoreIntegrityError, "duplicate project memory identity")
		}
		events[item.Event.ID] = item
	}
	retiredBy := map[string]string{}
	supersededBy := map[string]string{}
	for _, item := range stored {
		if item.Copy != nil || item.CopyPin != nil {
			continue
		}
		event := item.Event
		if event.Action != "RETIRE" && event.Action != "CORRECT" {
			continue
		}
		target, exists := events[event.TargetID]
		if !exists {
			return nil, nil, nil, c.Fail(c.StoreIntegrityError, "project memory lineage target is missing")
		}
		if target.Digest != event.TargetDigest || target.TaskID != item.TaskID || target.Event.Action == "RETIRE" {
			return nil, nil, nil, c.Fail(c.StoreIntegrityError, "project memory lineage target digest mismatch")
		}
		if target.Event.Source.ProjectDigest != event.Source.ProjectDigest {
			return nil, nil, nil, c.Fail(c.StoreIntegrityError, "project memory lineage crosses project scope")
		}
		if event.Action == "RETIRE" {
			if prior := retiredBy[event.TargetID]; prior != "" && prior != event.ID {
				return nil, nil, nil, c.Fail(c.StoreIntegrityError, "project memory fact retired more than once")
			}
			retiredBy[event.TargetID] = event.ID
		} else {
			if prior := supersededBy[event.TargetID]; prior != "" && prior != event.ID {
				return nil, nil, nil, c.Fail(c.StoreIntegrityError, "project memory fact corrected more than once")
			}
			supersededBy[event.TargetID] = event.ID
		}
	}
	return events, retiredBy, supersededBy, nil
}

func projectMemoryBuildItems(stored []projectMemoryStored, current map[string]projectMemoryCurrent, querySource ProjectMemorySource) ([]ProjectMemoryItem, error) {
	events, retiredBy, supersededBy, err := projectMemoryEventLineage(stored)
	if err != nil {
		return nil, err
	}
	items := make([]ProjectMemoryItem, 0, len(stored))
	for _, item := range stored {
		if item.Copy != nil || item.CopyPin != nil {
			continue
		}
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
	for _, storedCopy := range stored {
		if storedCopy.CopyPin != nil {
			pin := storedCopy.CopyPin
			out := ProjectMemoryItem{SchemaVersion: 1, Source: pin.Target, Origin: &pin.Origin,
				TransferPolicyDigest: pin.TransferPolicyDigest, SourceItemID: pin.SourceItemID, SourceItemDigest: pin.SourceItemDigest,
				Digest: pin.CopyDigest, ID: pin.CopyID, EvidenceClass: "USER_COPIED", AdvisoryOnly: true,
				Status: "STALE", StatusReason: "source policy or lineage no longer permits copied content", Redacted: true}
			items = append(items, out)
			continue
		}
		copy := storedCopy.Copy
		if copy == nil {
			continue
		}
		out := ProjectMemoryItem{SchemaVersion: 1, Source: copy.Target, Origin: &copy.Origin, TransferPolicyDigest: copy.TransferPolicyDigest, SourceItemID: copy.SourceItemID,
			SourceItemDigest: copy.SourceItemDigest, Digest: storedCopy.Digest, ContentDigest: copy.ContentDigest,
			ID: copy.ID, Kind: copy.Kind, Key: copy.Key, Text: copy.Text, EvidenceClass: "USER_COPIED", AdvisoryOnly: true}
		if copy.Target.PolicyDigest != querySource.PolicyDigest {
			out.Redacted = true
			out.Key, out.Text, out.ContentDigest = "", "", ""
		}
		if target, exists := current[copy.Target.TaskID]; !exists || target.State.Deletion != nil {
			out.Status, out.StatusReason = "STALE", "copy target task is no longer available"
		} else if !target.Readable {
			out.Status, out.StatusReason, out.Redacted = "STALE", "target policy does not permit memory reads", true
		} else if target.Project != copy.Target.ProjectDigest || target.State.BaselineDigest != copy.Target.BaselineDigest ||
			target.State.CandidateDigest != copy.Target.CandidateDigest || target.State.PolicyEpoch != copy.Target.PolicyEpoch {
			out.Status, out.StatusReason = "STALE", "copy target candidate, baseline or policy epoch changed"
		} else if copy.Target.ProjectDigest != querySource.ProjectDigest || copy.Target.BaselineDigest != querySource.BaselineDigest ||
			copy.Target.CandidateDigest != querySource.CandidateDigest || copy.Target.PolicyEpoch != querySource.PolicyEpoch ||
			copy.Target.PolicyDigest != querySource.PolicyDigest {
			out.Status, out.StatusReason = "STALE", "query task baseline, candidate or policy differs from copied target"
		} else if origin, exists := current[copy.Origin.TaskID]; !exists || origin.State.Deletion != nil {
			out.Status, out.StatusReason, out.Redacted = "STALE", "original source task is no longer available", true
		} else if !origin.Readable {
			out.Status, out.StatusReason, out.Redacted = "STALE", "original source policy does not permit memory reads", true
		} else if origin.Project != copy.Origin.ProjectDigest || origin.State.BaselineDigest != copy.Origin.BaselineDigest ||
			origin.State.CandidateDigest != copy.Origin.CandidateDigest || origin.State.PolicyEpoch != copy.Origin.PolicyEpoch {
			out.Status, out.StatusReason, out.Redacted = "STALE", "original source candidate, baseline or policy changed", true
		} else {
			targetPolicyDigest, targetPolicyErr := projectMemoryEffectivePolicyDigest(target.Doc)
			originPolicyDigest, policyErr := projectMemoryPolicyDigest(origin.Doc)
			if policyErr != nil {
				return nil, policyErr
			}
			originEffectivePolicyDigest, effectivePolicyErr := projectMemoryEffectivePolicyDigest(origin.Doc)
			originItem, found := events[copy.SourceItemID]
			if targetPolicyErr != nil || effectivePolicyErr != nil || targetPolicyDigest != copy.TransferPolicyDigest || originEffectivePolicyDigest != copy.TransferPolicyDigest {
				out.Status, out.StatusReason, out.Redacted = "STALE", "source and target effective policies no longer match the explicit copy", true
			} else if originPolicyDigest != copy.Origin.PolicyDigest {
				out.Status, out.StatusReason, out.Redacted = "STALE", "original source policy changed", true
			} else if !found || originItem.TaskID != copy.Origin.TaskID || originItem.Digest != copy.SourceItemDigest ||
				originItem.Event.Action != "ASSERT" || originItem.Event.EvidenceClass != "USER_ASSERTED" {
				return nil, c.Fail(c.StoreIntegrityError, "project memory copy origin item is missing")
			} else if retiredBy[copy.SourceItemID] != "" || supersededBy[copy.SourceItemID] != "" ||
				!projectMemoryAssertionCurrent(originItem.Event, origin, originPolicyDigest) {
				out.Status, out.StatusReason, out.Redacted = "STALE", "original source memory was retired, corrected or became stale", true
			} else if projectMemoryAssertionConflicted(originItem.Event, events, current, retiredBy, supersededBy) {
				out.Status, out.StatusReason, out.Redacted = "CONFLICT", "original source memory is in conflict", true
			} else {
				out.Status = "ACTIVE"
			}
		}
		if out.Redacted {
			out.Key, out.Text, out.ContentDigest = "", "", ""
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

func projectMemoryAssertionCurrent(event ProjectMemoryEvent, source projectMemoryCurrent, currentPolicyDigest string) bool {
	return event.Action == "ASSERT" && event.EvidenceClass == "USER_ASSERTED" && source.Readable && source.State.Deletion == nil &&
		source.Project == event.Source.ProjectDigest && source.State.BaselineDigest == event.Source.BaselineDigest &&
		source.State.CandidateDigest == event.Source.CandidateDigest && source.State.PolicyEpoch == event.Source.PolicyEpoch &&
		currentPolicyDigest == event.Source.PolicyDigest
}

func projectMemoryAssertionConflicted(assertion ProjectMemoryEvent, events map[string]projectMemoryStored, current map[string]projectMemoryCurrent,
	retiredBy, supersededBy map[string]string) bool {
	for id, other := range events {
		event := other.Event
		if id == assertion.ID || event.Action != "ASSERT" || event.EvidenceClass != "USER_ASSERTED" || event.Source.ProjectDigest != assertion.Source.ProjectDigest ||
			event.Kind != assertion.Kind || event.Key != assertion.Key || event.ContentDigest == assertion.ContentDigest || retiredBy[id] != "" || supersededBy[id] != "" {
			continue
		}
		source, exists := current[event.Source.TaskID]
		if !exists || !source.Readable || source.State.Deletion != nil || source.Project != event.Source.ProjectDigest ||
			source.State.BaselineDigest != event.Source.BaselineDigest || source.State.CandidateDigest != event.Source.CandidateDigest ||
			source.State.PolicyEpoch != event.Source.PolicyEpoch {
			continue
		}
		policyDigest, err := projectMemoryPolicyDigest(source.Doc)
		if err == nil && policyDigest == event.Source.PolicyDigest {
			return true
		}
	}
	return false
}

func projectMemoryConflictMetadataForEvent(event ProjectMemoryEvent) *c.ProjectMemoryConflictMetadata {
	return &c.ProjectMemoryConflictMetadata{
		SchemaVersion: 1, ProjectDigest: event.Source.ProjectDigest, BaselineDigest: event.Source.BaselineDigest,
		CandidateDigest: event.Source.CandidateDigest, PolicyDigest: event.Source.PolicyDigest,
		AssertionID: event.ID, ContentDigest: event.ContentDigest, TargetID: event.TargetID, TargetDigest: event.TargetDigest,
		Kind: event.Kind, Key: event.Key,
	}
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
	if err = currentOperatorEffect(doc, "context.read"); err != nil {
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
	if err := currentOperatorEffect(doc, "context.read"); err != nil {
		return nil, false, err
	}
	querySource, err := s.projectMemorySource(ctx, state, doc)
	if err != nil {
		return nil, false, err
	}
	// Automatic context reads only this task's validated document inventory.
	// Cross-task text is eligible only after an explicit, durable copy into
	// this task; project-wide search never runs on the inference path.
	if len(doc.ProjectMemoryRefs) == 0 {
		return []ProjectMemoryItem{}, false, nil
	}
	stored, current, err := s.projectMemoryInventoryScoped(ctx, querySource.ProjectDigest, state.TaskID)
	if err != nil {
		return nil, false, err
	}
	items, err := projectMemoryBuildItems(stored, current, querySource)
	if err != nil {
		return nil, false, err
	}
	conflictDigests, err := s.projectMemoryConflictDigests(ctx, querySource)
	if err != nil {
		return nil, false, err
	}
	for index := range items {
		item := &items[index]
		if projectMemoryItemHasCurrentConflict(*item, conflictDigests) {
			item.Status = "CONFLICT"
			item.StatusReason = "different current project assertions share this key"
		}
	}
	selected := make([]ProjectMemoryItem, 0, 8)
	usedBytes := 0
	truncated := false
	for _, item := range items {
		if item.Status != "ACTIVE" {
			continue
		}
		// The compiled request is durably retained in the querying task. A copied
		// fact is eligible only through its explicit typed ref and current pin.
		if item.Source.TaskID != state.TaskID {
			continue
		}
		if item.EvidenceClass == "USER_COPIED" && (item.Origin == nil || !c.ValidDigest(item.TransferPolicyDigest) ||
			!projectMemoryIDPattern.MatchString(item.SourceItemID) || !c.ValidDigest(item.SourceItemDigest)) {
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

func projectMemoryItemHasCurrentConflict(item ProjectMemoryItem, conflictDigests map[string]map[string]bool) bool {
	if item.Status != "ACTIVE" || item.Redacted || !c.ValidDigest(item.ContentDigest) ||
		(item.EvidenceClass != "USER_ASSERTED" && item.EvidenceClass != "USER_COPIED") {
		return false
	}
	key := item.Source.ProjectDigest + "\x00" + item.Kind + "\x00" + item.Key
	for digest := range conflictDigests[key] {
		if digest != item.ContentDigest {
			return true
		}
	}
	return false
}

// projectMemoryConflictDigests resolves project-wide assertion freshness from
// the journal's bounded metadata index. It does not load any other task's
// archive document or assertion body.
func (s *Session) projectMemoryConflictDigests(ctx context.Context, querySource ProjectMemorySource) (map[string]map[string]bool, error) {
	index, err := s.Journal.ProjectMemoryConflictMetadata(ctx, querySource.ProjectDigest, projectMemoryMaxEvents, projectMemoryMaxBytes)
	if err != nil {
		return nil, &projectMemoryConflictIndexError{cause: err}
	}
	if !index.Complete {
		return nil, &projectMemoryConflictIndexError{incomplete: true}
	}
	var privacy *privacyView
	if s.privacy != nil {
		view, viewErr := s.retentionView(ctx)
		if viewErr != nil {
			return nil, &projectMemoryConflictIndexError{cause: viewErr}
		}
		privacy = &view
	}
	current := make(map[string]bool, len(index.Records))
	byID := make(map[string]store.ProjectMemoryConflictRecord, len(index.Records))
	for _, record := range index.Records {
		mutation := record.Mutation
		witness := mutation.ConflictMetadata
		if witness == nil {
			return nil, &projectMemoryConflictIndexError{cause: c.Fail(c.StoreIntegrityError, "project memory conflict witness disappeared")}
		}
		if mutation.Action == "COPY" {
			continue
		}
		if witness.AssertionID == "" || witness.ProjectDigest != querySource.ProjectDigest {
			return nil, &projectMemoryConflictIndexError{cause: c.Fail(c.StoreIntegrityError, "project memory assertion witness is incomplete")}
		}
		if _, duplicate := byID[witness.AssertionID]; duplicate {
			return nil, &projectMemoryConflictIndexError{cause: c.Fail(c.StoreIntegrityError, "duplicate project memory assertion identity")}
		}
		byID[witness.AssertionID] = record
		state, found := index.CurrentStates[record.TaskID]
		isCurrent := found && state.Deletion == nil && state.TaskID == record.TaskID && state.TaskSeq > mutation.SourceTaskSeq &&
			state.SpecVersion == mutation.SpecVersion && state.PolicyEpoch == mutation.PolicyEpoch &&
			state.BaselineDigest == witness.BaselineDigest && state.CandidateDigest == witness.CandidateDigest &&
			witness.ProjectDigest == querySource.ProjectDigest && witness.BaselineDigest == querySource.BaselineDigest &&
			witness.CandidateDigest == querySource.CandidateDigest && mutation.PolicyEpoch == querySource.PolicyEpoch &&
			witness.PolicyDigest == querySource.PolicyDigest
		if isCurrent && privacy != nil {
			if _, deleted := privacy.DeletionSequences[record.TaskID]; deleted {
				isCurrent = false
			}
			if _, deleted := privacy.Deletions[record.TaskID]; deleted {
				isCurrent = false
			}
			for _, attempt := range privacy.Attempts {
				if attempt.ChildTask == record.TaskID {
					if _, deleted := privacy.Deletions[attempt.ParentTask]; deleted {
						isCurrent = false
						break
					}
				}
			}
		}
		current[witness.AssertionID] = isCurrent
	}
	retiredOrSuperseded := map[string]bool{}
	for _, record := range index.Records {
		mutation := record.Mutation
		witness := mutation.ConflictMetadata
		if mutation.Action != "CORRECT" && mutation.Action != "RETIRE" {
			continue
		}
		target, found := byID[witness.TargetID]
		if !found || target.TaskID != record.TaskID || target.Mutation.EventDigest != witness.TargetDigest || target.Mutation.Action == "RETIRE" ||
			target.Mutation.ConflictMetadata.ProjectDigest != witness.ProjectDigest {
			return nil, &projectMemoryConflictIndexError{cause: c.Fail(c.StoreIntegrityError, "project memory conflict lineage is invalid")}
		}
		if current[witness.AssertionID] {
			if retiredOrSuperseded[witness.TargetID] {
				return nil, &projectMemoryConflictIndexError{cause: c.Fail(c.StoreIntegrityError, "project memory target has multiple current lineage changes")}
			}
			retiredOrSuperseded[witness.TargetID] = true
		}
	}
	values := map[string]map[string]bool{}
	for _, record := range index.Records {
		mutation := record.Mutation
		witness := mutation.ConflictMetadata
		if mutation.Action != "ASSERT" && mutation.Action != "CORRECT" || !current[witness.AssertionID] || retiredOrSuperseded[witness.AssertionID] {
			continue
		}
		key := witness.ProjectDigest + "\x00" + witness.Kind + "\x00" + witness.Key
		if values[key] == nil {
			values[key] = map[string]bool{}
		}
		values[key][witness.ContentDigest] = true
	}
	return values, nil
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
	if state.Deletion != nil || state.InputBarrier || len(state.PendingInputIDs) != 0 ||
		state.WorkBudget != nil && state.WorkBudget.EffectPending || projectMemoryHasUnsettledExposure(state) {
		return receipt, c.Fail(c.PolicyDenied, "project memory mutation requires settled task effects and no pending input or deletion")
	}
	if err = currentOperatorEffect(doc, "context.edit"); err != nil {
		return receipt, err
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
	stored, current, err := s.projectMemoryInventory(ctx, source.ProjectDigest)
	if err != nil {
		return receipt, err
	}
	for _, existing := range stored {
		if existing.Event.CommandID != commandID {
			continue
		}
		if existing.Event.RequestDigest != requestDigest || existing.Event.Source.ProjectDigest != source.ProjectDigest || existing.Event.Source.TaskID != task {
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
	if state.ProjectMemoryRevision == int64(^uint64(0)>>1) {
		return receipt, c.Fail(c.BudgetLimitReached, "project memory revision counter is exhausted")
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
	// Persist and validate the exact next document before making the typed
	// metadata-only journal mutation visible.
	docBytes, err := c.CanonicalV1(doc)
	if err != nil {
		return receipt, err
	}
	if int64(len(docBytes)) > projectMemoryMaxDocumentBytes {
		return receipt, c.Fail(c.BudgetLimitReached, "project memory task document exceeds its byte bound")
	}
	documentDigest, err := s.Archive.PutControlBytes(task, docBytes, true)
	if err != nil {
		return receipt, err
	}
	generation := s.Journal.Generation()
	mutation := &c.ProjectMemoryMutation{SourceDocumentDigest: source.DocumentDigest, EventDigest: ref.Digest,
		CommandID: commandID, EventBytes: int64(len(raw)), SourceTaskSeq: source.TaskSeq, SpecVersion: state.SpecVersion,
		PolicyEpoch: state.PolicyEpoch, KernelGeneration: generation, Action: action,
		ConflictMetadata: projectMemoryConflictMetadataForEvent(event)}
	if err = mutation.Validate(); err != nil {
		return receipt, err
	}
	predicted := state
	predicted.DocumentDigest = documentDigest
	predicted.TaskSeq++
	predicted.KernelGeneration = generation
	predicted.ProjectMemoryRevision++
	if _, _, err = s.loadDocument(predicted); err != nil {
		return receipt, err
	}
	if err = currentOperatorEffect(doc, "context.edit"); err != nil {
		return receipt, err
	}
	budgetBefore := state.WorkBudget
	next, executeErr := s.Journal.Execute(ctx, store.Command{ID: commandID, TaskID: task, Actor: "user", ExpectedTaskSeq: state.TaskSeq,
		Type: "ProjectMemoryRecorded", Payload: c.EventPayload{DocumentDigest: documentDigest, ProjectMemoryMutation: mutation}})
	if executeErr != nil {
		return receipt, executeErr
	}
	if next.DocumentDigest != documentDigest || next.ProjectMemoryRevision != state.ProjectMemoryRevision+1 ||
		next.SpecVersion != state.SpecVersion || next.PolicyEpoch != state.PolicyEpoch || next.Execution != state.Execution ||
		!sameProjectMemoryWorkBudget(budgetBefore, next.WorkBudget) {
		return receipt, c.Fail(c.StoreIntegrityError, "project memory event changed task authority or work budget")
	}
	verifiedState, verifiedDoc, loadErr := s.Load(ctx, task)
	if loadErr != nil {
		return receipt, loadErr
	}
	if verifiedState.DocumentDigest != documentDigest || len(verifiedDoc.ProjectMemoryRefs) != len(doc.ProjectMemoryRefs) ||
		verifiedDoc.ProjectMemoryRefs[len(verifiedDoc.ProjectMemoryRefs)-1] != ref ||
		verifiedState.ProjectMemoryRevision != next.ProjectMemoryRevision {
		return receipt, c.Fail(c.StoreIntegrityError, "project memory event failed full document validation")
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

func projectMemoryHasUnsettledExposure(state c.TaskState) bool {
	if state.Tokens != nil {
		for _, reservation := range state.Tokens.Reservations {
			if reservation.Status != "SETTLED" {
				return true
			}
		}
	}
	if state.Resources != nil {
		for _, reservation := range state.Resources.Reservations {
			if reservation.Status != "SETTLED" {
				return true
			}
		}
	}
	for _, effect := range state.DeliveryEffects {
		if effect.Status == c.DeliveryUnknown {
			return true
		}
	}
	return false
}

func sameProjectMemoryWorkBudget(left, right *c.TaskWorkBudget) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
