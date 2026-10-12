package intelligence

import (
	"context"
	"encoding/json"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
)

const GoSemanticWorkerProtocol = "VIBER_GO_GRAPH_HELPER_V1"
const GoSemanticWorkerMaxInput = 64 << 10
const GoSemanticWorkerMaxOutput = 8 << 20

type GoSemanticWorkerRequest struct {
	Protocol        string            `json:"protocol"`
	CandidateDigest string            `json:"candidate_digest"`
	Options         GoSemanticOptions `json:"options"`
}

type GoSemanticWorkerResponse struct {
	Protocol      string          `json:"protocol"`
	TypeCheckMode string          `json:"typecheck_mode"`
	Graph         GoSemanticGraph `json:"graph"`
}

const GoSemanticWorkerTypeCheckMode = "GO_TYPES_CAPTURED_SOURCE_ONLY"

// RunGoSemanticWorker is the fixed helper protocol entry point. The caller
// supplies a read-only materialized source mount; this function only captures
// those bytes and emits bounded JSON. The host container broker supplies the
// hard CPU, memory, wall-time, process and output limits.
func RunGoSemanticWorker(ctx context.Context, sourceRoot string, requestRaw []byte) ([]byte, error) {
	if len(requestRaw) == 0 || len(requestRaw) > GoSemanticWorkerMaxInput {
		return nil, c.Fail(c.InvalidArgument, "Go graph worker request exceeds protocol bound")
	}
	var request GoSemanticWorkerRequest
	if err := c.DecodeStrict(requestRaw, &request); err != nil || request.Protocol != GoSemanticWorkerProtocol || !c.ValidDigest(request.CandidateDigest) {
		return nil, c.Fail(c.InvalidArgument, "invalid Go graph worker request")
	}
	if ctx == nil {
		return nil, c.Fail(c.InvalidArgument, "Go graph worker requires context")
	}
	source, err := workspace.CaptureDirectory(sourceRoot, workspace.DefaultLimits())
	if err != nil {
		return nil, err
	}
	graph, err := BuildGoSemanticGraphForSnapshot(ctx, source, request.Options, request.CandidateDigest)
	if err != nil {
		return nil, err
	}
	response := GoSemanticWorkerResponse{Protocol: GoSemanticWorkerProtocol, TypeCheckMode: GoSemanticWorkerTypeCheckMode, Graph: graph}
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	if len(raw) > GoSemanticWorkerMaxOutput {
		return nil, c.Fail(c.BudgetLimitReached, "Go graph worker output exceeds protocol bound")
	}
	return raw, nil
}
