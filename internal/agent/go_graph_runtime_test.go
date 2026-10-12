package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
)

func TestGoGraphIntentIsBoundToPinnedRuntimeAndOwnedCapsule(t *testing.T) {
	root := t.TempDir()
	runtimeDirectory := filepath.Join(root, "operator")
	if err := os.Mkdir(runtimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	profile := runner.DefaultProfile("sha256:" + strings.Repeat("a", 64))
	manifest := GoGraphRuntimeManifest{SchemaVersion: 1, Profile: profile, HelperCommand: GoGraphHelperCommand, HelperVersion: GoGraphHelperVersion}
	raw, err := c.CanonicalV1(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(runtimeDirectory, "go-graph-runtime.json")
	if err = os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	binding, err := BindGoGraphRuntime(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"goos":"linux","goarch":"amd64","compiler":"gc","go_version":"1.27.2","tags_complete":true,"cgo":"DISABLED"}`)
	call := model.Call{ID: "graph-call", Name: "fs_graph", Arguments: arguments}
	state := c.TaskState{PolicyEpoch: 1, KernelGeneration: 1}
	options, err := goGraphOptions(goGraphQuery{GOOS: "linux", GOARCH: "amd64", Compiler: "gc", Version: "1.27.2", TagsComplete: true, CGO: "DISABLED"})
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest := c.HashBytes([]byte("candidate snapshot"))
	doc := Document{TaskID: "graph-task", Spec: c.TaskSpec{Version: 1}, Candidate: artifact.Ref{SchemaVersion: 1, TaskID: "graph-task", SnapshotDigest: candidateDigest}, GoGraphRuntime: &binding, Budget: Budget{ToolCalls: 1}}
	intent, err := newGoGraphIntent(state, doc, call, options)
	if err != nil {
		t.Fatal(err)
	}
	doc.Pending = &Pending{ID: call.ID, Kind: "NATIVE_TOOL", Candidate: candidateDigest, Generation: 1, Epoch: 1, ArgumentsDigest: c.HashBytes(arguments), Status: "ADMITTED", GoGraphIntent: intent}
	doc.Messages = []model.Message{{Role: "assistant", Calls: []model.Call{call}}}
	lease := runner.Lease{SchemaVersion: 1, InstanceID: strings.Repeat("1", 32), ID: strings.Repeat("2", 32), TaskDigest: c.HashBytes([]byte(doc.TaskID)), OperationDigest: c.HashBytes([]byte(nativeResourceID(doc))), Generation: 1, PolicyEpoch: 1, SpecVersion: 1, ClockDomain: strings.Repeat("3", 32), DeadlineMillis: 120000}
	doc.Pending.NativeLease = &lease
	invocation, err := goGraphInvocation(doc, *intent)
	if err != nil {
		t.Fatal(err)
	}
	profileDigest, err := c.Digest(profile)
	if err != nil {
		t.Fatal(err)
	}
	invocationDigest, err := c.Digest(invocation)
	if err != nil {
		t.Fatal(err)
	}
	capsule := runner.Capsule{SchemaVersion: 1, Owner: runner.Ownership{Lease: lease, Phase: "CHECK", Ordinal: 1}, CandidateDigest: candidateDigest, ProfileDigest: profileDigest, InvocationDigest: invocationDigest, SourceDigest: c.HashBytes([]byte("materialized-source-path"))}
	doc.Pending.NativeSubjects = []runner.Capsule{capsule}
	if err = validateNativeLease(doc); err != nil {
		t.Fatalf("valid graph request/capsule rejected: %v", err)
	}
	if invocation.RequestDigest != intent.OperationDigest {
		t.Fatal("runner invocation did not bind the graph operation digest")
	}

	forged := doc
	forged.Pending = new(Pending)
	*forged.Pending = *doc.Pending
	forged.Pending.NativeSubjects = append([]runner.Capsule(nil), doc.Pending.NativeSubjects...)
	forged.Pending.NativeSubjects[0].InvocationDigest = c.HashBytes([]byte("forged worker request"))
	if err = validateNativeLease(forged); err == nil {
		t.Fatal("forged worker request was accepted under the retained runner capsule")
	}
}

func TestGoGraphAdmissionStopsAtInputBarrierBeforeIntent(t *testing.T) {
	call := model.Call{ID: "graph-call", Name: "fs_graph", Arguments: json.RawMessage(`{"goos":"linux","goarch":"amd64","compiler":"gc","go_version":"1.27.2","tags_complete":true,"cgo":"DISABLED"}`)}
	doc := Document{TaskID: "graph-task", Candidate: artifact.Ref{SchemaVersion: 1, TaskID: "graph-task", SnapshotDigest: c.HashBytes([]byte("candidate"))}, GoGraphRuntime: &GoGraphRuntimeBinding{}}
	state := c.TaskState{PolicyEpoch: 1, KernelGeneration: 1, InputBarrier: true}
	intent, err := (&Session{}).admitGoGraphCall(state, doc, call)
	if err == nil || intent != nil {
		t.Fatalf("graph admission crossed a durable input barrier: intent=%#v err=%v", intent, err)
	}
}
