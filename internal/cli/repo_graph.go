package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/intelligence"
	"github.com/ixayldz/Viber/internal/workspace"
)

type graphValues []string

func (values *graphValues) String() string { return strings.Join(*values, ",") }
func (values *graphValues) Set(raw string) error {
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 || len(*values) >= 128 {
			return c.Fail(c.InvalidArgument, "bounded nonempty graph option required")
		}
		*values = append(*values, value)
	}
	return nil
}

func runRepoGraph(args []string, out, errout io.Writer) int {
	f := flags("repo-graph", errout)
	root := f.String("root", ".", "source directory; captured bytes only")
	goos := f.String("goos", "", "declared Go target OS; empty means unknown")
	goarch := f.String("goarch", "", "declared Go target architecture; empty means unknown")
	compiler := f.String("compiler", "", "declared Go compiler; empty means unknown")
	version := f.String("go-version", "", "target compiler release tags; default analyzer version, language semantics from captured go.mod")
	complete := f.Bool("tags-complete", false, "declare that the supplied build tag inventory is complete")
	cgo := f.String("cgo", "UNKNOWN", "ENABLED, DISABLED or UNKNOWN; does not execute cgo")
	jsonMode := f.Bool("json", false, "source-bound graph and conservative selection")
	var tags, changed graphValues
	f.Var(&tags, "tags", "build tags, comma separated or repeated")
	f.Var(&changed, "changed", "changed relative source paths, comma separated or repeated")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *root == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "source root and no extra arguments required"), *jsonMode)
	}
	capture, err := workspace.CaptureDirectory(*root, workspace.DefaultLimits())
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	graph, err := intelligence.BuildGoSemanticGraph(ctx, capture, intelligence.GoSemanticOptions{
		GOOS: *goos, GOARCH: *goarch, Compiler: *compiler, GoVersion: *version,
		BuildTags: []string(tags), BuildTagsComplete: *complete, CGOEnabled: *cgo,
	})
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var selection *intelligence.GoTestSelection
	if len(changed) > 0 {
		value, err := intelligence.SelectGoTests(graph, []string(changed))
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		selection = &value
	}
	if *jsonMode {
		if err := jsonWrite(out, struct {
			Graph     intelligence.GoSemanticGraph  `json:"graph"`
			Selection *intelligence.GoTestSelection `json:"test_selection,omitempty"`
			Trust     string                        `json:"trust"`
		}{graph, selection, "SOURCE_BOUND_ANALYSIS; NOT_RUNTIME_COVERAGE_OR_VERIFICATION"}); err != nil {
			return 4
		}
		return 0
	}
	if _, err := fmt.Fprintf(out, "Go source graph: %d nodes, %d edges, coverage %s\nSource %s\nGraph %s\n", len(graph.Nodes), len(graph.Edges), graph.Coverage.Overall, graph.SourceSnapshotDigest, graph.Fingerprint); err != nil {
		return 4
	}
	if selection != nil {
		if _, err := fmt.Fprintf(out, "Selected test files: %d; widen to all checks: %t\n", len(selection.SelectedTestPaths), selection.WidenToAllChecks); err != nil {
			return 4
		}
	}
	_, err = fmt.Fprintln(out, "Captured source only. Unresolved imports/build context widen selection; this is not runtime coverage or a verification verdict.")
	if err != nil {
		return 4
	}
	return 0
}
