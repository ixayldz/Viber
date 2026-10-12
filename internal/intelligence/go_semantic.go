package intelligence

import (
	"context"
	"fmt"
	"go/ast"
	buildconstraint "go/build/constraint"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

const GoSemanticExtractor = "GO_TYPES_CAPTURED_V1"

const (
	maxGoSemanticFiles         = 512
	maxGoSemanticFileBytes     = 1 << 20
	maxGoSemanticTotalBytes    = 8 << 20
	maxGoSemanticTokensFile    = 65536
	maxGoSemanticTokens        = 262144
	maxGoSemanticASTFile       = 100000
	maxGoSemanticAST           = 300000
	maxGoSemanticNodes         = 50000
	maxGoSemanticEdges         = 100000
	maxGoSemanticDiagnostics   = 256
	maxGoSemanticExcludedPaths = 512
)

// GoSemanticOptions describes the build context used for this analysis. Empty
// platform/compiler fields mean unknown; uncertain conditional files are
// excluded and force broad check selection.
type GoSemanticOptions struct {
	GOOS              string   `json:"goos,omitempty"`
	GOARCH            string   `json:"goarch,omitempty"`
	Compiler          string   `json:"compiler,omitempty"`
	GoVersion         string   `json:"go_version,omitempty"`
	BuildTags         []string `json:"build_tags"`
	BuildTagsComplete bool     `json:"build_tags_complete"`
	CGOEnabled        string   `json:"cgo_enabled"` // ENABLED, DISABLED, or UNKNOWN
}

type GoSemanticNode struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	PackageID  string `json:"package_id,omitempty"`
	FileDigest string `json:"file_digest,omitempty"`
	Start      int    `json:"byte_start,omitempty"`
	End        int    `json:"byte_end,omitempty"`
}

type GoSemanticEdge struct {
	ID              string `json:"id"`
	From            string `json:"from"`
	To              string `json:"to"`
	Relation        string `json:"relation"`
	ResolutionClass string `json:"resolution_class"`
	EvidenceKind    string `json:"evidence_kind"`
	EvidencePath    string `json:"evidence_path"`
	EvidenceDigest  string `json:"evidence_digest"`
	EvidenceStart   int    `json:"evidence_byte_start"`
	EvidenceEnd     int    `json:"evidence_byte_end"`
}

type GoSemanticCoverage struct {
	Overall                  string   `json:"overall"`
	ParsedFiles              int      `json:"parsed_files"`
	ExcludedFiles            int      `json:"excluded_files"`
	ASTNodes                 int      `json:"ast_nodes"`
	TypeCheckedPackages      int      `json:"type_checked_packages"`
	TypeCheckSkippedPackages int      `json:"type_check_skipped_packages"`
	TypeCheckErrors          int      `json:"type_check_errors"`
	UnresolvedImports        int      `json:"unresolved_imports"`
	DiagnosticsTruncated     bool     `json:"diagnostics_truncated"`
	Diagnostics              []string `json:"diagnostics"`
	Unknowns                 []string `json:"unknowns"`
	ExcludedPaths            []string `json:"excluded_paths"`
	ExcludedPathsTruncated   bool     `json:"excluded_paths_truncated"`
	WidenCheckSelection      bool     `json:"widen_check_selection"`
	EdgesTruncated           bool     `json:"edges_truncated"`
}

type GoSemanticGraph struct {
	SchemaVersion        int                `json:"schema_version"`
	Extractor            string             `json:"extractor"`
	ToolchainVersion     string             `json:"toolchain_version"`
	SourceSnapshotDigest string             `json:"source_snapshot_digest"`
	FileSetDigest        string             `json:"file_set_digest"`
	BuildContext         GoSemanticOptions  `json:"build_context"`
	ModulePath           string             `json:"module_path,omitempty"`
	LanguageVersion      string             `json:"language_version,omitempty"`
	Nodes                []GoSemanticNode   `json:"nodes"`
	Edges                []GoSemanticEdge   `json:"edges"`
	Coverage             GoSemanticCoverage `json:"coverage"`
	Fingerprint          string             `json:"fingerprint"`
}

type GoTestSelection struct {
	SourceSnapshotDigest string   `json:"source_snapshot_digest"`
	GraphFingerprint     string   `json:"graph_fingerprint"`
	ChangedPaths         []string `json:"changed_paths"`
	SelectedTestPaths    []string `json:"selected_test_paths"`
	WidenToAllTests      bool     `json:"widen_to_all_tests"`
	WidenToAllChecks     bool     `json:"widen_to_all_checks"`
	Reason               string   `json:"reason"`
}

type goSemanticFile struct {
	path      string
	raw       []byte
	digest    string
	ast       *ast.File
	tokenFile *token.File
	group     *goSemanticPackage
	testFile  bool
	parsed    bool
	excluded  bool
}

type goSemanticPackage struct {
	dir           string
	name          string
	importPath    string
	key           string
	isTest        bool
	externalTest  bool
	files         []*goSemanticFile
	nodeID        string
	state         uint8
	skipTypeCheck bool
	typesPkg      *types.Package
	info          *types.Info
}

type goSemanticImporter struct {
	packages map[string]*goSemanticPackage
	check    func(*goSemanticPackage) *types.Package
	unknown  map[string]*types.Package
}

// BuildGoSemanticGraph performs cooperative source-only go/types analysis over
// captured files. Synchronous go/types work has no hard host CPU/RSS bound;
// use BuildGoSemanticHintsGraph on an untrusted host path or the pinned helper
// worker for isolated bounded type checking.
func BuildGoSemanticGraph(ctx context.Context, source workspace.Capture, options GoSemanticOptions) (GoSemanticGraph, error) {
	return buildGoSemanticGraphForSnapshot(ctx, source, options, source.Snapshot.Digest, true)
}

// BuildGoSemanticHintsGraph returns bounded parse/import/declaration hints and
// deliberately skips go/types. It always marks coverage partial and widens
// check selection because lexical evidence cannot establish semantic closure.
func BuildGoSemanticHintsGraph(ctx context.Context, source workspace.Capture, options GoSemanticOptions) (GoSemanticGraph, error) {
	return buildGoSemanticGraphForSnapshot(ctx, source, options, source.Snapshot.Digest, false)
}

// BuildGoSemanticGraphForSnapshot analyzes captured materialized bytes while
// preserving their original immutable snapshot identity. The supplied digest
// is only an identity binding: callers must verify it against their retained
// capture and ValidateGoSemanticGraphEnvelope must be used on worker output.
func BuildGoSemanticGraphForSnapshot(ctx context.Context, source workspace.Capture, options GoSemanticOptions, snapshotDigest string) (GoSemanticGraph, error) {
	return buildGoSemanticGraphForSnapshot(ctx, source, options, snapshotDigest, true)
}

func buildGoSemanticGraphForSnapshot(ctx context.Context, source workspace.Capture, options GoSemanticOptions, snapshotDigest string, allowTypeCheck bool) (GoSemanticGraph, error) {
	out := emptyGoSemanticGraph(snapshotDigest)
	if err := workspace.VerifyCapture(source); err != nil {
		return out, err
	}
	if !c.ValidDigest(snapshotDigest) {
		return out, c.Fail(c.InvalidArgument, "valid source snapshot binding required")
	}
	out.Coverage.WidenCheckSelection = false
	if ctx == nil {
		return out, c.Fail(c.InvalidArgument, "Go semantic analysis requires a context")
	}
	options, err := normalizeGoSemanticOptions(options)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if options.GoVersion == "" {
		options.GoVersion = parseRuntimeGoVersion(runtime.Version())
	}
	out.BuildContext = options
	out.ToolchainVersion = runtime.Version()
	out.FileSetDigest, err = capturedGoFileSetDigestForSnapshot(source, snapshotDigest)
	if err != nil {
		return out, err
	}
	modulePath, moduleGoVersion, moduleUnknowns := capturedGoModuleInfo(source)
	if moduleGoVersion == "" {
		moduleGoVersion = out.BuildContext.GoVersion
		out.Coverage.Unknowns = append(out.Coverage.Unknowns, "GO_LANGUAGE_VERSION_ASSUMED_FROM_ANALYZER_TOOLCHAIN")
		out.Coverage.WidenCheckSelection = true
	}
	out.LanguageVersion = moduleGoVersion
	out.ModulePath = modulePath
	out.Coverage.Unknowns = append(out.Coverage.Unknowns, moduleUnknowns...)
	moduleImportsSafe := modulePath != ""
	if len(moduleUnknowns) != 0 {
		out.Coverage.WidenCheckSelection = true
	}
	for _, unknown := range moduleUnknowns {
		if unknown == "GO_WORKSPACE_REPLACEMENTS_NOT_RESOLVED" || unknown == "GO_MODULE_REPLACEMENT_OR_WORKSPACE_NOT_RESOLVED" || unknown == "GO_NESTED_MODULE_BOUNDARIES_NOT_RESOLVED" {
			moduleImportsSafe = false
		}
	}
	for _, entry := range source.Snapshot.Entries {
		lowerPath := strings.ToLower(entry.Path)
		if strings.HasSuffix(lowerPath, ".s") || strings.HasSuffix(lowerPath, ".syso") || strings.HasSuffix(lowerPath, ".c") || strings.HasSuffix(lowerPath, ".h") {
			addGoUnknown(&out.Coverage, "GO_NATIVE_OR_ASSEMBLY_SOURCE_NOT_MODELED")
			out.Coverage.WidenCheckSelection = true
		}
	}
	if out.BuildContext.BuildTags == nil {
		out.BuildContext.BuildTags = []string{}
	}
	out.Nodes = []GoSemanticNode{}
	out.Edges = []GoSemanticEdge{}
	out.Coverage.Diagnostics = []string{}
	out.Coverage.Unknowns = uniqueSorted(out.Coverage.Unknowns)
	out.Coverage.ExcludedPaths = []string{}

	fset := token.NewFileSet()
	files := make([]*goSemanticFile, 0)
	fileEntries := make(map[string]c.Entry)
	goFilesSeen := 0
	for _, entry := range source.Snapshot.Entries {
		if !strings.HasSuffix(entry.Path, ".go") {
			continue
		}
		goFilesSeen++
		fileEntries[entry.Path] = entry
		if goFilesSeen > maxGoSemanticFiles || entry.Size > maxGoSemanticFileBytes {
			out.Coverage.ExcludedFiles++
			addGoExcludedPath(&out.Coverage, entry.Path)
			addGoUnknown(&out.Coverage, "GO_SOURCE_BOUND_EXCLUSION_LIMIT")
			out.Coverage.WidenCheckSelection = true
			continue
		}
		raw := source.Contents[entry.Path]
		if len(raw) != int(entry.Size) || c.HashBytes(raw) != entry.Hash {
			return out, c.Fail(c.StoreIntegrityError, "Go semantic file differs from captured source")
		}
		files = append(files, &goSemanticFile{path: entry.Path, raw: raw, digest: entry.Hash, testFile: strings.HasSuffix(entry.Path, "_test.go")})
	}
	if goFilesSeen == 0 {
		out.Coverage.Unknowns = append(out.Coverage.Unknowns, "NO_CAPTURED_GO_FILES")
		out.Coverage.WidenCheckSelection = true
	}
	var totalBytes int
	totalTokens, totalAST := 0, 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		totalBytes += len(file.raw)
		if totalBytes > maxGoSemanticTotalBytes {
			excludeGoFile(&out.Coverage, file, "GO_TOTAL_SOURCE_BYTE_LIMIT")
			continue
		}
		tokens, scanErr := boundedGoLex(ctx, file.path, file.raw)
		if scanErr != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			excludeGoFile(&out.Coverage, file, "GO_LEXICAL_OR_TOKEN_LIMIT")
			continue
		}
		totalTokens += tokens
		if totalTokens > maxGoSemanticTokens {
			excludeGoFile(&out.Coverage, file, "GO_TOTAL_TOKEN_LIMIT")
			continue
		}
		parsed, parseErr := parser.ParseFile(fset, file.path, file.raw, parser.ParseComments|parser.SkipObjectResolution)
		if parseErr != nil || parsed == nil {
			excludeGoFile(&out.Coverage, file, "GO_PARSE_ERROR")
			addGoDiagnostic(&out.Coverage, file.path+":GO_PARSE_ERROR")
			continue
		}
		astNodes := 0
		overflow := false
		ast.Inspect(parsed, func(node ast.Node) bool {
			if node == nil {
				return true
			}
			astNodes++
			if astNodes > maxGoSemanticASTFile || totalAST+astNodes > maxGoSemanticAST {
				overflow = true
				return false
			}
			if astNodes&1023 == 0 && ctx.Err() != nil {
				overflow = true
				return false
			}
			return true
		})
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if overflow {
			excludeGoFile(&out.Coverage, file, "GO_AST_NODE_LIMIT")
			continue
		}
		totalAST += astNodes
		file.ast, file.tokenFile, file.parsed = parsed, fset.File(parsed.Pos()), true
		out.Coverage.ASTNodes += astNodes
		if containsGoDirective(file.raw, "//go:linkname") {
			addGoUnknown(&out.Coverage, "GO_LINKNAME_SYMBOL_BINDING_NOT_MODELED")
			out.Coverage.WidenCheckSelection = true
		}
		if containsGoDirective(file.raw, "//go:embed") {
			addGoUnknown(&out.Coverage, "GO_EMBEDDED_ASSET_EDGES_NOT_MODELED")
			out.Coverage.WidenCheckSelection = true
		}
	}

	packages, packageByPath, packageNodes, declByPos := buildGoSemanticPackages(&out, files, modulePath, moduleImportsSafe)
	markGoGenericTypeCheckSkips(&out, packages, packageByPath)
	if !allowTypeCheck {
		for _, pkg := range packages {
			pkg.skipTypeCheck = true
		}
		addGoUnknown(&out.Coverage, "GO_TYPES_NOT_RUN_IN_COOPERATIVE_PATH")
		out.Coverage.WidenCheckSelection = true
	}
	out.Nodes = append(out.Nodes, packageNodes...)
	filePaths := make([]string, 0, len(fileEntries))
	for filePath := range fileEntries {
		filePaths = append(filePaths, filePath)
	}
	sort.Strings(filePaths)
	for _, filePath := range filePaths {
		entry := fileEntries[filePath]
		id, digestErr := goSemanticNodeID(snapshotDigest, "FILE", filePath, "", entry.Hash, 0, 0)
		if digestErr != nil {
			return out, digestErr
		}
		packageID := ""
		for _, file := range files {
			if file.path == filePath && file.group != nil {
				packageID = file.group.nodeID
				break
			}
		}
		appendGoNodeLimited(&out, GoSemanticNode{ID: id, Kind: "FILE", Path: filePath, Name: path.Base(filePath), PackageID: packageID, FileDigest: entry.Hash})
	}
	for _, pkg := range packages {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		for _, file := range pkg.files {
			if file.ast != nil {
				if err := addGoDeclarations(&out, file, file.ast, file.tokenFile, declByPos, snapshotDigest); err != nil {
					return out, err
				}
			}
		}
	}

	importer := &goSemanticImporter{packages: packageByPath, unknown: make(map[string]*types.Package)}
	var checkPackage func(*goSemanticPackage) *types.Package
	checkPackage = func(pkg *goSemanticPackage) *types.Package {
		if pkg == nil {
			return nil
		}
		if pkg.state == 2 {
			return pkg.typesPkg
		}
		if pkg.state == 1 {
			out.Coverage.Unknowns = append(out.Coverage.Unknowns, "GO_PACKAGE_IMPORT_CYCLE")
			out.Coverage.WidenCheckSelection = true
			return nil
		}
		if pkg.skipTypeCheck {
			pkg.state = 2
			pkg.typesPkg = types.NewPackage(pkg.importPath, pkg.name)
			out.Coverage.TypeCheckSkippedPackages++
			return pkg.typesPkg
		}
		if ctx.Err() != nil {
			return nil
		}
		pkg.state = 1
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
			Implicits: map[ast.Node]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}, Scopes: map[ast.Node]*types.Scope{},
			Instances: map[*ast.Ident]types.Instance{}}
		filesForCheck := make([]*ast.File, 0, len(pkg.files))
		for _, file := range pkg.files {
			if file.ast != nil {
				filesForCheck = append(filesForCheck, file.ast)
			}
		}
		if len(filesForCheck) == 0 {
			pkg.state = 2
			pkg.typesPkg = types.NewPackage(pkg.importPath, pkg.name)
			return pkg.typesPkg
		}
		config := types.Config{Importer: importer, GoVersion: toTypesGoVersion(out.LanguageVersion), Error: func(err error) {
			out.Coverage.TypeCheckErrors++
			if len(out.Coverage.Diagnostics) < maxGoSemanticDiagnostics {
				pos := token.Position{}
				if typeErr, ok := err.(types.Error); ok {
					pos = fset.Position(typeErr.Pos)
				}
				if pos.Filename != "" {
					addGoDiagnostic(&out.Coverage, pos.Filename+":GO_TYPE_CHECK_ERROR")
				} else {
					addGoDiagnostic(&out.Coverage, "GO_TYPE_CHECK_ERROR")
				}
			} else {
				out.Coverage.DiagnosticsTruncated = true
			}
		}}
		checked, checkErr := config.Check(pkg.importPath, fset, filesForCheck, info)
		if ctx.Err() != nil {
			return nil
		}
		if checked == nil {
			checked = types.NewPackage(pkg.importPath, pkg.name)
		}
		if checkErr != nil || !checked.Complete() {
			out.Coverage.WidenCheckSelection = true
			out.Coverage.Unknowns = append(out.Coverage.Unknowns, "GO_TYPES_INCOMPLETE_PACKAGE")
		}
		pkg.typesPkg, pkg.info, pkg.state = checked, info, 2
		out.Coverage.TypeCheckedPackages++
		return checked
	}
	importer.check = checkPackage
	for _, pkg := range packages {
		checkPackage(pkg)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	for _, pkg := range packages {
		addGoImportEdges(&out, pkg, packageByPath, snapshotDigest)
		addGoResolvedEdges(&out, pkg, fset, declByPos)
	}
	if out.Coverage.UnresolvedImports > 0 || out.Coverage.TypeCheckErrors > 0 || len(out.Coverage.Unknowns) > 0 || out.Coverage.EdgesTruncated {
		out.Coverage.WidenCheckSelection = true
	}
	out.Coverage.Unknowns = uniqueSorted(out.Coverage.Unknowns)
	out.Coverage.ExcludedPaths = uniqueSorted(out.Coverage.ExcludedPaths)
	sort.Strings(out.Coverage.Diagnostics)
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	sort.Slice(out.Edges, func(i, j int) bool { return out.Edges[i].ID < out.Edges[j].ID })
	if out.Coverage.WidenCheckSelection {
		out.Coverage.Overall = "PARTIAL_SOURCE_BOUND_GO_TYPES_GRAPH"
	} else {
		out.Coverage.Overall = "COMPLETE_FOR_CAPTURED_BUILD_CONTEXT"
	}
	if err := sealGoSemanticGraph(&out); err != nil {
		return GoSemanticGraph{}, err
	}
	return out, nil
}

// ValidateGoSemanticGraphEnvelope checks source identity, build-context binding,
// fingerprint, captured file-set evidence and all edge source spans without
// invoking go/types. It is suitable for validating output from the pinned
// analysis helper; it does not independently prove the helper's semantics.
func ValidateGoSemanticGraphEnvelope(report GoSemanticGraph, source workspace.Capture, options GoSemanticOptions, snapshotDigest string) error {
	if err := workspace.VerifyCapture(source); err != nil {
		return err
	}
	if !c.ValidDigest(snapshotDigest) || report.SourceSnapshotDigest != snapshotDigest || report.SchemaVersion != c.SchemaVersion || report.Extractor != GoSemanticExtractor || report.ToolchainVersion == "" || len(report.ToolchainVersion) > 64 {
		return c.Fail(c.StoreIntegrityError, "Go graph worker source/protocol binding mismatch")
	}
	normalized, err := NormalizeGoSemanticOptions(options)
	if err != nil {
		return err
	}
	gotOptions, _ := c.Digest(report.BuildContext)
	wantOptions, _ := c.Digest(normalized)
	if gotOptions != wantOptions {
		return c.Fail(c.StoreIntegrityError, "Go graph worker build context mismatch")
	}
	fileSet, err := capturedGoFileSetDigestForSnapshot(source, snapshotDigest)
	if err != nil || report.FileSetDigest != fileSet {
		return c.Fail(c.StaleBase, "Go graph worker file set differs from retained capture")
	}
	if len(report.Nodes) > maxGoSemanticNodes || len(report.Edges) > maxGoSemanticEdges || len(report.Coverage.Diagnostics) > maxGoSemanticDiagnostics || len(report.Coverage.ExcludedPaths) > maxGoSemanticExcludedPaths {
		return c.Fail(c.StoreIntegrityError, "Go graph worker output exceeded structural limits")
	}
	fingerprint, err := goSemanticGraphDigest(report)
	if err != nil || !c.ValidDigest(report.Fingerprint) || fingerprint != report.Fingerprint {
		return c.Fail(c.StoreIntegrityError, "Go graph worker fingerprint mismatch")
	}
	entries := make(map[string]c.Entry, len(source.Snapshot.Entries))
	capturedDirectories := make(map[string]bool, len(source.Snapshot.Directories))
	capturedGoDirectories := make(map[string]bool)
	for _, entry := range source.Snapshot.Entries {
		entries[entry.Path] = entry
		if strings.HasSuffix(entry.Path, ".go") {
			capturedGoDirectories[path.Dir(entry.Path)] = true
		}
	}
	for _, directory := range source.Snapshot.Directories {
		capturedDirectories[directory] = true
	}
	nodes := make(map[string]GoSemanticNode, len(report.Nodes))
	for _, node := range report.Nodes {
		if !c.ValidDigest(node.ID) || nodes[node.ID].ID != "" || len(node.Name) > 4096 || len(node.Kind) > 64 || len(node.Path) > 1024 {
			return c.Fail(c.StoreIntegrityError, "Go graph worker node envelope invalid")
		}
		if node.Kind == "PACKAGE" {
			// Package nodes identify a captured source directory; their Path is not
			// a file path and they have no byte span or file digest. The root
			// directory is represented by "." in Go's path package but omitted
			// from snapshot.Directories, so accept it only when a root-level Go
			// file is actually captured.
			directoryCaptured := capturedDirectories[node.Path] && capturedGoDirectories[node.Path]
			if node.Path == "." {
				directoryCaptured = capturedGoDirectories["."]
			}
			if !directoryCaptured || node.Name == "" || node.PackageID != "" || node.FileDigest != "" || node.Start != 0 || node.End != 0 {
				return c.Fail(c.StoreIntegrityError, "Go graph worker package is outside captured source")
			}
			wantID, digestErr := goSemanticNodeID(snapshotDigest, "PACKAGE", node.Path, node.Name, "", 0, 0)
			if digestErr != nil || node.ID != wantID {
				return c.Fail(c.StoreIntegrityError, "Go graph worker package identity mismatch")
			}
		} else if node.Path != "" {
			entry, ok := entries[node.Path]
			if !ok || node.FileDigest != "" && node.FileDigest != entry.Hash || node.Start < 0 || node.End < node.Start || node.End > int(entry.Size) {
				return c.Fail(c.StoreIntegrityError, "Go graph worker node is outside captured source")
			}
		}
		nodes[node.ID] = node
	}
	packagesWithFiles := make(map[string]bool)
	for _, node := range report.Nodes {
		if node.Kind == "PACKAGE" || node.PackageID == "" {
			continue
		}
		pkg, ok := nodes[node.PackageID]
		if !ok || pkg.Kind != "PACKAGE" || node.Path == "" || path.Dir(node.Path) != pkg.Path {
			return c.Fail(c.StoreIntegrityError, "Go graph worker node package binding mismatch")
		}
		if node.Kind == "FILE" {
			packagesWithFiles[pkg.ID] = true
		}
	}
	for _, node := range report.Nodes {
		if node.Kind == "PACKAGE" && !packagesWithFiles[node.ID] {
			return c.Fail(c.StoreIntegrityError, "Go graph worker package has no captured file node")
		}
	}
	for _, edge := range report.Edges {
		if !c.ValidDigest(edge.ID) || nodes[edge.From].ID == "" || nodes[edge.To].ID == "" || edge.EvidencePath == "" {
			return c.Fail(c.StoreIntegrityError, "Go graph worker edge envelope invalid")
		}
		entry, ok := entries[edge.EvidencePath]
		raw := source.Contents[edge.EvidencePath]
		if !ok || !c.ValidDigest(edge.EvidenceDigest) || edge.EvidenceStart < 0 || edge.EvidenceEnd <= edge.EvidenceStart || edge.EvidenceEnd > len(raw) || c.HashBytes(raw[edge.EvidenceStart:edge.EvidenceEnd]) != edge.EvidenceDigest || entry.Hash == "" {
			return c.Fail(c.StoreIntegrityError, "Go graph worker edge evidence is outside captured bytes")
		}
	}
	return nil
}

// WidenGoSemanticGraphForCapture prevents a worker's fresh materialized view
// from hiding exclusions recorded by the operator's original capture.
func WidenGoSemanticGraphForCapture(report *GoSemanticGraph, source workspace.Capture) error {
	if report == nil || report.SourceSnapshotDigest != source.Snapshot.Digest {
		return c.Fail(c.InvalidArgument, "source-bound Go graph required")
	}
	if len(source.Snapshot.Exclusions) != 0 {
		addGoUnknown(&report.Coverage, "GO_SOURCE_CAPTURE_EXCLUSIONS_NOT_VISIBLE_TO_HELPER")
		report.Coverage.WidenCheckSelection = true
		report.Coverage.Overall = "PARTIAL_SOURCE_BOUND_GO_TYPES_GRAPH"
		report.Coverage.Unknowns = uniqueSorted(report.Coverage.Unknowns)
		if err := sealGoSemanticGraph(report); err != nil {
			return err
		}
	}
	return nil
}

func ValidateGoSemanticGraph(report GoSemanticGraph, source workspace.Capture, options GoSemanticOptions) error {
	return ValidateGoSemanticGraphContext(context.Background(), report, source, options)
}

func ValidateGoSemanticGraphContext(ctx context.Context, report GoSemanticGraph, source workspace.Capture, options GoSemanticOptions) error {
	if err := workspace.VerifyCapture(source); err != nil {
		return err
	}
	var expected GoSemanticGraph
	var err error
	if containsGoString(report.Coverage.Unknowns, "GO_TYPES_NOT_RUN_IN_COOPERATIVE_PATH") {
		expected, err = BuildGoSemanticHintsGraph(ctx, source, options)
	} else {
		expected, err = BuildGoSemanticGraph(ctx, source, options)
	}
	if err != nil {
		return err
	}
	if report.Fingerprint == "" || report.Fingerprint != expected.Fingerprint {
		return c.Fail(c.StoreIntegrityError, "Go semantic graph does not match recomputed captured-source evidence")
	}
	gotDigest, err := goSemanticGraphDigest(report)
	if err != nil {
		return err
	}
	wantDigest, err := goSemanticGraphDigest(expected)
	if err != nil {
		return err
	}
	if gotDigest != wantDigest {
		return c.Fail(c.StoreIntegrityError, "Go semantic graph fields differ from recomputed graph")
	}
	return nil
}

// SelectGoTests is advisory. Partial graph coverage returns every captured Go
// test and requires the caller to retain all mandatory checks.
func SelectGoTests(graph GoSemanticGraph, changedPaths []string) (GoTestSelection, error) {
	out := GoTestSelection{SourceSnapshotDigest: graph.SourceSnapshotDigest, GraphFingerprint: graph.Fingerprint,
		ChangedPaths: []string{}, SelectedTestPaths: []string{}}
	if graph.SchemaVersion != c.SchemaVersion || graph.Extractor != GoSemanticExtractor || !c.ValidDigest(graph.SourceSnapshotDigest) || !c.ValidDigest(graph.Fingerprint) {
		return out, c.Fail(c.InvalidArgument, "valid source-bound Go semantic graph required")
	}
	graphDigest, err := goSemanticGraphDigest(graph)
	if err != nil || graphDigest != graph.Fingerprint {
		return out, c.Fail(c.StoreIntegrityError, "Go semantic graph fingerprint mismatch")
	}
	testsByPath := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Kind == "FILE" && strings.HasSuffix(node.Path, "_test.go") {
			testsByPath[node.Path] = true
		}
	}
	paths := map[string]bool{}
	for _, pathName := range changedPaths {
		if !policy.SafePath(pathName) {
			return out, c.Fail(c.InvalidArgument, "changed Go path is unsafe")
		}
		found := false
		for _, node := range graph.Nodes {
			if node.Kind == "FILE" && node.Path == pathName {
				found = true
				break
			}
		}
		if !found {
			if !graph.Coverage.WidenCheckSelection {
				return out, c.Fail(c.InvalidArgument, "changed path is outside graph source scope")
			}
		}
		paths[pathName] = true
	}
	for pathName := range paths {
		out.ChangedPaths = append(out.ChangedPaths, pathName)
	}
	sort.Strings(out.ChangedPaths)
	if graph.Coverage.WidenCheckSelection || graph.Coverage.Overall != "COMPLETE_FOR_CAPTURED_BUILD_CONTEXT" {
		out.WidenToAllTests, out.WidenToAllChecks = true, true
		out.Reason = "PARTIAL_GO_GRAPH_REQUIRES_BROAD_CHECKS"
		for testPath := range testsByPath {
			out.SelectedTestPaths = append(out.SelectedTestPaths, testPath)
		}
		sort.Strings(out.SelectedTestPaths)
		return out, nil
	}
	impactedPackages := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Kind == "FILE" && paths[node.Path] && node.PackageID != "" {
			impactedPackages[node.PackageID] = true
		}
	}
	// Importers of changed packages are affected too. This is a deterministic
	// reverse closure over only actual package imports from captured source.
	for grew := true; grew; {
		grew = false
		for _, edge := range graph.Edges {
			if edge.Relation == "IMPORTS" && edge.ResolutionClass == "ACTUAL_RESOLVED" && impactedPackages[edge.To] && !impactedPackages[edge.From] {
				impactedPackages[edge.From] = true
				grew = true
			}
		}
	}
	for _, node := range graph.Nodes {
		if node.Kind == "FILE" && strings.HasSuffix(node.Path, "_test.go") && node.PackageID != "" && impactedPackages[node.PackageID] {
			out.SelectedTestPaths = append(out.SelectedTestPaths, node.Path)
		}
	}
	sort.Strings(out.SelectedTestPaths)
	out.Reason = "COMPLETE_CAPTURED_GO_PACKAGE_IMPORT_CLOSURE"
	out.WidenToAllChecks = true
	return out, nil
}

func containsGoString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func SelectCapturedGoTests(ctx context.Context, graph GoSemanticGraph, source workspace.Capture, options GoSemanticOptions, changedPaths []string) (GoTestSelection, error) {
	if err := ValidateGoSemanticGraphContext(ctx, graph, source, options); err != nil {
		return GoTestSelection{}, err
	}
	return SelectGoTests(graph, changedPaths)
}

func emptyGoSemanticGraph(snapshot string) GoSemanticGraph {
	return GoSemanticGraph{SchemaVersion: c.SchemaVersion, Extractor: GoSemanticExtractor,
		ToolchainVersion: runtime.Version(), SourceSnapshotDigest: snapshot,
		Nodes: []GoSemanticNode{}, Edges: []GoSemanticEdge{},
		Coverage: GoSemanticCoverage{Overall: "UNBUILT", Diagnostics: []string{}, Unknowns: []string{}, ExcludedPaths: []string{}, WidenCheckSelection: true}}
}

func normalizeGoSemanticOptions(options GoSemanticOptions) (GoSemanticOptions, error) {
	options.GOOS, options.GOARCH = strings.TrimSpace(options.GOOS), strings.TrimSpace(options.GOARCH)
	options.Compiler, options.GoVersion = strings.TrimSpace(options.Compiler), strings.TrimSpace(options.GoVersion)
	options.CGOEnabled = strings.ToUpper(strings.TrimSpace(options.CGOEnabled))
	if options.CGOEnabled == "" {
		options.CGOEnabled = "UNKNOWN"
	}
	if options.CGOEnabled != "UNKNOWN" && options.CGOEnabled != "ENABLED" && options.CGOEnabled != "DISABLED" {
		return options, c.Fail(c.InvalidArgument, "invalid Go cgo build context")
	}
	if options.Compiler != "" && options.Compiler != "gc" && options.Compiler != "gccgo" {
		return options, c.Fail(c.InvalidArgument, "unsupported Go compiler build tag")
	}
	if options.GOOS != "" && !isGoOS(options.GOOS) || options.GOARCH != "" && !isGoArch(options.GOARCH) {
		return options, c.Fail(c.InvalidArgument, "unsupported Go target platform")
	}
	if options.GoVersion != "" {
		version := strings.TrimPrefix(options.GoVersion, "go")
		if !validGoVersion(version) {
			return options, c.Fail(c.InvalidArgument, "invalid Go language version")
		}
		options.GoVersion = version
	}
	tags := append([]string(nil), options.BuildTags...)
	sort.Strings(tags)
	for i, tag := range tags {
		if !token.IsIdentifier(tag) || i > 0 && tags[i-1] == tag {
			return options, c.Fail(c.InvalidArgument, "invalid or duplicate Go build tag")
		}
	}
	options.BuildTags = tags
	return options, nil
}

// NormalizeGoSemanticOptions returns the exact build-context envelope used by
// both the cooperative in-process path and the pinned helper protocol.
func NormalizeGoSemanticOptions(options GoSemanticOptions) (GoSemanticOptions, error) {
	options, err := normalizeGoSemanticOptions(options)
	if err != nil {
		return options, err
	}
	if options.GoVersion == "" {
		options.GoVersion = parseRuntimeGoVersion(runtime.Version())
	}
	if options.BuildTags == nil {
		options.BuildTags = []string{}
	}
	return options, nil
}

func capturedGoModuleInfo(source workspace.Capture) (modulePath, goVersion string, unknowns []string) {
	var moduleFile []byte
	var workFile bool
	for _, entry := range source.Snapshot.Entries {
		switch entry.Path {
		case "go.mod":
			moduleFile = source.Contents[entry.Path]
		case "go.work":
			workFile = true
		}
		if entry.Path != "go.mod" && strings.HasSuffix(entry.Path, "/go.mod") || entry.Path != "go.work" && strings.HasSuffix(entry.Path, "/go.work") {
			unknowns = append(unknowns, "GO_NESTED_MODULE_BOUNDARIES_NOT_RESOLVED")
		}
	}
	if workFile {
		unknowns = append(unknowns, "GO_WORKSPACE_REPLACEMENTS_NOT_RESOLVED")
	}
	if len(moduleFile) == 0 {
		unknowns = append(unknowns, "GO_MODULE_PATH_UNAVAILABLE")
		return "", "", unknowns
	}
	for _, line := range strings.Split(string(moduleFile), "\n") {
		fields := strings.Fields(strings.TrimSpace(strings.SplitN(line, "//", 2)[0]))
		if len(fields) == 2 && fields[0] == "module" {
			if modulePath == "" {
				modulePath = strings.Trim(fields[1], "\"`")
			}
			continue
		}
		if len(fields) == 2 && fields[0] == "go" {
			goVersion = strings.TrimPrefix(fields[1], "go")
		}
		if len(fields) > 0 && (fields[0] == "replace" || fields[0] == "use") || strings.Contains(line, "=>") {
			unknowns = append(unknowns, "GO_MODULE_REPLACEMENT_OR_WORKSPACE_NOT_RESOLVED")
		}
	}
	if modulePath == "" || strings.ContainsAny(modulePath, " \t\r\n\x00") || strings.HasPrefix(modulePath, "/") || strings.Contains(modulePath, "..") {
		modulePath = ""
		unknowns = append(unknowns, "GO_MODULE_PATH_INVALID_OR_UNAVAILABLE")
	}
	if goVersion != "" && !validGoVersion(goVersion) {
		goVersion = ""
		unknowns = append(unknowns, "GO_MODULE_VERSION_UNAVAILABLE")
	}
	return modulePath, goVersion, uniqueSorted(unknowns)
}

func capturedGoFileSetDigest(source workspace.Capture) (string, error) {
	return capturedGoFileSetDigestForSnapshot(source, source.Snapshot.Digest)
}

func capturedGoFileSetDigestForSnapshot(source workspace.Capture, snapshotDigest string) (string, error) {
	type capturedFile struct {
		Path, Digest string
		Size         int64
	}
	items := make([]capturedFile, 0)
	for _, entry := range source.Snapshot.Entries {
		if strings.HasSuffix(entry.Path, ".go") || entry.Path == "go.mod" || entry.Path == "go.work" || strings.HasSuffix(entry.Path, "/go.mod") || strings.HasSuffix(entry.Path, "/go.work") {
			items = append(items, capturedFile{entry.Path, entry.Hash, entry.Size})
		}
	}
	return c.Digest(struct {
		Snapshot string
		Files    []capturedFile
	}{snapshotDigest, items})
}

func buildGoSemanticPackages(graph *GoSemanticGraph, files []*goSemanticFile, modulePath string, moduleImportsSafe bool) ([]*goSemanticPackage, map[string]*goSemanticPackage, []GoSemanticNode, map[string]string) {
	groups := map[string]*goSemanticPackage{}
	packageNamesByDir := map[string]map[string]bool{}
	for _, file := range files {
		if !file.parsed || file.ast == nil {
			continue
		}
		included, reason, uncertain := goFileBuildSelection(file, graph.BuildContext)
		if !included {
			file.ast = nil
			file.excluded = true
			graph.Coverage.ExcludedFiles++
			addGoExcludedPath(&graph.Coverage, file.path)
			if uncertain {
				graph.Coverage.Unknowns = append(graph.Coverage.Unknowns, "GO_BUILD_CONTEXT_UNKNOWN:"+reason)
				graph.Coverage.WidenCheckSelection = true
			}
			continue
		}
		graph.Coverage.ParsedFiles++
		dir := path.Dir(file.path)
		name := file.ast.Name.Name
		if packageNamesByDir[dir] == nil {
			packageNamesByDir[dir] = map[string]bool{}
		}
		packageNamesByDir[dir][name] = true
		key := dir + "\x00" + name
		if file.testFile && name != strings.TrimSuffix(name, "_test") {
			// External test packages have a distinct synthetic import identity.
			key += "\x00external-test"
		}
		pkg := groups[key]
		if pkg == nil {
			importPath := ""
			if modulePath != "" {
				importPath = modulePath
				if dir != "." {
					importPath += "/" + dir
				}
			}
			isExternalTest := strings.HasSuffix(key, "\x00external-test")
			if isExternalTest {
				importPath += "_test"
			}
			if importPath == "" {
				// This synthetic identity is local to this analysis. It does not
				// claim that an import path was discovered or resolved.
				importPath = "captured.invalid/" + strings.TrimPrefix(dir, "./")
				if dir == "." {
					importPath = "captured.invalid/root"
				}
			}
			pkg = &goSemanticPackage{dir: dir, name: name, importPath: importPath, key: key, isTest: file.testFile, externalTest: isExternalTest}
			groups[key] = pkg
		}
		pkg.isTest = pkg.isTest && file.testFile
		file.group = pkg
		pkg.files = append(pkg.files, file)
	}
	ambiguousDirectories := map[string]bool{}
	for dir, names := range packageNamesByDir {
		validExternalTestPair := false
		if len(names) == 2 {
			for name := range names {
				if strings.HasSuffix(name, "_test") && names[strings.TrimSuffix(name, "_test")] {
					validExternalTestPair = true
				}
			}
		}
		if len(names) > 1 && !validExternalTestPair {
			ambiguousDirectories[dir] = true
			graph.Coverage.Unknowns = append(graph.Coverage.Unknowns, "GO_DIRECTORY_HAS_MULTIPLE_PACKAGE_CLAUSES:"+dir)
			graph.Coverage.WidenCheckSelection = true
		}
	}
	packages := make([]*goSemanticPackage, 0, len(groups))
	for _, pkg := range groups {
		sort.Slice(pkg.files, func(i, j int) bool { return pkg.files[i].path < pkg.files[j].path })
		if pkg.importPath == "" {
			graph.Coverage.Unknowns = append(graph.Coverage.Unknowns, "GO_INTERNAL_IMPORT_PATH_UNAVAILABLE:"+pkg.dir)
			graph.Coverage.WidenCheckSelection = true
		}
		pkg.nodeID, _ = goSemanticNodeID(graph.SourceSnapshotDigest, "PACKAGE", pkg.dir, pkg.name, "", 0, 0)
		packages = append(packages, pkg)
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].key < packages[j].key })
	byImportPath := map[string]*goSemanticPackage{}
	for _, pkg := range packages {
		if !moduleImportsSafe || modulePath == "" || pkg.importPath == "" || pkg.externalTest || pkg.isTest || ambiguousDirectories[pkg.dir] {
			continue
		}
		if current := byImportPath[pkg.importPath]; current == nil || current.isTest && !pkg.isTest {
			byImportPath[pkg.importPath] = pkg
		}
	}
	packageNodes := make([]GoSemanticNode, 0, len(packages))
	for _, pkg := range packages {
		packageNodes = append(packageNodes, GoSemanticNode{ID: pkg.nodeID, Kind: "PACKAGE", Name: pkg.name, Path: pkg.dir})
	}
	declByPos := map[string]string{}
	return packages, byImportPath, packageNodes, declByPos
}

// Generic instantiation can recursively expand type arguments (for example
// F[T] calling F[[]T]). go/types.Check is synchronous, so context cancellation
// cannot impose a hard CPU bound once it starts. Skip packages that declare
// type parameters, plus captured importers that would ask the checker to load
// them. Such packages remain source-bound lexical evidence and force broad
// checks; this is a conservative admission rule, not a complete CPU bound for
// all possible Go source.
func markGoGenericTypeCheckSkips(graph *GoSemanticGraph, packages []*goSemanticPackage, byImportPath map[string]*goSemanticPackage) {
	for _, pkg := range packages {
		for _, file := range pkg.files {
			if file.ast == nil {
				continue
			}
			ast.Inspect(file.ast, func(node ast.Node) bool {
				switch declaration := node.(type) {
				case *ast.FuncDecl:
					if declaration.Type != nil && goTypeParamsPresent(declaration.Type.TypeParams) {
						pkg.skipTypeCheck = true
						return false
					}
				case *ast.TypeSpec:
					if goTypeParamsPresent(declaration.TypeParams) {
						pkg.skipTypeCheck = true
						return false
					}
				}
				return true
			})
			if pkg.skipTypeCheck {
				break
			}
		}
		if pkg.skipTypeCheck {
			addGoUnknown(&graph.Coverage, "GO_GENERIC_PACKAGE_TYPES_NOT_CHECKED_FOR_COMPLEXITY_BOUND")
		}
	}
	for grew := true; grew; {
		grew = false
		for _, pkg := range packages {
			if pkg.skipTypeCheck {
				continue
			}
			for _, file := range pkg.files {
				if file.ast == nil {
					continue
				}
				for _, spec := range file.ast.Imports {
					importPath, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						continue
					}
					if dependency := byImportPath[importPath]; dependency != nil && dependency.skipTypeCheck {
						pkg.skipTypeCheck = true
						addGoUnknown(&graph.Coverage, "GO_IMPORTER_TYPES_SKIPPED_WITH_GENERIC_DEPENDENCY")
						grew = true
						break
					}
				}
				if pkg.skipTypeCheck {
					break
				}
			}
		}
	}
}

func goTypeParamsPresent(params *ast.FieldList) bool {
	return params != nil && len(params.List) > 0
}

func addGoDeclarations(graph *GoSemanticGraph, file *goSemanticFile, parsed *ast.File, tf *token.File, declByPos map[string]string, snapshot string) error {
	add := func(ident *ast.Ident, kind string, declaration ast.Node) error {
		if ident == nil || ident.Name == "_" || tf == nil {
			return nil
		}
		start, end := tf.Offset(ident.Pos()), tf.Offset(ident.End())
		declStart, declEnd := tf.Offset(declaration.Pos()), tf.Offset(declaration.End())
		if start < 0 || end <= start || end > len(file.raw) || declStart < 0 || declEnd <= declStart || declEnd > len(file.raw) || string(file.raw[start:end]) != ident.Name {
			return c.Fail(c.StoreIntegrityError, "Go declaration does not bind to captured source offsets")
		}
		id, err := goSemanticNodeID(snapshot, kind, file.path, ident.Name, file.digest, declStart, declEnd)
		if err != nil {
			return err
		}
		if len(graph.Nodes) >= maxGoSemanticNodes {
			addGoUnknown(&graph.Coverage, "GO_GRAPH_NODE_LIMIT")
			graph.Coverage.WidenCheckSelection = true
			return nil
		}
		packageID := ""
		if file.group != nil {
			packageID = file.group.nodeID
		}
		graph.Nodes = append(graph.Nodes, GoSemanticNode{ID: id, Kind: kind, Name: ident.Name, Path: file.path, PackageID: packageID, FileDigest: file.digest, Start: declStart, End: declEnd})
		declByPos[file.path+"\x00"+strconv.Itoa(start)] = id
		return nil
	}
	for _, declaration := range parsed.Decls {
		switch node := declaration.(type) {
		case *ast.FuncDecl:
			kind := "FUNCTION"
			if node.Recv != nil {
				kind = "METHOD"
			}
			if file.testFile && isGoTestFunction(node.Name.Name) {
				kind = "TEST_FUNCTION"
			}
			if err := add(node.Name, kind, node); err != nil {
				return err
			}
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if err := add(spec.Name, "TYPE", spec); err != nil {
						return err
					}
					ast.Inspect(spec.Type, func(child ast.Node) bool {
						field, ok := child.(*ast.Field)
						if ok {
							for _, name := range field.Names {
								_ = add(name, "FIELD", field)
							}
						}
						return true
					})
				case *ast.ValueSpec:
					kind := "VARIABLE"
					if node.Tok == token.CONST {
						kind = "CONSTANT"
					}
					for _, name := range spec.Names {
						if err := add(name, kind, spec); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

func addGoImportEdges(graph *GoSemanticGraph, pkg *goSemanticPackage, byPath map[string]*goSemanticPackage, snapshot string) {
	for _, file := range pkg.files {
		if file.ast == nil || file.tokenFile == nil {
			continue
		}
		for _, spec := range file.ast.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil || importPath == "" {
				continue
			}
			start, end := file.tokenFile.Offset(spec.Path.Pos()), file.tokenFile.Offset(spec.Path.End())
			if start < 0 || end <= start || end > len(file.raw) {
				continue
			}
			to := byPath[importPath]
			if !goInternalImportAllowed(pkg.importPath, importPath) {
				to = nil
				addGoUnknown(&graph.Coverage, "GO_INTERNAL_IMPORT_BOUNDARY_UNVERIFIED")
				graph.Coverage.WidenCheckSelection = true
			}
			if to == nil {
				graph.Coverage.UnresolvedImports++
				id, _ := goSemanticNodeID(snapshot, "UNRESOLVED_IMPORT", "", importPath, "", 0, 0)
				if !appendGoNodeOnce(graph, GoSemanticNode{ID: id, Kind: "UNRESOLVED_IMPORT", Name: importPath}) {
					continue
				}
				addGoEdge(graph, GoSemanticEdge{From: pkg.nodeID, To: id, Relation: "IMPORTS", ResolutionClass: "LEXICAL_POSSIBLE", EvidenceKind: "GO_IMPORT_LITERAL", EvidencePath: file.path, EvidenceDigest: c.HashBytes(file.raw[start:end]), EvidenceStart: start, EvidenceEnd: end})
				graph.Coverage.Unknowns = append(graph.Coverage.Unknowns, "GO_IMPORT_NOT_PRESENT_IN_CAPTURE:"+importPath)
				continue
			}
			addGoEdge(graph, GoSemanticEdge{From: pkg.nodeID, To: to.nodeID, Relation: "IMPORTS", ResolutionClass: "ACTUAL_RESOLVED", EvidenceKind: "GO_TYPES_CAPTURED_PACKAGE_IMPORT", EvidencePath: file.path, EvidenceDigest: c.HashBytes(file.raw[start:end]), EvidenceStart: start, EvidenceEnd: end})
		}
	}
}

func addGoResolvedEdges(graph *GoSemanticGraph, pkg *goSemanticPackage, fset *token.FileSet, declByPos map[string]string) {
	if pkg.info == nil {
		return
	}
	fileNodes := map[string]string{}
	for _, node := range graph.Nodes {
		if node.Kind == "FILE" {
			fileNodes[node.Path] = node.ID
		}
	}
	testFunctionIDs := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Kind == "TEST_FUNCTION" {
			testFunctionIDs[node.ID] = true
		}
	}
	for _, file := range pkg.files {
		if file.ast == nil || file.tokenFile == nil {
			continue
		}
		uses := make([]*ast.Ident, 0)
		called := map[token.Pos]bool{}
		ast.Inspect(file.ast, func(node ast.Node) bool {
			if node == nil {
				return true
			}
			if call, ok := node.(*ast.CallExpr); ok {
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					called[fun.Pos()] = true
				case *ast.SelectorExpr:
					called[fun.Sel.Pos()] = true
					if selection := pkg.info.Selections[fun]; selection != nil && selection.Kind() == types.MethodVal && goTypeIsInterface(selection.Recv()) {
						addGoUnknown(&graph.Coverage, "GO_INTERFACE_DISPATCH_TARGETS_NOT_EXPANDED")
						graph.Coverage.WidenCheckSelection = true
					}
				default:
					if typeAndValue, ok := pkg.info.Types[call.Fun]; ok && typeAndValue.Type != nil && !typeAndValue.IsType() {
						if _, functionType := typeAndValue.Type.Underlying().(*types.Signature); functionType {
							addGoUnknown(&graph.Coverage, "GO_INDIRECT_CALL_TARGET_NOT_RESOLVED")
							graph.Coverage.WidenCheckSelection = true
						}
					}
				}
				if _, literal := call.Fun.(*ast.FuncLit); literal {
					addGoUnknown(&graph.Coverage, "GO_FUNCTION_LITERAL_CALL_TARGET_NOT_EXPANDED")
					graph.Coverage.WidenCheckSelection = true
				}
			}
			if ident, ok := node.(*ast.Ident); ok && pkg.info.Uses[ident] != nil {
				uses = append(uses, ident)
			}
			return true
		})
		sort.Slice(uses, func(i, j int) bool { return uses[i].Pos() < uses[j].Pos() })
		functions := make([]goFuncSpan, 0)
		ast.Inspect(file.ast, func(node ast.Node) bool {
			if node == nil {
				return true
			}
			switch fn := node.(type) {
			case *ast.FuncDecl:
				id := declByPos[file.path+"\x00"+strconv.Itoa(file.tokenFile.Offset(fn.Name.Pos()))]
				if id != "" {
					functions = append(functions, goFuncSpan{start: file.tokenFile.Offset(fn.Pos()), end: file.tokenFile.Offset(fn.End()), id: id})
				}
			case *ast.FuncLit:
				start, end := file.tokenFile.Offset(fn.Pos()), file.tokenFile.Offset(fn.End())
				id, _ := goSemanticNodeID(graph.SourceSnapshotDigest, "FUNCTION_LITERAL", file.path, "<literal>", file.digest, start, end)
				if len(graph.Nodes) < maxGoSemanticNodes {
					appendGoNodeLimited(graph, GoSemanticNode{ID: id, Kind: "FUNCTION_LITERAL", Name: "<literal>", Path: file.path, PackageID: pkg.nodeID, FileDigest: file.digest, Start: start, End: end})
					functions = append(functions, goFuncSpan{start: start, end: end, id: id})
				} else {
					addGoUnknown(&graph.Coverage, "GO_GRAPH_NODE_LIMIT")
					graph.Coverage.WidenCheckSelection = true
				}
			}
			return true
		})
		sort.Slice(functions, func(i, j int) bool {
			if functions[i].start != functions[j].start {
				return functions[i].start < functions[j].start
			}
			return functions[i].end > functions[j].end
		})
		active := make([]goFuncSpan, 0)
		functionIndex := 0
		for _, ident := range uses {
			offset := file.tokenFile.Offset(ident.Pos())
			for len(active) > 0 && active[len(active)-1].end <= offset {
				active = active[:len(active)-1]
			}
			for functionIndex < len(functions) && functions[functionIndex].start <= offset {
				span := functions[functionIndex]
				for len(active) > 0 && active[len(active)-1].end <= span.start {
					active = active[:len(active)-1]
				}
				active = append(active, span)
				functionIndex++
			}
			// A prior sibling may have ended before this use; discard it even if
			// a later nested span was never entered.
			for len(active) > 0 && (active[len(active)-1].end <= offset || active[len(active)-1].start > offset) {
				active = active[:len(active)-1]
			}
			object := pkg.info.Uses[ident]
			if object == nil || object.Pkg() == nil {
				continue
			}
			if objectCanCall(object) && !called[ident.Pos()] {
				addGoUnknown(&graph.Coverage, "GO_FUNCTION_VALUE_OR_METHOD_REFERENCE_NOT_EXPANDED")
				graph.Coverage.WidenCheckSelection = true
			}
			if object.Pkg().Path() != pkg.importPath && !goInternalImportAllowed(pkg.importPath, object.Pkg().Path()) {
				addGoUnknown(&graph.Coverage, "GO_INTERNAL_IMPORT_BOUNDARY_UNVERIFIED")
				graph.Coverage.WidenCheckSelection = true
				continue
			}
			objTokenFile := fset.File(object.Pos())
			if objTokenFile == nil {
				continue
			}
			objPath := objTokenFile.Name()
			objOffset := objTokenFile.Offset(object.Pos())
			to := declByPos[objPath+"\x00"+strconv.Itoa(objOffset)]
			if to == "" {
				continue
			}
			start, end := file.tokenFile.Offset(ident.Pos()), file.tokenFile.Offset(ident.End())
			if start < 0 || end <= start || end > len(file.raw) || string(file.raw[start:end]) != ident.Name {
				continue
			}
			from := fileNodes[file.path]
			if len(active) > 0 {
				from = active[len(active)-1].id
			}
			relation := "REFERENCES"
			if called[ident.Pos()] && objectCanCall(object) {
				relation = "CALLS"
			}
			addGoEdge(graph, GoSemanticEdge{From: from, To: to, Relation: relation, ResolutionClass: "ACTUAL_RESOLVED", EvidenceKind: "GO_TYPES_OBJECT_USE", EvidencePath: file.path, EvidenceDigest: c.HashBytes(file.raw[start:end]), EvidenceStart: start, EvidenceEnd: end})
			if relation == "CALLS" && testFunctionIDs[from] {
				addGoEdge(graph, GoSemanticEdge{From: from, To: to, Relation: "TESTS", ResolutionClass: "ACTUAL_RESOLVED", EvidenceKind: "GO_TYPES_STATIC_TEST_CALL", EvidencePath: file.path, EvidenceDigest: c.HashBytes(file.raw[start:end]), EvidenceStart: start, EvidenceEnd: end})
			}
		}
	}
}

type goFuncSpan struct {
	start, end int
	id         string
}

func objectCanCall(object types.Object) bool {
	_, ok := object.(*types.Func)
	return ok
}

func addGoEdge(graph *GoSemanticGraph, edge GoSemanticEdge) {
	if len(graph.Edges) >= maxGoSemanticEdges {
		graph.Coverage.EdgesTruncated = true
		graph.Coverage.WidenCheckSelection = true
		return
	}
	id, err := c.Digest(struct {
		From, To, Relation, ResolutionClass, EvidenceKind, EvidencePath, EvidenceDigest string
		EvidenceStart, EvidenceEnd                                                      int
	}{edge.From, edge.To, edge.Relation, edge.ResolutionClass, edge.EvidenceKind, edge.EvidencePath, edge.EvidenceDigest, edge.EvidenceStart, edge.EvidenceEnd})
	if err == nil {
		edge.ID = id
		graph.Edges = append(graph.Edges, edge)
	}
}

func appendGoNodeOnce(graph *GoSemanticGraph, node GoSemanticNode) bool {
	for _, existing := range graph.Nodes {
		if existing.ID == node.ID {
			return true
		}
	}
	return appendGoNodeLimited(graph, node)
}

func appendGoNodeLimited(graph *GoSemanticGraph, node GoSemanticNode) bool {
	if len(graph.Nodes) < maxGoSemanticNodes {
		graph.Nodes = append(graph.Nodes, node)
		return true
	}
	addGoUnknown(&graph.Coverage, "GO_GRAPH_NODE_LIMIT")
	graph.Coverage.WidenCheckSelection = true
	return false
}

func addGoExcludedPath(coverage *GoSemanticCoverage, pathName string) {
	if len(coverage.ExcludedPaths) < maxGoSemanticExcludedPaths {
		coverage.ExcludedPaths = append(coverage.ExcludedPaths, pathName)
	} else {
		coverage.ExcludedPathsTruncated = true
	}
}

func containsGoDirective(raw []byte, directive string) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(strings.TrimSuffix(line, "\r")), directive) {
			return true
		}
	}
	return false
}

func goInternalImportAllowed(fromImportPath, targetImportPath string) bool {
	parts := strings.Split(targetImportPath, "/")
	for index, part := range parts {
		if part != "internal" {
			continue
		}
		parent := strings.Join(parts[:index], "/")
		return fromImportPath == parent || strings.HasPrefix(fromImportPath, parent+"/")
	}
	return true
}

func goTypeIsInterface(typ types.Type) bool {
	if typ == nil {
		return false
	}
	typ = types.Unalias(typ)
	if pointer, ok := typ.Underlying().(*types.Pointer); ok {
		typ = types.Unalias(pointer.Elem())
	}
	_, ok := typ.Underlying().(*types.Interface)
	return ok
}

func addGoUnknown(coverage *GoSemanticCoverage, value string) {
	for _, existing := range coverage.Unknowns {
		if existing == value {
			return
		}
	}
	coverage.Unknowns = append(coverage.Unknowns, value)
}

func goSemanticNodeID(snapshot, kind, pathName, name, fileDigest string, start, end int) (string, error) {
	return c.Digest(struct {
		Snapshot, Kind, Path, Name, FileDigest string
		Start, End                             int
	}{snapshot, kind, pathName, name, fileDigest, start, end})
}

func goSemanticGraphDigest(graph GoSemanticGraph) (string, error) {
	graph.Fingerprint = ""
	return c.Digest(graph)
}

func sealGoSemanticGraph(graph *GoSemanticGraph) error {
	fingerprint, err := goSemanticGraphDigest(*graph)
	if err != nil {
		return err
	}
	graph.Fingerprint = fingerprint
	return nil
}

func boundedGoLex(ctx context.Context, name string, raw []byte) (int, error) {
	fset := token.NewFileSet()
	file := fset.AddFile(name, -1, len(raw))
	var scannerState scanner.Scanner
	scannerState.Init(file, raw, nil, 0)
	depth, count := 0, 0
	for {
		if count&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return count, err
			}
		}
		_, kind, _ := scannerState.Scan()
		if kind == token.EOF {
			break
		}
		count++
		if count > maxGoSemanticTokensFile {
			return count, fmt.Errorf("token limit")
		}
		switch kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		}
		if depth < 0 || depth > 256 {
			return count, fmt.Errorf("nesting limit")
		}
	}
	if scannerState.ErrorCount != 0 {
		return count, fmt.Errorf("lexical error")
	}
	return count, nil
}

func excludeGoFile(coverage *GoSemanticCoverage, file *goSemanticFile, reason string) {
	if file == nil || file.excluded {
		return
	}
	file.ast, file.parsed = nil, false
	coverage.ExcludedFiles++
	addGoExcludedPath(coverage, file.path)
	addGoUnknown(coverage, reason)
	coverage.WidenCheckSelection = true
}

func addGoDiagnostic(coverage *GoSemanticCoverage, value string) {
	if len(coverage.Diagnostics) >= maxGoSemanticDiagnostics {
		coverage.DiagnosticsTruncated = true
		return
	}
	coverage.Diagnostics = append(coverage.Diagnostics, value)
}

func uniqueSorted(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func validGoVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

func toTypesGoVersion(value string) string {
	if value == "" {
		return ""
	}
	return "go" + value
}

func parseRuntimeGoVersion(value string) string {
	version := strings.TrimPrefix(value, "go")
	if validGoVersion(version) {
		return version
	}
	return ""
}

type triState int8

const (
	triUnknown triState = -1
	triFalse   triState = 0
	triTrue    triState = 1
)

func goFileBuildSelection(file *goSemanticFile, options GoSemanticOptions) (include bool, reason string, uncertain bool) {
	state, suffixUnknown := goFilenameBuildState(file.path, options)
	if state == triFalse {
		return false, "FILENAME_BUILD_CONSTRAINT_FALSE", false
	}
	if state == triUnknown {
		return false, "FILENAME_BUILD_CONSTRAINT_UNKNOWN", true
	}
	if suffixUnknown {
		return false, "FILENAME_BUILD_CONSTRAINT_UNKNOWN", true
	}
	goExpr, legacyExprs, parseUnknown := goBuildExpressions(file.raw)
	if parseUnknown {
		return false, "BUILD_CONSTRAINT_PARSE_UNKNOWN", true
	}
	goState, legacyState := triUnknown, triUnknown
	if goExpr != nil {
		goState = evalGoBuildExpr(goExpr, options)
	}
	if len(legacyExprs) != 0 {
		legacyState = triTrue
		for _, expr := range legacyExprs {
			legacyState = triAnd(legacyState, evalGoBuildExpr(expr, options))
		}
	}
	if goExpr != nil && len(legacyExprs) != 0 {
		if goState == triUnknown || legacyState == triUnknown || goState != legacyState {
			return false, "GO_AND_LEGACY_BUILD_CONSTRAINTS_DISAGREE_OR_UNKNOWN", true
		}
		state = goState
	} else if goExpr != nil {
		state = goState
	} else if len(legacyExprs) != 0 {
		state = legacyState
	}
	if state == triFalse {
		return false, "GO_BUILD_CONSTRAINT_FALSE", false
	}
	if state == triUnknown {
		return false, "GO_BUILD_CONSTRAINT_UNKNOWN", true
	}
	if file.ast != nil {
		for _, spec := range file.ast.Imports {
			if pathValue, err := strconv.Unquote(spec.Path.Value); err == nil && pathValue == "C" {
				if options.CGOEnabled == "DISABLED" {
					return false, "CGO_DISABLED", false
				}
				return false, "CGO_SOURCE_UNSUPPORTED", true
			}
		}
	}
	return true, "", false
}

func goBuildExpressions(raw []byte) (buildconstraint.Expr, []buildconstraint.Expr, bool) {
	var goExpr buildconstraint.Expr
	legacy := []buildconstraint.Expr{}
	inBlock := false
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines {
		trim := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if inBlock {
			end := strings.Index(trim, "*/")
			if end < 0 {
				continue
			}
			trim = strings.TrimSpace(trim[end+2:])
			inBlock = false
		}
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "/*") {
			end := strings.Index(trim, "*/")
			if end < 0 {
				inBlock = true
				continue
			}
			trim = strings.TrimSpace(trim[end+2:])
			if trim == "" {
				continue
			}
		}
		if strings.HasPrefix(trim, "//") {
			if strings.HasPrefix(trim, "//go:build") {
				expr, err := buildconstraint.Parse(trim)
				if err != nil || goExpr != nil {
					return nil, nil, true
				}
				goExpr = expr
			} else if strings.HasPrefix(trim, "// +build") {
				expr, err := buildconstraint.Parse(trim)
				if err != nil {
					return nil, nil, true
				}
				legacy = append(legacy, expr)
			}
			continue
		}
		break
	}
	return goExpr, legacy, false
}

func evalGoBuildExpr(expr buildconstraint.Expr, options GoSemanticOptions) triState {
	switch node := expr.(type) {
	case *buildconstraint.TagExpr:
		return evalGoBuildTag(node.Tag, options)
	case *buildconstraint.NotExpr:
		return triNot(evalGoBuildExpr(node.X, options))
	case *buildconstraint.AndExpr:
		return triAnd(evalGoBuildExpr(node.X, options), evalGoBuildExpr(node.Y, options))
	case *buildconstraint.OrExpr:
		return triOr(evalGoBuildExpr(node.X, options), evalGoBuildExpr(node.Y, options))
	default:
		return triUnknown
	}
}

func evalGoBuildTag(tag string, options GoSemanticOptions) triState {
	if tag == "cgo" {
		if options.CGOEnabled == "ENABLED" {
			return triTrue
		}
		if options.CGOEnabled == "DISABLED" {
			return triFalse
		}
		return triUnknown
	}
	if tag == "unix" {
		if options.GOOS == "" {
			return triUnknown
		}
		if oneOf(options.GOOS, "aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "linux", "netbsd", "openbsd", "solaris") {
			return triTrue
		}
		return triFalse
	}
	if oneOf(tag, "aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows") {
		if options.GOOS == "" {
			return triUnknown
		}
		if tag == options.GOOS || options.GOOS == "android" && tag == "linux" || options.GOOS == "ios" && tag == "darwin" || options.GOOS == "illumos" && tag == "solaris" {
			return triTrue
		}
		return triFalse
	}
	if oneOf(tag, "386", "amd64", "amd64p32", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "sparc64", "wasm") {
		if options.GOARCH == "" {
			return triUnknown
		}
		if tag == options.GOARCH {
			return triTrue
		}
		return triFalse
	}
	if tag == "gc" || tag == "gccgo" {
		if options.Compiler == "" {
			return triUnknown
		}
		if options.Compiler == tag {
			return triTrue
		}
		return triFalse
	}
	if strings.HasPrefix(tag, "go1.") {
		if options.GoVersion == "" {
			return triUnknown
		}
		return compareGoRelease(options.GoVersion, strings.TrimPrefix(tag, "go"))
	}
	for _, known := range options.BuildTags {
		if tag == known {
			return triTrue
		}
	}
	if options.BuildTagsComplete {
		return triFalse
	}
	return triUnknown
}

func compareGoRelease(actual, wanted string) triState {
	a, aok := goMinor(actual)
	w, wok := goMinor(wanted)
	if !aok || !wok {
		return triUnknown
	}
	if a >= w {
		return triTrue
	}
	return triFalse
}

func goMinor(value string) (int, bool) {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return 0, false
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || major != 1 {
		return 0, false
	}
	return minor, true
}

func goFilenameBuildState(filePath string, options GoSemanticOptions) (triState, bool) {
	base := strings.TrimSuffix(path.Base(filePath), ".go")
	base = strings.TrimSuffix(base, "_test")
	parts := strings.Split(base, "_")
	if len(parts) < 2 {
		return triTrue, false
	}
	last := parts[len(parts)-1]
	last2 := ""
	if len(parts) >= 3 {
		last2 = parts[len(parts)-2]
	}
	state := triTrue
	if isGoOS(last) {
		if options.GOOS == "" {
			state = triUnknown
		} else if last != options.GOOS && !(options.GOOS == "android" && last == "linux") && !(options.GOOS == "ios" && last == "darwin") && !(options.GOOS == "illumos" && last == "solaris") {
			state = triFalse
		}
	} else if isGoArch(last) {
		if options.GOARCH == "" {
			state = triUnknown
		} else if last != options.GOARCH {
			state = triFalse
		}
	}
	if isGoOS(last2) && isGoArch(last) {
		if options.GOOS == "" || options.GOARCH == "" {
			state = triUnknown
		} else if (last2 != options.GOOS && !(options.GOOS == "android" && last2 == "linux") && !(options.GOOS == "ios" && last2 == "darwin") && !(options.GOOS == "illumos" && last2 == "solaris")) || last != options.GOARCH {
			state = triFalse
		}
	}
	return state, state == triUnknown
}

func isGoOS(value string) bool {
	return oneOf(value, "aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows")
}
func isGoArch(value string) bool {
	return oneOf(value, "386", "amd64", "amd64p32", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "sparc64", "wasm")
}
func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
func triNot(value triState) triState {
	if value == triTrue {
		return triFalse
	}
	if value == triFalse {
		return triTrue
	}
	return triUnknown
}
func triAnd(a, b triState) triState {
	if a == triFalse || b == triFalse {
		return triFalse
	}
	if a == triUnknown || b == triUnknown {
		return triUnknown
	}
	return triTrue
}
func triOr(a, b triState) triState {
	if a == triTrue || b == triTrue {
		return triTrue
	}
	if a == triUnknown || b == triUnknown {
		return triUnknown
	}
	return triFalse
}

func isGoTestFunction(name string) bool {
	return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Fuzz") || strings.HasPrefix(name, "Example")
}

func (importer *goSemanticImporter) Import(importPath string) (*types.Package, error) {
	if pkg := importer.packages[importPath]; pkg != nil {
		if importer.check != nil {
			if result := importer.check(pkg); result != nil {
				return result, nil
			}
		}
		return nil, fmt.Errorf("captured Go package is unavailable")
	}
	if pkg := importer.unknown[importPath]; pkg != nil {
		return pkg, nil
	}
	if len(importer.unknown) >= maxGoSemanticFiles {
		return nil, fmt.Errorf("unresolved import package limit reached")
	}
	name := path.Base(importPath)
	if name == "." || name == "/" || name == "" {
		name = "unresolved"
	}
	pkg := types.NewPackage(importPath, name)
	pkg.MarkComplete()
	importer.unknown[importPath] = pkg
	return pkg, nil
}

func (importer *goSemanticImporter) ImportFrom(importPath, dir string, mode types.ImportMode) (*types.Package, error) {
	return importer.Import(importPath)
}
