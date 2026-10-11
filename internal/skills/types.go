// Package skills stores explicitly scoped, versioned procedural advice.
// Skills never grant tools or policy effects; consumers must intersect their
// hints with the current task's authority before use.
package skills

import (
	"crypto/ed25519"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
)

const (
	SchemaVersion      = 1
	MaxDefinitionBytes = 64 << 10
	MaxSteps           = 32
	MaxStepPaths       = 32
	MaxSkillEvents     = 10000
	MaxSkillEventBytes = 4096
	MaxEvaluationBytes = 16 << 10
)

var skillIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,95}$`)
var licensePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+-]{0,63}$`)

var allowedLicenses = map[string]bool{
	"Apache-2.0": true, "BSD-2-Clause": true, "BSD-3-Clause": true, "CC0-1.0": true,
	"GPL-2.0-only": true, "GPL-3.0-only": true, "ISC": true, "MIT": true,
	"MPL-2.0": true, "Proprietary": true, "Unlicense": true,
}

type Scope struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

type Source struct {
	TaskID        string `json:"task_id"`
	TaskSeq       int64  `json:"task_seq"`
	Document      string `json:"document_digest"`
	Snapshot      string `json:"snapshot_digest"`
	Candidate     string `json:"candidate_digest"`
	ProjectDigest string `json:"project_digest,omitempty"`
}

type Step struct {
	Action      string   `json:"action"`
	Instruction string   `json:"instruction"`
	Paths       []string `json:"paths,omitempty"`
}

type Definition struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	Version       uint64   `json:"version"`
	Scope         Scope    `json:"scope"`
	Trigger       string   `json:"trigger"`
	Steps         []Step   `json:"steps"`
	Verification  []string `json:"verification"`
	Capabilities  []string `json:"capability_hints"`
	License       string   `json:"license"`
	ExpiresAtUTC  string   `json:"expires_at_utc,omitempty"`
	Source        Source   `json:"source"`
}

type EvaluationReceipt struct {
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
	Signature        []byte         `json:"signature"`
}

// EvaluationSigningBytes returns the canonical message a trusted external
// evaluator must sign. The receipt itself is never considered trusted until
// Registry.ImportEvaluation verifies this signature against operator pins.
func EvaluationSigningBytes(receipt EvaluationReceipt) ([]byte, error) {
	return c.CanonicalV1(receiptMessage{SchemaVersion: receipt.SchemaVersion, EvaluatorKeyID: receipt.EvaluatorKeyID, EvaluatorDigest: receipt.EvaluatorDigest, ProtocolDigest: receipt.ProtocolDigest, DatasetDigest: receipt.DatasetDigest, HoldoutDigest: receipt.HoldoutDigest, DefinitionDigest: receipt.DefinitionDigest, SkillID: receipt.SkillID, SkillVersion: receipt.SkillVersion, Source: receipt.Source, Scope: receipt.Scope, Cases: receipt.Cases, CreatedAtUTC: receipt.CreatedAtUTC})
}

// ProjectScope derives a stable project scope key from an absolute workspace
// root. It deliberately does not use the project's contents or task ID.
func ProjectScope(projectRoot string) (Scope, error) {
	if projectRoot == "" || !filepath.IsAbs(projectRoot) {
		return Scope{}, c.Fail(c.InvalidArgument, "absolute project root required for project skill scope")
	}
	canonical, err := fileguard.ResolveProspective(projectRoot)
	if err != nil {
		return Scope{}, c.Fail(c.InvalidArgument, "project root could not be resolved")
	}
	canonical = filepathToSlash(canonical)
	canonical = strings.TrimRight(canonical, "/")
	if canonical == "" {
		canonical = "/"
	}
	if strings.ContainsAny(canonical, "\x00\r\n") {
		return Scope{}, c.Fail(c.InvalidArgument, "invalid project root")
	}
	// Windows paths are case-insensitive, while other supported platforms
	// preserve case. This key is only a scope boundary, not an access grant.
	if strings.Contains(canonical, ":/") {
		canonical = strings.ToLower(canonical)
	}
	return Scope{Kind: "PROJECT", Key: c.HashBytes([]byte(canonical))}, nil
}

func filepathToSlash(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}

type CaseEvidence struct {
	CaseID         string `json:"case_id"`
	InputDigest    string `json:"input_digest"`
	ExpectedDigest string `json:"expected_digest"`
	ObservedDigest string `json:"observed_digest"`
	ResultDigest   string `json:"result_digest"`
	Verdict        string `json:"verdict"`
}

type PinnedEvaluator struct {
	ID        string `json:"id"`
	PublicKey []byte `json:"public_key"`
}

type EvaluationProtocol struct {
	Digest          string   `json:"protocol_digest"`
	DatasetDigest   string   `json:"dataset_digest"`
	HoldoutDigest   string   `json:"holdout_digest"`
	EvaluatorDigest string   `json:"evaluator_digest"`
	MinCases        int      `json:"min_cases"`
	MaxCases        int      `json:"max_cases"`
	AllowedScopes   []string `json:"allowed_scopes"`
}

// TrustPolicy is supplied from an operator-pinned local policy file on every
// registry open. Its digest is bound into activation and checked at load time.
type TrustPolicy struct {
	SchemaVersion int                  `json:"schema_version"`
	Revision      int64                `json:"revision"`
	Evaluators    []PinnedEvaluator    `json:"evaluators"`
	Protocols     []EvaluationProtocol `json:"protocols"`
	Capabilities  []string             `json:"allowed_capability_hints"`
	Scopes        []string             `json:"allowed_scope_kinds"`
}

type Status struct {
	ID                string `json:"id"`
	Version           uint64 `json:"version"`
	DefinitionDigest  string `json:"definition_digest"`
	EvaluationDigest  string `json:"evaluation_digest,omitempty"`
	Lifecycle         string `json:"lifecycle"`
	LastEventSequence uint64 `json:"last_event_sequence"`
	LastEventDigest   string `json:"last_event_digest"`
}

type Active struct {
	Definition          Definition `json:"definition"`
	DefinitionDigest    string     `json:"definition_digest"`
	EvaluationDigest    string     `json:"evaluation_digest"`
	TrustPolicyDigest   string     `json:"trust_policy_digest"`
	AuthorityDigest     string     `json:"authority_digest"`
	CurrentCapabilities []string   `json:"current_capability_intersection"`
}

func (s Scope) Validate() error {
	if s.Key == "" || len(s.Key) > 128 || strings.ContainsAny(s.Key, "\x00\r\n") {
		return c.Fail(c.InvalidArgument, "bounded skill scope required")
	}
	switch s.Kind {
	case "TASK":
		if !skillIDPattern.MatchString(s.Key) {
			return c.Fail(c.InvalidArgument, "invalid task skill scope key")
		}
	case "PROJECT":
		if !c.ValidDigest(s.Key) {
			return c.Fail(c.InvalidArgument, "project skill scope must be a project digest")
		}
	case "USER":
		if s.Key != "local-user" {
			return c.Fail(c.InvalidArgument, "unsupported user skill scope")
		}
	case "GENERIC":
		if s.Key != "local-generic" {
			return c.Fail(c.InvalidArgument, "unsupported generic skill scope")
		}
	default:
		return c.Fail(c.InvalidArgument, "unknown skill scope")
	}
	return nil
}

func (s Source) Validate() error {
	if s.TaskID == "" || len(s.TaskID) > 128 || strings.ContainsAny(s.TaskID, "\x00\r\n") || s.TaskSeq < 1 || s.TaskSeq > 1<<53 ||
		!c.ValidDigest(s.Document) || !c.ValidDigest(s.Snapshot) || !c.ValidDigest(s.Candidate) {
		return c.Fail(c.InvalidArgument, "skill source needs retained task, document, snapshot and candidate bindings")
	}
	if !c.ValidDigest(s.ProjectDigest) {
		return c.Fail(c.InvalidArgument, "skill source must bind its originating project")
	}
	return nil
}

func (d Definition) Validate() error {
	if d.SchemaVersion != SchemaVersion || !skillIDPattern.MatchString(d.ID) || d.Version < 1 || d.Version > 1<<53 || d.Scope.Validate() != nil || d.Source.Validate() != nil ||
		!validText(d.Trigger, 1, 2048) || len(d.Steps) < 1 || len(d.Steps) > MaxSteps || len(d.Verification) < 1 || len(d.Verification) > 16 ||
		!licensePattern.MatchString(d.License) || !allowedLicenses[d.License] || len(d.Capabilities) > 8 {
		return c.Fail(c.InvalidArgument, "invalid bounded versioned skill definition")
	}
	if d.ExpiresAtUTC != "" {
		parsed, err := time.Parse(time.RFC3339, d.ExpiresAtUTC)
		if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339) != d.ExpiresAtUTC {
			return c.Fail(c.InvalidArgument, "skill expiry must be canonical UTC RFC3339")
		}
	}
	if d.Scope.Kind == "TASK" && d.Scope.Key != d.Source.TaskID || d.Scope.Kind == "PROJECT" && d.Source.ProjectDigest != d.Scope.Key {
		return c.Fail(c.StaleAuthority, "skill scope does not match its retained source")
	}
	for _, step := range d.Steps {
		if !validText(step.Instruction, 1, 2048) || len(step.Paths) > MaxStepPaths {
			return c.Fail(c.InvalidArgument, "invalid bounded skill procedure step")
		}
		switch step.Action {
		case "inspect", "plan", "change", "verify", "ask":
		default:
			return c.Fail(c.InvalidArgument, "skill steps cannot invoke arbitrary commands or tools")
		}
		seenPaths := map[string]bool{}
		for _, name := range step.Paths {
			clean := path.Clean(name)
			lower := strings.ToLower(clean)
			if !policy.SafePath(name) || clean != name || strings.HasPrefix(clean, "../") || clean == ".." || lower == ".git" || strings.HasPrefix(lower, ".git/") || seenPaths[lower] {
				return c.Fail(c.InvalidArgument, "skill path must be a unique safe project-relative path")
			}
			seenPaths[lower] = true
		}
	}
	for _, check := range d.Verification {
		if !validText(check, 1, 1024) {
			return c.Fail(c.InvalidArgument, "invalid skill verification instruction")
		}
	}
	seenCapabilities := map[string]bool{}
	for _, capability := range d.Capabilities {
		switch capability {
		case "fs.read", "fs.list", "plan.update", "candidate.write", "check.run":
		default:
			return c.Fail(c.PolicyDenied, "skill capability hint exceeds the non-executable allowlist")
		}
		if seenCapabilities[capability] {
			return c.Fail(c.InvalidArgument, "duplicate skill capability hint")
		}
		seenCapabilities[capability] = true
	}
	raw, err := c.CanonicalV1(d)
	if err != nil || len(raw) > MaxDefinitionBytes {
		return c.Fail(c.UnsupportedCapability, "skill definition exceeds its size bound")
	}
	return nil
}

func (p TrustPolicy) Validate() error {
	if p.SchemaVersion != SchemaVersion || p.Revision < 1 || p.Revision > 1<<53 || len(p.Evaluators) == 0 || len(p.Evaluators) > 16 || len(p.Protocols) == 0 || len(p.Protocols) > 64 || len(p.Capabilities) > 8 || len(p.Scopes) == 0 || len(p.Scopes) > 4 {
		return c.Fail(c.InvalidArgument, "invalid bounded operator skill-evaluation policy")
	}
	evaluators := map[string]bool{}
	for _, evaluator := range p.Evaluators {
		if len(evaluator.PublicKey) != ed25519.PublicKeySize || evaluator.ID != c.HashBytes(evaluator.PublicKey) || evaluators[evaluator.ID] {
			return c.Fail(c.InvalidArgument, "evaluator key must be pinned by its public-key digest")
		}
		evaluators[evaluator.ID] = true
	}
	protocols := map[string]bool{}
	for _, protocol := range p.Protocols {
		if !c.ValidDigest(protocol.Digest) || !c.ValidDigest(protocol.DatasetDigest) || !c.ValidDigest(protocol.HoldoutDigest) || !c.ValidDigest(protocol.EvaluatorDigest) || protocol.MinCases < 1 || protocol.MaxCases < protocol.MinCases || protocol.MaxCases > 256 || len(protocol.AllowedScopes) == 0 || len(protocol.AllowedScopes) > 4 || protocols[protocol.Digest] {
			return c.Fail(c.InvalidArgument, "invalid pinned skill evaluation protocol")
		}
		protocols[protocol.Digest] = true
		seen := map[string]bool{}
		for _, scope := range protocol.AllowedScopes {
			if scope != "TASK" && scope != "PROJECT" && scope != "USER" && scope != "GENERIC" || seen[scope] {
				return c.Fail(c.InvalidArgument, "invalid evaluation protocol scope")
			}
			seen[scope] = true
		}
	}
	seenCapabilities := map[string]bool{}
	for _, capability := range p.Capabilities {
		if capabilityEffect(capability) == "" {
			return c.Fail(c.PolicyDenied, "operator policy contains unsupported skill capability")
		}
		if seenCapabilities[capability] {
			return c.Fail(c.InvalidArgument, "duplicate operator skill capability")
		}
		seenCapabilities[capability] = true
	}
	seenScopes := map[string]bool{}
	for _, scope := range p.Scopes {
		if scope != "TASK" && scope != "PROJECT" && scope != "USER" && scope != "GENERIC" || seenScopes[scope] {
			return c.Fail(c.InvalidArgument, "invalid operator skill scope")
		}
		seenScopes[scope] = true
	}
	_, err := c.CanonicalV1(p)
	return err
}

func validText(value string, min, max int) bool {
	if len(value) < min || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == 0 || r == '\r' || r < 0x20 && r != '\n' && r != '\t' {
			return false
		}
	}
	return strings.TrimSpace(value) != ""
}

func capabilityEffect(capability string) string {
	switch capability {
	case "fs.read", "fs.list":
		return "context.read"
	case "plan.update":
		return "plan.update"
	case "candidate.write":
		return "candidate.write"
	case "check.run":
		return "check.run"
	default:
		return ""
	}
}
