package cli

import (
	"flag"
	"fmt"
	"io"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/discovery"
	"github.com/ixayldz/Viber/internal/workspace"
)

func runCheckDiscover(args []string, out, errout io.Writer) int {
	f := flag.NewFlagSet("check-discover", flag.ContinueOnError)
	f.SetOutput(errout)
	root := f.String("root", ".", "source root to capture for bounded check hints")
	jsonMode := f.Bool("json", false, "emit the source-bound discovery report")
	graphOffset := f.Int("graph-offset", 0, "first graph edge shown in text output")
	graphLimit := f.Int("graph-limit", 40, "maximum graph edges shown in text output (1..200)")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *root == "" || *graphOffset < 0 || *graphLimit < 1 || *graphLimit > 200 {
		return report(out, errout, c.Fail(c.InvalidArgument, "source root and no extra arguments required"), *jsonMode)
	}
	capture, err := workspace.CaptureDirectory(*root, workspace.DefaultLimits())
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	result, err := discovery.Discover(capture)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err := jsonWrite(out, result); err != nil {
			return 4
		}
		return 0
	}
	fmt.Fprintf(out, "Captured-source check hints · snapshot %s · verification unavailable\n", result.SnapshotDigest)
	fmt.Fprintf(out, "Framework declarations %d · test path hints %d · config files %d · graph edges %d\n", len(result.Frameworks), len(result.Tests), len(result.Configs), len(result.Graph))
	for _, framework := range result.Frameworks {
		fmt.Fprintf(out, "Framework hint %s (%s, %s)\n", framework.ID, framework.Ecosystem, framework.Finding)
	}
	fmt.Fprintf(out, "Coverage %s · %s · %s\n", result.Coverage.Overall, result.Coverage.Dependencies, result.Coverage.CrossFileResolution)
	if len(result.Graph) > 0 {
		end := min(*graphOffset+*graphLimit, len(result.Graph))
		if *graphOffset >= len(result.Graph) {
			fmt.Fprintf(out, "Graph page empty · offset %d · total %d\n", *graphOffset, len(result.Graph))
		} else {
			fmt.Fprintf(out, "Graph edges %d-%d of %d\n", *graphOffset+1, end, len(result.Graph))
			for _, edge := range result.Graph[*graphOffset:end] {
				fmt.Fprintf(out, "  %s --%s/%s--> %s\n", edge.FromPath, edge.Relation, edge.Resolution, edge.Target)
			}
			if end < len(result.Graph) {
				fmt.Fprintf(out, "More edges: --graph-offset %d\n", end)
			}
		}
	}
	fmt.Fprintln(out, "Repository framework/config code was not run; report is not an observer or a complete runtime dependency closure.")
	for _, unknown := range result.Coverage.Unknowns {
		fmt.Fprintf(out, "Unknown %s\n", unknown)
	}
	return 0
}
