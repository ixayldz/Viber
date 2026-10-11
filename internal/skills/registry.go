package skills

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

type Registry struct {
	mu       sync.Mutex
	path     string
	root     *os.Root
	policy   TrustPolicy
	policyID string
	closed   bool
}

const (
	MaxRegistryBytes   = 64 << 20
	MaxRegistryEntries = 40000
)

type eventBody struct {
	SchemaVersion      int    `json:"schema_version"`
	Sequence           uint64 `json:"sequence"`
	PreviousDigest     string `json:"previous_digest,omitempty"`
	Kind               string `json:"kind"`
	SkillID            string `json:"skill_id"`
	Version            uint64 `json:"version"`
	Definition         string `json:"definition_digest"`
	Evaluation         string `json:"evaluation_digest,omitempty"`
	PreviousVersion    uint64 `json:"previous_version,omitempty"`
	PolicyDigest       string `json:"trust_policy_digest,omitempty"`
	AuthorityDigest    string `json:"authority_digest,omitempty"`
	Scope              Scope  `json:"scope"`
	CrossScopeReviewed bool   `json:"cross_scope_reviewed,omitempty"`
	AtUTC              string `json:"at_utc"`
	Actor              string `json:"actor"`
}

type lifecycleEvent struct {
	SchemaVersion      int    `json:"schema_version"`
	Sequence           uint64 `json:"sequence"`
	PreviousDigest     string `json:"previous_digest,omitempty"`
	Kind               string `json:"kind"`
	SkillID            string `json:"skill_id"`
	Version            uint64 `json:"version"`
	Definition         string `json:"definition_digest"`
	Evaluation         string `json:"evaluation_digest,omitempty"`
	PreviousVersion    uint64 `json:"previous_version,omitempty"`
	PolicyDigest       string `json:"trust_policy_digest,omitempty"`
	AuthorityDigest    string `json:"authority_digest,omitempty"`
	Scope              Scope  `json:"scope"`
	CrossScopeReviewed bool   `json:"cross_scope_reviewed,omitempty"`
	AtUTC              string `json:"at_utc"`
	Actor              string `json:"actor"`
	Digest             string `json:"digest"`
}

func (event lifecycleEvent) body() eventBody {
	return eventBody{SchemaVersion: event.SchemaVersion, Sequence: event.Sequence, PreviousDigest: event.PreviousDigest, Kind: event.Kind, SkillID: event.SkillID, Version: event.Version, Definition: event.Definition, Evaluation: event.Evaluation, PreviousVersion: event.PreviousVersion, PolicyDigest: event.PolicyDigest, AuthorityDigest: event.AuthorityDigest, Scope: event.Scope, CrossScopeReviewed: event.CrossScopeReviewed, AtUTC: event.AtUTC, Actor: event.Actor}
}

type versionRecord struct {
	Definition       Definition
	DefinitionDigest string
	Evaluation       EvaluationReceipt
	EvaluationDigest string
	Lifecycle        string
	LastSequence     uint64
	LastDigest       string
	PolicyDigest     string
	CandidatePolicy  string
	EvaluationPolicy string
	AuthorityDigest  string
}

type registryState struct {
	Versions map[string]map[uint64]*versionRecord
	Active   map[string]uint64
	Sequence uint64
	Digest   string
}

type receiptMessage struct {
	SchemaVersion    int            `json:"schema_version"`
	EvaluatorKeyID   string         `json:"evaluator_key_id"`
	EvaluatorDigest  string         `json:"evaluator_digest"`
	ProtocolDigest   string         `json:"protocol_digest"`
	DatasetDigest    string         `json:"dataset_digest"`
	HoldoutDigest    string         `json:"holdout_digest"`
	DefinitionDigest string         `json:"definition_digest"`
	SkillID          string         `json:"skill_id"`
	SkillVersion     uint64         `json:"skill_version"`
	Source           Source         `json:"source"`
	Scope            Scope          `json:"scope"`
	Cases            []CaseEvidence `json:"cases"`
	CreatedAtUTC     string         `json:"created_at_utc"`
}

func Open(directory string, trust TrustPolicy) (*Registry, error) {
	if err := trust.Validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(directory) || strings.ContainsAny(directory, "\x00\r\n") {
		return nil, c.Fail(c.InvalidArgument, "absolute private skills registry path required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, c.Fail(c.InvalidArgument, "skills registry path unavailable")
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, c.Fail(c.PolicyDenied, "private skills registry unavailable")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, c.Fail(c.PolicyDenied, "private skills registry unavailable")
	}
	policyRaw, err := c.CanonicalV1(trust)
	if err != nil {
		root.Close()
		return nil, err
	}
	policyID := c.HashBytes(policyRaw)
	if len(policyRaw) > 32<<10 {
		root.Close()
		return nil, c.Fail(c.UnsupportedCapability, "operator skill trust policy exceeds its size bound")
	}
	if err = publishBounded(root, filepath.ToSlash(filepath.Join("policies", policyID+".json")), policyRaw); err != nil {
		root.Close()
		return nil, err
	}
	return &Registry{path: absolute, root: root, policy: trust, policyID: policyID}, nil
}

// ReadTrustPolicy reads a bounded, canonical trust policy from an operator
// supplied local file. Callers must keep that file outside the project and
// registry and treat its path as an operator pin, not skill-authored input.
func ReadTrustPolicy(name string) (TrustPolicy, error) {
	var trust TrustPolicy
	if !filepath.IsAbs(name) || strings.ContainsAny(name, "\x00\r\n") {
		return trust, c.Fail(c.InvalidArgument, "absolute operator skill trust-policy path required")
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return trust, c.Fail(c.InvalidArgument, "operator trust-policy path unavailable")
	}
	root, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return trust, c.Fail(c.PolicyDenied, "operator skill trust policy unavailable")
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(abs), 32<<10)
	if err != nil || c.DecodeStrict(raw, &trust) != nil {
		return TrustPolicy{}, c.Fail(c.PolicyDenied, "operator skill trust policy is invalid")
	}
	canonical, err := c.CanonicalV1(trust)
	if err != nil || !bytes.Equal(canonical, raw) {
		return TrustPolicy{}, c.Fail(c.StoreIntegrityError, "operator skill trust policy must be canonical")
	}
	if err = trust.Validate(); err != nil {
		return TrustPolicy{}, err
	}
	return trust, nil
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.root.Close()
}

func (r *Registry) TrustPolicyDigest() string { return r.policyID }

func (r *Registry) Propose(ctx context.Context, definition Definition) (Status, error) {
	var empty Status
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := definition.Validate(); err != nil {
		return empty, err
	}
	if err := r.checkPolicyDefinition(definition); err != nil {
		return empty, err
	}
	raw, err := c.CanonicalV1(definition)
	if err != nil || len(raw) > MaxDefinitionBytes {
		return empty, c.Fail(c.UnsupportedCapability, "skill definition exceeds its size bound")
	}
	digest := c.HashBytes(raw)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return empty, err
	}
	state, err := r.replayLocked()
	if err != nil {
		return empty, err
	}
	versions := state.Versions[definition.ID]
	if versions == nil {
		if definition.Version != 1 {
			return empty, c.Fail(c.StaleRequest, "first skill version must be 1")
		}
	} else {
		var latest uint64
		for version := range versions {
			if version > latest {
				latest = version
			}
		}
		if definition.Version != latest+1 {
			return empty, c.Fail(c.StaleRequest, "skill versions must be appended in sequence")
		}
		previous := versions[latest].Definition
		if previous.Scope != definition.Scope {
			return empty, c.Fail(c.StaleAuthority, "a skill version cannot change its scope")
		}
	}
	if err = publishBounded(r.root, filepath.ToSlash(filepath.Join("definitions", digest+".json")), raw); err != nil {
		return empty, err
	}
	if err = r.verifyDefinitionFile(digest, definition); err != nil {
		return empty, err
	}
	event, err := r.appendLocked(ctx, state, eventBody{Kind: "CANDIDATE", SkillID: definition.ID, Version: definition.Version, Definition: digest, PolicyDigest: r.policyID, Scope: definition.Scope, Actor: "USER"})
	if err != nil {
		return empty, err
	}
	return Status{ID: definition.ID, Version: definition.Version, DefinitionDigest: digest, Lifecycle: "CANDIDATE", LastEventSequence: event.Sequence, LastEventDigest: event.Digest}, nil
}

// ImportEvaluation accepts only a signed result from an operator-pinned
// evaluator and protocol. It does not infer skill quality from task success.
func (r *Registry) ImportEvaluation(ctx context.Context, id string, version uint64, raw []byte) (Status, error) {
	var empty Status
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if len(raw) == 0 || len(raw) > MaxEvaluationBytes || !utf8.Valid(raw) {
		return empty, c.Fail(c.UnsupportedCapability, "bounded signed skill evaluation receipt required")
	}
	var receipt EvaluationReceipt
	if err := c.DecodeStrict(raw, &receipt); err != nil {
		return empty, err
	}
	canonical, err := c.CanonicalV1(receipt)
	if err != nil || !bytes.Equal(canonical, raw) {
		return empty, c.Fail(c.StoreIntegrityError, "evaluation receipt must use canonical bytes")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return empty, err
	}
	state, err := r.replayLocked()
	if err != nil {
		return empty, err
	}
	record := findVersion(state, id, version)
	if record == nil || record.Lifecycle == "REVOKED" || record.Lifecycle == "EXPIRED" {
		return empty, c.Fail(c.StaleRequest, "skill candidate is unavailable for evaluation")
	}
	if record.EvaluationDigest != "" {
		return empty, c.Fail(c.Conflict, "skill version already has an immutable evaluation receipt")
	}
	if err = r.validateReceipt(receipt, record); err != nil {
		return empty, err
	}
	if err = r.checkPolicyDefinition(record.Definition); err != nil {
		return empty, err
	}
	digest := c.HashBytes(raw)
	if err = publishBounded(r.root, filepath.ToSlash(filepath.Join("evaluations", digest+".json")), raw); err != nil {
		return empty, err
	}
	event, err := r.appendLocked(ctx, state, eventBody{Kind: "EVALUATED", SkillID: id, Version: version, Definition: record.DefinitionDigest, Evaluation: digest, PolicyDigest: r.policyID, Scope: record.Definition.Scope, Actor: "PINNED_EVALUATOR"})
	if err != nil {
		return empty, err
	}
	lifecycle := "EVALUATED_FAILED"
	if receiptPasses(receipt) {
		lifecycle = "EVALUATED"
	}
	return Status{ID: id, Version: version, DefinitionDigest: record.DefinitionDigest, EvaluationDigest: digest, Lifecycle: lifecycle, LastEventSequence: event.Sequence, LastEventDigest: event.Digest}, nil
}

func (r *Registry) Activate(ctx context.Context, id string, version uint64, scope Scope, authorityDigest string) (Status, error) {
	return r.activate(ctx, "ACTIVATED", id, version, 0, scope, authorityDigest, false)
}

func (r *Registry) Rollback(ctx context.Context, id string, version uint64, scope Scope, authorityDigest string) (Status, error) {
	return r.activate(ctx, "ROLLBACK", id, version, 0, scope, authorityDigest, false)
}

// ActivateReviewed requires a separate acknowledgement before an explicitly
// cross-scope USER or GENERIC skill can become active.
func (r *Registry) ActivateReviewed(ctx context.Context, id string, version uint64, scope Scope, authorityDigest string, crossScopeReviewed bool) (Status, error) {
	return r.activate(ctx, "ACTIVATED", id, version, 0, scope, authorityDigest, crossScopeReviewed)
}

// RollbackReviewed applies the same explicit cross-scope review to rollback.
func (r *Registry) RollbackReviewed(ctx context.Context, id string, version uint64, scope Scope, authorityDigest string, crossScopeReviewed bool) (Status, error) {
	return r.activate(ctx, "ROLLBACK", id, version, 0, scope, authorityDigest, crossScopeReviewed)
}

func (r *Registry) activate(ctx context.Context, kind, id string, version, requestedPrevious uint64, scope Scope, authorityDigest string, crossScopeReviewed bool) (Status, error) {
	var empty Status
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if scope.Validate() != nil || !c.ValidDigest(authorityDigest) {
		return empty, c.Fail(c.InvalidArgument, "current skill scope and operator authority binding required")
	}
	needsCrossScopeReview := scope.Kind == "USER" || scope.Kind == "GENERIC"
	if crossScopeReviewed != needsCrossScopeReview {
		return empty, c.Fail(c.PolicyDenied, "USER/GENERIC skill activation requires a separate explicit cross-scope review")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return empty, err
	}
	state, err := r.replayLocked()
	if err != nil {
		return empty, err
	}
	record := findVersion(state, id, version)
	if record == nil || record.Lifecycle == "REVOKED" || record.Lifecycle == "EXPIRED" || record.EvaluationDigest == "" || !receiptPasses(record.Evaluation) || record.Definition.Scope != scope {
		return empty, c.Fail(c.PolicyDenied, "activation requires an exact passing pinned evaluation and matching scope")
	}
	if err = r.checkPolicyDefinition(record.Definition); err != nil {
		return empty, err
	}
	if err = r.validateReceiptWithPolicy(record.Evaluation, record, r.policy); err != nil {
		return empty, c.Fail(c.PolicyDenied, "current operator policy does not admit the stored evaluation receipt")
	}
	if expired(record.Definition, time.Now().UTC()) {
		return empty, c.Fail(c.StaleAuthority, "expired skill cannot be activated")
	}
	previous := state.Active[id]
	if kind == "ROLLBACK" {
		if previous == 0 || version >= previous || requestedPrevious != 0 && requestedPrevious != previous {
			return empty, c.Fail(c.StaleRequest, "rollback target must be an evaluated earlier version")
		}
	} else if previous == version {
		if record.PolicyDigest == r.policyID && record.AuthorityDigest == authorityDigest {
			return statusOf(id, version, record), nil
		}
	}
	if state.Active[id] != previous {
		return empty, c.Fail(c.StaleBase, "active skill changed during admission")
	}
	_, err = r.appendLocked(ctx, state, eventBody{Kind: kind, SkillID: id, Version: version, Definition: record.DefinitionDigest, Evaluation: record.EvaluationDigest, PreviousVersion: previous, PolicyDigest: r.policyID, AuthorityDigest: authorityDigest, Scope: scope, CrossScopeReviewed: crossScopeReviewed, Actor: "USER"})
	if err != nil {
		return empty, err
	}
	updated, err := r.replayLocked()
	if err != nil {
		return empty, err
	}
	return statusOf(id, version, updated.Versions[id][version]), nil
}

func (r *Registry) Revoke(ctx context.Context, id string, version uint64, authorityDigest string) (Status, error) {
	var empty Status
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !c.ValidDigest(authorityDigest) {
		return empty, c.Fail(c.InvalidArgument, "current operator authority binding required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return empty, err
	}
	state, err := r.replayLocked()
	if err != nil {
		return empty, err
	}
	if state.Active[id] != version {
		return empty, c.Fail(c.StaleRequest, "only the active skill version can be revoked")
	}
	record := findVersion(state, id, version)
	event, err := r.appendLocked(ctx, state, eventBody{Kind: "REVOKED", SkillID: id, Version: version, Definition: record.DefinitionDigest, Evaluation: record.EvaluationDigest, PreviousVersion: version, PolicyDigest: r.policyID, AuthorityDigest: authorityDigest, Scope: record.Definition.Scope, Actor: "USER"})
	if err != nil {
		return empty, err
	}
	return Status{ID: id, Version: version, DefinitionDigest: record.DefinitionDigest, EvaluationDigest: record.EvaluationDigest, Lifecycle: "REVOKED", LastEventSequence: event.Sequence, LastEventDigest: event.Digest}, nil
}

func (r *Registry) Expire(ctx context.Context, now time.Time) ([]Status, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if now.IsZero() || now.Location() != time.UTC {
		return nil, c.Fail(c.InvalidArgument, "UTC expiry clock required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return nil, err
	}
	state, err := r.replayLocked()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(state.Active))
	for id := range state.Active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := []Status{}
	for _, id := range ids {
		version := state.Active[id]
		record := findVersion(state, id, version)
		if record == nil || !expired(record.Definition, now) {
			continue
		}
		event, appendErr := r.appendLocked(ctx, state, eventBody{Kind: "EXPIRED", SkillID: id, Version: version, Definition: record.DefinitionDigest, Evaluation: record.EvaluationDigest, PreviousVersion: version, PolicyDigest: record.PolicyDigest, AuthorityDigest: record.AuthorityDigest, Scope: record.Definition.Scope, Actor: "POLICY_EXPIRY"})
		if appendErr != nil {
			return results, appendErr
		}
		state, err = r.replayLocked()
		if err != nil {
			return results, err
		}
		results = append(results, Status{ID: id, Version: version, DefinitionDigest: record.DefinitionDigest, EvaluationDigest: record.EvaluationDigest, Lifecycle: "EXPIRED", LastEventSequence: event.Sequence, LastEventDigest: event.Digest})
	}
	return results, nil
}

func (r *Registry) Status(ctx context.Context) ([]Status, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return nil, err
	}
	state, err := r.replayLocked()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(state.Versions))
	for id := range state.Versions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := []Status{}
	for _, id := range ids {
		versions := make([]uint64, 0, len(state.Versions[id]))
		for version := range state.Versions[id] {
			versions = append(versions, version)
		}
		sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
		for _, version := range versions {
			record := state.Versions[id][version]
			result = append(result, statusOf(id, version, record))
		}
	}
	return result, nil
}

// InspectVersion returns the immutable source and definition bytes bound to a
// particular lifecycle version. It is intended for current-source admission
// before activation; callers must still use ActivateReviewed to append the
// audited lifecycle event.
func (r *Registry) InspectVersion(ctx context.Context, id string, version uint64) (Definition, Status, error) {
	var definition Definition
	var status Status
	if err := ctx.Err(); err != nil {
		return definition, status, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return definition, status, err
	}
	state, err := r.replayLocked()
	if err != nil {
		return definition, status, err
	}
	record := findVersion(state, id, version)
	if record == nil {
		return definition, status, c.Fail(c.StaleRequest, "skill version is unavailable")
	}
	return record.Definition, statusOf(id, version, record), nil
}

func (r *Registry) LoadActive(ctx context.Context, scopes []Scope, authorityDigest string, currentCapabilities []string) ([]Active, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.ValidDigest(authorityDigest) || len(scopes) > 4 || len(currentCapabilities) > 8 {
		return nil, c.Fail(c.InvalidArgument, "bounded current scope and authority required")
	}
	if _, err := r.Expire(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return nil, err
	}
	state, err := r.replayLocked()
	if err != nil {
		return nil, err
	}
	scopeSet := map[Scope]bool{}
	for _, scope := range scopes {
		if err := scope.Validate(); err != nil {
			return nil, err
		}
		scopeSet[scope] = true
	}
	activeIDs := make([]string, 0, len(state.Active))
	for id := range state.Active {
		activeIDs = append(activeIDs, id)
	}
	sort.Strings(activeIDs)
	result := []Active{}
	for _, id := range activeIDs {
		version := state.Active[id]
		record := findVersion(state, id, version)
		if record == nil || record.Lifecycle != "ACTIVE" {
			return nil, c.Fail(c.StoreIntegrityError, "active skill lifecycle receipt is inconsistent")
		}
		if record.PolicyDigest != r.policyID || record.AuthorityDigest != authorityDigest {
			return nil, c.Fail(c.StaleAuthority, "active skill trust policy or operator authority revision changed")
		}
		if !scopeSet[record.Definition.Scope] {
			continue
		}
		if err = r.checkPolicyDefinition(record.Definition); err != nil || record.EvaluationDigest == "" || !receiptPasses(record.Evaluation) {
			return nil, c.Fail(c.PolicyDenied, "active skill no longer meets the pinned evaluation policy")
		}
		capabilities := intersectCapabilities(record.Definition.Capabilities, currentCapabilities)
		result = append(result, Active{Definition: record.Definition, DefinitionDigest: record.DefinitionDigest, EvaluationDigest: record.EvaluationDigest, TrustPolicyDigest: record.PolicyDigest, AuthorityDigest: record.AuthorityDigest, CurrentCapabilities: capabilities})
	}
	return result, nil
}

func (r *Registry) checkPolicyDefinition(definition Definition) error {
	return checkPolicyDefinitionUnder(definition, r.policy)
}

func checkPolicyDefinitionUnder(definition Definition, policy TrustPolicy) error {
	if !contains(policy.Scopes, definition.Scope.Kind) {
		return c.Fail(c.PolicyDenied, "operator policy does not admit this skill scope")
	}
	for _, capability := range definition.Capabilities {
		if !contains(policy.Capabilities, capability) {
			return c.Fail(c.PolicyDenied, "operator policy does not admit this skill capability hint")
		}
	}
	return nil
}

func (r *Registry) validateReceipt(receipt EvaluationReceipt, record *versionRecord) error {
	return r.validateReceiptWithPolicy(receipt, record, r.policy)
}

func (r *Registry) validateReceiptWithPolicy(receipt EvaluationReceipt, record *versionRecord, trust TrustPolicy) error {
	if receipt.SchemaVersion != SchemaVersion || receipt.SkillID != record.Definition.ID || receipt.SkillVersion != record.Definition.Version || receipt.DefinitionDigest != record.DefinitionDigest || receipt.Source != record.Definition.Source || receipt.Scope != record.Definition.Scope || receipt.Source.Validate() != nil || receipt.Scope.Validate() != nil || len(receipt.Signature) != ed25519.SignatureSize || !c.ValidDigest(receipt.EvaluatorDigest) || !c.ValidDigest(receipt.ProtocolDigest) || !c.ValidDigest(receipt.DatasetDigest) || !c.ValidDigest(receipt.HoldoutDigest) || len(receipt.Cases) < 1 || len(receipt.Cases) > 256 {
		return c.Fail(c.StoreIntegrityError, "evaluation receipt does not bind the exact skill, source and scope")
	}
	created, err := time.Parse(time.RFC3339, receipt.CreatedAtUTC)
	if err != nil || created.Location() != time.UTC || created.Format(time.RFC3339) != receipt.CreatedAtUTC {
		return c.Fail(c.StoreIntegrityError, "evaluation receipt creation time is invalid")
	}
	var public ed25519.PublicKey
	for _, key := range trust.Evaluators {
		if key.ID == receipt.EvaluatorKeyID {
			public = ed25519.PublicKey(key.PublicKey)
			break
		}
	}
	if len(public) != ed25519.PublicKeySize {
		return c.Fail(c.PolicyDenied, "evaluation signer is not operator-pinned")
	}
	var protocol *EvaluationProtocol
	for i := range trust.Protocols {
		if trust.Protocols[i].Digest == receipt.ProtocolDigest {
			protocol = &trust.Protocols[i]
			break
		}
	}
	if protocol == nil || protocol.DatasetDigest != receipt.DatasetDigest || protocol.HoldoutDigest != receipt.HoldoutDigest || protocol.EvaluatorDigest != receipt.EvaluatorDigest || len(receipt.Cases) < protocol.MinCases || len(receipt.Cases) > protocol.MaxCases || !contains(protocol.AllowedScopes, receipt.Scope.Kind) {
		return c.Fail(c.PolicyDenied, "evaluation dataset, holdout, evaluator or protocol is not pinned")
	}
	seen := map[string]bool{}
	for _, item := range receipt.Cases {
		if !skillIDPattern.MatchString(item.CaseID) || seen[item.CaseID] || !c.ValidDigest(item.InputDigest) || !c.ValidDigest(item.ExpectedDigest) || !c.ValidDigest(item.ObservedDigest) || !c.ValidDigest(item.ResultDigest) {
			return c.Fail(c.StoreIntegrityError, "evaluation case evidence is incomplete or duplicated")
		}
		if item.ExpectedDigest == item.ObservedDigest && item.Verdict != "PASS" || item.ExpectedDigest != item.ObservedDigest && item.Verdict != "FAIL" {
			return c.Fail(c.StoreIntegrityError, "evaluation verdict differs from bound case digests")
		}
		seen[item.CaseID] = true
	}
	message, err := EvaluationSigningBytes(receipt)
	if err != nil || !ed25519.Verify(public, message, receipt.Signature) {
		return c.Fail(c.StoreIntegrityError, "evaluation receipt signature is invalid")
	}
	return nil
}

func receiptPasses(receipt EvaluationReceipt) bool {
	if len(receipt.Cases) == 0 {
		return false
	}
	for _, item := range receipt.Cases {
		if item.Verdict != "PASS" {
			return false
		}
	}
	return true
}

func intersectCapabilities(requested, current []string) []string {
	currentSet := map[string]bool{}
	for _, capability := range current {
		currentSet[capability] = true
	}
	result := []string{}
	for _, capability := range requested {
		if currentSet[capability] {
			result = append(result, capability)
		}
	}
	sort.Strings(result)
	return result
}

func expired(definition Definition, now time.Time) bool {
	if definition.ExpiresAtUTC == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, definition.ExpiresAtUTC)
	return err != nil || !now.Before(expires)
}

func findVersion(state registryState, id string, version uint64) *versionRecord {
	if state.Versions[id] == nil {
		return nil
	}
	return state.Versions[id][version]
}

func statusOf(id string, version uint64, record *versionRecord) Status {
	if record == nil {
		return Status{}
	}
	return Status{ID: id, Version: version, DefinitionDigest: record.DefinitionDigest, EvaluationDigest: record.EvaluationDigest, Lifecycle: record.Lifecycle, LastEventSequence: record.LastSequence, LastEventDigest: record.LastDigest}
}

func (r *Registry) ensureOpen() error {
	if r == nil || r.root == nil || r.closed {
		return c.Fail(c.StoreOwned, "skills registry is closed")
	}
	return nil
}

func (r *Registry) replayLocked() (registryState, error) {
	state := registryState{Versions: map[string]map[uint64]*versionRecord{}, Active: map[string]uint64{}}
	entries, err := readDir(r.root, "events")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state, err
	}
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if len(entries) > MaxSkillEvents {
		return state, c.Fail(c.UnsupportedCapability, "skill lifecycle event quota exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for index, entry := range entries {
		sequence := uint64(index + 1)
		name := fmt.Sprintf("%020d.json", sequence)
		if entry.Name() != name || entry.IsDir() {
			return state, c.Fail(c.StoreIntegrityError, "skill lifecycle sequence contains an unexpected file")
		}
		raw, readErr := fileguard.ReadRegular(r.root, filepath.ToSlash(filepath.Join("events", name)), MaxSkillEventBytes)
		if readErr != nil {
			return state, readErr
		}
		var event lifecycleEvent
		if readErr = c.DecodeStrict(raw, &event); readErr != nil {
			return state, readErr
		}
		canonical, encodeErr := c.CanonicalV1(event)
		if encodeErr != nil || !bytes.Equal(canonical, raw) {
			return state, c.Fail(c.StoreIntegrityError, "skill lifecycle event is not canonical")
		}
		bodyRaw, encodeErr := c.CanonicalV1(event.body())
		parsedAt, timeErr := time.Parse(time.RFC3339Nano, event.AtUTC)
		if encodeErr != nil || event.SchemaVersion != SchemaVersion || event.Sequence != sequence || event.PreviousDigest != state.Digest || event.Digest != c.HashBytes(bodyRaw) || timeErr != nil || parsedAt.UTC().Format(time.RFC3339Nano) != event.AtUTC || event.Actor == "" {
			return state, c.Fail(c.StoreIntegrityError, "skill lifecycle hash chain is invalid")
		}
		if err = r.applyEvent(&state, event); err != nil {
			return state, err
		}
		state.Sequence, state.Digest = event.Sequence, event.Digest
	}
	return state, nil
}

func (r *Registry) applyEvent(state *registryState, event lifecycleEvent) error {
	if event.SkillID == "" || !skillIDPattern.MatchString(event.SkillID) || event.Version < 1 || event.Version > 1<<53 || event.Scope.Validate() != nil {
		return c.Fail(c.StoreIntegrityError, "invalid skill lifecycle binding")
	}
	expectedActor := map[string]string{"CANDIDATE": "USER", "EVALUATED": "PINNED_EVALUATOR", "ACTIVATED": "USER", "ROLLBACK": "USER", "REVOKED": "USER", "EXPIRED": "POLICY_EXPIRY"}[event.Kind]
	if expectedActor == "" || event.Actor != expectedActor {
		return c.Fail(c.StoreIntegrityError, "skill lifecycle actor does not match event kind")
	}
	switch event.Kind {
	case "CANDIDATE":
		policy, err := r.readPolicy(event.PolicyDigest)
		if err != nil {
			return err
		}
		definition, err := r.readDefinition(event.Definition)
		if err != nil {
			return err
		}
		if err = checkPolicyDefinitionUnder(definition, policy); err != nil {
			return c.Fail(c.StoreIntegrityError, "candidate exceeded its recorded operator policy")
		}
		versions := state.Versions[event.SkillID]
		if versions == nil {
			if event.Version != 1 {
				return c.Fail(c.StoreIntegrityError, "skill version history does not start at one")
			}
			versions = map[uint64]*versionRecord{}
			state.Versions[event.SkillID] = versions
		}
		if len(versions) != 0 {
			var latest uint64
			for version := range versions {
				if version > latest {
					latest = version
				}
			}
			if event.Version != latest+1 {
				return c.Fail(c.StoreIntegrityError, "skill version sequence is invalid")
			}
		}
		if definition.ID != event.SkillID || definition.Version != event.Version || definition.Scope != event.Scope || state.Versions[event.SkillID][event.Version] != nil {
			return c.Fail(c.StoreIntegrityError, "skill candidate event differs from immutable definition")
		}
		state.Versions[event.SkillID][event.Version] = &versionRecord{Definition: definition, DefinitionDigest: event.Definition, Lifecycle: "CANDIDATE", CandidatePolicy: event.PolicyDigest, LastSequence: event.Sequence, LastDigest: event.Digest}
	case "EVALUATED":
		record := findVersion(*state, event.SkillID, event.Version)
		policy, policyErr := r.readPolicy(event.PolicyDigest)
		if record == nil || record.Lifecycle != "CANDIDATE" || record.DefinitionDigest != event.Definition || event.Evaluation == "" || policyErr != nil {
			return c.Fail(c.StoreIntegrityError, "evaluation event does not bind a candidate")
		}
		receipt, err := r.readEvaluation(event.Evaluation)
		if err != nil || checkPolicyDefinitionUnder(record.Definition, policy) != nil || r.validateReceiptWithPolicy(receipt, record, policy) != nil {
			return c.Fail(c.StoreIntegrityError, "protected skill evaluation receipt is invalid")
		}
		record.Evaluation, record.EvaluationDigest = receipt, event.Evaluation
		record.EvaluationPolicy = event.PolicyDigest
		if receiptPasses(receipt) {
			record.Lifecycle = "EVALUATED"
		} else {
			record.Lifecycle = "EVALUATED_FAILED"
		}
		record.LastSequence, record.LastDigest = event.Sequence, event.Digest
	case "ACTIVATED", "ROLLBACK":
		record := findVersion(*state, event.SkillID, event.Version)
		policy, policyErr := r.readPolicy(event.PolicyDigest)
		needsCrossScopeReview := event.Scope.Kind == "USER" || event.Scope.Kind == "GENERIC"
		if record == nil || record.Lifecycle == "REVOKED" || record.Lifecycle == "EXPIRED" || record.DefinitionDigest != event.Definition || record.EvaluationDigest == "" || record.EvaluationDigest != event.Evaluation || !receiptPasses(record.Evaluation) || record.Definition.Scope != event.Scope || policyErr != nil || !c.ValidDigest(event.AuthorityDigest) || event.CrossScopeReviewed != needsCrossScopeReview || checkPolicyDefinitionUnder(record.Definition, policy) != nil || r.validateReceiptWithPolicy(record.Evaluation, record, policy) != nil {
			return c.Fail(c.StoreIntegrityError, "activation lacks a matching pinned passing evaluation or policy binding")
		}
		previous := state.Active[event.SkillID]
		if previous != event.PreviousVersion || event.Kind == "ROLLBACK" && (previous == 0 || event.Version >= previous) || event.Kind == "ACTIVATED" && previous == event.Version && record.PolicyDigest == event.PolicyDigest && record.AuthorityDigest == event.AuthorityDigest {
			return c.Fail(c.StoreIntegrityError, "skill activation or rollback sequence is invalid")
		}
		if expired(record.Definition, mustParseTime(event.AtUTC)) {
			return c.Fail(c.StoreIntegrityError, "expired skill was activated")
		}
		record.Lifecycle, record.PolicyDigest, record.AuthorityDigest = "ACTIVE", event.PolicyDigest, event.AuthorityDigest
		state.Active[event.SkillID] = event.Version
		record.LastSequence, record.LastDigest = event.Sequence, event.Digest
		if previous != 0 && previous != event.Version {
			if old := findVersion(*state, event.SkillID, previous); old != nil {
				old.Lifecycle = "EVALUATED"
				old.LastSequence, old.LastDigest = event.Sequence, event.Digest
			}
		}
	case "REVOKED", "EXPIRED":
		record := findVersion(*state, event.SkillID, event.Version)
		if _, policyErr := r.readPolicy(event.PolicyDigest); policyErr != nil || !c.ValidDigest(event.AuthorityDigest) || record == nil || state.Active[event.SkillID] != event.Version || event.PreviousVersion != event.Version || record.DefinitionDigest != event.Definition || record.EvaluationDigest != event.Evaluation || record.Definition.Scope != event.Scope || record.Lifecycle != "ACTIVE" {
			return c.Fail(c.StoreIntegrityError, "skill deactivation does not bind the active version")
		}
		if event.Kind == "EXPIRED" && !expired(record.Definition, mustParseTime(event.AtUTC)) {
			return c.Fail(c.StoreIntegrityError, "skill expiry receipt predates its deadline")
		}
		delete(state.Active, event.SkillID)
		record.Lifecycle = event.Kind
		record.LastSequence, record.LastDigest = event.Sequence, event.Digest
	default:
		return c.Fail(c.StoreIntegrityError, "unknown skill lifecycle event")
	}
	return nil
}

func mustParseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed.UTC()
}

func (r *Registry) appendLocked(ctx context.Context, state registryState, body eventBody) (lifecycleEvent, error) {
	if err := ctx.Err(); err != nil {
		return lifecycleEvent{}, err
	}
	if state.Sequence >= MaxSkillEvents {
		return lifecycleEvent{}, c.Fail(c.UnsupportedCapability, "skill lifecycle event quota reached")
	}
	body.SchemaVersion, body.Sequence, body.PreviousDigest = SchemaVersion, state.Sequence+1, state.Digest
	if body.AtUTC == "" {
		body.AtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	}
	bodyRaw, err := c.CanonicalV1(body)
	if err != nil {
		return lifecycleEvent{}, err
	}
	event := lifecycleEvent{SchemaVersion: body.SchemaVersion, Sequence: body.Sequence, PreviousDigest: body.PreviousDigest, Kind: body.Kind, SkillID: body.SkillID, Version: body.Version, Definition: body.Definition, Evaluation: body.Evaluation, PreviousVersion: body.PreviousVersion, PolicyDigest: body.PolicyDigest, AuthorityDigest: body.AuthorityDigest, Scope: body.Scope, CrossScopeReviewed: body.CrossScopeReviewed, AtUTC: body.AtUTC, Actor: body.Actor, Digest: c.HashBytes(bodyRaw)}
	raw, err := c.CanonicalV1(event)
	if err != nil || len(raw) > MaxSkillEventBytes {
		return lifecycleEvent{}, c.Fail(c.UnsupportedCapability, "skill lifecycle event exceeds its size bound")
	}
	if err = ensureRegistryCapacity(r.root, int64(len(raw))); err != nil {
		return lifecycleEvent{}, err
	}
	name := filepath.ToSlash(filepath.Join("events", fmt.Sprintf("%020d.json", body.Sequence)))
	if err = fileguard.Publish(r.root, name, raw); err != nil {
		return lifecycleEvent{}, err
	}
	return event, nil
}

func (r *Registry) readDefinition(digest string) (Definition, error) {
	var definition Definition
	if !c.ValidDigest(digest) {
		return definition, c.Fail(c.StoreIntegrityError, "invalid skill content digest")
	}
	raw, err := fileguard.ReadRegular(r.root, filepath.ToSlash(filepath.Join("definitions", digest+".json")), MaxDefinitionBytes)
	if err != nil || c.HashBytes(raw) != digest || c.DecodeStrict(raw, &definition) != nil || definition.Validate() != nil {
		return definition, c.Fail(c.StoreIntegrityError, "skill definition content is unavailable or invalid")
	}
	canonical, err := c.CanonicalV1(definition)
	if err != nil || !bytes.Equal(canonical, raw) {
		return definition, c.Fail(c.StoreIntegrityError, "skill definition bytes are not canonical")
	}
	return definition, nil
}

func (r *Registry) readPolicy(digest string) (TrustPolicy, error) {
	var policy TrustPolicy
	if !c.ValidDigest(digest) {
		return policy, c.Fail(c.StoreIntegrityError, "invalid skill trust policy digest")
	}
	raw, err := fileguard.ReadRegular(r.root, filepath.ToSlash(filepath.Join("policies", digest+".json")), 32<<10)
	if err != nil || c.HashBytes(raw) != digest || c.DecodeStrict(raw, &policy) != nil || policy.Validate() != nil {
		return policy, c.Fail(c.StoreIntegrityError, "historical operator skill policy is unavailable or invalid")
	}
	canonical, err := c.CanonicalV1(policy)
	if err != nil || !bytes.Equal(canonical, raw) {
		return policy, c.Fail(c.StoreIntegrityError, "historical operator skill policy is not canonical")
	}
	return policy, nil
}

func (r *Registry) verifyDefinitionFile(digest string, expected Definition) error {
	actual, err := r.readDefinition(digest)
	if err != nil {
		return err
	}
	if actual.ID != expected.ID || actual.Version != expected.Version {
		return c.Fail(c.StoreIntegrityError, "skill definition content address mismatch")
	}
	return nil
}

func (r *Registry) readEvaluation(digest string) (EvaluationReceipt, error) {
	var receipt EvaluationReceipt
	if !c.ValidDigest(digest) {
		return receipt, c.Fail(c.StoreIntegrityError, "invalid protected evaluation digest")
	}
	raw, err := fileguard.ReadRegular(r.root, filepath.ToSlash(filepath.Join("evaluations", digest+".json")), MaxEvaluationBytes)
	if err != nil || c.HashBytes(raw) != digest || c.DecodeStrict(raw, &receipt) != nil {
		return receipt, c.Fail(c.StoreIntegrityError, "protected skill evaluation receipt is unavailable")
	}
	canonical, err := c.CanonicalV1(receipt)
	if err != nil || !bytes.Equal(canonical, raw) {
		return receipt, c.Fail(c.StoreIntegrityError, "protected evaluation receipt is not canonical")
	}
	return receipt, nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (r *Registry) String() string { return r.path }

func (r *Registry) ExportEvaluation(ctx context.Context, digest string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return nil, err
	}
	if !c.ValidDigest(digest) {
		return nil, c.Fail(c.InvalidArgument, "evaluation digest required")
	}
	raw, err := fileguard.ReadRegular(r.root, filepath.ToSlash(filepath.Join("evaluations", digest+".json")), MaxEvaluationBytes)
	if err != nil || c.HashBytes(raw) != digest {
		return nil, c.Fail(c.StoreIntegrityError, "pinned evaluation receipt is unavailable")
	}
	return raw, nil
}

func (r *Registry) EventHead(ctx context.Context) (uint64, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return 0, "", err
	}
	state, err := r.replayLocked()
	return state.Sequence, state.Digest, err
}

func publishImmutable(root *os.Root, name string, raw []byte) error {
	err := fileguard.Publish(root, name, raw)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrExist) && !errors.Is(err, os.ErrExist) {
		return err
	}
	stored, readErr := fileguard.ReadRegular(root, name, int64(len(raw)))
	if readErr != nil || !bytes.Equal(stored, raw) {
		return c.Fail(c.StoreIntegrityError, "content-addressed skill artifact conflicts with existing bytes")
	}
	return nil
}

func publishBounded(root *os.Root, name string, raw []byte) error {
	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() != int64(len(raw)) {
			return c.Fail(c.StoreIntegrityError, "content-addressed skill artifact has conflicting metadata")
		}
		stored, readErr := fileguard.ReadRegular(root, name, int64(len(raw)))
		if readErr != nil || !bytes.Equal(stored, raw) {
			return c.Fail(c.StoreIntegrityError, "content-addressed skill artifact conflicts with existing bytes")
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = ensureRegistryCapacity(root, int64(len(raw))); err != nil {
		return err
	}
	return publishImmutable(root, name, raw)
}

func ensureRegistryCapacity(root *os.Root, additional int64) error {
	if additional < 0 {
		return c.Fail(c.InvalidArgument, "invalid skills registry capacity request")
	}
	type pendingDir struct{ name string }
	stack := []pendingDir{{name: "."}}
	var total int64
	entriesSeen := 0
	for len(stack) != 0 {
		current := stack[len(stack)-1].name
		stack = stack[:len(stack)-1]
		entries, err := readDir(root, current)
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return c.Fail(c.StoreIntegrityError, "skills registry contents could not be inspected")
		}
		for _, entry := range entries {
			entriesSeen++
			if entriesSeen > MaxRegistryEntries {
				return c.Fail(c.UnsupportedCapability, "skills registry entry quota exceeded")
			}
			name := filepath.ToSlash(filepath.Join(current, entry.Name()))
			info, statErr := root.Lstat(name)
			if statErr != nil {
				return c.Fail(c.StoreIntegrityError, "skills registry entry changed during quota inspection")
			}
			if info.IsDir() {
				stack = append(stack, pendingDir{name: name})
				continue
			}
			if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaxRegistryBytes {
				return c.Fail(c.StoreIntegrityError, "skills registry contains a nonregular or oversized entry")
			}
			total += info.Size()
			if total > MaxRegistryBytes || total+additional > MaxRegistryBytes {
				return c.Fail(c.UnsupportedCapability, "skills registry byte quota exceeded")
			}
		}
	}
	if total+additional > MaxRegistryBytes {
		return c.Fail(c.UnsupportedCapability, "skills registry byte quota exceeded")
	}
	return nil
}

func readDir(root *os.Root, name string) ([]os.DirEntry, error) {
	directory, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, c.Fail(c.StoreIntegrityError, "skills registry directory path is not a directory")
	}
	return directory.ReadDir(-1)
}
