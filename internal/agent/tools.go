package agent

import (
	"context"
	"encoding/json"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

func Tools() []model.Tool {
	tools := []model.Tool{
		{Name: "fs_read", Description: "Read a bounded exact-byte page of one captured file. Follow coverage.next_cursor with the same limit. Base64 exact_bytes is authoritative; content exists only for valid UTF-8. The read condition binds the entire file, not observed coverage.", Parameters: readToolParameters("fs_read")},
		{Name: "fs_list", Description: "Page captured files, directories and explicit exclusions with a bound listing condition. Follow coverage.next_cursor with the same directory/limit. Missing rows on one page are not proof of absence.", Parameters: readToolParameters("fs_list")},
		{Name: "fs_search", Description: "Page captured literal matches with exact byte offsets and bounded excerpts. Long-line excerpts explicitly declare incomplete coverage; use fs_read to hydrate exact ranges. A negative search covers only captured policy scope.", Parameters: readToolParameters("fs_search")},
		{Name: "candidate_propose", Description: "Propose exact bytes for an isolated candidate. Every write needs a FILE or ABSENT read condition. Never touches the live workspace or Git index.", Parameters: json.RawMessage(`{"type":"object","properties":{"read_set":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"kind":{"type":"string","enum":["FILE","ABSENT","LISTING"]},"digest":{"type":"string"}},"required":["path","kind","digest"],"additionalProperties":false}},"changes":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"before_digest":{"type":"string"},"after_bytes":{"type":["string","null"]},"delete":{"type":"boolean"}},"required":["path","before_digest","after_bytes","delete"],"additionalProperties":false}}},"required":["read_set","changes"],"additionalProperties":false}`)},
	}
	tools = append(tools, planTools()...)
	tools = append(tools, model.Tool{Name: "fs_outline", Description: "Page source-bound TS/JS/Python lexical declaration hints. Names/spans are captured-byte references; semantic resolution remains UNKNOWN. Unsupported languages emit explicit fallback status.", Parameters: readToolParameters("fs_outline")})
	return append(tools, checkTools()...)
}
func (s *Session) executeTool(ctx context.Context, state c.TaskState, doc *Document, call model.Call) (model.Reply, *artifact.Ref, error) {
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return model.Reply{}, nil, err
	}
	layers := s.layers(state, *doc)
	if doc.Autonomy == "review" && call.Name == "candidate_propose" && callApproved(*doc, state, call) {
		layers[0].Effects = append(layers[0].Effects, "candidate.write")
	}

	encode := func(value any) (model.Reply, *artifact.Ref, error) {
		raw, err := json.Marshal(value)
		return model.Reply{CallID: call.ID, Content: string(raw)}, nil, err
	}
	switch call.Name {
	case "check_run", "check_output":
		value, checkErr := s.executeCheck(ctx, state, doc, call)
		if checkErr != nil {
			return model.Reply{}, nil, checkErr
		}
		return encode(value)
	case "plan_propose", "plan_next", "plan_finish":
		value, planErr := s.executePlan(ctx, state, doc, call)
		if planErr != nil {
			return model.Reply{}, nil, planErr
		}
		return encode(value)
	case "fs_read", "fs_list", "fs_search", "fs_outline":
		readLayers := layers
		if node := doc.Plan.Active(); node != nil {
			readLayers = append(readLayers, policy.Policy{SchemaVersion: 1, Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Effects: []string{"snapshot.read"}, Paths: node.ReadScope})
		}
		value, err := nativeReadPage(ctx, candidate, readLayers, state, call)
		if err != nil {
			return model.Reply{}, nil, err
		}
		return encode(value)
	case "candidate_propose":
		if taskKind(*doc) == "ANALYSIS" {
			return model.Reply{}, nil, c.Fail(c.PolicyDenied, "analysis tasks cannot write candidates")
		}
		var args struct {
			ReadSet []c.ReadCondition `json:"read_set"`
			Changes []c.Change        `json:"changes"`
		}
		if err = c.DecodeStrict(call.Arguments, &args); err != nil {
			return model.Reply{}, nil, err
		}
		if err = doc.Plan.AdmitWrites(args.Changes); err != nil {
			return model.Reply{}, nil, err
		}
		if err = doc.Plan.AdmitReadSet(args.ReadSet); err != nil {
			return model.Reply{}, nil, err
		}
		p := c.Proposal{SchemaVersion: 1, ID: call.ID, TaskID: doc.TaskID, SpecVersion: doc.Spec.Version, BaseSnapshot: doc.Candidate.SnapshotDigest, PolicyEpoch: state.PolicyEpoch, KernelGeneration: state.KernelGeneration, ReadSet: args.ReadSet, Changes: args.Changes}
		next, err := workspace.Preview(candidate, candidate, p, layers, state)
		if err != nil {
			return model.Reply{}, nil, err
		}
		if err = guardProtected(*doc, next); err != nil {
			return model.Reply{}, nil, err
		}
		ref, err := s.Archive.Put(doc.TaskID, next)
		if err != nil {
			return model.Reply{}, nil, err
		}
		if doc.Plan != nil {
			doc.Plan.CurrentCandidate = ref.SnapshotDigest
		}
		reply, _, err := encode(struct {
			Candidate   string    `json:"candidate"`
			Quality     c.Quality `json:"quality"`
			LiveWritten bool      `json:"live_workspace_written"`
		}{ref.SnapshotDigest, c.Unverified, false})
		return reply, &ref, err
	default:
		return model.Reply{}, nil, c.Fail(c.UnsupportedCapability, "tool is not in the native registry")
	}
}
