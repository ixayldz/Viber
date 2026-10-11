// Package discovery extracts bounded, source-capture-only framework and
// check-dependency hints. It never runs repository code and never grants
// verification authority.
package discovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

const (
	SchemaVersion = 1
	Extractor     = "CAPTURED_SOURCE_DISCOVERY_V1"
	maxFiles      = 10000
	maxBytes      = 32 << 20
	maxSources    = 4096
	maxEdges      = 16384
	maxTokens     = 65536
)

type Evidence struct {
	Path             string `json:"path"`
	SourceDigest     string `json:"source_digest"`
	ExtractorVersion string `json:"extractor_version"`
}

type VersionDeclaration struct {
	Value          string   `json:"declared_version"`
	Origin         Evidence `json:"origin"`
	DependencyKind string   `json:"dependency_kind"`
}

type Framework struct {
	ID           string               `json:"id"`
	Ecosystem    string               `json:"ecosystem"`
	Evidence     []Evidence           `json:"evidence"`
	Declarations []VersionDeclaration `json:"version_declarations"`
	Finding      string               `json:"finding"`
}

type Dependency struct {
	Name            string   `json:"name"`
	Ecosystem       string   `json:"ecosystem"`
	DeclaredVersion string   `json:"declared_version"`
	VersionOrigin   Evidence `json:"version_origin"`
	DependencyKind  string   `json:"dependency_kind"`
	Resolution      string   `json:"resolution"`
}

type SourceFile struct {
	Path             string   `json:"path"`
	SourceDigest     string   `json:"source_digest"`
	Kind             string   `json:"kind"`
	Language         string   `json:"language"`
	ExtractorVersion string   `json:"extractor_version"`
	Origin           Evidence `json:"origin"`
}

type ClosureFile struct {
	Path             string   `json:"path"`
	SourceDigest     string   `json:"source_digest"`
	Role             string   `json:"role"`
	Origin           Evidence `json:"origin"`
	ExtractorVersion string   `json:"extractor_version"`
}

type Edge struct {
	FromPath     string `json:"from_path"`
	FromDigest   string `json:"from_digest"`
	Target       string `json:"target"`
	TargetDigest string `json:"target_digest,omitempty"`
	Relation     string `json:"relation"`
	Resolution   string `json:"resolution"`
	Protected    bool   `json:"protected_check_input"`
}

type Coverage struct {
	Overall             string   `json:"overall"`
	Frameworks          string   `json:"frameworks"`
	Tests               string   `json:"tests"`
	Configs             string   `json:"configs"`
	Dependencies        string   `json:"dependencies"`
	CrossFileResolution string   `json:"cross_file_resolution"`
	Unknowns            []string `json:"unknowns"`
}

// Report is a digest-bound description of visible source declarations. A
// consumer must revalidate it against a current Capture before using paths.
// Even a fresh report is not a complete runtime closure or a trusted observer.
type Report struct {
	SchemaVersion      int           `json:"schema_version"`
	Extractor          string        `json:"extractor"`
	SnapshotDigest     string        `json:"snapshot_digest"`
	CaptureConsistency string        `json:"capture_consistency"`
	Freshness          string        `json:"freshness_contract"`
	StrongVerification bool          `json:"strong_verification_available"`
	Frameworks         []Framework   `json:"frameworks"`
	Tests              []SourceFile  `json:"tests"`
	Configs            []SourceFile  `json:"configs"`
	Dependencies       []Dependency  `json:"dependencies"`
	Closure            []ClosureFile `json:"dependency_closure"`
	Graph              []Edge        `json:"graph"`
	ProtectedFiles     []string      `json:"suggested_protected_files"`
	Coverage           Coverage      `json:"coverage"`
	ReportDigest       string        `json:"report_digest"`
}

type source struct {
	path string
	raw  []byte
	hash string
}

type manifest struct {
	path       string
	root       string
	deps       []Dependency
	byName     map[string]Dependency
	workspaces bool
}

type packageJSON struct {
	Name                 string            `json:"name"`
	Workspaces           json.RawMessage   `json:"workspaces"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

var requirementRE = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[A-Za-z0-9_,.-]+\])?\s*(?:((?:===|==|~=|!=|<=|>=|<|>))\s*([^;\s]+))?\s*(?:;.*)?$`)
var pyProjectRequirementRE = regexp.MustCompile(`^[\s"']*([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[A-Za-z0-9_,.-]+\])?\s*((?:===|==|~=|!=|<=|>=|<|>|\^)\s*[^;\s"']+)?`)

var jsExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".json"}
var jsFrameworks = map[string]string{
	"vitest": "vitest", "jest": "jest", "@jest/globals": "jest", "mocha": "mocha",
	"ava": "ava", "@playwright/test": "playwright", "playwright": "playwright",
	"cypress": "cypress", "tap": "tap", "uvu": "uvu", "jasmine": "jasmine",
}
var pyFrameworks = map[string]string{"pytest": "pytest", "nose": "nose", "nose2": "nose2", "unittest": "unittest"}

func normalizePackage(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "_", "-"), ".", "-"))
}

func evidence(s source, extractor string) Evidence {
	return Evidence{Path: s.path, SourceDigest: s.hash, ExtractorVersion: extractor}
}

func sourceLanguage(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py":
		return "python"
	default:
		return ""
	}
}

func isJS(name string) bool {
	lang := sourceLanguage(name)
	return lang == "javascript" || lang == "typescript"
}

func testLike(name string) bool {
	base := strings.ToLower(path.Base(name))
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	for _, part := range strings.Split(strings.ToLower(path.Dir(name)), "/") {
		if part == "test" || part == "tests" || part == "__tests__" {
			return true
		}
	}
	return false
}

func configKind(name string) string {
	base := strings.ToLower(path.Base(name))
	if base == "tsconfig.json" || base == "jsconfig.json" {
		return "JS_TEST_OR_TOOL_CONFIG"
	}
	if strings.HasPrefix(base, ".mocharc.") || strings.HasPrefix(base, ".babelrc") {
		return "JS_TEST_OR_TOOL_CONFIG"
	}
	if isJS(name) {
		for _, prefix := range []string{"vitest.config.", "jest.config.", "playwright.config.", "cypress.config.", "karma.conf.", "babel.config.", "webpack.config."} {
			if strings.HasPrefix(base, prefix) {
				return "JS_TEST_OR_TOOL_CONFIG"
			}
		}
	}
	if sourceLanguage(name) == "python" {
		switch base {
		case "conftest.py", "noxfile.py", "setup.py":
			return "PYTHON_TEST_OR_TOOL_CONFIG"
		}
	}
	switch base {
	case "pytest.ini", "tox.ini", "setup.cfg", "pyproject.toml":
		return "PYTHON_TEST_OR_TOOL_CONFIG"
	}
	return ""
}

func dependencyManifest(name string) bool {
	base := strings.ToLower(path.Base(name))
	if base == "package.json" || base == "requirements.txt" || base == "requirements-dev.txt" || base == "requirements-test.txt" || base == "requirements-test.in" || base == "requirements.in" || base == "pyproject.toml" || base == "setup.cfg" {
		return true
	}
	return false
}

func lockfile(name string) bool {
	base := strings.ToLower(path.Base(name))
	switch base {
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "poetry.lock", "pdm.lock", "uv.lock", "pipfile.lock":
		return true
	}
	return false
}

func sortedUnique(values []string) []string {
	sort.Strings(values)
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func addUnknown(report *Report, message string) {
	if message == "" {
		return
	}
	report.Coverage.Unknowns = append(report.Coverage.Unknowns, message)
}

func hasUnknown(report *Report, message string) bool {
	for _, value := range report.Coverage.Unknowns {
		if value == message {
			return true
		}
	}
	return false
}

func addFramework(report *Report, id, ecosystem, finding string, ev Evidence, declaration *VersionDeclaration) {
	for i := range report.Frameworks {
		f := &report.Frameworks[i]
		if f.ID != id || f.Ecosystem != ecosystem {
			continue
		}
		f.Evidence = append(f.Evidence, ev)
		if declaration != nil {
			f.Declarations = append(f.Declarations, *declaration)
		}
		if finding == "IMPORT_HINT_ONLY" && f.Finding != "DECLARATION_HINT" {
			f.Finding = finding
		}
		return
	}
	f := Framework{ID: id, Ecosystem: ecosystem, Evidence: []Evidence{ev}, Declarations: []VersionDeclaration{}, Finding: finding}
	if declaration != nil {
		f.Declarations = append(f.Declarations, *declaration)
	}
	report.Frameworks = append(report.Frameworks, f)
}

func addDependency(report *Report, dep Dependency) {
	for _, old := range report.Dependencies {
		if old.Name == dep.Name && old.Ecosystem == dep.Ecosystem && old.VersionOrigin.Path == dep.VersionOrigin.Path {
			return
		}
	}
	report.Dependencies = append(report.Dependencies, dep)
}

func addClosure(report *Report, s source, role, originPath, extractor string) {
	for _, old := range report.Closure {
		if old.Path == s.path && old.Role == role {
			return
		}
	}
	origin := evidence(s, extractor)
	if originPath != "" {
		for _, item := range report.Closure {
			if item.Path == originPath {
				origin = item.Origin
				break
			}
		}
	}
	report.Closure = append(report.Closure, ClosureFile{Path: s.path, SourceDigest: s.hash, Role: role, Origin: origin, ExtractorVersion: extractor})
	if role != "SUBJECT_SOURCE_DEPENDENCY" {
		report.ProtectedFiles = append(report.ProtectedFiles, s.path)
	}
}

func AddDiscoveredClosureFiles(report *Report) []string {
	if report == nil {
		return nil
	}
	return append([]string(nil), report.ProtectedFiles...)
}

// Discover analyzes only exact bytes already present in capture. It does not
// read paths, launch framework commands, parse executable configuration, or
// execute compiler/generator/LSP code.
func Discover(capture workspace.Capture) (Report, error) {
	if err := workspace.VerifyCapture(capture); err != nil {
		return Report{}, err
	}
	if len(capture.Snapshot.Entries) > maxFiles {
		return Report{}, c.Fail(c.BudgetLimitReached, "source discovery file quota exceeded")
	}
	var total int64
	byPath := make(map[string]source, len(capture.Snapshot.Entries))
	for _, entry := range capture.Snapshot.Entries {
		total += entry.Size
		if total > maxBytes {
			return Report{}, c.Fail(c.BudgetLimitReached, "source discovery byte quota exceeded")
		}
		raw := capture.Contents[entry.Path]
		byPath[entry.Path] = source{path: entry.Path, raw: raw, hash: entry.Hash}
	}
	paths := make([]string, 0, len(byPath))
	for name := range byPath {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	report := Report{
		SchemaVersion: SchemaVersion, Extractor: Extractor,
		SnapshotDigest: capture.Snapshot.Digest, CaptureConsistency: capture.Snapshot.Consistency,
		Freshness: "REVALIDATE_CAPTURE_DIGEST_BEFORE_USE", StrongVerification: false,
		Frameworks: []Framework{}, Tests: []SourceFile{}, Configs: []SourceFile{}, Dependencies: []Dependency{},
		Closure: []ClosureFile{}, Graph: []Edge{}, ProtectedFiles: []string{},
		Coverage: Coverage{Overall: "PARTIAL_SOURCE_HINTS_ONLY", Frameworks: "DECLARATION_AND_FILENAME_HINTS", Tests: "FILENAME_AND_PATH_HINTS_ONLY", Configs: "KNOWN_FILENAMES_ONLY_CONTENT_NOT_EXECUTED", Dependencies: "DECLARED_DIRECT_DEPENDENCIES_ONLY", CrossFileResolution: "STATIC_RESOLVED_CAPTURED_FILES_ONLY", Unknowns: []string{}},
	}
	manifestByPath := map[string]manifest{}
	for _, name := range paths {
		s := byPath[name]
		base := strings.ToLower(path.Base(name))
		if base == "package.json" {
			m, err := parsePackageManifest(s)
			if err != nil {
				addUnknown(&report, "UNPARSEABLE_PACKAGE_MANIFEST:"+name)
				continue
			}
			manifestByPath[name] = m
			if m.workspaces {
				addUnknown(&report, "PACKAGE_WORKSPACES_NOT_RESOLVED:"+name)
			}
			for _, dep := range m.deps {
				addDependency(&report, dep)
				if framework, ok := jsFrameworks[dep.Name]; ok {
					v := VersionDeclaration{Value: dep.DeclaredVersion, Origin: dep.VersionOrigin, DependencyKind: dep.DependencyKind}
					addFramework(&report, framework, "javascript", "DECLARATION_HINT", dep.VersionOrigin, &v)
				}
			}
		} else if isRequirementsFile(name) {
			m, err := parseRequirementsManifest(s, byPath, &report, 0, map[string]bool{})
			if err != nil {
				addUnknown(&report, "PARTIALLY_PARSED_PYTHON_REQUIREMENTS:"+name)
			}
			manifestByPath[name] = m
			for _, dep := range m.deps {
				addDependency(&report, dep)
				if framework, ok := pyFrameworks[dep.Name]; ok {
					v := VersionDeclaration{Value: dep.DeclaredVersion, Origin: dep.VersionOrigin, DependencyKind: dep.DependencyKind}
					addFramework(&report, framework, "python", "DECLARATION_HINT", dep.VersionOrigin, &v)
				}
			}
		} else if strings.EqualFold(path.Base(name), "pyproject.toml") || strings.EqualFold(path.Base(name), "setup.cfg") {
			m := parsePythonDependencyConfig(s, &report)
			manifestByPath[name] = m
			for _, dep := range m.deps {
				addDependency(&report, dep)
				if framework, ok := pyFrameworks[dep.Name]; ok {
					v := VersionDeclaration{Value: dep.DeclaredVersion, Origin: dep.VersionOrigin, DependencyKind: dep.DependencyKind}
					addFramework(&report, framework, "python", "DECLARATION_HINT", dep.VersionOrigin, &v)
				}
			}
		}
		if lockfile(name) {
			addClosure(&report, s, "LOCKFILE_DECLARATION_NOT_RESOLVED", "", Extractor)
			addUnknown(&report, "LOCKFILE_NOT_RESOLVED:"+name)
		}
	}
	for _, name := range paths {
		s := byPath[name]
		if dependencyManifest(name) {
			addClosure(&report, s, "DEPENDENCY_MANIFEST", "", Extractor)
		}
		if cfg := configKind(name); cfg != "" {
			lang := sourceLanguage(name)
			extractor := extractorFor(lang)
			if lang == "" {
				extractor = "CONFIG_FILENAME_V1"
				lang = configEcosystem(name)
			}
			f := SourceFile{Path: name, SourceDigest: s.hash, Kind: cfg, Language: lang, ExtractorVersion: extractor, Origin: evidence(s, extractor)}
			report.Configs = append(report.Configs, f)
			addClosure(&report, s, "CONFIG_SOURCE", "", extractor)
			for _, framework := range frameworksFromConfig(name) {
				addFramework(&report, framework, langEcosystem(lang), "CONFIG_FILENAME_HINT_ONLY", evidence(s, extractor), nil)
			}
		}
		lang := sourceLanguage(name)
		if lang == "" || !utf8.Valid(s.raw) {
			if lang != "" {
				addUnknown(&report, "NON_UTF8_SOURCE:"+name)
			}
			continue
		}
		if len(s.raw) > 2<<20 {
			addUnknown(&report, "SOURCE_TOO_LARGE_FOR_DISCOVERY:"+name)
			continue
		}
		kind := ""
		if testLike(name) {
			kind = "TEST_SOURCE_HINT"
			f := SourceFile{Path: name, SourceDigest: s.hash, Kind: kind, Language: lang, ExtractorVersion: extractorFor(lang), Origin: evidence(s, extractorFor(lang))}
			report.Tests = append(report.Tests, f)
			addClosure(&report, s, "TEST_SOURCE", "", extractorFor(lang))
		}
	}
	if len(report.Tests) == 0 {
		addUnknown(&report, "NO_TEST_FILES_MATCHED_CONSERVATIVE_FILENAME_HINTS")
	}
	if len(report.Frameworks) == 0 {
		addUnknown(&report, "NO_FRAMEWORK_DECLARATION_OR_KNOWN_CONFIG_HINT")
	}
	if err := discoverGraph(&report, byPath, paths, manifestByPath); err != nil {
		return Report{}, err
	}
	if len(report.Graph) > maxEdges || len(report.Closure) > maxSources {
		return Report{}, c.Fail(c.BudgetLimitReached, "source dependency closure quota exceeded")
	}
	report.Coverage.Unknowns = sortedUnique(report.Coverage.Unknowns)
	report.ProtectedFiles = sortedUnique(report.ProtectedFiles)
	sort.Slice(report.Tests, func(i, j int) bool { return report.Tests[i].Path < report.Tests[j].Path })
	sort.Slice(report.Configs, func(i, j int) bool { return report.Configs[i].Path < report.Configs[j].Path })
	sort.Slice(report.Frameworks, func(i, j int) bool {
		if report.Frameworks[i].Ecosystem != report.Frameworks[j].Ecosystem {
			return report.Frameworks[i].Ecosystem < report.Frameworks[j].Ecosystem
		}
		return report.Frameworks[i].ID < report.Frameworks[j].ID
	})
	sort.Slice(report.Dependencies, func(i, j int) bool {
		if report.Dependencies[i].Ecosystem != report.Dependencies[j].Ecosystem {
			return report.Dependencies[i].Ecosystem < report.Dependencies[j].Ecosystem
		}
		if report.Dependencies[i].Name != report.Dependencies[j].Name {
			return report.Dependencies[i].Name < report.Dependencies[j].Name
		}
		return report.Dependencies[i].VersionOrigin.Path < report.Dependencies[j].VersionOrigin.Path
	})
	sort.Slice(report.Closure, func(i, j int) bool {
		if report.Closure[i].Path != report.Closure[j].Path {
			return report.Closure[i].Path < report.Closure[j].Path
		}
		return report.Closure[i].Role < report.Closure[j].Role
	})
	sort.Slice(report.Graph, func(i, j int) bool {
		a, b := report.Graph[i], report.Graph[j]
		if a.FromPath != b.FromPath {
			return a.FromPath < b.FromPath
		}
		if a.Relation != b.Relation {
			return a.Relation < b.Relation
		}
		return a.Target < b.Target
	})
	unsigned := report
	unsigned.ReportDigest = ""
	digest, err := c.Digest(unsigned)
	if err != nil {
		return Report{}, err
	}
	report.ReportDigest = digest
	return report, nil
}

func extractorFor(language string) string {
	switch language {
	case "python":
		return "PYTHON_LEXICAL_IMPORTS_V1"
	case "javascript", "typescript":
		return "JS_TS_LEXICAL_IMPORTS_V1"
	default:
		return "UNSUPPORTED"
	}
}

func langEcosystem(language string) string {
	if language == "python" {
		return "python"
	}
	return "javascript"
}

func configEcosystem(name string) string {
	base := strings.ToLower(path.Base(name))
	if base == "pyproject.toml" || base == "setup.cfg" || base == "pytest.ini" || base == "tox.ini" || strings.HasSuffix(base, ".py") {
		return "python"
	}
	return "javascript"
}

func frameworksFromConfig(name string) []string {
	base := strings.ToLower(path.Base(name))
	if strings.HasPrefix(base, "vitest.config.") {
		return []string{"vitest"}
	}
	if strings.HasPrefix(base, "jest.config.") {
		return []string{"jest"}
	}
	if strings.HasPrefix(base, "playwright.config.") {
		return []string{"playwright"}
	}
	if strings.HasPrefix(base, "cypress.config.") {
		return []string{"cypress"}
	}
	if strings.HasPrefix(base, ".mocharc.") {
		return []string{"mocha"}
	}
	if strings.HasPrefix(base, "pytest.ini") || base == "tox.ini" || base == "conftest.py" {
		return []string{"pytest"}
	}
	return nil
}

func isRequirementsFile(name string) bool {
	base := strings.ToLower(path.Base(name))
	return strings.HasPrefix(base, "requirements") && (strings.HasSuffix(base, ".txt") || strings.HasSuffix(base, ".in"))
}

func isPythonManifest(name string) bool {
	base := strings.ToLower(path.Base(name))
	return isRequirementsFile(name) || base == "pyproject.toml" || base == "setup.cfg"
}

func parsePythonDependencyConfig(s source, report *Report) manifest {
	out := manifest{path: s.path, root: path.Dir(s.path), byName: map[string]Dependency{}}
	if len(s.raw) > 1<<20 || !utf8.Valid(s.raw) {
		addUnknown(report, "PYTHON_DEPENDENCY_CONFIG_UNREADABLE:"+s.path)
		return out
	}
	section := ""
	arrayMode := false
	var arrayValue strings.Builder
	add := func(spec, kind string) {
		match := pyProjectRequirementRE.FindStringSubmatch(strings.TrimSpace(spec))
		if match == nil {
			return
		}
		name := normalizePackage(match[1])
		if name == "" {
			return
		}
		decl := strings.TrimSpace(match[2])
		dep := Dependency{Name: name, Ecosystem: "python", DeclaredVersion: decl, VersionOrigin: evidence(s, "PYTHON_CONFIG_LEXICAL_V1"), DependencyKind: kind, Resolution: "CONFIG_DECLARATION_NOT_INSTALLED_VERSION"}
		out.deps = append(out.deps, dep)
		out.byName[name] = dep
	}
	consumeArray := func(value, kind string) {
		for _, match := range regexp.MustCompile(`["']([^"']{1,512})["']`).FindAllStringSubmatch(value, 128) {
			add(match[1], kind)
		}
	}
	lines := strings.Split(string(s.raw), "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if arrayMode {
				consumeArray(arrayValue.String(), "project.dependencies")
				arrayValue.Reset()
				arrayMode = false
			}
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if strings.EqualFold(path.Base(s.path), "pyproject.toml") {
			if arrayMode {
				arrayValue.WriteString(" ")
				arrayValue.WriteString(line)
				if strings.Contains(line, "]") {
					consumeArray(arrayValue.String(), "project.dependencies")
					arrayValue.Reset()
					arrayMode = false
				}
				continue
			}
			if section == "project" || strings.HasPrefix(section, "project.optional-dependencies") {
				left, right, ok := strings.Cut(line, "=")
				if ok && strings.TrimSpace(left) == "dependencies" || ok && section != "project" && strings.TrimSpace(left) != "" {
					right = strings.TrimSpace(right)
					if strings.HasPrefix(right, "[") {
						arrayValue.WriteString(right)
						if strings.Contains(right, "]") {
							consumeArray(arrayValue.String(), "project.dependencies")
							arrayValue.Reset()
						} else {
							arrayMode = true
						}
					} else if left != "" && strings.TrimSpace(left) == "dependencies" {
						add(strings.Trim(right, " \"'"), "project.dependencies")
					}
				}
			}
			if section == "tool.poetry.dependencies" {
				if left, right, ok := strings.Cut(line, "="); ok {
					name := strings.TrimSpace(left)
					if strings.Contains(name, " ") || name == "python" {
						continue
					}
					version := strings.Trim(strings.TrimSpace(right), "\"'")
					if version != "" && !strings.HasPrefix(version, "{") {
						add(name+" "+version, "tool.poetry.dependencies")
					}
				}
			}
		} else {
			if section == "options" || section == "options.extras_require" || section == "options.tests_require" {
				if strings.HasPrefix(line, "install_requires") || strings.HasPrefix(line, "tests_require") {
					continue
				}
				if strings.HasPrefix(line, "[") {
					continue
				}
				add(line, "setup.cfg")
			}
		}
	}
	if arrayMode {
		addUnknown(report, "UNTERMINATED_PYPROJECT_DEPENDENCY_ARRAY:"+s.path)
	}
	return out
}

func parsePackageManifest(s source) (manifest, error) {
	var out manifest
	out.path, out.root, out.byName = s.path, path.Dir(s.path), map[string]Dependency{}
	if len(s.raw) > 1<<20 || !utf8.Valid(s.raw) || duplicateJSONKeys(s.raw) != nil {
		return out, fmt.Errorf("invalid bounded package manifest")
	}
	var data packageJSON
	if err := json.Unmarshal(s.raw, &data); err != nil {
		return out, err
	}
	out.workspaces = len(data.Workspaces) != 0
	groups := []struct {
		name   string
		values map[string]string
	}{{"dependencies", data.Dependencies}, {"devDependencies", data.DevDependencies}, {"optionalDependencies", data.OptionalDependencies}, {"peerDependencies", data.PeerDependencies}}
	for _, group := range groups {
		keys := make([]string, 0, len(group.values))
		for name := range group.values {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			version := group.values[name]
			if name == "" || len(name) > 256 || len(version) > 512 || strings.ContainsAny(name+version, "\x00\r\n") {
				return out, fmt.Errorf("invalid dependency declaration")
			}
			dep := Dependency{Name: name, Ecosystem: "javascript", DeclaredVersion: version, VersionOrigin: evidence(s, "PACKAGE_JSON_V1"), DependencyKind: group.name, Resolution: "MANIFEST_DECLARATION_NOT_INSTALLED_VERSION"}
			out.deps = append(out.deps, dep)
			out.byName[name] = dep
		}
	}
	return out, nil
}

// duplicateJSONKeys conservatively rejects ambiguous JSON objects without
// imposing a repository-specific package.json schema.
func duplicateJSONKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("JSON depth quota")
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate or invalid JSON key")
				}
				seen[key] = true
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("invalid JSON object")
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("invalid JSON array")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err == nil {
		return fmt.Errorf("trailing JSON content")
	}
	return nil
}

func parseRequirementsManifest(s source, all map[string]source, report *Report, depth int, visited map[string]bool) (manifest, error) {
	out := manifest{path: s.path, root: path.Dir(s.path), byName: map[string]Dependency{}}
	if depth > 8 || len(s.raw) > 1<<20 || !utf8.Valid(s.raw) || visited[s.path] {
		return out, fmt.Errorf("requirements include depth, size, encoding, or cycle")
	}
	visited[s.path] = true
	defer delete(visited, s.path)
	addClosure(report, s, "DEPENDENCY_MANIFEST", "", "REQUIREMENTS_V1")
	for _, rawLine := range bytes.Split(s.raw, []byte{'\n'}) {
		line := strings.TrimSpace(string(rawLine))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "-r ") || strings.HasPrefix(line, "--requirement ") || strings.HasPrefix(line, "-c ") || strings.HasPrefix(line, "--constraint ") {
			parts := strings.Fields(line)
			if len(parts) != 2 || strings.HasPrefix(parts[1], "-") {
				return out, fmt.Errorf("unsupported requirements include")
			}
			rel := path.Clean(path.Join(path.Dir(s.path), parts[1]))
			if !policy.SafePath(rel) {
				return out, fmt.Errorf("unsafe requirements include")
			}
			included, ok := all[rel]
			if !ok {
				addUnknown(report, "UNRESOLVED_REQUIREMENTS_INCLUDE:"+rel)
				relation := "PYTHON_REQUIREMENTS_INCLUDE"
				if strings.HasPrefix(line, "-c ") || strings.HasPrefix(line, "--constraint ") {
					relation = "PYTHON_CONSTRAINT_INCLUDE"
				}
				addEdge(report, Edge{FromPath: s.path, FromDigest: s.hash, Target: rel, Relation: relation, Resolution: "UNRESOLVED"})
				continue
			}
			addClosure(report, included, "REQUIREMENTS_INCLUDE", s.path, "REQUIREMENTS_V1")
			relation := "PYTHON_REQUIREMENTS_INCLUDE"
			if strings.HasPrefix(line, "-c ") || strings.HasPrefix(line, "--constraint ") {
				relation = "PYTHON_CONSTRAINT_INCLUDE"
			}
			addEdge(report, Edge{FromPath: s.path, FromDigest: s.hash, Target: rel, TargetDigest: included.hash, Relation: relation, Resolution: "RESOLVED_CAPTURED_CONFIG", Protected: true})
			child, err := parseRequirementsManifest(included, all, report, depth+1, visited)
			out.deps = append(out.deps, child.deps...)
			for name, dep := range child.byName {
				if _, exists := out.byName[name]; !exists {
					out.byName[name] = dep
				}
			}
			if err != nil {
				return out, err
			}
			continue
		}
		if strings.HasPrefix(line, "-") || strings.Contains(line, " @ ") || strings.Contains(line, "git+") || strings.Contains(line, "${") {
			addUnknown(report, "UNSUPPORTED_REQUIREMENT_SYNTAX:"+s.path)
			continue
		}
		if cut, _, ok := strings.Cut(line, " #"); ok {
			line = strings.TrimSpace(cut)
		}
		match := requirementRE.FindStringSubmatch(line)
		if match == nil {
			addUnknown(report, "UNPARSED_REQUIREMENT_LINE:"+s.path)
			continue
		}
		name := normalizePackage(match[1])
		decl := strings.TrimSpace(match[2] + match[3])
		dep := Dependency{Name: name, Ecosystem: "python", DeclaredVersion: decl, VersionOrigin: evidence(s, "REQUIREMENTS_V1"), DependencyKind: "requirements", Resolution: "MANIFEST_DECLARATION_NOT_INSTALLED_VERSION"}
		out.deps = append(out.deps, dep)
		out.byName[name] = dep
	}
	return out, nil
}

func nearestManifest(name string, manifests map[string]manifest, ecosystem string) manifest {
	best := manifest{byName: map[string]Dependency{}}
	bestLen := -1
	keys := make([]string, 0, len(manifests))
	for key := range manifests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		m := manifests[key]
		isMatch := ecosystem == "javascript" && path.Base(m.path) == "package.json" || ecosystem == "python" && isPythonManifest(m.path)
		if !isMatch {
			continue
		}
		root := m.root
		if root == "." {
			root = ""
		}
		if name == m.path || root != "" && !strings.HasPrefix(name, root+"/") {
			continue
		}
		if len(root) > bestLen {
			best = manifest{root: root, byName: map[string]Dependency{}}
			bestLen = len(root)
		}
		if len(root) == bestLen {
			for name, dep := range m.byName {
				if _, exists := best.byName[name]; !exists {
					best.byName[name] = dep
				}
			}
		}
	}
	return best
}

func addEdge(report *Report, edge Edge) {
	for _, existing := range report.Graph {
		if existing.FromPath == edge.FromPath && existing.Target == edge.Target && existing.Relation == edge.Relation {
			return
		}
	}
	if len(report.Graph) >= maxEdges {
		if !hasUnknown(report, "GRAPH_EDGE_QUOTA_EXCEEDED") {
			addUnknown(report, "GRAPH_EDGE_QUOTA_EXCEEDED")
		}
		return
	}
	report.Graph = append(report.Graph, edge)
	if edge.Resolution == "UNRESOLVED" || edge.Resolution == "AMBIGUOUS_LOCAL_IMPORT_ROLE" || edge.Resolution == "DYNAMIC_IMPORT_UNRESOLVED" {
		addUnknown(report, "UNRESOLVED_EDGE:"+edge.FromPath+":"+edge.Target)
	}
}

func discoverGraph(report *Report, sources map[string]source, paths []string, manifests map[string]manifest) error {
	seeds := map[string]string{}
	for _, f := range report.Tests {
		seeds[f.Path] = "TEST_SOURCE"
	}
	for _, f := range report.Configs {
		seeds[f.Path] = "CONFIG_SOURCE"
	}
	queue := make([]string, 0, len(seeds))
	roles := map[string]string{}
	for name := range seeds {
		queue = append(queue, name)
		roles[name] = seeds[name]
	}
	sort.Strings(queue)
	visited := map[string]bool{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		current, ok := sources[name]
		if !ok {
			continue
		}
		currentRole := roles[name]
		lang := sourceLanguage(name)
		if strings.EqualFold(path.Base(name), "tsconfig.json") || strings.EqualFold(path.Base(name), "jsconfig.json") {
			targets, unknown := tsConfigReferences(name, current.raw)
			if unknown {
				addUnknown(report, "TS_CONFIG_REFERENCE_NOT_RESOLVED:"+name)
			}
			for _, ref := range targets {
				if target, targetSource, found := resolveJSImport(name, ref, sources); found {
					addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: target, TargetDigest: targetSource.hash, Relation: "TS_CONFIG_EXTENDS_OR_REFERENCE", Resolution: "RESOLVED_CAPTURED_CONFIG", Protected: true})
					addClosure(report, targetSource, "CONFIG_REFERENCE", name, "TS_CONFIG_JSON_V1")
					queue = append(queue, target)
				} else {
					addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: ref, Relation: "TS_CONFIG_EXTENDS_OR_REFERENCE", Resolution: "UNRESOLVED", Protected: false})
				}
			}
		}
		if lang == "python" {
			for _, imp := range pythonImports(current.raw) {
				if imp.partial {
					addUnknown(report, "PARTIAL_PYTHON_IMPORT_SYNTAX:"+name)
				}
				if framework, ok := pyFrameworks[normalizePackage(imp.module)]; ok {
					dep := nearestManifest(name, manifests, "python").byName[normalizePackage(imp.module)]
					var declaration *VersionDeclaration
					if dep.Name != "" {
						declaration = &VersionDeclaration{Value: dep.DeclaredVersion, Origin: dep.VersionOrigin, DependencyKind: dep.DependencyKind}
					}
					addFramework(report, framework, "python", "IMPORT_HINT", evidence(current, extractorFor(lang)), declaration)
				}
				target, resolution, targetSource := resolvePythonRelative(name, imp.module, imp.level, sources)
				if imp.level == 0 {
					if _, stdlib := pythonStdlib[normalizePackage(imp.module)]; stdlib {
						addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: imp.module, Relation: "PYTHON_IMPORT", Resolution: "RUNTIME_STDLIB_NAME_ONLY"})
					} else if dep, ok := nearestManifest(name, manifests, "python").byName[normalizePackage(imp.module)]; ok {
						addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: dep.Name, Relation: "PYTHON_IMPORT", Resolution: "DECLARED_EXTERNAL_NOT_RESOLVED"})
					} else {
						addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: imp.module, Relation: "PYTHON_IMPORT", Resolution: "UNRESOLVED"})
					}
					continue
				}
				protected := false
				role := "SUBJECT_SOURCE_DEPENDENCY"
				if resolution == "RESOLVED_CAPTURED_FILE" && (currentRole == "CONFIG_SOURCE" || currentRole == "CONFIG_SUPPORT" || testLike(target)) {
					protected, role = true, "TEST_SUPPORT"
				}
				if targetSource.path != "" && !protected && resolution == "RESOLVED_CAPTURED_FILE" {
					resolution = "RESOLVED_CAPTURED_SUBJECT_SOURCE"
				}
				edge := Edge{FromPath: name, FromDigest: current.hash, Target: target, Relation: "PYTHON_RELATIVE_IMPORT", Resolution: resolution, Protected: protected}
				if targetSource.path != "" {
					edge.TargetDigest = targetSource.hash
				}
				addEdge(report, edge)
				if protected {
					addClosure(report, targetSource, role, name, extractorFor(lang))
					roles[target] = "CONFIG_SUPPORT"
					queue = append(queue, target)
				}
			}
		} else if isJS(name) {
			imports, incomplete := javascriptImports(current.raw)
			if incomplete {
				addUnknown(report, "UNSUPPORTED_OR_DYNAMIC_IMPORT_SYNTAX:"+name)
			}
			for _, imp := range imports {
				frameworkName := imp.specifier
				if framework, ok := jsFrameworks[frameworkName]; ok {
					dep := nearestManifest(name, manifests, "javascript").byName[frameworkName]
					var declaration *VersionDeclaration
					if dep.Name != "" {
						declaration = &VersionDeclaration{Value: dep.DeclaredVersion, Origin: dep.VersionOrigin, DependencyKind: dep.DependencyKind}
					}
					addFramework(report, framework, "javascript", "IMPORT_HINT", evidence(current, extractorFor(lang)), declaration)
				}
				if strings.HasPrefix(imp.specifier, ".") {
					target, targetSource, found := resolveJSImport(name, imp.specifier, sources)
					if !found {
						addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: imp.specifier, Relation: imp.kind, Resolution: "UNRESOLVED"})
						continue
					}
					protected := currentRole == "CONFIG_SOURCE" || currentRole == "CONFIG_SUPPORT"
					role := "SUBJECT_SOURCE_DEPENDENCY"
					if protected {
						role = "CONFIG_SUPPORT"
					} else if testLike(target) {
						protected, role = true, "TEST_SUPPORT"
					}
					resolution := "RESOLVED_CAPTURED_SUBJECT_SOURCE"
					if protected {
						resolution = "RESOLVED_CAPTURED_TEST_SUPPORT"
					}
					addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: target, TargetDigest: targetSource.hash, Relation: imp.kind, Resolution: resolution, Protected: protected})
					if protected {
						addClosure(report, targetSource, role, name, extractorFor(lang))
						roles[target] = role
						queue = append(queue, target)
					}
					continue
				}
				if dep, ok := nearestManifest(name, manifests, "javascript").byName[imp.specifier]; ok {
					addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: dep.Name, Relation: imp.kind, Resolution: "DECLARED_EXTERNAL_NOT_RESOLVED"})
				} else if strings.HasPrefix(imp.specifier, "node:") || isNodeBuiltin(imp.specifier) {
					addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: imp.specifier, Relation: imp.kind, Resolution: "RUNTIME_BUILTIN_NAME_ONLY"})
				} else {
					addEdge(report, Edge{FromPath: name, FromDigest: current.hash, Target: imp.specifier, Relation: imp.kind, Resolution: "UNRESOLVED"})
				}
			}
		}
		if len(visited) > maxSources {
			return c.Fail(c.BudgetLimitReached, "dependency graph traversal quota exceeded")
		}
	}
	_ = paths
	return nil
}

type pyImport struct {
	module  string
	level   int
	partial bool
}

var pyFromRE = regexp.MustCompile(`^\s*from\s+([.]*[A-Za-z_][A-Za-z0-9_.]*|[.]+)\s+import\b\s*(.*)$`)
var pyImportRE = regexp.MustCompile(`^\s*import\s+(.+)$`)
var pyModuleRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

func pythonImports(raw []byte) []pyImport {
	if !utf8.Valid(raw) {
		return nil
	}
	var out []pyImport
	masked := maskPython(raw)
	for _, line := range strings.Split(string(masked), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if match := pyFromRE.FindStringSubmatch(line); match != nil {
			module := match[1]
			level := len(module) - len(strings.TrimLeft(module, "."))
			name := strings.TrimLeft(module, ".")
			members := strings.TrimSpace(match[2])
			partial := strings.ContainsAny(members, ",();")
			if level > 0 && name == "" && !partial && pyModuleRE.MatchString(members) {
				// "from . import helper" may name a submodule or a symbol. Keep
				// the module candidate edge while marking its meaning partial.
				name = members
				partial = true
			}
			out = append(out, pyImport{module: name, level: level, partial: partial || level > 0 && name == ""})
		} else if match := pyImportRE.FindStringSubmatch(line); match != nil {
			parts := strings.Split(match[1], ",")
			for i, part := range parts {
				part = strings.TrimSpace(strings.SplitN(part, " as ", 2)[0])
				if pyModuleRE.MatchString(part) {
					out = append(out, pyImport{module: strings.Split(part, ".")[0], partial: len(parts) > 1 || i > 0})
				} else if fields := strings.Fields(part); len(fields) > 0 && i == 0 {
					out = append(out, pyImport{module: fields[0], partial: true})
				}
			}
		}
	}
	return out
}

func maskPython(raw []byte) []byte {
	out := append([]byte(nil), raw...)
	blank := func(start, end int) {
		for i := start; i < end; i++ {
			if raw[i] != '\n' && raw[i] != '\r' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < len(raw); {
		if raw[i] == '#' {
			start := i
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			blank(start, i)
			continue
		}
		if raw[i] != '\'' && raw[i] != '"' {
			i++
			continue
		}
		start, quote := i, raw[i]
		triple := i+2 < len(raw) && raw[i+1] == quote && raw[i+2] == quote
		width := 1
		if triple {
			width = 3
		}
		i += width
		for i < len(raw) {
			if raw[i] == '\\' {
				i = min(i+2, len(raw))
				continue
			}
			if raw[i] == quote && (!triple || i+2 < len(raw) && raw[i+1] == quote && raw[i+2] == quote) {
				i += width
				break
			}
			i++
		}
		blank(start, i)
	}
	return out
}

func tsConfigReferences(name string, raw []byte) ([]string, bool) {
	if len(raw) > 1<<20 || duplicateJSONKeys(raw) != nil {
		return nil, true
	}
	var config struct {
		Extends    json.RawMessage `json:"extends"`
		References []struct {
			Path string `json:"path"`
		} `json:"references"`
		CompilerOptions struct {
			Paths map[string]json.RawMessage `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, true
	}
	refs := []string{}
	unknown := false
	if len(config.Extends) != 0 {
		var ext string
		if json.Unmarshal(config.Extends, &ext) != nil {
			unknown = true
		} else if strings.HasPrefix(ext, ".") {
			refs = append(refs, ext)
		} else {
			unknown = true
		}
	}
	for _, reference := range config.References {
		if reference.Path == "" || !strings.HasPrefix(reference.Path, ".") {
			unknown = true
			continue
		}
		ref := path.Clean(path.Join(path.Dir(name), reference.Path))
		if !policy.SafePath(ref) {
			unknown = true
			continue
		}
		if path.Ext(ref) == "" {
			ref = path.Join(ref, "tsconfig.json")
		}
		refs = append(refs, "./"+strings.TrimPrefix(ref, "./"))
	}
	if len(config.CompilerOptions.Paths) != 0 {
		unknown = true
	}
	sort.Strings(refs)
	return sortedUnique(refs), unknown
}

func resolvePythonRelative(from, module string, level int, sources map[string]source) (string, string, source) {
	if level < 1 || module == "" {
		return "", "UNRESOLVED", source{}
	}
	dir := path.Dir(from)
	for i := 1; i < level; i++ {
		if dir == "." {
			return "", "UNRESOLVED", source{}
		}
		dir = path.Dir(dir)
	}
	parts := []string{}
	if module != "" {
		parts = strings.Split(module, ".")
	}
	base := path.Join(append([]string{dir}, parts...)...)
	candidates := []string{base + ".py", path.Join(base, "__init__.py")}
	for _, candidate := range candidates {
		s, ok := sources[candidate]
		if !ok {
			continue
		}
		if candidate != path.Join(dir, "__init__.py") {
			for parent := path.Dir(candidate); parent != "." && parent != dir; parent = path.Dir(parent) {
				if _, ok := sources[path.Join(parent, "__init__.py")]; !ok {
					return candidate, "AMBIGUOUS_LOCAL_IMPORT_ROLE", s
				}
			}
		}
		return candidate, "RESOLVED_CAPTURED_FILE", s
	}
	return base, "UNRESOLVED", source{}
}

var pythonStdlib = map[string]bool{"abc": true, "argparse": true, "asyncio": true, "collections": true, "contextlib": true, "dataclasses": true, "datetime": true, "enum": true, "functools": true, "hashlib": true, "io": true, "itertools": true, "json": true, "logging": true, "math": true, "os": true, "pathlib": true, "re": true, "shutil": true, "statistics": true, "subprocess": true, "sys": true, "tempfile": true, "threading": true, "time": true, "typing": true, "unittest": true}

type jsImport struct{ specifier, kind string }
type jsToken struct{ kind, value string }

func javascriptImports(raw []byte) ([]jsImport, bool) {
	tokens, uncertain := lexJS(raw)
	var out []jsImport
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.kind != "ident" {
			continue
		}
		if t.value == "require" && i+3 < len(tokens) && tokens[i+1].value == "(" && tokens[i+2].kind == "string" && tokens[i+3].value == ")" {
			out = append(out, jsImport{specifier: tokens[i+2].value, kind: "JS_REQUIRE_LITERAL"})
			continue
		}
		if t.value != "import" && t.value != "export" {
			continue
		}
		if i+1 < len(tokens) && tokens[i+1].value == "(" {
			if i+3 < len(tokens) && tokens[i+2].kind == "string" && tokens[i+3].value == ")" {
				out = append(out, jsImport{specifier: tokens[i+2].value, kind: "JS_DYNAMIC_IMPORT_LITERAL"})
			} else {
				uncertain = true
			}
			continue
		}
		if t.value == "import" && i+1 < len(tokens) && tokens[i+1].kind == "string" {
			out = append(out, jsImport{specifier: tokens[i+1].value, kind: "JS_STATIC_IMPORT"})
			continue
		}
		for j := i + 1; j < len(tokens) && j-i <= 128; j++ {
			if tokens[j].value == ";" || j > i+1 && tokens[j].kind == "ident" && (tokens[j].value == "import" || tokens[j].value == "export") {
				break
			}
			if tokens[j].value == "from" && j+1 < len(tokens) && tokens[j+1].kind == "string" {
				kind := "JS_STATIC_IMPORT"
				if t.value == "export" {
					kind = "JS_REEXPORT"
				}
				out = append(out, jsImport{specifier: tokens[j+1].value, kind: kind})
				break
			}
		}
	}
	return out, uncertain
}

func lexJS(raw []byte) ([]jsToken, bool) {
	var out []jsToken
	uncertain := false
	for i := 0; i < len(raw); {
		if len(out) >= maxTokens {
			return out, true
		}
		b := raw[i]
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			i++
			continue
		}
		if i+1 < len(raw) && b == '/' && raw[i+1] == '/' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(raw) && b == '/' && raw[i+1] == '*' {
			i += 2
			for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
				i++
			}
			if i+1 >= len(raw) {
				uncertain = true
				i = len(raw)
			} else {
				i += 2
			}
			continue
		}
		if b == '`' { // Template interpolation and computed module paths are unresolved.
			uncertain = true
			i++
			for i < len(raw) {
				if raw[i] == '\\' {
					i = min(i+2, len(raw))
					continue
				}
				if raw[i] == '`' {
					i++
					break
				}
				i++
			}
			continue
		}
		if b == '\'' || b == '"' {
			quote := b
			i++
			start := i
			escaped := false
			for i < len(raw) {
				if raw[i] == '\n' || raw[i] == '\r' {
					uncertain = true
					break
				}
				if raw[i] == '\\' {
					escaped = true
					i = min(i+2, len(raw))
					continue
				}
				if raw[i] == quote {
					break
				}
				i++
			}
			if i >= len(raw) || raw[i] != quote {
				uncertain = true
				continue
			}
			value := string(raw[start:i])
			i++
			if escaped { // Avoid implementing JavaScript escape semantics for module paths.
				out = append(out, jsToken{kind: "string", value: ""})
				uncertain = true
			} else {
				out = append(out, jsToken{kind: "string", value: value})
			}
			continue
		}
		if isJSIdentStart(b) {
			start := i
			i++
			for i < len(raw) && isJSIdentContinue(raw[i]) {
				i++
			}
			out = append(out, jsToken{kind: "ident", value: string(raw[start:i])})
			continue
		}
		if b == '/' && regexCanStart(out) { // Skip regex bodies so text inside them cannot create import edges.
			i++
			inClass, escaped := false, false
			for i < len(raw) && raw[i] != '\n' {
				if escaped {
					escaped = false
					i++
					continue
				}
				if raw[i] == '\\' {
					escaped = true
					i++
					continue
				}
				if raw[i] == '[' {
					inClass = true
				} else if raw[i] == ']' {
					inClass = false
				} else if raw[i] == '/' && !inClass {
					i++
					for i < len(raw) && isJSIdentContinue(raw[i]) {
						i++
					}
					break
				}
				i++
			}
			continue
		}
		out = append(out, jsToken{kind: "punct", value: string(b)})
		i++
	}
	return out, uncertain
}

func isJSIdentStart(b byte) bool {
	return b == '_' || b == '$' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}
func isJSIdentContinue(b byte) bool { return isJSIdentStart(b) || b >= '0' && b <= '9' }
func regexCanStart(tokens []jsToken) bool {
	if len(tokens) == 0 {
		return true
	}
	last := tokens[len(tokens)-1]
	if last.kind == "ident" && (last.value == "return" || last.value == "throw" || last.value == "case" || last.value == "typeof" || last.value == "void" || last.value == "delete" || last.value == "new" || last.value == "in" || last.value == "of" || last.value == "yield" || last.value == "await") {
		return true
	}
	if last.kind == "string" || last.kind == "ident" && last.value != "else" {
		return false
	}
	return strings.ContainsRune("([{=,:;!?&|+-*%^~<>", rune(last.value[0]))
}

func resolveJSImport(from, spec string, sources map[string]source) (string, source, bool) {
	if len(spec) > 2048 || strings.ContainsAny(spec, "\\\x00") {
		return "", source{}, false
	}
	base := path.Clean(path.Join(path.Dir(from), spec))
	if !policy.SafePath(base) {
		return "", source{}, false
	}
	candidates := []string{base}
	if path.Ext(base) == "" {
		for _, ext := range jsExtensions {
			candidates = append(candidates, base+ext)
		}
		for _, ext := range jsExtensions {
			candidates = append(candidates, path.Join(base, "index"+ext))
		}
	}
	for _, candidate := range candidates {
		if s, ok := sources[candidate]; ok {
			return candidate, s, true
		}
	}
	return "", source{}, false
}

var nodeBuiltins = map[string]bool{"assert": true, "buffer": true, "crypto": true, "events": true, "fs": true, "http": true, "https": true, "os": true, "path": true, "process": true, "stream": true, "timers": true, "url": true, "util": true, "worker_threads": true}

func isNodeBuiltin(name string) bool { return nodeBuiltins[name] }

// Validate re-discovers from the supplied capture and rejects stale, forged,
// or internally inconsistent reports. It provides no historical provenance.
func Validate(report Report, capture workspace.Capture) error {
	if report.SchemaVersion != SchemaVersion || report.Extractor != Extractor || report.Freshness != "REVALIDATE_CAPTURE_DIGEST_BEFORE_USE" || report.StrongVerification || !c.ValidDigest(report.ReportDigest) {
		return c.Fail(c.StoreIntegrityError, "invalid source discovery report")
	}
	if err := workspace.VerifyCapture(capture); err != nil {
		return err
	}
	if report.SnapshotDigest != capture.Snapshot.Digest {
		return c.Fail(c.Conflict, "source discovery report is stale for current capture")
	}
	unsigned := report
	unsigned.ReportDigest = ""
	digest, err := c.Digest(unsigned)
	if err != nil || digest != report.ReportDigest {
		return c.Fail(c.StoreIntegrityError, "source discovery report digest mismatch")
	}
	fresh, err := Discover(capture)
	if err != nil {
		return err
	}
	if fresh.ReportDigest != report.ReportDigest {
		return c.Fail(c.StoreIntegrityError, "source discovery report differs from captured source")
	}
	return nil
}

// IsCovered reports whether an explicit protected check scope covers a path.
// It is exported to make scope review deterministic for check-plan admission.
func IsCovered(scopePath string, recursive bool, target string) bool {
	if scopePath == "." {
		return recursive
	}
	if strings.EqualFold(scopePath, target) {
		return true
	}
	return recursive && strings.HasPrefix(strings.ToLower(target), strings.ToLower(scopePath)+"/")
}
