package discovery

import (
	"path"
	"sort"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
)

func capturedFiles(t *testing.T, files map[string]string) workspace.Capture {
	t.Helper()
	paths := make([]string, 0, len(files))
	contents := make(map[string][]byte, len(files))
	directories := map[string]bool{}
	entries := make([]c.Entry, 0, len(files))
	for name, body := range files {
		paths = append(paths, name)
		contents[name] = []byte(body)
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			directories[parent] = true
		}
	}
	sort.Strings(paths)
	for _, name := range paths {
		body := contents[name]
		entries = append(entries, c.Entry{Path: name, Hash: c.HashBytes(body), Size: int64(len(body)), Mode: 0600})
	}
	dirs := make([]string, 0, len(directories))
	for name := range directories {
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	snapshot := c.Snapshot{SchemaVersion: c.SchemaVersion, Root: "captured-fixture", Consistency: "BEST_EFFORT", Entries: entries, Exclusions: []string{}, Directories: dirs}
	var err error
	snapshot.Digest, err = c.Digest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	capture := workspace.Capture{Snapshot: snapshot, Contents: contents}
	if err := workspace.VerifyCapture(capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

func TestCapturedDiscoveryBuildsPartialFrameworkAndCheckGraph(t *testing.T) {
	files := map[string]string{
		"package.json":       `{"scripts":{"test":"node -e 'process.exit(1)'"},"devDependencies":{"vitest":"^2.1.0"}}`,
		"vitest.config.ts":   `import "./test-setup"; process.exit(1);`,
		"test-setup.ts":      `export const setup = true;`,
		"tsconfig.json":      `{"extends":"./tsconfig.base.json","compilerOptions":{"paths":{"@app/*":["src/*"]}}}`,
		"tsconfig.base.json": `{}`,
		"tests/sample.test.ts": `// import "./not-real"
const fake = /import\s+['"]also-not-real/;
import { subject } from "../src/subject";
import { helper } from "./helper";
import { describe } from "vitest";`,
		"tests/helper.ts":       `export const helper = true;`,
		"src/subject.ts":        `export const subject = true;`,
		"requirements-dev.txt":  "-r requirements/base.txt\n",
		"requirements/base.txt": "pytest==8.3.1\n",
		"pyproject.toml":        "[project]\ndependencies = [\n  \"pytest>=7\",\n]\n",
		"tests/__init__.py":     "",
		"tests/test_api.py":     "\"\"\"\nimport forged_from_docstring\n\"\"\"\nfrom . import helper\nimport pytest\n",
		"tests/helper.py":       "VALUE = 1\n",
	}
	capture := capturedFiles(t, files)
	report, err := Discover(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(report, capture); err != nil {
		t.Fatal("fresh report rejected:", err)
	}
	if report.StrongVerification || report.Coverage.Overall != "PARTIAL_SOURCE_HINTS_ONLY" {
		t.Fatalf("discovery overstated authority: %+v", report.Coverage)
	}
	frameworks := map[string]Framework{}
	for _, framework := range report.Frameworks {
		frameworks[framework.ID] = framework
	}
	if frameworks["vitest"].ID == "" || len(frameworks["vitest"].Declarations) == 0 || frameworks["pytest"].ID == "" {
		t.Fatalf("framework/version declaration hints missing: %+v", report.Frameworks)
	}
	if frameworks["vitest"].Declarations[0].Value != "^2.1.0" {
		t.Fatalf("expected declared range, not resolved version: %+v", frameworks["vitest"])
	}
	edges := map[string]Edge{}
	for _, edge := range report.Graph {
		edges[edge.FromPath+"->"+edge.Target] = edge
	}
	if got := edges["tests/sample.test.ts->tests/helper.ts"]; got.Resolution != "RESOLVED_CAPTURED_TEST_SUPPORT" || !got.Protected {
		t.Fatalf("test helper edge not captured/protected: %+v", got)
	}
	if got := edges["tests/sample.test.ts->src/subject.ts"]; got.Resolution != "RESOLVED_CAPTURED_SUBJECT_SOURCE" || got.Protected {
		t.Fatalf("production source was misclassified as protected oracle: %+v", got)
	}
	if got := edges["vitest.config.ts->test-setup.ts"]; got.Resolution != "RESOLVED_CAPTURED_TEST_SUPPORT" || !got.Protected {
		t.Fatalf("config support edge missing: %+v", got)
	}
	if _, ok := edges["tests/sample.test.ts->./not-real"]; ok {
		t.Fatal("comment text became an import edge")
	}
	if _, ok := edges["tests/test_api.py->forged_from_docstring"]; ok {
		t.Fatal("Python docstring became an import edge")
	}
	if _, ok := edges["tsconfig.json->tsconfig.base.json"]; !ok {
		t.Fatalf("captured tsconfig extends edge missing: %+v", report.Graph)
	}
	if got := edges["requirements-dev.txt->requirements/base.txt"]; got.Resolution != "RESOLVED_CAPTURED_CONFIG" || !got.Protected {
		t.Fatalf("captured requirements include edge missing: %+v", got)
	}
	if !strings.Contains(strings.Join(report.Coverage.Unknowns, "|"), "TS_CONFIG_REFERENCE_NOT_RESOLVED") {
		t.Fatal("unresolved TS path aliases were silently treated as complete")
	}
	if !contains(report.ProtectedFiles, "package.json") || !contains(report.ProtectedFiles, "requirements-dev.txt") || !contains(report.ProtectedFiles, "requirements/base.txt") || !contains(report.ProtectedFiles, "tests/helper.ts") || !contains(report.ProtectedFiles, "tests/helper.py") || contains(report.ProtectedFiles, "src/subject.ts") {
		t.Fatalf("suggested protected closure has wrong source roles: %v", report.ProtectedFiles)
	}
	if !strings.Contains(strings.Join(report.Coverage.Unknowns, "|"), "PARTIAL_PYTHON_IMPORT_SYNTAX:tests/test_api.py") {
		t.Fatal("relative from-import member ambiguity was not reported")
	}
}

func TestDiscoveryValidationRejectsForgedAndStaleReports(t *testing.T) {
	files := map[string]string{"package.json": `{"devDependencies":{"jest":"29.0.0"}}`, "tests/a.test.js": `import "./helper.js";`, "tests/helper.js": `export const x = 1;`}
	capture := capturedFiles(t, files)
	report, err := Discover(capture)
	if err != nil {
		t.Fatal(err)
	}
	forged := report
	forged.Coverage.Overall = "COMPLETE"
	forged.ReportDigest = ""
	forged.ReportDigest, err = c.Digest(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(forged, capture); err == nil {
		t.Fatal("rehashable forged coverage accepted")
	}

	files["tests/helper.js"] = `export const x = 2;`
	current := capturedFiles(t, files)
	if err := Validate(report, current); err == nil {
		t.Fatal("stale dependency graph accepted for a changed capture")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
