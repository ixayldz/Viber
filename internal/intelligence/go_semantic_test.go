package intelligence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/workspace"
)

func TestGoSemanticWorkerPinnedDockerIntegration(t *testing.T) {
	image := os.Getenv("VIBER_GO_GRAPH_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned Go graph helper image required")
	}
	sourceRoot := t.TempDir()
	if err := os.Chmod(sourceRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "go.mod"), []byte("module fixture.invalid/graph\n\ngo 1.27.2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "graph.go"), []byte("package graph\nfunc Target() {}\nfunc Caller() { Target() }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	capture, err := workspace.CaptureDirectory(sourceRoot, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	options, err := NormalizeGoSemanticOptions(GoSemanticOptions{GOOS: "linux", GOARCH: "amd64", Compiler: "gc", GoVersion: "1.27.2", BuildTagsComplete: true, CGOEnabled: "DISABLED"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(GoSemanticWorkerRequest{Protocol: GoSemanticWorkerProtocol, CandidateDigest: capture.Snapshot.Digest, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	requestDigest := c.HashBytes(request)
	lease := runner.Lease{SchemaVersion: 1, InstanceID: strings.Repeat("1", 32), ID: strings.Repeat("2", 32), TaskDigest: capture.Snapshot.Digest, OperationDigest: requestDigest, Generation: 1, PolicyEpoch: 1, SpecVersion: 1, ClockDomain: strings.Repeat("3", 32), DeadlineMillis: 120000}
	input := append([]byte(nil), request...)
	profile := runner.DefaultProfile(image)
	target, capsule, err := runner.PrepareTarget(runner.Target{Owner: runner.Ownership{Lease: lease, Phase: "CHECK", Ordinal: 1}, Source: sourceRoot, Profile: profile, Invocation: runner.Invocation{CandidateDigest: capture.Snapshot.Digest, RequestDigest: requestDigest, Argv: []string{"/usr/local/bin/viber-go-graph-helper"}, Stdin: &input}})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := runner.OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	owned, err := runner.WithOwnership(ctx, target.Owner, time.Now().Add(120*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	result, err := broker.Run(owned, target.Source, target.Profile, target.Invocation)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.OutputTruncated || !result.SourceReadOnly || !result.ProtectedResultChannel || !result.ProcessTreeQuiescent || result.Ownership == nil || *result.Ownership != capsule {
		t.Fatalf("pinned graph helper lacks owned bounded completion evidence: %#v", result)
	}
	var response GoSemanticWorkerResponse
	if err = c.DecodeStrict(result.Stdout, &response); err != nil || response.Protocol != GoSemanticWorkerProtocol || response.TypeCheckMode != GoSemanticWorkerTypeCheckMode {
		t.Fatalf("pinned helper omitted its fixed type-check mode: %v", err)
	}
	if err = ValidateGoSemanticGraphEnvelope(response.Graph, capture, options, capture.Snapshot.Digest); err != nil {
		t.Fatalf("pinned helper graph did not bind to exact source bytes: %v", err)
	}
	for _, edge := range response.Graph.Edges {
		if edge.Relation == "CALLS" && edge.ResolutionClass == "ACTUAL_RESOLVED" && edge.EvidencePath == "graph.go" && edge.EvidenceKind == "GO_TYPES_OBJECT_USE" {
			return
		}
	}
	t.Fatalf("pinned helper did not produce the expected resolved source call: %#v", response.Graph.Edges)
}

func TestCapturedGoSemanticGraphResolvesCallsAndWidensUncertainBuilds(t *testing.T) {
	source := goSemanticFixture(t, map[string][]byte{
		"go.mod":                     []byte("module example.test/m\n\ngo 1.27.2\n"),
		"pkg/base.go":                []byte("package base\nfunc Changed() {}\n"),
		"pkg/peer.go":                []byte("package base\nfunc Caller() { Changed() }\n"),
		"pkg/platform_linux.go":      []byte("//go:build linux\n\npackage base\nfunc Platform() {}\n"),
		"consumer/use.go":            []byte("package consumer\nimport \"example.test/m/pkg\"\nfunc Use() { base.Caller() }\n"),
		"consumer/use_test.go":       []byte("package consumer\nfunc TestUse() { Use() }\n"),
		"consumer/remote_windows.go": []byte("//go:build windows\n\npackage consumer\nimport \"fmt\"\nfunc Remote() { fmt.Println(\"x\") }\n"),
	})
	options := GoSemanticOptions{GOOS: "linux", GOARCH: "amd64", Compiler: "gc", CGOEnabled: "DISABLED", BuildTagsComplete: true}
	graph, err := BuildGoSemanticGraph(context.Background(), source, options)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Coverage.Overall != "COMPLETE_FOR_CAPTURED_BUILD_CONTEXT" {
		t.Fatalf("expected complete captured build scope, got %#v", graph.Coverage)
	}
	if graph.Coverage.ExcludedFiles != 1 || graph.Coverage.WidenCheckSelection {
		t.Fatalf("known non-target build file should be excluded without widening this exact context: %#v", graph.Coverage)
	}
	resolvedCalls := 0
	for _, edge := range graph.Edges {
		if edge.Relation == "CALLS" && edge.ResolutionClass == "ACTUAL_RESOLVED" {
			resolvedCalls++
		}
	}
	if resolvedCalls < 3 {
		t.Fatalf("expected calls resolved across source files and packages, got %d edges: %#v", resolvedCalls, graph.Edges)
	}
	if err := ValidateGoSemanticGraph(graph, source, options); err != nil {
		t.Fatalf("fresh graph did not validate: %v", err)
	}
	hints, err := BuildGoSemanticHintsGraph(context.Background(), source, options)
	if err != nil {
		t.Fatal(err)
	}
	if hints.Coverage.TypeCheckedPackages != 0 || !hints.Coverage.WidenCheckSelection || hints.Coverage.Overall != "PARTIAL_SOURCE_BOUND_GO_TYPES_GRAPH" || !containsGoString(hints.Coverage.Unknowns, "GO_TYPES_NOT_RUN_IN_COOPERATIVE_PATH") {
		t.Fatalf("parse-only host hints did not remain partial and broad: %#v", hints.Coverage)
	}
	if err := ValidateGoSemanticGraph(hints, source, options); err != nil {
		t.Fatalf("parse-only graph failed source validation: %v", err)
	}
	selection, err := SelectGoTests(graph, []string{"pkg/base.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.SelectedTestPaths) != 1 || selection.SelectedTestPaths[0] != "consumer/use_test.go" || !selection.WidenToAllChecks {
		t.Fatalf("unexpected package import closure or check authority: %#v", selection)
	}

	forged := graph
	forged.Edges = append([]GoSemanticEdge(nil), graph.Edges...)
	for n := range forged.Edges {
		if forged.Edges[n].Relation == "CALLS" && forged.Edges[n].ResolutionClass == "ACTUAL_RESOLVED" {
			forged.Edges[n].To = c.HashBytes([]byte("invented target"))
			break
		}
	}
	if err := sealGoSemanticGraph(&forged); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoSemanticGraph(forged, source, options); err == nil {
		t.Fatal("rehashed forged type-resolution edge was accepted")
	}

	uncertain, err := BuildGoSemanticGraph(context.Background(), source, GoSemanticOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if uncertain.Coverage.Overall != "PARTIAL_SOURCE_BOUND_GO_TYPES_GRAPH" || !uncertain.Coverage.WidenCheckSelection {
		t.Fatalf("unknown platform context did not mark graph partial: %#v", uncertain.Coverage)
	}
	widened, err := SelectGoTests(uncertain, []string{"pkg/base.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !widened.WidenToAllTests || !widened.WidenToAllChecks || len(widened.SelectedTestPaths) != 1 {
		t.Fatalf("partial graph narrowed test/check coverage: %#v", widened)
	}

	windowsOptions := GoSemanticOptions{GOOS: "windows", GOARCH: "amd64", Compiler: "gc", CGOEnabled: "DISABLED", BuildTagsComplete: true}
	windowsGraph, err := BuildGoSemanticGraph(context.Background(), source, windowsOptions)
	if err != nil {
		t.Fatal(err)
	}
	if windowsGraph.Coverage.UnresolvedImports == 0 || windowsGraph.Coverage.Overall != "PARTIAL_SOURCE_BOUND_GO_TYPES_GRAPH" {
		t.Fatalf("uncaptured external import was not explicit partial coverage: %#v", windowsGraph.Coverage)
	}
	possibleImports := 0
	for _, edge := range windowsGraph.Edges {
		if edge.Relation == "IMPORTS" && edge.ResolutionClass == "LEXICAL_POSSIBLE" {
			possibleImports++
		}
	}
	if possibleImports == 0 {
		t.Fatal("uncaptured external import did not remain a lexical possible edge")
	}
}

func TestCapturedGenericRecursionSkipsSynchronousTypesCheck(t *testing.T) {
	source := goSemanticFixture(t, map[string][]byte{
		"go.mod":          []byte("module example.test/m\n\ngo 1.27.2\n"),
		"generic/base.go": []byte("package base\nfunc Grow[T any](value T) { Grow[[]T](nil) }\nfunc Plain() {}\n"),
		"consumer/use.go": []byte("package consumer\nimport \"example.test/m/generic\"\nfunc Use() { base.Plain() }\n"),
	})
	options := GoSemanticOptions{GOOS: "linux", GOARCH: "amd64", Compiler: "gc", CGOEnabled: "DISABLED", BuildTagsComplete: true}
	graph, err := BuildGoSemanticGraph(context.Background(), source, options)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Coverage.TypeCheckSkippedPackages != 2 || !graph.Coverage.WidenCheckSelection || graph.Coverage.Overall != "PARTIAL_SOURCE_BOUND_GO_TYPES_GRAPH" {
		t.Fatalf("generic recursion did not trigger conservative package closure skip: %#v", graph.Coverage)
	}
	for _, edge := range graph.Edges {
		if edge.Relation == "CALLS" && edge.ResolutionClass == "ACTUAL_RESOLVED" {
			t.Fatalf("synchronous type checking still emitted resolved call evidence: %#v", edge)
		}
	}
	if err := ValidateGoSemanticGraph(graph, source, options); err != nil {
		t.Fatalf("partial generic graph did not validate against captured source: %v", err)
	}
}

func goSemanticFixture(t *testing.T, contents map[string][]byte) workspace.Capture {
	t.Helper()
	entries := make([]c.Entry, 0, len(contents))
	directories := map[string]bool{}
	for name, data := range contents {
		entries = append(entries, c.Entry{Path: name, Hash: c.HashBytes(data), Size: int64(len(data)), Mode: 0644})
		for parent := name; strings.Contains(parent, "/"); {
			parent = parent[:strings.LastIndex(parent, "/")]
			directories[parent] = true
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	dirs := make([]string, 0, len(directories))
	for dir := range directories {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	snapshot := c.Snapshot{SchemaVersion: c.SchemaVersion, Root: "C:/semantic-fixture", Consistency: "BEST_EFFORT",
		Entries: entries, Exclusions: []string{}, Directories: dirs}
	snapshot.Digest, _ = c.Digest(snapshot)
	capture := workspace.Capture{Snapshot: snapshot, Contents: contents}
	if err := workspace.VerifyCapture(capture); err != nil {
		t.Fatalf("invalid Go fixture capture: %v", err)
	}
	return capture
}
