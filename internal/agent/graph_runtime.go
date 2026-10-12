package agent

import (
	"context"
	"encoding/json"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/intelligence"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/workspace"
)

func graphTool() model.Tool {
	return model.Tool{Name: "fs_graph", Description: "Page source-bound Go graph hints from exact captured bytes. Without an operator-pinned isolated helper this uses bounded parsing/import hints only, skips go/types, marks coverage partial, and widens checks. The pinned helper can emit type-resolved edges inside its bounded sandbox. No host imports or source code execution. Build context is declared input, not measured runtime coverage. Pin candidate_digest and graph_fingerprint on later pages; node_offset and edge_offset are separate cursors.", Parameters: json.RawMessage(`{"type":"object","properties":{"candidate_digest":{"type":"string"},"graph_fingerprint":{"type":"string"},"goos":{"type":"string"},"goarch":{"type":"string"},"compiler":{"type":"string"},"go_version":{"type":"string"},"build_tags":{"type":"array","items":{"type":"string"}},"tags_complete":{"type":"boolean"},"cgo":{"type":"string","enum":["ENABLED","DISABLED","UNKNOWN"]},"node_offset":{"type":"integer","minimum":0},"edge_offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":64}},"additionalProperties":false}`)}
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
	graph, err := intelligence.BuildGoSemanticHintsGraph(queryCtx, candidate, intelligence.GoSemanticOptions{
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
	}{candidate.Snapshot.Digest, graph.Fingerprint, graph.BuildContext, graph.Coverage, graph.Nodes[args.NodeOffset:nodeEnd], graph.Edges[args.EdgeOffset:edgeEnd], len(graph.Nodes), len(graph.Edges), nodeEnd, edgeEnd, nodeEnd == len(graph.Nodes) && edgeEnd == len(graph.Edges), "SOURCE_BOUND_PARSE_ONLY; GO_TYPES_SKIPPED; PARTIAL_AND_CHECKS_WIDENED; NOT_RUNTIME_COVERAGE_OR_VERIFICATION"}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, c.Fail(c.BudgetLimitReached, "graph page/coverage exceeds native output bound")
	}
	return result, nil
}

func (s *Session) isolatedGoGraph(ctx context.Context, state c.TaskState, doc *Document, call model.Call, layers []policy.Policy, candidate workspace.Capture) (any, error) {
	if doc.Pending == nil || doc.Pending.GoGraphIntent == nil || doc.GoGraphRuntime == nil || validateGoGraphIntent(state, *doc, call, *doc.Pending.GoGraphIntent) != nil {
		return nil, c.Fail(c.StaleAuthority, "isolated Go graph admission is absent or stale")
	}
	intent := *doc.Pending.GoGraphIntent
	args, err := decodeGoGraphQuery(call)
	if err != nil {
		return nil, err
	}
	if args.Candidate != "" && args.Candidate != doc.Candidate.SnapshotDigest {
		return nil, c.Fail(c.StaleBase, "graph candidate changed")
	}
	action := policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "snapshot.read"}
	if err = policy.Admit(layers, action); err != nil {
		return nil, err
	}
	for _, entry := range candidate.Snapshot.Entries {
		action.Path = entry.Path
		if err = policy.Admit(layers, action); err != nil {
			return nil, c.Fail(c.PolicyDenied, "cross-file graph is outside the current source read scope")
		}
	}
	manifest, err := doc.GoGraphRuntime.Read()
	if err != nil {
		return nil, err
	}
	fresh, err := s.State(ctx, doc.TaskID)
	if err != nil {
		return nil, err
	}
	if fresh.TaskSeq != state.TaskSeq || fresh.Execution != c.Running || fresh.SpecVersion != doc.Spec.Version || fresh.CandidateDigest != doc.Candidate.SnapshotDigest || fresh.KernelGeneration != s.Journal.Generation() || fresh.PolicyEpoch != state.PolicyEpoch {
		return nil, c.Fail(c.StaleAuthority, "Go graph authority changed before dispatch")
	}
	checkAction := policy.Action{Epoch: fresh.PolicyEpoch, Generation: fresh.KernelGeneration, InputBarrier: fresh.InputBarrier, Effect: "check.run"}
	if err = policy.Admit(s.layers(fresh, *doc), checkAction); err != nil {
		return nil, err
	}
	if err = currentOperatorEffect(*doc, "check.run"); err != nil {
		return nil, err
	}
	if doc.Config != nil {
		if err = doc.Config.AdmitEffect("check.run"); err != nil {
			return nil, err
		}
	}
	materialized, err := s.Archive.Materialize(doc.Candidate)
	if err != nil {
		return nil, err
	}
	if !fileguard.Disjoint(doc.GoGraphRuntime.ManifestPath, materialized.SourceDirectory) {
		return nil, c.Fail(c.PolicyDenied, "Go graph runtime and candidate source overlap")
	}
	broker, err := runner.OpenDocker()
	if err != nil {
		return nil, err
	}
	defer broker.Close()
	if err = broker.Check(ctx); err != nil {
		return nil, err
	}
	subject, err := s.leasedRunner(*doc, broker, "CHECK")
	if err != nil {
		return nil, err
	}
	invocation, err := goGraphInvocation(*doc, intent)
	if err != nil {
		return nil, err
	}
	result, runErr := subject.Run(ctx, materialized.SourceDirectory, manifest.Profile, invocation)
	if runErr != nil {
		if !result.ProcessTreeQuiescent {
			doc.UnknownEffect = true
			doc.Pending.Status = "UNKNOWN"
		}
		return nil, runErr
	}
	if result.ExitCode != 0 || result.OutputTruncated || len(result.Stdout) == 0 || int64(len(result.Stdout)) > manifest.Profile.MaxOutputBytes || len(result.Stderr) != 0 || !result.SourceReadOnly || !result.ProtectedResultChannel || !result.ProcessTreeQuiescent || len(doc.Pending.NativeSubjects) != 1 || result.Ownership == nil || *result.Ownership != doc.Pending.NativeSubjects[0] {
		if !result.ProcessTreeQuiescent {
			doc.UnknownEffect = true
			doc.Pending.Status = "UNKNOWN"
		}
		return nil, c.Fail(c.UnknownOutcome, "Go graph helper lacks bounded output and owned quiescence evidence")
	}
	var response intelligence.GoSemanticWorkerResponse
	if err = c.DecodeStrict(result.Stdout, &response); err != nil || response.Protocol != intelligence.GoSemanticWorkerProtocol || response.TypeCheckMode != intelligence.GoSemanticWorkerTypeCheckMode {
		return nil, c.Fail(c.StoreIntegrityError, "Go graph helper response protocol mismatch")
	}
	if err = intelligence.ValidateGoSemanticGraphEnvelope(response.Graph, candidate, intent.Options, doc.Candidate.SnapshotDigest); err != nil {
		return nil, err
	}
	if err = intelligence.WidenGoSemanticGraphForCapture(&response.Graph, candidate); err != nil {
		return nil, err
	}
	if args.Fingerprint != "" && args.Fingerprint != response.Graph.Fingerprint {
		return nil, c.Fail(c.StaleBase, "graph build context/source changed")
	}
	if args.NodeOffset > len(response.Graph.Nodes) || args.EdgeOffset > len(response.Graph.Edges) {
		return nil, c.Fail(c.StaleBase, "graph offset beyond captured extent")
	}
	if _, err = s.Archive.Materialize(doc.Candidate); err != nil {
		return nil, err
	}
	manifestAfter, err := doc.GoGraphRuntime.Read()
	if err != nil || manifestAfter != manifest {
		return nil, c.Fail(c.StaleAuthority, "Go graph runtime manifest changed during dispatch")
	}
	nodeEnd, edgeEnd := min(args.NodeOffset+args.Limit, len(response.Graph.Nodes)), min(args.EdgeOffset+args.Limit, len(response.Graph.Edges))
	page := struct {
		Candidate   string                          `json:"candidate_digest"`
		Fingerprint string                          `json:"graph_fingerprint"`
		Build       intelligence.GoSemanticOptions  `json:"declared_build_context"`
		Coverage    intelligence.GoSemanticCoverage `json:"coverage"`
		Nodes       []intelligence.GoSemanticNode   `json:"nodes"`
		Edges       []intelligence.GoSemanticEdge   `json:"edges"`
		NodeTotal   int                             `json:"node_total"`
		EdgeTotal   int                             `json:"edge_total"`
		NextNode    int                             `json:"next_node_offset"`
		NextEdge    int                             `json:"next_edge_offset"`
		Complete    bool                            `json:"complete_page_coverage"`
		Trust       string                          `json:"trust"`
	}{doc.Candidate.SnapshotDigest, response.Graph.Fingerprint, response.Graph.BuildContext, response.Graph.Coverage, response.Graph.Nodes[args.NodeOffset:nodeEnd], response.Graph.Edges[args.EdgeOffset:edgeEnd], len(response.Graph.Nodes), len(response.Graph.Edges), nodeEnd, edgeEnd, nodeEnd == len(response.Graph.Nodes) && edgeEnd == len(response.Graph.Edges), "PINNED_BOUNDED_SOURCE_ANALYSIS_HINT; NOT_RUNTIME_COVERAGE_OR_VERIFICATION"}
	raw, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, c.Fail(c.BudgetLimitReached, "graph page exceeds native output bound")
	}
	return page, nil
}
