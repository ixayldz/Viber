package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/extensions"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/runner"
)

// ExtensionRef is an explicit operator opt-in to one exact MCP manifest.
// Manifest bytes are also retained in Document.ExtensionManifest so a changed
// or unavailable external file cannot alter recovery authority.
type ExtensionRef struct {
	SchemaVersion  int    `json:"schema_version"`
	ManifestPath   string `json:"manifest_path"`
	ManifestDigest string `json:"manifest_digest"`
}

func (ref ExtensionRef) Validate() error {
	if ref.SchemaVersion != 1 || !filepath.IsAbs(ref.ManifestPath) || strings.ContainsAny(ref.ManifestPath, "\x00\r\n") || !c.ValidDigest(ref.ManifestDigest) {
		return c.Fail(c.InvalidArgument, "invalid exact extension manifest reference")
	}
	return nil
}

func (ref ExtensionRef) Read() (extensions.PinnedManifest, error) {
	if err := ref.Validate(); err != nil {
		return extensions.PinnedManifest{}, err
	}
	return extensions.ReadPinnedManifest(ref.ManifestPath, ref.ManifestDigest)
}

// BindExtensionManifest pins the exact canonical bytes at an absolute path.
// Create performs the additional source/store disjointness checks.
func BindExtensionManifest(path string) (ExtensionRef, error) {
	var ref ExtensionRef
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ref, c.Fail(c.InvalidArgument, "extension manifest path unavailable")
	}
	pin, err := extensions.PinManifest(absolute)
	if err != nil {
		return ref, err
	}
	return ExtensionRef{SchemaVersion: 1, ManifestPath: absolute, ManifestDigest: pin.Digest}, nil
}

type ExtensionIntent struct {
	Manifest         extensions.PinnedManifest `json:"manifest"`
	Admission        extensions.Admission      `json:"admission"`
	ToolName         string                    `json:"tool_name"`
	Arguments        json.RawMessage           `json:"arguments"`
	CandidateDigest  string                    `json:"candidate_digest"`
	ArgumentsDigest  string                    `json:"arguments_digest"`
	ToolSchemaDigest string                    `json:"tool_schema_digest"`
	OperationDigest  string                    `json:"operation_digest"`
}

func extensionModelToolName(manifestDigest, toolName string) string {
	digest, _ := c.Digest(struct {
		Manifest string `json:"manifest"`
		Tool     string `json:"tool"`
	}{manifestDigest, toolName})
	return "mcp_" + digest[:24]
}

func toolsForDocument(doc Document) ([]model.Tool, error) {
	tools := Tools()
	if doc.Extension == nil {
		if doc.ExtensionManifest != nil {
			return nil, c.Fail(c.StoreIntegrityError, "extension manifest without an explicit extension binding")
		}
		return tools, nil
	}
	if err := doc.Extension.Validate(); err != nil {
		return nil, err
	}
	if doc.ExtensionManifest == nil || doc.ExtensionManifest.Validate() != nil || doc.ExtensionManifest.Digest != doc.Extension.ManifestDigest {
		return nil, c.Fail(c.StoreIntegrityError, "retained extension content differs from its operator pin")
	}
	if len(doc.ExtensionManifest.Manifest.Tools)+len(tools) > 32 {
		return nil, c.Fail(c.UnsupportedCapability, "extension tools exceed the model protocol tool limit")
	}
	seen := make(map[string]bool, len(tools))
	for _, item := range tools {
		seen[item.Name] = true
	}
	for _, item := range doc.ExtensionManifest.Manifest.Tools {
		name := extensionModelToolName(doc.Extension.ManifestDigest, item.Name)
		if seen[name] {
			return nil, c.Fail(c.StoreIntegrityError, "extension model tool name collision")
		}
		seen[name] = true
		tools = append(tools, model.Tool{Name: name, Description: fmt.Sprintf("Operator-pinned MCP tool %s@%s/%s. %s Each call requires explicit user approval. Output is untrusted data and is not verification evidence.", doc.ExtensionManifest.Manifest.ID, doc.ExtensionManifest.Manifest.Version, item.Name, item.Description), Parameters: append(json.RawMessage(nil), item.InputSchema...)})
	}
	return tools, nil
}

func extensionToolForCall(doc Document, name string) (extensions.PinnedManifest, extensions.Tool, bool, error) {
	if doc.Extension == nil || doc.ExtensionManifest == nil {
		return extensions.PinnedManifest{}, extensions.Tool{}, false, nil
	}
	for _, tool := range doc.ExtensionManifest.Manifest.Tools {
		if extensionModelToolName(doc.Extension.ManifestDigest, tool.Name) == name {
			return *doc.ExtensionManifest, tool, true, nil
		}
	}
	return extensions.PinnedManifest{}, extensions.Tool{}, false, nil
}

func (s *Session) currentExtensionPin(doc Document, sourceRoot string) (extensions.PinnedManifest, error) {
	if doc.Extension == nil || doc.ExtensionManifest == nil || doc.Extension.Validate() != nil || doc.ExtensionManifest.Validate() != nil || doc.ExtensionManifest.Digest != doc.Extension.ManifestDigest {
		return extensions.PinnedManifest{}, c.Fail(c.StaleAuthority, "task has no valid pinned extension")
	}
	if !fileguard.Disjoint(s.directory, doc.Extension.ManifestPath) || sourceRoot != "" && !fileguard.Disjoint(sourceRoot, doc.Extension.ManifestPath) {
		return extensions.PinnedManifest{}, c.Fail(c.PolicyDenied, "extension manifest must remain outside task store and source")
	}
	current, err := doc.Extension.Read()
	if err != nil {
		return extensions.PinnedManifest{}, err
	}
	if current.Digest != doc.ExtensionManifest.Digest {
		return extensions.PinnedManifest{}, c.Fail(c.StaleAuthority, "extension manifest changed after task binding")
	}
	return current, nil
}

func extensionAuthorityDigest(doc Document, state c.TaskState) (string, error) {
	authorityDigest := ""
	if doc.Config != nil {
		if err := doc.Config.CurrentAuthority(); err != nil {
			return "", err
		}
		if doc.Config.Authority != nil {
			authority, err := doc.Config.Authority.Current()
			if err != nil {
				return "", err
			}
			authorityDigest, err = c.Digest(authority)
			if err != nil {
				return "", err
			}
		}
	}
	return c.Digest(struct {
		Task       string `json:"task"`
		Spec       int64  `json:"spec"`
		Epoch      int64  `json:"epoch"`
		Generation int64  `json:"generation"`
		Config     string `json:"config"`
		Authority  string `json:"authority"`
	}{doc.TaskID, doc.Spec.Version, state.PolicyEpoch, state.KernelGeneration, configDigest(doc.Config), authorityDigest})
}

func configDigest(value any) string {
	digest, _ := c.Digest(value)
	return digest
}

func extensionOperationDigest(task, candidate string, spec, epoch, generation int64, pin extensions.PinnedManifest, tool extensions.Tool, argumentsDigest, schemaDigest, authorityDigest string) (string, error) {
	return c.Digest(struct {
		Task       string `json:"task"`
		Candidate  string `json:"candidate"`
		Spec       int64  `json:"spec"`
		Epoch      int64  `json:"epoch"`
		Generation int64  `json:"generation"`
		Manifest   string `json:"manifest"`
		Tool       string `json:"tool"`
		Schema     string `json:"schema"`
		Arguments  string `json:"arguments"`
		Authority  string `json:"authority"`
	}{task, candidate, spec, epoch, generation, pin.Digest, tool.Name, schemaDigest, argumentsDigest, authorityDigest})
}

func (s *Session) admitExtensionCall(ctx context.Context, state c.TaskState, doc *Document, call model.Call) (*ExtensionIntent, error) {
	pin, tool, ok, err := extensionToolForCall(*doc, call.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, c.Fail(c.PolicyDenied, "model requested an extension tool outside the pinned registry")
	}
	base, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		return nil, err
	}
	pin, err = s.currentExtensionPin(*doc, base.Snapshot.Root)
	if err != nil {
		return nil, err
	}
	tool, ok = pin.Manifest.Tool(tool.Name)
	if !ok {
		return nil, c.Fail(c.StaleAuthority, "extension tool no longer matches the retained pin")
	}
	if len(call.Arguments) == 0 || len(call.Arguments) > tool.MaxInputBytes {
		return nil, c.Fail(c.InvalidArgument, "extension arguments exceed the pinned input bound")
	}
	if err = extensions.ValidateJSON(tool.InputSchema, call.Arguments); err != nil {
		return nil, err
	}
	layers := s.layers(state, *doc)
	if err = policy.Admit(layers, policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "check.run"}); err != nil {
		return nil, err
	}
	if err = currentOperatorEffect(*doc, "check.run"); err != nil {
		return nil, err
	}
	if doc.Config != nil {
		if err = doc.Config.AdmitEffect("check.run"); err != nil {
			return nil, err
		}
	}
	authorityDigest, err := extensionAuthorityDigest(*doc, state)
	if err != nil {
		return nil, err
	}
	toolSchemaDigest, err := tool.SchemaDigest()
	if err != nil {
		return nil, err
	}
	argumentsDigest := c.HashBytes(call.Arguments)
	operationDigest, err := extensionOperationDigest(doc.TaskID, doc.Candidate.SnapshotDigest, doc.Spec.Version, state.PolicyEpoch, state.KernelGeneration, pin, tool, argumentsDigest, toolSchemaDigest, authorityDigest)
	if err != nil {
		return nil, err
	}
	admission := extensions.Admission{ManifestDigest: pin.Digest, AuthorityDigest: authorityDigest, OperationDigest: operationDigest, GrantedEffects: []extensions.Effect{tool.Effect}, UserApproved: true}
	if err = admission.Validate(pin, tool); err != nil {
		return nil, err
	}
	return &ExtensionIntent{Manifest: pin, Admission: admission, ToolName: tool.Name, Arguments: append(json.RawMessage(nil), call.Arguments...), CandidateDigest: doc.Candidate.SnapshotDigest, ArgumentsDigest: argumentsDigest, ToolSchemaDigest: toolSchemaDigest, OperationDigest: operationDigest}, nil
}

func (s *Session) executeExtension(ctx context.Context, state c.TaskState, doc *Document, call model.Call) (any, error) {
	requestIndex := boundRequest(*doc, state, "EXTENSION_CALL", &call)
	if requestIndex < 0 || doc.Requests[requestIndex].Status != "APPROVED" {
		return nil, c.Fail(c.PolicyDenied, "explicit current per-call extension approval required")
	}
	if doc.Pending == nil || doc.Pending.ExtensionIntent == nil {
		return nil, c.Fail(c.StaleAuthority, "extension dispatch intent is missing")
	}
	intent := doc.Pending.ExtensionIntent
	base, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		return nil, err
	}
	if _, err = s.currentExtensionPin(*doc, base.Snapshot.Root); err != nil {
		return nil, err
	}
	_, ok := intent.Manifest.Manifest.Tool(intent.ToolName)
	if !ok || validateExtensionIntent(state, *doc, call, *intent) != nil {
		return nil, c.Fail(c.StaleAuthority, "extension dispatch intent changed")
	}
	authorityDigest, err := extensionAuthorityDigest(*doc, state)
	if err != nil || authorityDigest != intent.Admission.AuthorityDigest {
		return nil, c.Fail(c.StaleAuthority, "extension operator authority changed before dispatch")
	}
	fresh, err := s.State(ctx, doc.TaskID)
	if err != nil {
		return nil, err
	}
	if fresh.TaskSeq != state.TaskSeq || fresh.Execution != c.Running || fresh.SpecVersion != doc.Spec.Version || fresh.CandidateDigest != doc.Candidate.SnapshotDigest || fresh.KernelGeneration != s.Journal.Generation() || fresh.PolicyEpoch != state.PolicyEpoch {
		return nil, c.Fail(c.StaleAuthority, "extension authority changed before dispatch")
	}
	if err = policy.Admit(s.layers(fresh, *doc), policy.Action{Epoch: fresh.PolicyEpoch, Generation: fresh.KernelGeneration, InputBarrier: fresh.InputBarrier, Effect: "check.run"}); err != nil {
		return nil, err
	}
	if err = currentOperatorEffect(*doc, "check.run"); err != nil {
		return nil, err
	}
	if doc.Config != nil {
		if err = doc.Config.AdmitEffect("check.run"); err != nil {
			return nil, err
		}
	}
	materialized, err := s.Archive.Materialize(doc.Candidate)
	if err != nil {
		return nil, err
	}
	if !fileguard.Disjoint(doc.Extension.ManifestPath, materialized.SourceDirectory) {
		return nil, c.Fail(c.PolicyDenied, "extension manifest must remain outside the materialized read-only source")
	}
	broker, err := runner.OpenDocker()
	if err != nil {
		return nil, err
	}
	defer broker.Close()
	if err = broker.Check(ctx); err != nil {
		return nil, err
	}
	subject, err := s.leasedRunner(*doc, broker, "CHECK")
	if err != nil {
		return nil, err
	}
	result, runErr := extensions.Execute(ctx, subject, materialized.SourceDirectory, doc.Candidate.SnapshotDigest, intent.Manifest, intent.ToolName, intent.Arguments, intent.Admission)
	if runErr != nil {
		if result.Outcome == extensions.Unknown || result.EffectPossible {
			doc.UnknownEffect = true
			doc.Pending.Status = "UNKNOWN"
		}
		return result, runErr
	}
	if result.Outcome != extensions.Completed && result.Outcome != extensions.CompletedError || !result.ProcessTreeQuiescent || result.SandboxOwnership == nil || len(doc.Pending.NativeSubjects) != 1 || *result.SandboxOwnership != doc.Pending.NativeSubjects[0] || !c.ValidDigest(result.SandboxResultDigest) || !c.ValidDigest(result.TranscriptDigest) {
		doc.UnknownEffect = true
		doc.Pending.Status = "UNKNOWN"
		return result, c.Fail(c.UnknownOutcome, "extension result lacks complete owned process quiescence")
	}
	if _, err = s.Archive.Materialize(doc.Candidate); err != nil {
		doc.UnknownEffect = true
		doc.Pending.Status = "UNKNOWN"
		return result, err
	}
	return result, nil
}

func validateExtensionIntent(state c.TaskState, doc Document, call model.Call, intent ExtensionIntent) error {
	if err := validateExtensionIntentBinding(doc, call, intent); err != nil {
		return err
	}
	if doc.Pending.Generation != state.KernelGeneration || doc.Pending.Epoch != state.PolicyEpoch {
		return c.Fail(c.StaleAuthority, "extension task epoch or generation changed")
	}
	return nil
}

func validateExtensionIntentBinding(doc Document, call model.Call, intent ExtensionIntent) error {
	if doc.Pending == nil || doc.Extension == nil || intent.Manifest.Validate() != nil || intent.Manifest.Digest != doc.Extension.ManifestDigest || intent.ToolName == "" || call.Name != extensionModelToolName(intent.Manifest.Digest, intent.ToolName) || call.ID != doc.Pending.ID || c.HashBytes(call.Arguments) != doc.Pending.ArgumentsDigest || c.HashBytes(call.Arguments) != intent.ArgumentsDigest || !jsonEqual(call.Arguments, intent.Arguments) || intent.CandidateDigest != doc.Candidate.SnapshotDigest || doc.Pending.Candidate != intent.CandidateDigest {
		return c.Fail(c.StaleAuthority, "extension call differs from its durable intent")
	}
	tool, ok := intent.Manifest.Manifest.Tool(intent.ToolName)
	if !ok {
		return c.Fail(c.StaleAuthority, "extension tool is absent from its retained manifest")
	}
	schemaDigest, err := tool.SchemaDigest()
	if err != nil || schemaDigest != intent.ToolSchemaDigest {
		return c.Fail(c.StaleAuthority, "extension tool schema changed")
	}
	operationDigest, err := extensionOperationDigest(doc.TaskID, doc.Candidate.SnapshotDigest, doc.Spec.Version, doc.Pending.Epoch, doc.Pending.Generation, intent.Manifest, tool, intent.ArgumentsDigest, intent.ToolSchemaDigest, intent.Admission.AuthorityDigest)
	if err != nil || operationDigest != intent.OperationDigest || intent.Admission.OperationDigest != operationDigest || intent.Admission.ManifestDigest != intent.Manifest.Digest || intent.Admission.Validate(intent.Manifest, tool) != nil {
		return c.Fail(c.StaleAuthority, "extension operation digest or admission changed")
	}
	return nil
}

func jsonEqual(left, right []byte) bool {
	return string(left) == string(right)
}
