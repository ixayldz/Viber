package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	ctxpack "github.com/ixayldz/Viber/internal/context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/plan"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

func projectInstructionTestPolicy() policy.Policy {
	return policy.Policy{SchemaVersion: c.SchemaVersion, Epoch: 1, Generation: 1,
		Effects: []string{"snapshot.read", "model.infer"}, Paths: []string{"**"}}
}

func TestCapturedClaudeInstructionsRespectFormatPrecedenceAndPolicy(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"CLAUDE.md":     []byte("root Claude guidance\n"),
		"src/AGENTS.md": []byte("preferred agent guidance\n"),
		"src/CLAUDE.md": []byte("lower priority guidance\n"),
		"src/main.go":   []byte("package main\n"),
	}
	for name, raw := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	capture, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	layers := []policy.Policy{projectInstructionTestPolicy()}
	loads := []string{}
	load := func(entry c.Entry) ([]byte, error) {
		loads = append(loads, entry.Path)
		return capture.Contents[entry.Path], nil
	}
	discovery, err := discoverProjectInstructionsFromSnapshot(capture.Snapshot, capture.Snapshot.Digest, layers, nil, load)
	if err != nil || discovery.Coverage != "COMPLETE" || len(discovery.Documents) != 2 || strings.Join(loads, ",") != "CLAUDE.md,src/AGENTS.md" {
		t.Fatalf("mixed-format discovery lost scope or precedence: loads=%v result=%+v err=%v", loads, discovery, err)
	}
	for _, document := range discovery.Documents {
		if document.CandidateDigest != capture.Snapshot.Digest || document.FileDigest != c.HashBytes(files[document.Path]) || document.Content != string(files[document.Path]) || document.ByteEnd != int64(len(files[document.Path])) {
			t.Fatal("Claude compatibility lost captured byte provenance", document)
		}
	}
	layers[0].DeniedPaths = []string{"src/AGENTS.md"}
	loads = nil
	discovery, err = discoverProjectInstructionsFromSnapshot(capture.Snapshot, capture.Snapshot.Digest, layers, nil, load)
	if err != nil || discovery.Coverage != "PARTIAL" || strings.Join(loads, ",") != "CLAUDE.md" || strings.Contains(renderInstructionsForTest(t, discovery), "lower priority guidance") {
		t.Fatalf("denied AGENTS guidance fell back to CLAUDE.md: loads=%v result=%+v err=%v", loads, discovery, err)
	}
	// Even when excluded from capture, a higher-priority format must not be
	// substituted with readable guidance from a lower-priority format.
	exclusions := workspace.DefaultLimits()
	exclusions.SensitivePaths = []string{"src/AGENTS.md"}
	excludedCapture, err := workspace.CaptureDirectory(root, exclusions)
	if err != nil {
		t.Fatal(err)
	}
	layers[0].DeniedPaths = nil
	loads = nil
	discovery, err = discoverProjectInstructionsFromSnapshot(excludedCapture.Snapshot, excludedCapture.Snapshot.Digest, layers, nil, func(entry c.Entry) ([]byte, error) {
		loads = append(loads, entry.Path)
		return excludedCapture.Contents[entry.Path], nil
	})
	if err != nil || discovery.Coverage != "PARTIAL" || strings.Join(loads, ",") != "CLAUDE.md" || strings.Contains(renderInstructionsForTest(t, discovery), "lower priority guidance") {
		t.Fatalf("excluded AGENTS guidance fell back to CLAUDE.md: loads=%v result=%+v err=%v", loads, discovery, err)
	}
}

func TestCapturedProjectInstructionsBindCompileAndReportLimits(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"AGENTS.md":                      []byte("root guidance\n"),
		"src/AGENTS.override.md":         []byte("override guidance\n"),
		"src/AGENTS.md":                  []byte("shadowed guidance\n"),
		"src/pkg/AGENTS.md":              []byte("package guidance\n"),
		"src/pkg/code.go":                []byte("package pkg\nfunc Work() {}\n"),
		"src/pkg/deep/AGENTS.md":         bytes.Repeat([]byte("large guidance\n"), 400),
		"src/pkg/deep/implementation.py": []byte("def work(): pass\n"),
		"other/AGENTS.md":                []byte("unrelated guidance\n"),
	}
	for name, raw := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	capture, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	layers := []policy.Policy{projectInstructionTestPolicy()}
	activePlan := &plan.State{
		Definition: plan.Definition{Nodes: []plan.Node{{ReadScope: []string{"**"}, WriteScope: []string{"src/pkg/**"}}}},
		Progress:   []plan.Progress{{Status: "RUNNING"}},
	}
	discovery, err := discoverProjectInstructions(capture, capture.Snapshot.Digest, layers, activePlan)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"AGENTS.md", "src/AGENTS.override.md", "src/pkg/AGENTS.md"}
	if discovery.Coverage != "PARTIAL" || len(discovery.Documents) != len(wantPaths) || len(discovery.Omissions) != 1 {
		t.Fatalf("expected bounded, visible partial discovery: %#v", discovery)
	}
	for i, want := range wantPaths {
		document := discovery.Documents[i]
		if document.Path != want || document.CandidateDigest != capture.Snapshot.Digest || document.FileDigest != c.HashBytes(capture.Contents[want]) || document.ByteStart != 0 || document.ByteEnd != int64(len(capture.Contents[want])) || document.Content != string(capture.Contents[want]) {
			t.Fatalf("instruction provenance does not bind exact captured bytes: %#v", document)
		}
	}
	if discovery.Documents[1].Content == "shadowed guidance\n" || discovery.Omissions[0].Path != "src/pkg/deep/AGENTS.md" || discovery.Omissions[0].Reason != "FILE_BYTE_LIMIT" {
		t.Fatalf("override precedence or byte-limit omission was lost: %#v", discovery)
	}

	session, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer session.Close()
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Candidate.SnapshotDigest = capture.Snapshot.Digest
	doc.Baseline.SnapshotDigest = capture.Snapshot.Digest
	state.CandidateDigest = capture.Snapshot.Digest
	_, pureRaw, pureManifest, err := compileOfflineRequest(doc, state, layers)
	if err != nil {
		t.Fatal(err)
	}
	request, encoded, manifest, err := compileOfflineRequestForCandidate(doc, state, layers, doc.Budget.MaxOutputTokens, capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.Instructions, "CAPTURED_PROJECT_INSTRUCTIONS_V1") || !strings.Contains(request.Instructions, capture.Snapshot.Digest) || !strings.Contains(request.Instructions, "root guidance") || strings.Contains(request.Instructions, "shadowed guidance") || !strings.Contains(request.Instructions, "FILE_BYTE_LIMIT") {
		t.Fatal("candidate instructions or their coverage/provenance did not enter the model request")
	}
	if manifest.InputTokens <= pureManifest.InputTokens || len(encoded) <= len(pureRaw) {
		t.Fatal("project instruction bytes were not included in request token accounting")
	}

	deniedPolicy := projectInstructionTestPolicy()
	deniedPolicy.DeniedPaths = []string{"src/AGENTS.override.md"}
	denied := []policy.Policy{deniedPolicy}
	restricted, err := discoverProjectInstructions(capture, capture.Snapshot.Digest, denied, nil)
	if err != nil {
		t.Fatal(err)
	}
	if restricted.Coverage != "PARTIAL" || strings.Contains(renderInstructionsForTest(t, restricted), "override guidance") || strings.Contains(renderInstructionsForTest(t, restricted), "shadowed guidance") {
		t.Fatal("read-scope deny was ignored or did not remain visible")
	}

	stale, err := discoverProjectInstructions(capture, c.HashBytes([]byte("different candidate")), layers, nil)
	if err == nil || stale.Freshness != "STALE" {
		t.Fatal("stale candidate instruction discovery was accepted")
	}
}

func TestProjectInstructionsRequireSnapshotReadEffect(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("restricted source guidance\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	capture, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	base := projectInstructionTestPolicy()
	operator := projectInstructionTestPolicy()
	operator.Effects = []string{"model.infer"}
	layers := []policy.Policy{base, operator}

	loads := 0
	discovery, err := discoverProjectInstructionsFromSnapshot(capture.Snapshot, capture.Snapshot.Digest, layers, nil, func(entry c.Entry) ([]byte, error) {
		loads++
		return capture.Contents[entry.Path], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if loads != 0 || len(discovery.Documents) != 0 || discovery.Coverage != "PARTIAL" ||
		!strings.Contains(strings.Join(discovery.Unknowns, " "), "PROJECT_INSTRUCTION_DENIED_BY_READ_POLICY") ||
		strings.Contains(renderInstructionsForTest(t, discovery), "restricted source guidance") {
		t.Fatalf("instruction source was read despite the missing snapshot.read effect: loads=%d discovery=%#v", loads, discovery)
	}

	// Model inference remains available; denied source guidance is represented
	// as partial coverage instead of failing request compilation or being read.
	session, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer session.Close()
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Candidate.SnapshotDigest = capture.Snapshot.Digest
	doc.Baseline.SnapshotDigest = capture.Snapshot.Digest
	state.CandidateDigest = capture.Snapshot.Digest
	request, _, _, err := compileOfflineRequestForCandidate(doc, state, layers, doc.Budget.MaxOutputTokens, capture)
	if err != nil {
		t.Fatalf("denied project guidance should not disable model inference compilation: %v", err)
	}
	if !strings.Contains(request.Instructions, "PROJECT_INSTRUCTION_DENIED_BY_READ_POLICY") || strings.Contains(request.Instructions, "restricted source guidance") {
		t.Fatal("compiled request omitted the explicit partial-coverage status or included denied guidance")
	}
}

func renderInstructionsForTest(t *testing.T, value projectInstructionContext) string {
	t.Helper()
	raw, err := renderProjectInstructions(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestArchiveProjectInstructionsLoadOnlySelectedBlobsAndRejectMissingSelected(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	archiveRoot := filepath.Join(root, "archive")
	if err := os.MkdirAll(sourceRoot, 0755); err != nil {
		t.Fatal(err)
	}
	instruction := []byte("captured instructions\n")
	unrelated := []byte("unrelated source bytes\n")
	if err := os.WriteFile(filepath.Join(sourceRoot, "AGENTS.md"), instruction, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "unrelated.txt"), unrelated, 0644); err != nil {
		t.Fatal(err)
	}
	capture, err := workspace.CaptureDirectory(sourceRoot, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := artifact.Open(archiveRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	ref, err := archive.Put("instruction-test", capture)
	if err != nil {
		t.Fatal(err)
	}
	entryByPath := make(map[string]c.Entry, len(capture.Snapshot.Entries))
	for _, entry := range capture.Snapshot.Entries {
		entryByPath[entry.Path] = entry
	}
	removeBlob := func(entry c.Entry) {
		t.Helper()
		name, err := artifact.ScopedBlobPath(ref.TaskID, entry.Hash)
		if err != nil {
			t.Fatal(err)
		}
		removed, err := archive.RemoveOrphan(artifact.Object{Path: name, Digest: entry.Hash, Size: entry.Size})
		if err != nil || !removed {
			t.Fatalf("remove test blob: removed=%v err=%v", removed, err)
		}
	}

	// A missing unrelated blob must not force hydration of the full capture.
	removeBlob(entryByPath["unrelated.txt"])
	discovery, err := discoverProjectInstructionsFromArchive(archive, ref, capture.Snapshot.Digest, []policy.Policy{projectInstructionTestPolicy()}, nil)
	if err != nil {
		t.Fatalf("selective discovery unnecessarily required an unrelated blob: %v", err)
	}
	if len(discovery.Documents) != 1 {
		t.Fatalf("expected one selected instruction document, got %#v", discovery)
	}
	document := discovery.Documents[0]
	if document.Path != "AGENTS.md" || document.CandidateDigest != capture.Snapshot.Digest ||
		document.FileDigest != c.HashBytes(instruction) || document.ByteStart != 0 || document.ByteEnd != int64(len(instruction)) || document.Content != string(instruction) {
		t.Fatalf("selected instruction provenance did not bind exact captured bytes: %#v", document)
	}

	// The selected blob is required and a missing/corrupt copy is fail-closed.
	removeBlob(entryByPath["AGENTS.md"])
	stale, err := discoverProjectInstructionsFromArchive(archive, ref, capture.Snapshot.Digest, []policy.Policy{projectInstructionTestPolicy()}, nil)
	if err == nil || stale.Freshness != "STALE" {
		t.Fatalf("missing selected instruction blob was not rejected as stale: result=%#v err=%v", stale, err)
	}
}

func TestProjectMemoryContextIsCurrentBoundAndAdvisory(t *testing.T) {
	session, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer session.Close()
	ctx := context.Background()
	receipt, err := session.RecordProjectMemory(ctx, options.TaskID, "memory-context-1", "CONVENTION", "gofmt", "Run gofmt before submitting Go changes.")
	if err != nil {
		t.Fatal(err)
	}
	state, doc, err := session.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := session.Archive.Get(doc.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	layers := session.layers(state, doc)
	_, withoutMemoryRaw, withoutMemoryManifest, err := compileOfflineRequestForCandidate(doc, state, layers, doc.Budget.MaxOutputTokens, source)
	if err != nil {
		t.Fatal(err)
	}
	var request model.Request
	var encoded []byte
	var manifest ctxpack.Manifest
	func() {
		session.mu.Lock()
		defer session.mu.Unlock()
		request, encoded, manifest, err = session.compileOfflineRequestForCurrentCandidate(ctx, doc, state, layers, doc.Budget.MaxOutputTokens)
	}()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.Instructions, projectMemoryContextProfile) || !strings.Contains(request.Instructions, projectMemoryContextHeader) ||
		!strings.Contains(request.Instructions, receipt.EventDigest) || !strings.Contains(request.Instructions, "Run gofmt before submitting Go changes.") ||
		!strings.Contains(request.Instructions, "candidate_snapshot_digest") || !strings.Contains(request.Instructions, "USER_ASSERTED") ||
		!strings.Contains(request.Instructions, "ACTIVE") || !strings.Contains(request.Instructions, "SAME_TASK_ONLY") ||
		!strings.Contains(request.Instructions, "CROSS_TASK_MEMORY_NOT_SEARCHED") {
		t.Fatal("current user memory did not enter model context with advisory status and exact provenance")
	}
	if manifest.InputTokens <= withoutMemoryManifest.InputTokens || len(encoded) <= len(withoutMemoryRaw) {
		t.Fatal("current project memory was not included in request budgeting")
	}

	staleState := state
	staleState.CandidateDigest = c.HashBytes([]byte("changed candidate"))
	var items []ProjectMemoryItem
	var truncated bool
	func() {
		session.mu.Lock()
		defer session.mu.Unlock()
		items, truncated, err = session.projectMemoryContextLocked(ctx, staleState, doc)
	}()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 || truncated {
		t.Fatal("memory from another candidate was returned as current advisory context")
	}
}
