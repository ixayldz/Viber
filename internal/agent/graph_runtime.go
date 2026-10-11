package agent

import (
	"context"
	"encoding/json"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/intelligence"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

func graphTool() model.Tool {
	return model.Tool{Name: "fs_graph", Description: "Page type-resolved Go graph hints from exact captured source. Never loads host imports or executes code. Only available when every captured path is readable in the current policy/plan scope. Build context is declared analysis input, not measured runtime coverage. Missing imports/build constraints widen checks. Pin candidate_digest and graph_fingerprint on later pages; node_offset and edge_offset are separate cursors.", Parameters: json.RawMessage(`{"type":"object","properties":{"candidate_digest":{"type":"string"},"graph_fingerprint":{"type":"string"},"goos":{"type":"string"},"goarch":{"type":"string"},"compiler":{"type":"string"},"go_version":{"type":"string"},"build_tags":{"type":"array","items":{"type":"string"}},"tags_complete":{"type":"boolean"},"cgo":{"type":"string","enum":["ENABLED","DISABLED","UNKNOWN"]},"node_offset":{"type":"integer","minimum":0},"edge_offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":64}},"additionalProperties":false}`)}
}

func nativeGoGraph(ctx context.Context, candidate workspace.Capture, layers []policy.Policy, state c.TaskState, call model.Call) (any, error) {
	var args struct {
		Candidate    string   `json:"candidate_digest,omitempty"`
		Fingerprint  string   `json:"graph_fingerprint,omitempty"`
		GOOS         string   `json:"goos,omitempty"`
		GOARCH       string   `json:"goarch,omitempty"`
		Compiler     string   `json:"compiler,omitempty"`
		Version      string   `json:"go_version,omitempty"`
		Tags         []string `json:"build_tags,omitempty"`
		TagsComplete bool     `json:"tags_complete,omitempty"`
		CGO          string   `json:"cgo,omitempty"`
		NodeOffset   int      `json:"node_offset,omitempty"`
		EdgeOffset   int      `json:"edge_offset,omitempty"`
		Limit        int      `json:"limit,omitempty"`
	}
	if err := c.DecodeStrict(call.Arguments, &args); err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = 32
	}
	if args.NodeOffset < 0 || args.EdgeOffset < 0 || args.Limit < 1 || args.Limit > 64 || len(args.Tags) > 64 {
		return nil, c.Fail(c.InvalidArgument, "bounded graph offsets/options required")
	}
	if args.Candidate != "" && args.Candidate != candidate.Snapshot.Digest {
		return nil, c.Fail(c.StaleBase, "graph candidate changed")
	}
	if (args.NodeOffset > 0 || args.EdgeOffset > 0) && (args.Candidate == "" || args.Fingerprint == "") {
		return nil, c.Fail(c.StaleBase, "later graph pages require exact candidate and graph fingerprint")
	}
	action := policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "snapshot.read"}
	if err := policy.Admit(layers, action); err != nil {
		return nil, err
	}
	// No filtered-source forgery: until a separate policy-derived capture is
	// available, a cross-file graph needs authority for every captured file.
	for _, entry := range candidate.Snapshot.Entries {
		action.Path = entry.Path
		if err := policy.Admit(layers, action); err != nil {
			return nil, c.Fail(c.PolicyDenied, "cross-file graph is outside the current source read scope")
		}
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	graph, err := intelligence.BuildGoSemanticGraph(queryCtx, candidate, intelligence.GoSemanticOptions{
		GOOS: args.GOOS, GOARCH: args.GOARCH, Compiler: args.Compiler, GoVersion: args.Version,
		BuildTags: args.Tags, BuildTagsComplete: args.TagsComplete, CGOEnabled: args.CGO,
	})
	if err != nil {
		return nil, err
	}
	if args.Fingerprint != "" && args.Fingerprint != graph.Fingerprint {
		return nil, c.Fail(c.StaleBase, "graph build context/source changed")
	}
	if args.NodeOffset > len(graph.Nodes) || args.EdgeOffset > len(graph.Edges) {
		return nil, c.Fail(c.StaleBase, "graph offset beyond captured extent")
	}
	nodeEnd, edgeEnd := min(args.NodeOffset+args.Limit, len(graph.Nodes)), min(args.EdgeOffset+args.Limit, len(graph.Edges))
	result := struct {
		Candidate    string                          `json:"candidate_digest"`
		Fingerprint  string                          `json:"graph_fingerprint"`
		BuildContext intelligence.GoSemanticOptions  `json:"declared_build_context"`
		Coverage     intelligence.GoSemanticCoverage `json:"coverage"`
		Nodes        []intelligence.GoSemanticNode   `json:"nodes"`
		Edges        []intelligence.GoSemanticEdge   `json:"edges"`
		NodeTotal    int                             `json:"node_total"`
		EdgeTotal    int                             `json:"edge_total"`
		NextNode     int                             `json:"next_node_offset"`
		NextEdge     int                             `json:"next_edge_offset"`
		Complete     bool                            `json:"complete_page_coverage"`
		Trust        string                          `json:"trust"`
	}{candidate.Snapshot.Digest, graph.Fingerprint, graph.BuildContext, graph.Coverage, graph.Nodes[args.NodeOffset:nodeEnd], graph.Edges[args.EdgeOffset:edgeEnd], len(graph.Nodes), len(graph.Edges), nodeEnd, edgeEnd, nodeEnd == len(graph.Nodes) && edgeEnd == len(graph.Edges), "SOURCE_BOUND_ANALYSIS_HINT; NOT_RUNTIME_COVERAGE_OR_VERIFICATION"}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, c.Fail(c.BudgetLimitReached, "graph page/coverage exceeds native output bound")
	}
	return result, nil
}
