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
	cooperativeTypes := f.Bool("cooperative-types", false, "opt in to synchronous host go/types analysis without a hard CPU/RSS bound")
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
	options := intelligence.GoSemanticOptions{
		GOOS: *goos, GOARCH: *goarch, Compiler: *compiler, GoVersion: *version,
		BuildTags: []string(tags), BuildTagsComplete: *complete, CGOEnabled: *cgo,
	}
	var graph intelligence.GoSemanticGraph
	if *cooperativeTypes {
		graph, err = intelligence.BuildGoSemanticGraph(ctx, capture, options)
	} else {
		graph, err = intelligence.BuildGoSemanticHintsGraph(ctx, capture, options)
	}
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
		}{graph, selection, func() string {
			if *cooperativeTypes {
				return "SOURCE_BOUND_COOPERATIVE_GO_TYPES; CPU_RSS_NOT_HARD_BOUNDED; NOT_RUNTIME_COVERAGE_OR_VERIFICATION"
			}
			return "SOURCE_BOUND_PARSE_ONLY; GO_TYPES_SKIPPED; PARTIAL_AND_CHECKS_WIDENED; NOT_RUNTIME_COVERAGE_OR_VERIFICATION"
		}()}); err != nil {
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
	if *cooperativeTypes {
		_, err = fmt.Fprintln(out, "Captured source only. Cooperative go/types analysis has no hard host CPU/RSS bound; unresolved imports/build context widen selection; this is not runtime coverage or a verification verdict.")
	} else {
		_, err = fmt.Fprintln(out, "Captured source parse/import hints only; go/types skipped, coverage partial and check selection widened. This is not runtime coverage or a verification verdict.")
	}
	if err != nil {
		return 4
	}
	return 0
}
