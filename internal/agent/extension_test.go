package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/extensions"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
)

func TestExtensionToolAndDurableCapsuleBinding(t *testing.T) {
	manifest := extensions.Manifest{
		SchemaVersion: extensions.SchemaVersion, ID: "agent-fixture", Version: "1.0.0", ProtocolVersion: extensions.MCPProtocolVersion,
		ServerName: "fixture", ServerVersion: "1.0.0", Profile: runner.DefaultProfile("fixture@sha256:" + strings.Repeat("a", 64)),
		Command: []string{"/fixture-server"}, Egress: extensions.EgressPolicy{Mode: extensions.EgressNone},
		Tools: []extensions.Tool{{Name: "lookup", Description: "Look up a local value", InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`), Effect: extensions.EffectReadOnly, Egress: extensions.EgressPolicy{Mode: extensions.EgressNone}, Idempotency: extensions.IdempotencyReadOnly, MaxInputBytes: 1024, MaxOutputBytes: 4096}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := c.CanonicalV1(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "extension.json")
	if err = os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := BindExtensionManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := ref.Read()
	if err != nil {
		t.Fatal(err)
	}
	doc := Document{Extension: &ref, ExtensionManifest: &pin}
	tools, err := toolsForDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	modelName := extensionModelToolName(pin.Digest, "lookup")
	found := false
	for _, item := range tools {
		gotSchema, _ := c.CanonicalV1(json.RawMessage(item.Parameters))
		wantSchema, _ := c.CanonicalV1(json.RawMessage(manifest.Tools[0].InputSchema))
		if item.Name == modelName && string(gotSchema) == string(wantSchema) {
			found = true
		}
	}
	if !found {
		t.Fatal("the pinned MCP schema was not exposed under its content-bound native tool name")
	}

	arguments := json.RawMessage(`{"key":"one"}`)
	candidate := c.HashBytes([]byte("candidate"))
	authority := c.HashBytes([]byte("authority"))
	argsDigest := c.HashBytes(arguments)
	schemaDigest, _ := manifest.Tools[0].SchemaDigest()
	operationDigest, _ := extensionOperationDigest("task", candidate, 1, 1, 1, pin, manifest.Tools[0], argsDigest, schemaDigest, authority)
	admission := extensions.Admission{ManifestDigest: pin.Digest, AuthorityDigest: authority, OperationDigest: operationDigest, GrantedEffects: []extensions.Effect{extensions.EffectReadOnly}, UserApproved: true}
	lease := runner.Lease{SchemaVersion: 1, InstanceID: strings.Repeat("1", 32), ID: strings.Repeat("2", 32), TaskDigest: c.HashBytes([]byte("task")), OperationDigest: c.HashBytes([]byte("placeholder")), Generation: 1, PolicyEpoch: 1, SpecVersion: 1, ClockDomain: strings.Repeat("3", 32), DeadlineMillis: 10000}
	intent := &ExtensionIntent{Manifest: pin, Admission: admission, ToolName: "lookup", Arguments: arguments, CandidateDigest: candidate, ArgumentsDigest: argsDigest, ToolSchemaDigest: schemaDigest, OperationDigest: operationDigest}
	doc = Document{TaskID: "task", Spec: c.TaskSpec{Version: 1}, Candidate: artifactRef("task", candidate), Extension: &ref, ExtensionManifest: &pin, ToolCursor: 0, Budget: Budget{ToolCalls: 1}}
	doc.Pending = &Pending{ID: "call-1", Kind: "NATIVE_TOOL", Candidate: candidate, Generation: 1, Epoch: 1, ArgumentsDigest: argsDigest, Status: "ADMITTED", ExtensionIntent: intent}
	lease.OperationDigest = c.HashBytes([]byte(nativeResourceID(doc)))
	doc.Pending.NativeLease = &lease
	call := model.Call{ID: "call-1", Name: modelName, Arguments: arguments}
	doc.Messages = []model.Message{{Role: "assistant", Calls: []model.Call{call}}}
	var empty []byte
	invocation := runner.Invocation{CandidateDigest: candidate, Argv: manifest.Command, RequestDigest: operationDigest, Stdin: &empty}
	owner := runner.Ownership{Lease: lease, Phase: "CHECK", Ordinal: 1}
	profileDigest, err := c.Digest(manifest.Profile)
	if err != nil {
		t.Fatal(err)
	}
	invocationDigest, err := c.Digest(invocation)
	if err != nil {
		t.Fatal(err)
	}
	capsule := runner.Capsule{SchemaVersion: 1, Owner: owner, CandidateDigest: candidate, ProfileDigest: profileDigest, InvocationDigest: invocationDigest, SourceDigest: c.HashBytes([]byte("source identity"))}
	doc.Pending.NativeSubjects = []runner.Capsule{capsule}
	if err = validateNativeLease(doc); err != nil {
		t.Fatalf("valid retained extension capsule rejected: %v", err)
	}

	mutated := doc
	mutated.Pending = new(Pending)
	*mutated.Pending = *doc.Pending
	mutated.Pending.ExtensionIntent = new(ExtensionIntent)
	*mutated.Pending.ExtensionIntent = *doc.Pending.ExtensionIntent
	mutated.Pending.ExtensionIntent.OperationDigest = c.HashBytes([]byte("changed operation"))
	if err = validateNativeLease(mutated); err == nil {
		t.Fatal("changed extension operation was accepted under the old runner capsule")
	}
}

func artifactRef(task, digest string) artifact.Ref {
	return artifact.Ref{TaskID: task, SnapshotDigest: digest}
}
