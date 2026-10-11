package verify

import (
	"path"
	"sort"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/discovery"
	"github.com/ixayldz/Viber/internal/workspace"
)

func discoveryFixtureCapture(t *testing.T) workspace.Capture {
	t.Helper()
	files := map[string][]byte{"package.json": []byte(`{"devDependencies":{"jest":"29.0.0"}}`), "tests/a.test.js": []byte(`import "./helper.js";`), "tests/helper.js": []byte(`export const value = 1;`)}
	paths := []string{}
	dirs := map[string]bool{}
	entries := []c.Entry{}
	for name := range files {
		paths = append(paths, name)
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	sort.Strings(paths)
	for _, name := range paths {
		raw := files[name]
		entries = append(entries, c.Entry{Path: name, Hash: c.HashBytes(raw), Size: int64(len(raw)), Mode: 0600})
	}
	directories := []string{}
	for name := range dirs {
		directories = append(directories, name)
	}
	sort.Strings(directories)
	snapshot := c.Snapshot{SchemaVersion: c.SchemaVersion, Root: "captured-fixture", Consistency: "BEST_EFFORT", Entries: entries, Exclusions: []string{}, Directories: directories}
	var err error
	snapshot.Digest, err = c.Digest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	capture := workspace.Capture{Snapshot: snapshot, Contents: files}
	if err := workspace.VerifyCapture(capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

func captureWithAddedFile(t *testing.T, base workspace.Capture, name, body string) workspace.Capture {
	t.Helper()
	capture := workspace.Capture{Snapshot: base.Snapshot, Contents: map[string][]byte{}}
	capture.Snapshot.Entries = append([]c.Entry(nil), base.Snapshot.Entries...)
	capture.Snapshot.Exclusions = append([]string{}, base.Snapshot.Exclusions...)
	capture.Snapshot.Directories = append([]string{}, base.Snapshot.Directories...)
	for path, raw := range base.Contents {
		capture.Contents[path] = append([]byte(nil), raw...)
	}
	raw := []byte(body)
	capture.Contents[name] = raw
	capture.Snapshot.Entries = append(capture.Snapshot.Entries, c.Entry{Path: name, Hash: c.HashBytes(raw), Size: int64(len(raw)), Mode: 0600})
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		found := false
		for _, current := range capture.Snapshot.Directories {
			found = found || current == parent
		}
		if !found {
			capture.Snapshot.Directories = append(capture.Snapshot.Directories, parent)
		}
	}
	sort.Slice(capture.Snapshot.Entries, func(i, j int) bool { return capture.Snapshot.Entries[i].Path < capture.Snapshot.Entries[j].Path })
	sort.Strings(capture.Snapshot.Directories)
	capture.Snapshot.Digest = ""
	var err error
	capture.Snapshot.Digest, err = c.Digest(capture.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.VerifyCapture(capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

func TestDiscoveredFilesMustBeCoveredByExplicitTestClosure(t *testing.T) {
	capture := discoveryFixtureCapture(t)
	report, err := discovery.Discover(capture)
	if err != nil {
		t.Fatal(err)
	}
	plan := CheckPlan{SchemaVersion: 1, Checks: []CheckDefinition{{ID: "tests", Kind: "TEST", RequirementIDs: []string{"goal"}, Argv: []string{"operator-runner"}, RunnerDigest: c.HashBytes([]byte("runner")), Selection: "all", ExpectedTests: []string{"registered-test"}, Closure: []CheckScope{{Path: "tests/a.test.js"}}}}}
	if err := RequireDiscoveredClosure(plan, report, capture); err == nil {
		t.Fatal("test check with omitted manifest and helper was admitted")
	}
	missing := DiscoveredClosureMissing(plan.Checks[0], report)
	if len(missing) != 2 {
		t.Fatalf("missing source dependencies not reviewable: %v", missing)
	}
	plan.Checks[0].Closure = []CheckScope{{Path: ".", Recursive: true}}
	if err := RequireDiscoveredClosure(plan, report, capture); err != nil {
		t.Fatal("explicit conservative root closure rejected:", err)
	}
	if report.Coverage.Overall != "PARTIAL_SOURCE_HINTS_ONLY" {
		t.Fatal("closure admission changed discovery coverage")
	}
}

func TestCheckOriginBindsDiscoveryAndRejectsNewUnscopedTestConfig(t *testing.T) {
	base := discoveryFixtureCapture(t)
	plan := CheckPlan{SchemaVersion: 1, Checks: []CheckDefinition{{ID: "tests", Kind: "TEST", RequirementIDs: []string{"goal"}, Argv: []string{"operator-runner"}, RunnerDigest: c.HashBytes([]byte("runner")), Selection: "all", ExpectedTests: []string{"registered-test"}, Closure: []CheckScope{{Path: "tests", Recursive: true}, {Path: "package.json"}}}}}
	origin, err := BuildCheckOrigin("task", base, c.HashBytes([]byte("intent")), plan)
	if err != nil {
		t.Fatal("automatic captured closure admission failed:", err)
	}
	if !c.ValidDigest(origin.DiscoveryDigest) || origin.DiscoveryCoverage != "PARTIAL_SOURCE_HINTS_ONLY" {
		t.Fatal("origin did not bind honest source discovery:", origin.DiscoveryDigest, origin.DiscoveryCoverage)
	}
	current := captureWithAddedFile(t, base, "jest.config.js", "module.exports = { testPathIgnorePatterns: [] };\n")
	if err := GuardProtectedCandidate(origin, current); err == nil {
		t.Fatal("new test config outside the authorized closure was accepted")
	}
}
