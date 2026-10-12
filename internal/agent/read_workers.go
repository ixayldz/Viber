package agent

import (
	"bytes"
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/intelligence"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

type ReadWorkerKind string

const (
	ReadWorkerInventory    ReadWorkerKind = "inventory"
	ReadWorkerGoGraph      ReadWorkerKind = "go-graph"
	ReadWorkerRequirements ReadWorkerKind = "requirements"
	ReadWorkerSearch       ReadWorkerKind = "search"
)

// ReadWorkersRequest asks for a small set of deterministic, local source
// analyses. CandidateDigest is mandatory so stale requests cannot silently
// retarget a later candidate. These workers never invoke a model or process.
type ReadWorkersRequest struct {
	OperationID            string           `json:"operation_id"`
	ExpectedPolicyDigest   string           `json:"expected_policy_digest,omitempty"`
	ExpectedDocumentDigest string           `json:"expected_document_digest,omitempty"`
	CandidateDigest        string           `json:"candidate_digest"`
	ChangedPaths           []string         `json:"changed_paths,omitempty"`
	Workers                []ReadWorkerKind `json:"workers"`
	Query                  string           `json:"query,omitempty"`
	TimeoutMillis          int64            `json:"timeout_millis,omitempty"`
}

type ReadWorkerBinding struct {
	TaskID           string `json:"task_id"`
	DocumentDigest   string `json:"document_digest"`
	CandidateDigest  string `json:"candidate_digest"`
	BaselineDigest   string `json:"baseline_digest"`
	PolicyDigest     string `json:"policy_digest"`
	TaskSeq          int64  `json:"task_seq"`
	SpecVersion      int64  `json:"spec_version"`
	PolicyEpoch      int64  `json:"policy_epoch"`
	KernelGeneration int64  `json:"kernel_generation"`
	ReadableFiles    int    `json:"policy_readable_file_count"`
	ReadableBytes    int64  `json:"policy_readable_bytes"`
}

type ReadWorkerCharge struct {
	Admission        c.ReadWorkerAdmission `json:"admission"`
	AdmittedTaskSeq  int64                 `json:"admitted_task_seq"`
	AdmittedDocument string                `json:"admitted_document_digest"`
}

type ReadWorkersResult struct {
	Binding       ReadWorkerBinding  `json:"binding"`
	Charge        ReadWorkerCharge   `json:"parent_budget_charge"`
	Workers       []ReadWorkerOutput `json:"workers"`
	Failure       *c.Error           `json:"failure,omitempty"`
	SchemaVersion int                `json:"schema_version"`
	DurationMS    int64              `json:"duration_ms"`
	ResultDigest  string             `json:"result_digest"`
	Status        string             `json:"status"`
	Trust         string             `json:"trust"`
}

type ReadWorkerOutput struct {
	Kind         ReadWorkerKind          `json:"kind"`
	ResultDigest string                  `json:"result_digest"`
	Inventory    *ReadInventoryResult    `json:"inventory,omitempty"`
	GoGraph      *ReadGoGraphResult      `json:"go_graph,omitempty"`
	Requirements *ReadRequirementsResult `json:"requirements,omitempty"`
	Search       *ReadSearchResult       `json:"search,omitempty"`
}

type ReadInventoryResult struct {
	Extensions []ReadExtensionCount `json:"extensions"`
	Files      int                  `json:"files"`
	TotalBytes int64                `json:"total_bytes"`
}

type ReadExtensionCount struct {
	Extension string `json:"extension"`
	Files     int    `json:"files"`
}

type ReadGoGraphResult struct {
	Coverage      string                        `json:"coverage"`
	ScopeDigest   string                        `json:"policy_filtered_scope_digest"`
	Fingerprint   string                        `json:"graph_fingerprint"`
	Unknowns      []string                      `json:"unknowns"`
	Selection     *intelligence.GoTestSelection `json:"test_selection,omitempty"`
	ParsedFiles   int                           `json:"parsed_files"`
	ExcludedFiles int                           `json:"excluded_files"`
	Nodes         int                           `json:"nodes"`
	Edges         int                           `json:"edges"`
	WidenCheckSet bool                          `json:"widen_check_selection"`
}

type ReadRequirement struct {
	ID                 string `json:"id"`
	Risk               string `json:"risk"`
	VerificationMethod string `json:"verification_method"`
	Required           bool   `json:"required"`
}

type ReadRequirementsResult struct {
	Requirements []ReadRequirement `json:"requirements"`
	Required     int               `json:"required"`
	Total        int               `json:"total"`
}

type ReadSearchHit struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	ByteOffset int    `json:"byte_offset"`
}

type ReadSearchResult struct {
	QueryDigest  string          `json:"query_digest"`
	Hits         []ReadSearchHit `json:"hits"`
	FilesScanned int             `json:"files_scanned"`
	BytesScanned int64           `json:"bytes_scanned"`
	Truncated    bool            `json:"truncated"`
}

const (
	readWorkersDefaultTimeout = int64(5000)
	readWorkersMaxTimeout     = int64(30000)
	readWorkersMaxFiles       = 10000
	readWorkersMaxBytes       = int64(32 << 20)
	readWorkersMaxHits        = 128
	readWorkersMaxOutputBytes = 64 << 10
)

func normalizeReadWorkersRequest(request ReadWorkersRequest) (ReadWorkersRequest, error) {
	if !c.ValidReadWorkerOperationID(request.OperationID) || !c.ValidDigest(request.CandidateDigest) || len(request.Workers) < 2 || len(request.Workers) > 4 || len(request.ChangedPaths) > 128 {
		return ReadWorkersRequest{}, c.Fail(c.InvalidArgument, "candidate digest and two to four bounded worker kinds required")
	}
	if request.TimeoutMillis == 0 {
		request.TimeoutMillis = readWorkersDefaultTimeout
	}
	if request.TimeoutMillis < 250 || request.TimeoutMillis > readWorkersMaxTimeout {
		return ReadWorkersRequest{}, c.Fail(c.InvalidArgument, "worker timeout must be between 250 and 30000 milliseconds")
	}
	request.Workers = append([]ReadWorkerKind{}, request.Workers...)
	sort.Slice(request.Workers, func(i, j int) bool { return request.Workers[i] < request.Workers[j] })
	for i, kind := range request.Workers {
		if kind != ReadWorkerInventory && kind != ReadWorkerGoGraph && kind != ReadWorkerRequirements && kind != ReadWorkerSearch || i > 0 && request.Workers[i-1] == kind {
			return ReadWorkersRequest{}, c.Fail(c.InvalidArgument, "unsupported or duplicate read worker kind")
		}
	}
	selected := map[ReadWorkerKind]bool{}
	for _, kind := range request.Workers {
		selected[kind] = true
	}
	if request.Query != "" && !selected[ReadWorkerSearch] || selected[ReadWorkerSearch] && (len(request.Query) == 0 || len(request.Query) > 128 || !utf8.ValidString(request.Query) || strings.ContainsAny(request.Query, "\x00\r\n")) {
		return ReadWorkersRequest{}, c.Fail(c.InvalidArgument, "bounded query is required only when the search worker is selected")
	}
	if len(request.ChangedPaths) > 0 && !selected[ReadWorkerGoGraph] {
		return ReadWorkersRequest{}, c.Fail(c.InvalidArgument, "changed paths require the Go graph worker")
	}
	seen := map[string]bool{}
	for _, name := range request.ChangedPaths {
		if !policy.SafePath(name) || seen[name] {
			return ReadWorkersRequest{}, c.Fail(c.InvalidArgument, "changed paths must be safe and unique")
		}
		seen[name] = true
	}
	request.ChangedPaths = append([]string{}, request.ChangedPaths...)
	sort.Strings(request.ChangedPaths)
	if request.ExpectedDocumentDigest != "" && !c.ValidDigest(request.ExpectedDocumentDigest) || request.ExpectedPolicyDigest != "" && !c.ValidDigest(request.ExpectedPolicyDigest) {
		return ReadWorkersRequest{}, c.Fail(c.InvalidArgument, "expected document and policy bindings must be SHA-256 digests")
	}
	return request, nil
}

func (s *Session) readWorkerPolicies(state c.TaskState, doc Document) []policy.Policy {
	layers := s.layers(state, doc)
	if doc.Plan != nil {
		if node := doc.Plan.Active(); node != nil {
			layers = append(layers, policy.Policy{SchemaVersion: 1, Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Effects: []string{"snapshot.read"}, Paths: append([]string{}, node.ReadScope...)})
		}
	}
	return layers
}

// loadReadWorkerAdmissionDocument binds the request to the durable task
// document without opening candidate manifests. Full loadDocument validation
// runs after budget admission, before any analyzer receives source bytes.
func (s *Session) loadReadWorkerAdmissionDocument(ctx context.Context, task string) (c.TaskState, Document, error) {
	state, err := s.State(ctx, task)
	if err != nil {
		return state, Document{}, err
	}
	if state.TaskID != task || state.TaskSeq < 1 || !c.ValidDigest(state.DocumentDigest) {
		return state, Document{}, c.Fail(c.StoreIntegrityError, "read-worker task journal has no current document binding")
	}
	if err = ctx.Err(); err != nil {
		return state, Document{}, err
	}
	raw, err := s.Archive.GetBytes(task, state.DocumentDigest)
	if err != nil {
		return state, Document{}, err
	}
	if err = ctx.Err(); err != nil {
		return state, Document{}, err
	}
	var doc Document
	if err = c.DecodeStrict(raw, &doc); err != nil {
		return state, doc, err
	}
	if doc.SchemaVersion != c.SchemaVersion || doc.TaskID != task || doc.Spec.TaskID != task || doc.Spec.Version != state.SpecVersion ||
		doc.Baseline.SchemaVersion != c.SchemaVersion || doc.Candidate.SchemaVersion != c.SchemaVersion ||
		doc.Baseline.TaskID != task || doc.Candidate.TaskID != task ||
		doc.Baseline.SnapshotDigest != state.BaselineDigest || doc.Candidate.SnapshotDigest != state.CandidateDigest {
		return state, doc, c.Fail(c.StoreIntegrityError, "read-worker document does not match the current durable task binding")
	}
	if err = doc.Spec.Validate(); err != nil {
		return state, doc, err
	}
	if err = doc.Budget.Validate(); err != nil {
		return state, doc, err
	}
	if state.WorkBudget != nil && !workBudgetMatchesDocument(*state.WorkBudget, doc) {
		return state, doc, c.Fail(c.StoreIntegrityError, "task work budget journal projection differs from its retained document")
	}
	if err = validateTaskConfig(doc); err != nil {
		return state, doc, err
	}
	if err = s.validateRuntime(doc); err != nil {
		return state, doc, err
	}
	if err = validateNativeLease(doc); err != nil {
		return state, doc, err
	}
	if err = s.validateNativeCleanup(doc); err != nil {
		return state, doc, err
	}
	if doc.Plan != nil {
		if doc.Plan.Definition.PolicyEpoch != state.PolicyEpoch {
			return state, doc, c.Fail(c.StoreIntegrityError, "plan policy binding mismatch")
		}
		if err = doc.Plan.Validate(doc.Spec, planChecks(doc), doc.Candidate.SnapshotDigest, doc.Plan.Definition.PolicyEpoch); err != nil {
			return state, doc, err
		}
	}
	if doc.SkillRegistry != nil {
		if err = doc.SkillRegistry.Validate(); err != nil {
			return state, doc, err
		}
	}
	if doc.Extension == nil {
		if doc.ExtensionManifest != nil {
			return state, doc, c.Fail(c.StoreIntegrityError, "extension manifest lacks an explicit operator binding")
		}
	} else if doc.Extension.Validate() != nil || doc.ExtensionManifest == nil || doc.ExtensionManifest.Validate() != nil || doc.Extension.ManifestDigest != doc.ExtensionManifest.Digest {
		return state, doc, c.Fail(c.StoreIntegrityError, "retained extension binding failed validation")
	}
	if doc.GoGraphRuntime != nil {
		if err = doc.GoGraphRuntime.Validate(); err != nil {
			return state, doc, c.Fail(c.StoreIntegrityError, "retained Go graph runtime binding failed validation")
		}
	}
	if doc.Ranking != nil {
		if err = doc.Ranking.Validate(); err != nil {
			return state, doc, err
		}
		if doc.Ranking.Definition.SourceDigest != doc.Baseline.SnapshotDigest {
			return state, doc, c.Fail(c.StoreIntegrityError, "ranking source binding changed")
		}
	}
	if doc.GoalOrigin != nil {
		if err = s.validateGoalOrigin(ctx, doc); err != nil {
			return state, doc, err
		}
	}
	if err = s.validateAttempt(doc); err != nil {
		return state, doc, err
	}
	if _, err = s.queueFromState(state); err != nil {
		return state, doc, err
	}
	return state, doc, nil
}

type readWorkerScope struct {
	Snapshot c.Snapshot
	Total    int64
}

func planReadWorkerScope(ctx context.Context, archive *artifact.Archive, ref artifact.Ref, layers []policy.Policy, state c.TaskState) (readWorkerScope, error) {
	if err := ctx.Err(); err != nil {
		return readWorkerScope{}, err
	}
	manifest, err := archive.ReadManifest(ref)
	if err != nil {
		return readWorkerScope{}, err
	}
	if err = ctx.Err(); err != nil {
		return readWorkerScope{}, err
	}
	original := manifest.Snapshot
	originalDigest := original.Digest
	original.Digest = ""
	verifiedDigest, err := c.Digest(original)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return readWorkerScope{}, ctxErr
	}
	if err != nil || verifiedDigest != originalDigest || originalDigest != ref.SnapshotDigest {
		return readWorkerScope{}, c.Fail(c.StoreIntegrityError, "candidate snapshot manifest digest changed before read-worker admission")
	}
	if len(manifest.Snapshot.Entries) > 100000 {
		return readWorkerScope{}, c.Fail(c.UnsupportedCapability, "candidate exceeds the read-worker manifest entry bound")
	}
	filtered := c.Snapshot{SchemaVersion: manifest.Snapshot.SchemaVersion, Root: manifest.Snapshot.Root, Consistency: manifest.Snapshot.Consistency, Entries: []c.Entry{}, Directories: []string{}, Exclusions: []string{}}
	directories := map[string]bool{}
	var total int64
	for i, entry := range manifest.Snapshot.Entries {
		if i&127 == 0 {
			if err := ctx.Err(); err != nil {
				return readWorkerScope{}, err
			}
		}
		if err := policy.Admit(layers, policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Effect: "snapshot.read", Path: entry.Path}); err != nil {
			continue
		}
		if len(filtered.Entries) >= readWorkersMaxFiles || entry.Size < 0 || entry.Size > 8<<20 || entry.Size > readWorkersMaxBytes-total {
			return readWorkerScope{}, c.Fail(c.UnsupportedCapability, "policy-readable candidate exceeds read-worker file or byte bounds")
		}
		if !policy.SafePath(entry.Path) || !c.ValidDigest(entry.Hash) || entry.Mode&^uint32(0777) != 0 {
			return readWorkerScope{}, c.Fail(c.StoreIntegrityError, "candidate manifest contains an invalid readable entry")
		}
		filtered.Entries = append(filtered.Entries, entry)
		total += entry.Size
		for parent := path.Dir(entry.Path); parent != "." && parent != "/"; parent = path.Dir(parent) {
			directories[parent] = true
		}
	}
	for directory := range directories {
		filtered.Directories = append(filtered.Directories, directory)
	}
	sort.Strings(filtered.Directories)
	filtered.Digest = ""
	digest, err := c.Digest(filtered)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return readWorkerScope{}, ctxErr
	}
	if err != nil {
		return readWorkerScope{}, err
	}
	filtered.Digest = digest
	return readWorkerScope{Snapshot: filtered, Total: total}, nil
}

func hydrateReadWorkerScope(ctx context.Context, archive *artifact.Archive, ref artifact.Ref, scope readWorkerScope) (workspace.Capture, error) {
	capture := workspace.Capture{Snapshot: scope.Snapshot, Contents: map[string][]byte{}}
	var total int64
	for i, entry := range scope.Snapshot.Entries {
		if i&31 == 0 {
			if err := ctx.Err(); err != nil {
				return workspace.Capture{}, err
			}
		}
		if err := ctx.Err(); err != nil {
			return workspace.Capture{}, err
		}
		data, err := archive.GetBytes(ref.TaskID, entry.Hash)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return workspace.Capture{}, ctxErr
		}
		if err != nil || int64(len(data)) != entry.Size || c.HashBytes(data) != entry.Hash {
			return workspace.Capture{}, c.Fail(c.StoreIntegrityError, "candidate bytes failed integrity checks during charged read-worker admission")
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return workspace.Capture{}, ctxErr
		}
		total += int64(len(data))
		if total > readWorkersMaxBytes || total > scope.Total {
			return workspace.Capture{}, c.Fail(c.StoreIntegrityError, "candidate bytes exceeded the read-worker scope after admission")
		}
		capture.Contents[entry.Path] = data
	}
	if total != scope.Total {
		return workspace.Capture{}, c.Fail(c.StoreIntegrityError, "candidate byte total changed during read-worker hydration")
	}
	if err := ctx.Err(); err != nil {
		return workspace.Capture{}, err
	}
	if err := workspace.VerifyCapture(capture); err != nil {
		return workspace.Capture{}, err
	}
	if err := ctx.Err(); err != nil {
		return workspace.Capture{}, err
	}
	return capture, nil
}

// RunReadWorkers performs bounded source-only analyses over the current
// retained candidate. It charges each selected worker and the full timeout
// ceiling to the parent's durable work budget before dispatch. Results are
// ephemeral; workers have no mutation, subprocess or network authority.
func (s *Session) RunReadWorkers(ctx context.Context, task string, request ReadWorkersRequest) (ReadWorkersResult, error) {
	if s == nil || ctx == nil || task == "" {
		return ReadWorkersResult{}, c.Fail(c.InvalidArgument, "session, context and task are required")
	}
	request, err := normalizeReadWorkersRequest(request)
	if err != nil {
		return ReadWorkersResult{}, err
	}
	if !s.mu.TryLock() {
		return ReadWorkersResult{}, c.Fail(c.Conflict, "task is busy; retry read workers after its current operation settles")
	}
	defer s.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return ReadWorkersResult{}, err
	}
	state, doc, err := s.loadReadWorkerAdmissionDocument(ctx, task)
	if err != nil {
		return ReadWorkersResult{}, err
	}
	for _, prior := range state.ReadWorkerAdmissions {
		if prior.OperationID == request.OperationID {
			return ReadWorkersResult{}, c.Fail(c.CommandIDConflict, "read-worker operation ID was already charged; inspect the task journal")
		}
	}
	if doc.Candidate.SnapshotDigest != request.CandidateDigest {
		return ReadWorkersResult{}, c.Fail(c.StaleBase, "read-worker candidate digest is no longer current")
	}
	if request.ExpectedDocumentDigest != "" && request.ExpectedDocumentDigest != state.DocumentDigest {
		return ReadWorkersResult{}, c.Fail(c.StaleBase, "read-worker task document changed")
	}
	if state.Execution == c.Terminated || state.Deletion != nil || state.InputBarrier || len(state.PendingInputIDs) != 0 || doc.Pending != nil || doc.UnknownEffect {
		return ReadWorkersResult{}, c.Fail(c.PolicyDenied, "read workers require a nonterminal task without pending input or uncertain effects")
	}
	if state.WorkBudget == nil {
		return ReadWorkersResult{}, c.Fail(c.UnsupportedCapability, "legacy task has no sealed parent budget witness; read workers cannot be admitted")
	}
	if len(state.ReadWorkerAdmissions) >= c.MaxReadWorkerAdmissions {
		return ReadWorkersResult{}, c.Fail(c.BudgetLimitReached, "task reached the bounded read-worker admission history limit")
	}
	headroom, err := budgetHeadroom(doc.Budget, state.GoalQueue, false)
	if err != nil {
		return ReadWorkersResult{}, err
	}
	workerCount := int64(len(request.Workers))
	if headroom.MaxSteps < workerCount || headroom.MaxToolCalls < workerCount || headroom.MaxActiveMillis < request.TimeoutMillis {
		return ReadWorkersResult{}, c.Fail(c.BudgetLimitReached, "parent work budget cannot reserve the requested read-worker fleet")
	}
	if err = ctx.Err(); err != nil {
		return ReadWorkersResult{}, err
	}
	currentGeneration := s.Journal.Generation()
	if state.KernelGeneration > currentGeneration {
		return ReadWorkersResult{}, c.Fail(c.StaleAuthority, "task generation is newer than the current read-worker owner")
	}
	policyState := state
	policyState.KernelGeneration = currentGeneration
	layers := s.readWorkerPolicies(policyState, doc)
	policyDigest, err := c.Digest(layers)
	if err != nil {
		return ReadWorkersResult{}, err
	}
	if request.ExpectedPolicyDigest != "" && request.ExpectedPolicyDigest != policyDigest {
		return ReadWorkersResult{}, c.Fail(c.StaleBase, "read-worker policy digest is no longer current")
	}
	if err = ctx.Err(); err != nil {
		return ReadWorkersResult{}, err
	}
	started := time.Now()
	binding := ReadWorkerBinding{TaskID: task, DocumentDigest: state.DocumentDigest, CandidateDigest: doc.Candidate.SnapshotDigest, BaselineDigest: doc.Baseline.SnapshotDigest, PolicyDigest: policyDigest, TaskSeq: state.TaskSeq, SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch, KernelGeneration: currentGeneration}
	requestDigest, err := c.Digest(request)
	if err != nil {
		return ReadWorkersResult{}, err
	}
	admission := c.ReadWorkerAdmission{
		OperationID: request.OperationID, RequestDigest: requestDigest, SourceDocumentDigest: state.DocumentDigest,
		CandidateDigest: doc.Candidate.SnapshotDigest, PolicyDigest: policyDigest, SourceTaskSeq: state.TaskSeq,
		SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch, KernelGeneration: currentGeneration,
		WorkerCount: len(request.Workers), ChargedSteps: workerCount, ChargedToolCalls: workerCount,
		ChargedActiveMillis: request.TimeoutMillis, Status: c.ReadWorkerAdmissionCharged,
	}
	admission.AdmissionDigest, err = admission.Digest()
	if err != nil {
		return ReadWorkersResult{}, err
	}
	if err = admission.Validate(); err != nil {
		return ReadWorkersResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return ReadWorkersResult{}, err
	}
	doc.Budget.Steps += workerCount
	doc.Budget.ToolCalls += workerCount
	doc.Budget.ActiveMillis += request.TimeoutMillis // full ceiling remains charged after cancellation or process loss
	reservedState, err := s.record(ctx, state, doc, "ReadWorkersAdmitted", c.EventPayload{ReadWorkerAdmission: &admission})
	if err != nil {
		return ReadWorkersResult{}, err
	}
	charge := ReadWorkerCharge{Admission: admission, AdmittedTaskSeq: reservedState.TaskSeq, AdmittedDocument: reservedState.DocumentDigest}
	result := ReadWorkersResult{Binding: binding, Charge: charge, Workers: []ReadWorkerOutput{}, SchemaVersion: 1, Status: "ADMITTED", Trust: "SOURCE_BOUND_LOCAL_READ_ONLY_ADVISORY; COOPERATIVE_CANCELLATION_ONLY; HOST_EXECUTION_NOT_HARD_ISOLATED; NO_AGENT_LEASE; NO_VERIFICATION"}
	workerCtx, cancel := context.WithTimeout(ctx, time.Duration(request.TimeoutMillis)*time.Millisecond)
	defer cancel()
	// Hydrate and run every structural, privacy, policy, and retained-reference
	// validator only after the durable ceiling is charged. A failed full load is
	// a charged failure; no analyzer sees a partially validated document.
	validatedState, validatedDoc, validationErr := s.Load(workerCtx, task)
	if validationErr != nil || workerCtx.Err() != nil {
		result.DurationMS = time.Since(started).Milliseconds()
		if ctx.Err() != nil || workerCtx.Err() != nil {
			result.Status = "CHARGED_SOURCE_VALIDATION_CANCELLED"
			result.Failure = &c.Error{Code: c.Conflict, Message: "full retained-document validation was cancelled after the parent-budget ceiling was charged"}
		} else {
			result.Status = "CHARGED_SOURCE_VALIDATION_FAILED"
			var typed *c.Error
			if errors.As(validationErr, &typed) {
				result.Failure = &c.Error{Code: typed.Code, Message: "retained source or task document failed full validation after the parent-budget ceiling was charged"}
			} else {
				result.Failure = &c.Error{Code: c.StoreIntegrityError, Message: "retained source or task document failed full validation after the parent-budget ceiling was charged"}
			}
		}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	validatedLayers := s.readWorkerPolicies(validatedState, validatedDoc)
	validatedPolicyDigest, digestErr := c.Digest(validatedLayers)
	if validatedState.TaskSeq != reservedState.TaskSeq || validatedState.DocumentDigest != reservedState.DocumentDigest ||
		validatedState.CandidateDigest != binding.CandidateDigest || validatedState.BaselineDigest != binding.BaselineDigest ||
		validatedState.SpecVersion != binding.SpecVersion || validatedState.PolicyEpoch != binding.PolicyEpoch ||
		validatedState.KernelGeneration != binding.KernelGeneration || validatedDoc.Candidate.SnapshotDigest != binding.CandidateDigest ||
		validatedDoc.Baseline.SnapshotDigest != binding.BaselineDigest || digestErr != nil || validatedPolicyDigest != binding.PolicyDigest {
		result.DurationMS = time.Since(started).Milliseconds()
		result.Status = "CHARGED_SOURCE_VALIDATION_FAILED"
		result.Failure = &c.Error{Code: c.StaleBase, Message: "task or policy binding changed during charged full validation; no worker output was produced"}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	state, doc, layers = validatedState, validatedDoc, validatedLayers
	scope, scopeErr := planReadWorkerScope(workerCtx, s.Archive, doc.Candidate, layers, state)
	if scopeErr != nil {
		result.DurationMS = time.Since(started).Milliseconds()
		if ctx.Err() != nil || workerCtx.Err() != nil {
			result.Status = "CHARGED_SOURCE_SCOPE_CANCELLED"
			result.Failure = &c.Error{Code: c.Conflict, Message: "candidate scope inspection was cancelled after the full parent-budget ceiling was charged"}
		} else {
			result.Status = "CHARGED_SOURCE_SCOPE_FAILED"
			var typed *c.Error
			if errors.As(scopeErr, &typed) {
				result.Failure = &c.Error{Code: typed.Code, Message: "candidate manifest could not be read, verified or bounded after the full parent-budget ceiling was charged"}
			} else {
				result.Failure = &c.Error{Code: c.StoreIntegrityError, Message: "candidate manifest could not be read, verified or bounded after the full parent-budget ceiling was charged"}
			}
		}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	result.Binding.ReadableFiles = len(scope.Snapshot.Entries)
	result.Binding.ReadableBytes = scope.Total
	readable := make(map[string]struct{}, len(scope.Snapshot.Entries))
	for i, entry := range scope.Snapshot.Entries {
		if i&127 == 0 {
			if workerCtx.Err() != nil {
				result.DurationMS = time.Since(started).Milliseconds()
				result.Status = "CHARGED_SOURCE_SCOPE_CANCELLED"
				result.Failure = &c.Error{Code: c.Conflict, Message: "candidate scope inspection was cancelled after the full parent-budget ceiling was charged"}
				result.ResultDigest, _ = readWorkersDigest(result)
				return result, nil
			}
		}
		readable[entry.Path] = struct{}{}
	}
	for _, changed := range request.ChangedPaths {
		if _, found := readable[changed]; !found {
			result.DurationMS = time.Since(started).Milliseconds()
			result.Status = "CHARGED_SOURCE_SCOPE_FAILED"
			result.Failure = &c.Error{Code: c.PolicyDenied, Message: "changed path is not in the current policy-readable candidate; full parent-budget ceiling remains charged"}
			result.ResultDigest, _ = readWorkersDigest(result)
			return result, nil
		}
	}
	if workerCtx.Err() != nil {
		result.DurationMS = time.Since(started).Milliseconds()
		result.Status = "CHARGED_SOURCE_SCOPE_CANCELLED"
		result.Failure = &c.Error{Code: c.Conflict, Message: "candidate scope inspection was cancelled after the full parent-budget ceiling was charged"}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	capture, captureErr := hydrateReadWorkerScope(workerCtx, s.Archive, doc.Candidate, scope)
	if captureErr != nil {
		timedOut := workerCtx.Err() != nil
		cancel()
		result.DurationMS = time.Since(started).Milliseconds()
		if ctx.Err() != nil || timedOut {
			result.Status = "CHARGED_SOURCE_READ_CANCELLED"
			result.Failure = &c.Error{Code: c.Conflict, Message: "source reading was cancelled after the full parent-budget ceiling was charged"}
		} else {
			result.Status = "CHARGED_SOURCE_READ_FAILED"
			result.Failure = &c.Error{Code: c.StoreIntegrityError, Message: "policy-filtered source bytes failed integrity checks after the full parent-budget ceiling was charged"}
		}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	outputs, workerErr := runReadWorkerSet(workerCtx, request, capture, doc)
	cancel()
	if workerErr != nil {
		result.DurationMS = time.Since(started).Milliseconds()
		result.Status = "CHARGED_WORK_FAILED_OR_TIMED_OUT"
		message := "worker fleet failed after its full parent-budget ceiling was charged; inspect this result and the task journal before retrying"
		if ctx.Err() != nil {
			result.Status = "CHARGED_WORK_CANCELLED"
			message = "worker fleet was cancelled after its full parent-budget ceiling was charged; inspect this result and the task journal before retrying"
		}
		result.Failure = &c.Error{Code: c.Conflict, Message: message}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	if ctx.Err() != nil {
		result.DurationMS = time.Since(started).Milliseconds()
		result.Status = "CHARGED_WORK_CANCELLED"
		result.Failure = &c.Error{Code: c.Conflict, Message: "worker fleet was cancelled after its full parent-budget ceiling was charged; inspect this result and the task journal before retrying"}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	checkCtx, cancelCheck := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelCheck()
	current, currentDoc, err := s.Load(checkCtx, task)
	if err != nil {
		result.DurationMS = time.Since(started).Milliseconds()
		result.Status = "CHARGED_RESULT_DISCARDED"
		result.Failure = &c.Error{Code: c.StaleBase, Message: "worker output was discarded after the task journal could not be reloaded; the full parent-budget charge remains"}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	currentLayers := s.readWorkerPolicies(current, currentDoc)
	currentPolicyDigest, digestErr := c.Digest(currentLayers)
	if digestErr != nil || current.DocumentDigest != reservedState.DocumentDigest || current.CandidateDigest != binding.CandidateDigest || current.BaselineDigest != binding.BaselineDigest || current.SpecVersion != binding.SpecVersion || current.PolicyEpoch != binding.PolicyEpoch || current.KernelGeneration != binding.KernelGeneration || current.InputBarrier || len(current.PendingInputIDs) != 0 || currentDoc.Pending != nil || currentDoc.UnknownEffect || currentPolicyDigest != binding.PolicyDigest {
		result.DurationMS = time.Since(started).Milliseconds()
		result.Status = "CHARGED_RESULT_DISCARDED"
		result.Failure = &c.Error{Code: c.StaleBase, Message: "task candidate or policy changed while read workers were running; output was discarded and the full parent-budget charge remains"}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	result.Workers = outputs
	result.DurationMS = time.Since(started).Milliseconds()
	result.Status = "COMPLETE"
	encoded, err := c.CanonicalV1(result)
	if err != nil {
		return ReadWorkersResult{}, err
	}
	if len(encoded) > readWorkersMaxOutputBytes {
		result.Workers = []ReadWorkerOutput{}
		result.Status = "CHARGED_RESULT_TOO_LARGE"
		result.Failure = &c.Error{Code: c.UnsupportedCapability, Message: "typed worker results exceeded the 64 KiB output ceiling; full parent-budget charge remains"}
		result.ResultDigest, _ = readWorkersDigest(result)
		return result, nil
	}
	result.ResultDigest, err = readWorkersDigest(result)
	return result, err
}

func readWorkersDigest(result ReadWorkersResult) (string, error) {
	body := struct {
		SchemaVersion int                `json:"schema_version"`
		Binding       ReadWorkerBinding  `json:"binding"`
		Charge        ReadWorkerCharge   `json:"parent_budget_charge"`
		Workers       []ReadWorkerOutput `json:"workers"`
		Failure       *c.Error           `json:"failure,omitempty"`
		Status        string             `json:"status"`
		Trust         string             `json:"trust"`
	}{result.SchemaVersion, result.Binding, result.Charge, result.Workers, result.Failure, result.Status, result.Trust}
	return c.Digest(body)
}

func runReadWorkerSet(ctx context.Context, request ReadWorkersRequest, capture workspace.Capture, doc Document) ([]ReadWorkerOutput, error) {
	outputs := make([]ReadWorkerOutput, len(request.Workers))
	errorsByIndex := make([]error, len(request.Workers))
	var wait sync.WaitGroup
	wait.Add(len(request.Workers))
	for index, kind := range request.Workers {
		go func(index int, kind ReadWorkerKind) {
			defer wait.Done()
			if ctx.Err() != nil {
				errorsByIndex[index] = ctx.Err()
				return
			}
			var output ReadWorkerOutput
			output.Kind = kind
			switch kind {
			case ReadWorkerInventory:
				value := readInventory(capture)
				output.Inventory = &value
				output.ResultDigest, errorsByIndex[index] = c.Digest(value)
			case ReadWorkerGoGraph:
				value, err := readGoGraph(ctx, capture, request)
				if err != nil {
					errorsByIndex[index] = err
					return
				}
				output.GoGraph = &value
				output.ResultDigest, errorsByIndex[index] = c.Digest(value)
			case ReadWorkerRequirements:
				value := readRequirements(doc.Spec)
				output.Requirements = &value
				output.ResultDigest, errorsByIndex[index] = c.Digest(value)
			case ReadWorkerSearch:
				value := readSearch(ctx, capture, request.Query)
				if value.err != nil {
					errorsByIndex[index] = value.err
					return
				}
				output.Search = &value.result
				output.ResultDigest, errorsByIndex[index] = c.Digest(value.result)
			}
			outputs[index] = output
		}(index, kind)
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for index, err := range errorsByIndex {
		if err != nil {
			return nil, err
		}
		if outputs[index].ResultDigest == "" {
			return nil, c.Fail(c.StoreIntegrityError, "read worker returned no typed result digest")
		}
	}
	return outputs, nil
}

func readInventory(capture workspace.Capture) ReadInventoryResult {
	result := ReadInventoryResult{Extensions: []ReadExtensionCount{}}
	counts := map[string]int{}
	for _, entry := range capture.Snapshot.Entries {
		extension := strings.ToLower(path.Ext(entry.Path))
		if extension == "" {
			extension = "[none]"
		}
		counts[extension]++
		result.Files++
		result.TotalBytes += entry.Size
	}
	for extension, files := range counts {
		result.Extensions = append(result.Extensions, ReadExtensionCount{Extension: extension, Files: files})
	}
	sort.Slice(result.Extensions, func(i, j int) bool { return result.Extensions[i].Extension < result.Extensions[j].Extension })
	return result
}

func readGoGraph(ctx context.Context, capture workspace.Capture, request ReadWorkersRequest) (ReadGoGraphResult, error) {
	graph, err := intelligence.BuildGoSemanticHintsGraph(ctx, capture, intelligence.GoSemanticOptions{BuildTags: []string{}, CGOEnabled: "UNKNOWN"})
	if err != nil {
		return ReadGoGraphResult{}, err
	}
	if len(request.ChangedPaths) > 0 {
		selection, selectErr := intelligence.SelectGoTests(graph, request.ChangedPaths)
		if selectErr != nil {
			return ReadGoGraphResult{}, selectErr
		}
		return readGoSummary(graph, &selection), nil
	}
	return readGoSummary(graph, nil), nil
}

func readGoSummary(graph intelligence.GoSemanticGraph, selection *intelligence.GoTestSelection) ReadGoGraphResult {
	unknowns := append([]string{}, graph.Coverage.Unknowns...)
	sort.Strings(unknowns)
	return ReadGoGraphResult{Coverage: graph.Coverage.Overall, ScopeDigest: graph.SourceSnapshotDigest, Fingerprint: graph.Fingerprint, Unknowns: unknowns,
		Selection: selection, ParsedFiles: graph.Coverage.ParsedFiles, ExcludedFiles: graph.Coverage.ExcludedFiles, Nodes: len(graph.Nodes), Edges: len(graph.Edges), WidenCheckSet: graph.Coverage.WidenCheckSelection}
}

func readRequirements(spec c.TaskSpec) ReadRequirementsResult {
	result := ReadRequirementsResult{Requirements: []ReadRequirement{}}
	for _, item := range spec.Requirements {
		result.Requirements = append(result.Requirements, ReadRequirement{ID: item.ID, Risk: item.Risk, VerificationMethod: item.VerificationMethod, Required: item.Required})
		if item.Required {
			result.Required++
		}
	}
	sort.Slice(result.Requirements, func(i, j int) bool { return result.Requirements[i].ID < result.Requirements[j].ID })
	result.Total = len(result.Requirements)
	return result
}

type readSearchOutcome struct {
	result ReadSearchResult
	err    error
}

func readSearch(ctx context.Context, capture workspace.Capture, query string) readSearchOutcome {
	result := ReadSearchResult{QueryDigest: c.HashBytes([]byte(query)), Hits: []ReadSearchHit{}}
	needle := []byte(query)
	for _, entry := range capture.Snapshot.Entries {
		if err := ctx.Err(); err != nil {
			return readSearchOutcome{err: err}
		}
		raw := capture.Contents[entry.Path]
		result.FilesScanned++
		result.BytesScanned += int64(len(raw))
		cursor, lineStart, line := 0, 0, 1
		for cursor <= len(raw)-len(needle) {
			relative := bytes.Index(raw[cursor:], needle)
			if relative < 0 {
				break
			}
			index := cursor + relative
			for lineStart < index {
				next := bytes.IndexByte(raw[lineStart:index], '\n')
				if next < 0 {
					break
				}
				line++
				lineStart += next + 1
			}
			if len(result.Hits) == readWorkersMaxHits {
				result.Truncated = true
				return readSearchOutcome{result: result}
			}
			result.Hits = append(result.Hits, ReadSearchHit{Path: entry.Path, Line: line, ByteOffset: index})
			cursor = index + len(needle)
		}
	}
	return readSearchOutcome{result: result}
}
