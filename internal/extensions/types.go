// Package extensions contains the narrow, content-pinned MCP stdio boundary.
// Extension code is only run through the trusted sandbox broker; this package
// never executes manifest commands on the host or opens the task database.
package extensions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/runner"
)

const (
	SchemaVersion          = 1
	MCPProtocolVersion     = "2025-11-25"
	MaxManifestBytes       = 64 << 10
	MaxManifestTools       = 64
	MaxSchemaBytes         = 16 << 10
	MaxRPCMessageBytes     = 64 << 10
	MaxRPCMessages         = 128
	MaxToolInputBytes      = 32 << 10
	MaxToolOutputBytes     = 128 << 10
	MaxRPCListPages        = 16
	MaxServerNotifications = 32
)

var (
	extensionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionPattern     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]{1,40})?$`)
	toolNamePattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

type Effect string

const (
	EffectReadOnly       Effect = "READ_ONLY"
	EffectSandboxCompute Effect = "SANDBOX_COMPUTE"
	EffectExternalRead   Effect = "EXTERNAL_READ"
	EffectExternalWrite  Effect = "EXTERNAL_WRITE"
	EffectUnknown        Effect = "UNKNOWN"
)

type Idempotency string

const (
	IdempotencyReadOnly      Idempotency = "READ_ONLY"
	IdempotencyIdempotent    Idempotency = "IDEMPOTENT"
	IdempotencyNonIdempotent Idempotency = "NON_IDEMPOTENT"
	IdempotencyUnknown       Idempotency = "UNKNOWN"
)

type EgressMode string

const (
	EgressNone            EgressMode = "NONE"
	EgressOriginAllowlist EgressMode = "ORIGIN_ALLOWLIST"
)

type EgressPolicy struct {
	Mode    EgressMode `json:"mode"`
	Origins []string   `json:"origins"`
}

type Tool struct {
	Name           string          `json:"name"`
	Title          string          `json:"title,omitempty"`
	Description    string          `json:"description"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"`
	Effect         Effect          `json:"effect"`
	Egress         EgressPolicy    `json:"egress"`
	Idempotency    Idempotency     `json:"idempotency"`
	MaxInputBytes  int             `json:"max_input_bytes"`
	MaxOutputBytes int             `json:"max_output_bytes"`
}

// Manifest is an operator reviewed description of one immutable extension
// server image, command, MCP protocol revision and exact tool schemas. The
// image is always digest pinned and run with the runner's network-none profile.
type Manifest struct {
	SchemaVersion   int            `json:"schema_version"`
	ID              string         `json:"id"`
	Version         string         `json:"version"`
	ProtocolVersion string         `json:"protocol_version"`
	ServerName      string         `json:"server_name"`
	ServerVersion   string         `json:"server_version"`
	Profile         runner.Profile `json:"profile"`
	Command         []string       `json:"command"`
	Egress          EgressPolicy   `json:"egress"`
	Tools           []Tool         `json:"tools"`
}

type PinnedManifest struct {
	Manifest Manifest `json:"manifest"`
	Digest   string   `json:"manifest_digest"`
}

// Admission carries the current operator authorization that the caller has
// already intersected with task and user policy. A manifest hash alone never
// grants authority. UserApproved is required for every call, including local
// read/compute calls, so enabling an extension cannot be inferred from model
// output or from the server's own annotations.
type Admission struct {
	ManifestDigest  string   `json:"manifest_digest"`
	AuthorityDigest string   `json:"authority_digest"`
	OperationDigest string   `json:"operation_digest"`
	GrantedEffects  []Effect `json:"granted_effects"`
	EgressOrigins   []string `json:"egress_origins"`
	UserApproved    bool     `json:"user_approved"`
}

type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Outcome string

const (
	NotDispatched  Outcome = "NOT_DISPATCHED"
	Completed      Outcome = "COMPLETED"
	CompletedError Outcome = "COMPLETED_TOOL_ERROR"
	Unknown        Outcome = "UNKNOWN"
)

// ToolResult is untrusted extension output. Provenance fields explain which
// exact image, schema, request and sandbox result produced it; none of those
// fields promote the result to verification evidence.
type ToolResult struct {
	SchemaVersion        int             `json:"schema_version"`
	ExtensionID          string          `json:"extension_id"`
	ExtensionVersion     string          `json:"extension_version"`
	ManifestDigest       string          `json:"manifest_digest"`
	ProtocolVersion      string          `json:"protocol_version"`
	ServerName           string          `json:"server_name"`
	ServerVersion        string          `json:"server_version"`
	ToolName             string          `json:"tool_name"`
	ToolSchemaDigest     string          `json:"tool_schema_digest"`
	ArgumentsDigest      string          `json:"arguments_digest"`
	OperationDigest      string          `json:"operation_digest"`
	RequestDigest        string          `json:"request_digest"`
	ResponseDigest       string          `json:"response_digest"`
	AuthorityDigest      string          `json:"authority_digest"`
	Effect               Effect          `json:"effect"`
	Egress               EgressPolicy    `json:"egress"`
	Idempotency          Idempotency     `json:"idempotency"`
	Outcome              Outcome         `json:"outcome"`
	ToolError            bool            `json:"tool_error"`
	EffectPossible       bool            `json:"effect_possible"`
	ErrorCode            int64           `json:"error_code,omitempty"`
	ErrorMessage         string          `json:"error_message,omitempty"`
	Content              []TextContent   `json:"content"`
	StructuredContent    json.RawMessage `json:"structured_content,omitempty"`
	SandboxResultDigest  string          `json:"sandbox_result_digest"`
	SandboxOwnership     *runner.Capsule `json:"sandbox_ownership,omitempty"`
	TranscriptDigest     string          `json:"transcript_digest"`
	ProcessTreeQuiescent bool            `json:"process_tree_quiescent"`
}

type TextContent struct {
	Text string `json:"text"`
}

func (policy EgressPolicy) Validate() error {
	if len(policy.Origins) > 16 {
		return c.Fail(c.UnsupportedCapability, "extension egress allowlist exceeds its bound")
	}
	seen := map[string]bool{}
	for _, origin := range policy.Origins {
		if !validOrigin(origin) || seen[origin] {
			return c.Fail(c.InvalidArgument, "invalid or duplicate extension egress origin")
		}
		seen[origin] = true
	}
	switch policy.Mode {
	case EgressNone:
		if len(policy.Origins) != 0 {
			return c.Fail(c.InvalidArgument, "NONE egress cannot carry origins")
		}
	case EgressOriginAllowlist:
		if len(policy.Origins) == 0 {
			return c.Fail(c.InvalidArgument, "origin-allowlist egress requires exact origins")
		}
	default:
		return c.Fail(c.InvalidArgument, "unknown extension egress mode")
	}
	return nil
}

func validOrigin(value string) bool {
	if len(value) < 9 || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	return !strings.ContainsAny(parsed.Host, "*/?#@")
}

func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != SchemaVersion || !extensionIDPattern.MatchString(manifest.ID) || !versionPattern.MatchString(manifest.Version) || manifest.ProtocolVersion != MCPProtocolVersion || !validText(manifest.ServerName, 1, 128) || !validText(manifest.ServerVersion, 1, 128) || len(manifest.Tools) == 0 || len(manifest.Tools) > MaxManifestTools || len(manifest.Command) == 0 || len(manifest.Command) > 32 {
		return c.Fail(c.InvalidArgument, "invalid bounded pinned extension manifest")
	}
	if err := manifest.Profile.Validate(); err != nil {
		return err
	}
	// The existing sandbox broker has no extension egress capability. Keep the
	// type explicit while refusing to claim support for an unimplemented grant.
	if err := manifest.Egress.Validate(); err != nil {
		return err
	}
	if manifest.Egress.Mode != EgressNone {
		return c.Fail(c.UnsupportedCapability, "extension runtime currently supports network-none only")
	}
	if manifest.Profile.TimeoutSeconds > 120 || manifest.Profile.MemoryBytes > 1<<30 || manifest.Profile.Pids > 128 || manifest.Profile.CPUs > 4 || manifest.Profile.ScratchBytes > 128<<20 || manifest.Profile.MaxOutputBytes > 1<<20 {
		return c.Fail(c.UnsupportedCapability, "extension sandbox resource profile exceeds the current bound")
	}
	if len(manifest.Command[0]) < 2 || !strings.HasPrefix(manifest.Command[0], "/") || strings.ContainsAny(manifest.Command[0], "\\:\x00\r\n") {
		return c.Fail(c.PolicyDenied, "extension command must be an absolute container executable, not a host command")
	}
	for index, argument := range manifest.Command {
		if len(argument) == 0 || len(argument) > 4096 || !utf8.ValidString(argument) || strings.ContainsAny(argument, "\x00\r\n") {
			return c.Fail(c.InvalidArgument, "invalid pinned extension argv")
		}
		if index == 0 && (strings.HasSuffix(strings.ToLower(argument), "/sh") || strings.HasSuffix(strings.ToLower(argument), "/bash") || strings.HasSuffix(strings.ToLower(argument), "/cmd.exe") || strings.HasSuffix(strings.ToLower(argument), "/powershell.exe")) {
			return c.Fail(c.PolicyDenied, "shell entrypoints are not allowed for MCP extensions")
		}
	}
	var totalSchemaBytes int
	seen := map[string]bool{}
	for _, tool := range manifest.Tools {
		if !toolNamePattern.MatchString(tool.Name) || !validText(tool.Description, 1, 2048) || len(tool.Title) > 128 || tool.Title != "" && !validText(tool.Title, 1, 128) || seen[tool.Name] {
			return c.Fail(c.InvalidArgument, "invalid or duplicate pinned extension tool")
		}
		seen[tool.Name] = true
		totalSchemaBytes += len(tool.InputSchema) + len(tool.OutputSchema)
		if totalSchemaBytes > MaxManifestTools*2*MaxSchemaBytes {
			return c.Fail(c.UnsupportedCapability, "aggregate extension schema budget exceeded")
		}
		if tool.MaxInputBytes < 1 || tool.MaxInputBytes > MaxToolInputBytes || tool.MaxOutputBytes < 1 || tool.MaxOutputBytes > MaxToolOutputBytes || len(tool.InputSchema) == 0 || len(tool.InputSchema) > MaxSchemaBytes || len(tool.OutputSchema) > MaxSchemaBytes {
			return c.Fail(c.UnsupportedCapability, "extension tool schema or message size exceeds its bound")
		}
		if err := tool.Egress.Validate(); err != nil {
			return err
		}
		if tool.Egress.Mode != EgressNone || len(tool.Egress.Origins) != 0 {
			return c.Fail(c.UnsupportedCapability, "extension tools currently run without network egress")
		}
		if tool.Effect != EffectReadOnly && tool.Effect != EffectSandboxCompute && tool.Effect != EffectExternalRead && tool.Effect != EffectExternalWrite {
			return c.Fail(c.PolicyDenied, "unknown or unsupported extension effect")
		}
		if (tool.Effect == EffectExternalRead || tool.Effect == EffectExternalWrite) && tool.Egress.Mode == EgressNone {
			return c.Fail(c.PolicyDenied, "external extension effects require a supported egress broker")
		}
		switch tool.Idempotency {
		case IdempotencyReadOnly:
			if tool.Effect != EffectReadOnly {
				return c.Fail(c.InvalidArgument, "READ_ONLY idempotency requires a read-only effect")
			}
		case IdempotencyIdempotent, IdempotencyNonIdempotent, IdempotencyUnknown:
		default:
			return c.Fail(c.InvalidArgument, "unknown extension idempotency class")
		}
		if err := validateSchema(tool.InputSchema); err != nil {
			return err
		}
		if len(tool.OutputSchema) > 0 {
			if err := validateSchema(tool.OutputSchema); err != nil {
				return err
			}
		}
	}
	raw, err := c.CanonicalV1(manifest)
	if err != nil || len(raw) > MaxManifestBytes {
		return c.Fail(c.UnsupportedCapability, "canonical extension manifest exceeds its size bound")
	}
	return nil
}

func (pin PinnedManifest) Validate() error {
	if err := pin.Manifest.Validate(); err != nil {
		return err
	}
	canonical, err := c.CanonicalV1(pin.Manifest)
	if err != nil || len(canonical) > MaxManifestBytes {
		return c.Fail(c.InvalidArgument, "extension manifest cannot be canonically represented within its bound")
	}
	digest := c.HashBytes(canonical)
	if digest != pin.Digest {
		return c.Fail(c.StaleAuthority, "extension manifest does not match its content pin")
	}
	return nil
}

// PinManifest reads one canonical manifest without following a caller-provided
// path through an unexpected directory entry. The returned digest covers the
// exact canonical bytes that were parsed.
func PinManifest(path string) (PinnedManifest, error) {
	var pin PinnedManifest
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return pin, c.Fail(c.InvalidArgument, "absolute extension manifest path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return pin, c.Fail(c.InvalidArgument, "extension manifest path unavailable")
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return pin, c.Fail(c.PolicyDenied, "extension manifest unavailable")
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(absolute), MaxManifestBytes)
	if err != nil || c.DecodeStrict(raw, &pin.Manifest) != nil || pin.Manifest.Validate() != nil {
		return PinnedManifest{}, c.Fail(c.InvalidArgument, "extension manifest is invalid or unavailable")
	}
	canonical, err := c.CanonicalV1(pin.Manifest)
	if err != nil || !bytes.Equal(canonical, raw) {
		return PinnedManifest{}, c.Fail(c.InvalidArgument, "extension manifest must use canonical encoding")
	}
	pin.Digest = c.HashBytes(raw)
	return pin, pin.Validate()
}

func ReadPinnedManifest(path, expectedDigest string) (PinnedManifest, error) {
	var pin PinnedManifest
	if !filepath.IsAbs(path) || !c.ValidDigest(expectedDigest) || strings.ContainsAny(path, "\x00\r\n") {
		return pin, c.Fail(c.InvalidArgument, "absolute manifest path and exact digest pin required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return pin, c.Fail(c.InvalidArgument, "extension manifest path unavailable")
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return pin, c.Fail(c.PolicyDenied, "extension manifest unavailable")
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(absolute), MaxManifestBytes)
	if err != nil || c.HashBytes(raw) != expectedDigest || c.DecodeStrict(raw, &pin.Manifest) != nil || pin.Manifest.Validate() != nil {
		return PinnedManifest{}, c.Fail(c.StaleAuthority, "extension manifest unavailable, invalid or changed")
	}
	canonical, err := c.CanonicalV1(pin.Manifest)
	if err != nil || !bytes.Equal(canonical, raw) {
		return PinnedManifest{}, c.Fail(c.StoreIntegrityError, "extension manifest must be canonical")
	}
	pin.Digest = expectedDigest
	return pin, pin.Validate()
}

func (manifest Manifest) Tool(name string) (Tool, bool) {
	for _, item := range manifest.Tools {
		if item.Name == name {
			return item, true
		}
	}
	return Tool{}, false
}

func (admission Admission) Validate(pin PinnedManifest, tool Tool) error {
	if err := pin.Validate(); err != nil {
		return err
	}
	if admission.ManifestDigest != pin.Digest || !c.ValidDigest(admission.AuthorityDigest) || !c.ValidDigest(admission.OperationDigest) || !admission.UserApproved || !containsEffect(admission.GrantedEffects, tool.Effect) {
		return c.Fail(c.PolicyDenied, "current operator admission and explicit tool approval required")
	}
	if tool.Egress.Mode != EgressNone || len(tool.EgressOrigins()) != 0 || len(admission.EgressOrigins) != 0 {
		return c.Fail(c.UnsupportedCapability, "extension egress is not available in the current network-none profile")
	}
	return nil
}

// EgressOrigins returns a copy because the immutable manifest is shared with
// caller and result construction.
func (tool Tool) EgressOrigins() []string { return append([]string(nil), tool.Egress.Origins...) }

func containsEffect(values []Effect, value Effect) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func validText(value string, min, max int) bool {
	if len(value) < min || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, item := range value {
		if item == 0 || item == '\r' || item < 0x20 && item != '\n' && item != '\t' {
			return false
		}
	}
	return true
}

func (tool Tool) SchemaDigest() (string, error) {
	return c.Digest(struct {
		Input  json.RawMessage `json:"input_schema"`
		Output json.RawMessage `json:"output_schema,omitempty"`
	}{tool.InputSchema, tool.OutputSchema})
}

func (tool Tool) String() string { return fmt.Sprintf("%s (%s)", tool.Name, tool.Effect) }
