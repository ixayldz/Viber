package agent

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/plan"
	"github.com/ixayldz/Viber/internal/policy"
)

type PlanWorkProduct struct {
	SchemaVersion int    `json:"schema_version"`
	Node          string `json:"node"`
	Candidate     string `json:"candidate"`
	Contract      string `json:"output_contract"`
	Output        string `json:"untrusted_output"`
	Trust         string `json:"trust"`
}

func planChecks(doc Document) []string {
	ids := []string{}
	if doc.Protection != nil {
		for _, check := range doc.Protection.Plan.Checks {
			ids = append(ids, check.ID)
		}
	}
	return ids
}
func (s *Session) validatePlan(doc Document) error {
	if doc.Plan == nil {
		return nil
	}
	if err := doc.Plan.Validate(doc.Spec, planChecks(doc), doc.Candidate.SnapshotDigest, doc.Plan.Definition.PolicyEpoch); err != nil {
		return err
	}
	if _, err := s.Archive.Get(artifact.Ref{SchemaVersion: 1, TaskID: doc.TaskID, SnapshotDigest: doc.Plan.Definition.BaseCandidate}); err != nil {
		return c.Fail(c.StoreIntegrityError, "plan base candidate unavailable")
	}
	for _, progress := range doc.Plan.Progress {
		if progress.Status == "IMPLEMENTED" {
			if _, err := s.Archive.Get(artifact.Ref{SchemaVersion: 1, TaskID: doc.TaskID, SnapshotDigest: progress.Candidate}); err != nil {
				return c.Fail(c.StoreIntegrityError, "plan node candidate unavailable")
			}
			raw, err := s.Archive.GetBytes(doc.TaskID, progress.OutputDigest)
			if err != nil {
				return c.Fail(c.StoreIntegrityError, "plan output contract artifact unavailable")
			}
			var product PlanWorkProduct
			if err = c.DecodeStrict(raw, &product); err != nil {
				return c.Fail(c.StoreIntegrityError, "invalid plan work product")
			}
			contract := ""
			for _, node := range doc.Plan.Definition.Nodes {
				if node.ID == progress.NodeID {
					contract = node.OutputContract
				}
			}
			if product.SchemaVersion != 1 || product.Node != progress.NodeID || product.Candidate != progress.Candidate || product.Contract != contract || product.Trust != "MODEL_AUTHORED_UNREVIEWED" || !utf8.ValidString(product.Output) || strings.TrimSpace(product.Output) == "" || len(product.Output) > 64<<10 {
				return c.Fail(c.StoreIntegrityError, "plan output contract/candidate binding mismatch")
			}
		}
	}
	return nil
}
func planTools() []model.Tool {
	return []model.Tool{
		{Name: "plan_propose", Description: "Propose a source-bound typed dependency plan. DEPENDS_ON must be acyclic. Contracts and required criteria cannot expand permission or claim verification.", Parameters: json.RawMessage(`{"type":"object","properties":{"schema_version":{"type":"integer"},"id":{"type":"string"},"spec_version":{"type":"integer"},"policy_epoch":{"type":"integer"},"base_candidate":{"type":"string"},"nodes":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"goal":{"type":"string"},"input_contract":{"type":"string"},"output_contract":{"type":"string"},"read_scope":{"type":"array","items":{"type":"string"}},"write_scope":{"type":"array","items":{"type":"string"}},"requirement_ids":{"type":"array","items":{"type":"string"}},"required_checks":{"type":"array","items":{"type":"string"}},"mutex":{"type":"array","items":{"type":"string"}}},"required":["id","goal","input_contract","output_contract","read_scope","write_scope","requirement_ids","required_checks","mutex"],"additionalProperties":false}},"relations":{"type":"array","items":{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"kind":{"type":"string"}},"required":["from","to","kind"],"additionalProperties":false}}},"required":["schema_version","id","spec_version","policy_epoch","base_candidate","nodes","relations"],"additionalProperties":false}`)},
		{Name: "plan_next", Description: "Select the lexicographically first dependency-ready node. At most one worker is active. Completion is not verification.", Parameters: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)},
		{Name: "plan_finish", Description: "Publish a bounded UNREVIEWED output-contract artifact for the current node/candidate and mark IMPLEMENTED. Does not mark any criterion PASS or quality VERIFIED.", Parameters: json.RawMessage(`{"type":"object","properties":{"node_id":{"type":"string"},"candidate":{"type":"string"},"output":{"type":"string"}},"required":["node_id","candidate","output"],"additionalProperties":false}`)},
	}
}
func (s *Session) executePlan(ctx context.Context, state c.TaskState, doc *Document, call model.Call) (any, error) {
	if err := policy.Admit(s.layers(state, *doc), policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "plan.update"}); err != nil {
		return nil, err
	}
	switch call.Name {
	case "plan_propose":
		if doc.Plan != nil {
			return nil, c.Fail(c.PolicyDenied, "existing plan requires an explicit scope revision before replacement")
		}
		var definition plan.Definition
		if err := c.DecodeStrict(call.Arguments, &definition); err != nil {
			return nil, err
		}
		next, err := plan.New(definition, doc.Spec, planChecks(*doc), doc.Candidate.SnapshotDigest, state.PolicyEpoch)
		if err != nil {
			return nil, err
		}
		doc.Plan = next
	case "plan_next":
		var args struct{}
		if err := c.DecodeStrict(call.Arguments, &args); err != nil {
			return nil, err
		}
		if _, err := doc.Plan.Next(); err != nil {
			return nil, err
		}
	case "plan_finish":
		var args struct {
			NodeID    string `json:"node_id"`
			Candidate string `json:"candidate"`
			Output    string `json:"output"`
		}
		if err := c.DecodeStrict(call.Arguments, &args); err != nil {
			return nil, err
		}
		if !utf8.ValidString(args.Output) || strings.TrimSpace(args.Output) == "" || len(args.Output) > 64<<10 || doc.Plan == nil || doc.Plan.Active() == nil || doc.Plan.Active().ID != args.NodeID || args.Candidate != doc.Candidate.SnapshotDigest {
			return nil, c.Fail(c.InvalidArgument, "bound active plan node and bounded output contract required")
		}
		raw, err := c.CanonicalV1(PlanWorkProduct{SchemaVersion: 1, Node: args.NodeID, Candidate: args.Candidate, Contract: doc.Plan.Active().OutputContract, Output: args.Output, Trust: "MODEL_AUTHORED_UNREVIEWED"})
		if err != nil {
			return nil, err
		}
		digest, err := s.Archive.PutBytes(doc.TaskID, raw)
		if err != nil {
			return nil, err
		}
		if err = doc.Plan.Finish(args.NodeID, digest, args.Candidate); err != nil {
			return nil, err
		}
	}
	return doc.Plan, nil
}
