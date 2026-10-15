package agent

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/skills"
)

// SkillBinding is the small, canonical task-document reference to an external
// skill registry entry. Procedure text and evaluation contents stay in that
// separate registry and are reloaded per call.
type SkillBinding struct {
	SchemaVersion     int           `json:"schema_version"`
	ID                string        `json:"id"`
	Version           uint64        `json:"version"`
	DefinitionDigest  string        `json:"definition_digest"`
	EvaluationDigest  string        `json:"evaluation_digest"`
	TrustPolicyDigest string        `json:"trust_policy_digest"`
	AuthorityDigest   string        `json:"authority_digest"`
	RegistryDigest    string        `json:"registry_digest"`
	Scope             skills.Scope  `json:"scope"`
	Source            skills.Source `json:"source"`
	MarkdownManifest  string        `json:"markdown_manifest_digest,omitempty"`
}

func (binding SkillBinding) Validate() error {
	if binding.SchemaVersion != 1 || binding.ID == "" || len(binding.ID) > 96 || binding.Version < 1 || binding.Version > 1<<53 ||
		!c.ValidDigest(binding.DefinitionDigest) || !c.ValidDigest(binding.EvaluationDigest) || !c.ValidDigest(binding.TrustPolicyDigest) ||
		!c.ValidDigest(binding.AuthorityDigest) || !c.ValidDigest(binding.RegistryDigest) || binding.Scope.Validate() != nil || binding.Source.Validate() != nil || binding.MarkdownManifest != "" && !c.ValidDigest(binding.MarkdownManifest) {
		return c.Fail(c.StoreIntegrityError, "invalid advisory skill binding")
	}
	return nil
}

func (binding SkillBinding) Digest() (string, error) {
	if err := binding.Validate(); err != nil {
		return "", err
	}
	return c.Digest(binding)
}

// SkillSource binds a proposed skill to one exact, retained task revision. The
// returned identity carries no approval or authority by itself.
func (s *Session) SkillSource(ctx context.Context, task string) (skills.Source, error) {
	var source skills.Source
	state, document, err := s.Load(ctx, task)
	if err != nil {
		return source, err
	}
	baseline, err := s.Archive.Get(document.Baseline)
	if err != nil {
		return source, err
	}
	projectScope, err := skills.ProjectScope(baseline.Snapshot.Root)
	if err != nil {
		return source, err
	}
	source = skills.Source{
		TaskID:        task,
		TaskSeq:       state.TaskSeq,
		Document:      state.DocumentDigest,
		Snapshot:      document.Baseline.SnapshotDigest,
		Candidate:     document.Candidate.SnapshotDigest,
		ProjectDigest: projectScope.Key,
	}
	return source, source.Validate()
}

// LoadActiveSkills returns explicitly activated advisory procedures for the
// current task. Callers must opt in by invoking this API; returned steps are
// data only and never dispatch tools or widen task capabilities.
func (s *Session) LoadActiveSkills(ctx context.Context, registryPath string, trust skills.TrustPolicy, task string) ([]skills.Active, error) {
	if !filepath.IsAbs(registryPath) || !fileguard.Disjoint(registryPath, s.directory) {
		return nil, c.Fail(c.PolicyDenied, "skills registry must be absolute and outside the task store")
	}
	state, document, err := s.Load(ctx, task)
	if err != nil {
		return nil, err
	}
	if document.Config == nil {
		return nil, c.Fail(c.StaleAuthority, "task has no retained operator authority to bind advisory skills")
	}
	if err = document.Config.CurrentAuthority(); err != nil {
		return nil, err
	}
	baseline, err := s.Archive.Get(document.Baseline)
	if err != nil {
		return nil, err
	}
	projectScope, err := skills.ProjectScope(baseline.Snapshot.Root)
	if err != nil {
		return nil, err
	}
	if !fileguard.Disjoint(registryPath, baseline.Snapshot.Root) {
		return nil, c.Fail(c.PolicyDenied, "skills registry must be outside project source")
	}
	authorityDigest, err := c.Digest(document.Config)
	if err != nil {
		return nil, err
	}
	scopes := []skills.Scope{{Kind: "TASK", Key: task}, projectScope}
	crossProjectAllowed, err := crossProjectSkillsAllowed(*document.Config)
	if err != nil {
		return nil, err
	}
	if crossProjectAllowed {
		scopes = append(scopes, skills.Scope{Kind: "USER", Key: "local-user"}, skills.Scope{Kind: "GENERIC", Key: "local-generic"})
	}
	capabilities := s.currentSkillCapabilities(state, document)
	registry, err := skills.Open(registryPath, trust)
	if err != nil {
		return nil, err
	}
	defer registry.Close()
	active, err := registry.LoadActive(ctx, scopes, authorityDigest, capabilities)
	if err != nil {
		return nil, err
	}
	for _, item := range active {
		if err = s.validateSkillEffects(state, document, item); err != nil {
			return nil, err
		}
		retained, retainedErr := s.validateSkillSource(ctx, item.Definition.Source)
		if retainedErr != nil {
			return nil, retainedErr
		}
		if retained != item.Definition.Source {
			return nil, c.Fail(c.StaleAuthority, "skill source no longer matches its retained task revision")
		}
		switch item.Definition.Scope.Kind {
		case "TASK":
			if item.Definition.Source.TaskID != task || item.Definition.Source.Snapshot != document.Baseline.SnapshotDigest || item.Definition.Source.Candidate != document.Candidate.SnapshotDigest {
				return nil, c.Fail(c.StaleAuthority, "task skill source changed; reopen and evaluate against the current candidate")
			}
		case "PROJECT":
			if item.Definition.Source.ProjectDigest != projectScope.Key || item.Definition.Source.Snapshot != document.Baseline.SnapshotDigest {
				return nil, c.Fail(c.StaleAuthority, "project skill source changed; reopen and evaluate against the current baseline")
			}
		case "USER", "GENERIC":
			if !crossProjectAllowed {
				return nil, c.Fail(c.PolicyDenied, "cross-project advisory skills require explicit current privacy permission")
			}
		}
	}
	if err = document.Config.CurrentAuthority(); err != nil {
		return nil, err
	}
	return active, nil
}

// LoadActiveSkillsFromPolicyFile is the user-facing opt-in path. The policy is
// loaded from a separate operator file and must be outside both the project
// and task store before it can serve as a trust pin.
func (s *Session) LoadActiveSkillsFromPolicyFile(ctx context.Context, registryPath, policyPath, task string) ([]skills.Active, error) {
	if !filepath.IsAbs(policyPath) || !fileguard.Disjoint(policyPath, s.directory) || !fileguard.Disjoint(policyPath, registryPath) {
		return nil, c.Fail(c.PolicyDenied, "operator skill policy must be absolute and separate from project data and registry")
	}
	_, document, err := s.Load(ctx, task)
	if err != nil {
		return nil, err
	}
	baseline, err := s.Archive.Get(document.Baseline)
	if err != nil {
		return nil, err
	}
	if !fileguard.Disjoint(policyPath, baseline.Snapshot.Root) {
		return nil, c.Fail(c.PolicyDenied, "operator skill policy must stay outside project source")
	}
	trust, err := skills.ReadTrustPolicy(policyPath)
	if err != nil {
		return nil, err
	}
	return s.LoadActiveSkills(ctx, registryPath, trust, task)
}

// ActivateSkillFromPolicyFile admits activation only after checking the exact
// candidate source against the task's current retained baseline/candidate. A
// USER or GENERIC scope additionally requires current cross-project policy and
// the caller's separate explicit cross-scope review acknowledgement.
func (s *Session) ActivateSkillFromPolicyFile(ctx context.Context, registryPath, policyPath, task, id string, version uint64, scope skills.Scope, crossScopeReviewed, rollback bool) (skills.Status, error) {
	var empty skills.Status
	if !filepath.IsAbs(registryPath) || !filepath.IsAbs(policyPath) || !fileguard.Disjoint(registryPath, s.directory) || !fileguard.Disjoint(policyPath, s.directory) || !fileguard.Disjoint(policyPath, registryPath) {
		return empty, c.Fail(c.PolicyDenied, "skill registry and operator policy must be separate absolute paths outside the task store")
	}
	_, document, err := s.Load(ctx, task)
	if err != nil {
		return empty, err
	}
	if document.Config == nil {
		return empty, c.Fail(c.StaleAuthority, "task has no retained operator authority to activate advisory skills")
	}
	if err = document.Config.CurrentAuthority(); err != nil {
		return empty, err
	}
	baseline, err := s.Archive.Get(document.Baseline)
	if err != nil {
		return empty, err
	}
	projectScope, err := skills.ProjectScope(baseline.Snapshot.Root)
	if err != nil {
		return empty, err
	}
	if !fileguard.Disjoint(registryPath, baseline.Snapshot.Root) || !fileguard.Disjoint(policyPath, baseline.Snapshot.Root) {
		return empty, c.Fail(c.PolicyDenied, "skill registry and operator policy must stay outside project source")
	}
	if scope.Validate() != nil {
		return empty, c.Fail(c.InvalidArgument, "current skill activation scope required")
	}
	needsCrossScopeReview := scope.Kind == "USER" || scope.Kind == "GENERIC"
	if crossScopeReviewed != needsCrossScopeReview {
		return empty, c.Fail(c.PolicyDenied, "cross-scope activation needs a separate explicit review acknowledgement")
	}
	if scope.Kind == "TASK" && scope.Key != task || scope.Kind == "PROJECT" && scope.Key != projectScope.Key {
		return empty, c.Fail(c.StaleAuthority, "skill activation scope does not match the current task/project")
	}
	if needsCrossScopeReview {
		allowed, allowErr := crossProjectSkillsAllowed(*document.Config)
		if allowErr != nil {
			return empty, allowErr
		}
		if !allowed {
			return empty, c.Fail(c.PolicyDenied, "cross-scope activation requires explicit current cross-project privacy permission")
		}
	}
	trust, err := skills.ReadTrustPolicy(policyPath)
	if err != nil {
		return empty, err
	}
	registry, err := skills.Open(registryPath, trust)
	if err != nil {
		return empty, err
	}
	defer registry.Close()
	definition, _, err := registry.InspectVersion(ctx, id, version)
	if err != nil {
		return empty, err
	}
	if definition.Scope != scope || definition.Source.ProjectDigest != projectScope.Key && !needsCrossScopeReview {
		return empty, c.Fail(c.StaleAuthority, "skill definition does not match the reviewed current scope")
	}
	if scope.Kind == "TASK" && (definition.Source.TaskID != task || definition.Source.Snapshot != document.Baseline.SnapshotDigest || definition.Source.Candidate != document.Candidate.SnapshotDigest) {
		return empty, c.Fail(c.StaleAuthority, "task skill source changed; reopen and evaluate against the current candidate")
	}
	if scope.Kind == "PROJECT" && definition.Source.Snapshot != document.Baseline.SnapshotDigest {
		return empty, c.Fail(c.StaleAuthority, "project skill source changed; reopen and evaluate against the current baseline")
	}
	authorityDigest, err := c.Digest(document.Config)
	if err != nil {
		return empty, err
	}
	if rollback {
		empty, err = registry.RollbackReviewed(ctx, id, version, scope, authorityDigest, crossScopeReviewed)
	} else {
		empty, err = registry.ActivateReviewed(ctx, id, version, scope, authorityDigest, crossScopeReviewed)
	}
	if err != nil {
		return empty, err
	}
	if err = document.Config.CurrentAuthority(); err != nil {
		return empty, err
	}
	return empty, nil
}

// BindActiveSkillsFromPolicyFile returns canonical references suitable for a
// task document's Skills field. The task document stores digests and source
// identity only; the procedure itself remains in the external registry.
func (s *Session) BindActiveSkillsFromPolicyFile(ctx context.Context, registryPath, policyPath, task string) ([]SkillBinding, error) {
	active, err := s.LoadActiveSkillsFromPolicyFile(ctx, registryPath, policyPath, task)
	if err != nil {
		return nil, err
	}
	_, document, err := s.Load(ctx, task)
	if err != nil {
		return nil, err
	}
	if document.Config == nil {
		return nil, c.Fail(c.StaleAuthority, "task has no retained operator authority to bind advisory skills")
	}
	if err = document.Config.CurrentAuthority(); err != nil {
		return nil, err
	}
	authorityDigest, err := c.Digest(document.Config)
	if err != nil {
		return nil, err
	}
	registryDigest, err := skillRegistryDigest(registryPath)
	if err != nil {
		return nil, err
	}
	bindings := make([]SkillBinding, 0, len(active))
	for _, item := range active {
		binding := SkillBinding{SchemaVersion: 1, ID: item.Definition.ID, Version: item.Definition.Version, DefinitionDigest: item.DefinitionDigest, EvaluationDigest: item.EvaluationDigest, TrustPolicyDigest: item.TrustPolicyDigest, AuthorityDigest: authorityDigest, RegistryDigest: registryDigest, Scope: item.Definition.Scope, Source: item.Definition.Source}
		if item.Definition.MarkdownSource != nil {
			binding.MarkdownManifest = item.Definition.MarkdownSource.ManifestDigest
		}
		if err = binding.Validate(); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, document.Config.CurrentAuthority()
}

// LoadBoundSkillAdvice rehydrates one exact task-document binding for a native
// per-call advice tool. It fails closed when policy, authority, source, or the
// active registry version has changed.
func (s *Session) LoadBoundSkillAdvice(ctx context.Context, registryPath, policyPath, task string, binding SkillBinding) (skills.Active, error) {
	var empty skills.Active
	if err := binding.Validate(); err != nil {
		return empty, err
	}
	registryDigest, err := skillRegistryDigest(registryPath)
	if err != nil || registryDigest != binding.RegistryDigest {
		return empty, c.Fail(c.StaleAuthority, "advisory skill registry identity changed")
	}
	active, err := s.LoadActiveSkillsFromPolicyFile(ctx, registryPath, policyPath, task)
	if err != nil {
		return empty, err
	}
	_, document, err := s.Load(ctx, task)
	if err != nil {
		return empty, err
	}
	if document.Config == nil {
		return empty, c.Fail(c.StaleAuthority, "task operator authority is unavailable")
	}
	authorityDigest, err := c.Digest(document.Config)
	if err != nil || authorityDigest != binding.AuthorityDigest {
		return empty, c.Fail(c.StaleAuthority, "advisory skill authority binding changed")
	}
	for _, item := range active {
		if item.Definition.ID == binding.ID && item.Definition.Version == binding.Version && item.DefinitionDigest == binding.DefinitionDigest && item.EvaluationDigest == binding.EvaluationDigest && item.TrustPolicyDigest == binding.TrustPolicyDigest && item.AuthorityDigest == binding.AuthorityDigest && item.Definition.Scope == binding.Scope && item.Definition.Source == binding.Source {
			if item.Definition.MarkdownSource == nil && binding.MarkdownManifest != "" || item.Definition.MarkdownSource != nil && item.Definition.MarkdownSource.ManifestDigest != binding.MarkdownManifest {
				return empty, c.Fail(c.StaleAuthority, "imported skill source manifest binding changed")
			}
			if err = document.Config.CurrentAuthority(); err != nil {
				return empty, err
			}
			return item, nil
		}
	}
	return empty, c.Fail(c.StaleAuthority, "advisory skill activation or source no longer matches the task binding")
}

func skillRegistryDigest(name string) (string, error) {
	if !filepath.IsAbs(name) {
		return "", c.Fail(c.InvalidArgument, "absolute skills registry path required")
	}
	canonical, err := fileguard.ResolveProspective(name)
	if err != nil {
		return "", c.Fail(c.InvalidArgument, "skills registry path could not be resolved")
	}
	canonical = filepath.ToSlash(filepath.Clean(canonical))
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	return c.HashBytes([]byte(canonical)), nil
}

func crossProjectSkillsAllowed(resolved config.Resolved) (bool, error) {
	if resolved.Authority == nil {
		return false, nil
	}
	authority, err := resolved.Authority.Current()
	if err != nil {
		return false, err
	}
	if authority.Restrictions.CrossProjectMemory == nil || !*authority.Restrictions.CrossProjectMemory {
		return false, nil
	}
	for _, layer := range resolved.Layers {
		allowed := layer.File.Restrictions.CrossProjectMemory
		if allowed == nil || !*allowed {
			return false, nil
		}
	}
	return true, nil
}

func (s *Session) currentSkillCapabilities(state c.TaskState, document Document) []string {
	bindings := []struct {
		capability string
		effect     string
	}{
		{capability: "fs.read", effect: "snapshot.read"},
		{capability: "fs.list", effect: "snapshot.read"},
		{capability: "plan.update", effect: "plan.update"},
		{capability: "candidate.write", effect: "candidate.write"},
		{capability: "check.run", effect: "check.run"},
	}
	result := make([]string, 0, len(bindings))
	layers := s.layers(state, document)
	for _, binding := range bindings {
		if policy.Admit(layers, policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: binding.effect}) == nil {
			result = append(result, binding.capability)
		}
	}
	return result
}

func (s *Session) validateSkillEffects(state c.TaskState, document Document, active skills.Active) error {
	layers := s.layers(state, document)
	for _, step := range active.Definition.Steps {
		effect := ""
		switch step.Action {
		case "inspect":
			effect = "snapshot.read"
		case "plan":
			effect = "plan.update"
		case "change":
			effect = "candidate.write"
		case "verify":
			if len(step.Paths) > 0 {
				effect = "snapshot.read"
			}
		case "ask":
			// Asking a question does not imply a tool capability.
		}
		if effect != "" && policy.Admit(layers, policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: effect}) != nil {
			return c.Fail(c.PolicyDenied, "active skill procedure exceeds the current task effect policy")
		}
		pathEffect := effect
		if pathEffect == "" {
			pathEffect = "snapshot.read"
		}
		for _, name := range step.Paths {
			if err := policy.Admit(layers, policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: pathEffect, Path: name}); err != nil {
				return c.Fail(c.PolicyDenied, "active skill path exceeds the current task scope")
			}
		}
	}
	return nil
}

func (s *Session) validateSkillSource(ctx context.Context, source skills.Source) (skills.Source, error) {
	var actual skills.Source
	state, document, err := s.Inspect(ctx, source.TaskID, source.TaskSeq)
	if err != nil {
		return actual, err
	}
	if state.TaskSeq != source.TaskSeq || state.DocumentDigest != source.Document || document.Baseline.SnapshotDigest != source.Snapshot || document.Candidate.SnapshotDigest != source.Candidate {
		return actual, c.Fail(c.StaleAuthority, "skill source does not match its retained task revision")
	}
	baseline, err := s.Archive.Get(document.Baseline)
	if err != nil {
		return actual, err
	}
	projectScope, err := skills.ProjectScope(baseline.Snapshot.Root)
	if err != nil {
		return actual, err
	}
	actual = skills.Source{TaskID: source.TaskID, TaskSeq: state.TaskSeq, Document: state.DocumentDigest, Snapshot: document.Baseline.SnapshotDigest, Candidate: document.Candidate.SnapshotDigest, ProjectDigest: projectScope.Key}
	if actual != source {
		return actual, c.Fail(c.StaleAuthority, "skill provenance does not match the retained source document and snapshots")
	}
	return actual, nil
}
