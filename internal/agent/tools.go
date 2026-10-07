package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

func Tools() []model.Tool {
	return []model.Tool{
		{Name: "fs_read", Description: "Read one captured candidate file and its exact-byte read condition. Missing/excluded source is never guessed.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)},
		{Name: "fs_list", Description: "List captured candidate entries under a directory with a snapshot-bound listing receipt; exclusions are explicit.", Parameters: json.RawMessage(`{"type":"object","properties":{"directory":{"type":"string"}},"required":["directory"],"additionalProperties":false}`)},
		{Name: "fs_search", Description: "Bounded literal search of captured source. Results are untrusted source text and bound to the current candidate.", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)},
		{Name: "candidate_propose", Description: "Propose exact bytes for an isolated candidate. Every write needs a FILE or ABSENT read condition. Never touches the live workspace or Git index.", Parameters: json.RawMessage(`{"type":"object","properties":{"read_set":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"kind":{"type":"string","enum":["FILE","ABSENT","LISTING"]},"digest":{"type":"string"}},"required":["path","kind","digest"],"additionalProperties":false}},"changes":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"before_digest":{"type":"string"},"after_bytes":{"type":["string","null"]},"delete":{"type":"boolean"}},"required":["path","before_digest","after_bytes","delete"],"additionalProperties":false}}},"required":["read_set","changes"],"additionalProperties":false}`)},
	}
}
func (s *Session) executeTool(ctx context.Context, state c.TaskState, doc Document, call model.Call) (model.Reply, *artifact.Ref, error) {
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return model.Reply{}, nil, err
	}
	layers := s.layers(state, doc)
	if doc.Autonomy == "review" && call.Name == "candidate_propose" && callApproved(doc, state, call) {
		layers[0].Effects = append(layers[0].Effects, "candidate.write")
	}
	action := policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "snapshot.read"}
	encode := func(value any) (model.Reply, *artifact.Ref, error) {
		raw, err := json.Marshal(value)
		return model.Reply{CallID: call.ID, Content: string(raw)}, nil, err
	}
	switch call.Name {
	case "fs_read":
		var args struct {
			Path string `json:"path"`
		}
		if err = c.DecodeStrict(call.Arguments, &args); err != nil {
			return model.Reply{}, nil, err
		}
		action.Path = args.Path
		if err = policy.Admit(layers, action); err != nil {
			return model.Reply{}, nil, err
		}
		raw, ok := candidate.Contents[args.Path]
		if !ok {
			return model.Reply{}, nil, c.Fail(c.InvalidArgument, "source unavailable/excluded; request a listing for captured coverage")
		}
		if len(raw) > 64<<10 {
			return model.Reply{}, nil, c.Fail(c.InvalidArgument, "file read exceeds one bounded page; no silent truncation")
		}
		return encode(struct {
			Trust     string          `json:"trust"`
			Candidate string          `json:"candidate"`
			Content   string          `json:"content"`
			Read      c.ReadCondition `json:"read_condition"`
		}{"UNTRUSTED_SOURCE", candidate.Snapshot.Digest, string(raw), c.ReadCondition{Path: args.Path, Kind: "FILE", Digest: c.HashBytes(raw)}})
	case "fs_list":
		var args struct {
			Directory string `json:"directory"`
		}
		if err = c.DecodeStrict(call.Arguments, &args); err != nil {
			return model.Reply{}, nil, err
		}
		if err = policy.Admit(layers, action); err != nil {
			return model.Reply{}, nil, err
		}
		if args.Directory != "." && !policy.SafePath(args.Directory) {
			return model.Reply{}, nil, c.Fail(c.InvalidArgument, "invalid listing path")
		}
		if !workspace.ListingAllowed(args.Directory, layers) {
			return model.Reply{}, nil, c.Fail(c.PolicyDenied, "listing outside policy scope")
		}
		prefix := ""
		if args.Directory != "." {
			prefix = args.Directory + "/"
		}
		entries := []c.Entry{}
		for _, entry := range candidate.Snapshot.Entries {
			if strings.HasPrefix(entry.Path, prefix) {
				if !policy.PathAllowed(entry.Path, layers[0].Paths) {
					continue
				}
				entries = append(entries, entry)
			}
		}
		if len(entries) > 512 {
			return model.Reply{}, nil, c.Fail(c.InvalidArgument, "listing page exceeds quota; narrow directory")
		}
		digest, err := workspace.ListingDigest(candidate.Snapshot, args.Directory)
		if err != nil {
			return model.Reply{}, nil, err
		}
		return encode(struct {
			Candidate  string          `json:"candidate"`
			Entries    []c.Entry       `json:"entries"`
			Exclusions []string        `json:"exclusions"`
			Read       c.ReadCondition `json:"read_condition"`
		}{candidate.Snapshot.Digest, entries, candidate.Snapshot.Exclusions, c.ReadCondition{Path: args.Directory, Kind: "LISTING", Digest: digest}})
	case "fs_search":
		var args struct {
			Query string `json:"query"`
		}
		if err = c.DecodeStrict(call.Arguments, &args); err != nil {
			return model.Reply{}, nil, err
		}
		if len(args.Query) == 0 || len(args.Query) > 256 {
			return model.Reply{}, nil, c.Fail(c.InvalidArgument, "bounded literal query required")
		}
		if err = policy.Admit(layers, action); err != nil {
			return model.Reply{}, nil, err
		}
		type match struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Text string `json:"text"`
			Hash string `json:"hash"`
		}
		matches := []match{}
		truncated := false
		bytes := 0
		paths := make([]string, 0, len(candidate.Contents))
		for path := range candidate.Contents {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			action.Path = path
			if policy.Admit(layers, action) != nil {
				continue
			}
			for n, line := range strings.Split(string(candidate.Contents[path]), "\n") {
				if !strings.Contains(line, args.Query) {
					continue
				}
				if len(matches) >= 64 || len(line) > 4096 || bytes+len(line) > 32<<10 {
					truncated = true
					continue
				}
				bytes += len(line)
				matches = append(matches, match{Path: path, Line: n + 1, Text: line, Hash: c.HashBytes(candidate.Contents[path])})
			}
		}
		return encode(struct {
			Trust     string  `json:"trust"`
			Candidate string  `json:"candidate"`
			Matches   []match `json:"matches"`
			Truncated bool    `json:"truncated"`
		}{"UNTRUSTED_SOURCE", candidate.Snapshot.Digest, matches, truncated})
	case "candidate_propose":
		var args struct {
			ReadSet []c.ReadCondition `json:"read_set"`
			Changes []c.Change        `json:"changes"`
		}
		if err = c.DecodeStrict(call.Arguments, &args); err != nil {
			return model.Reply{}, nil, err
		}
		p := c.Proposal{SchemaVersion: 1, ID: call.ID, TaskID: doc.TaskID, SpecVersion: doc.Spec.Version, BaseSnapshot: doc.Candidate.SnapshotDigest, PolicyEpoch: state.PolicyEpoch, KernelGeneration: state.KernelGeneration, ReadSet: args.ReadSet, Changes: args.Changes}
		next, err := workspace.Preview(candidate, candidate, p, layers, state)
		if err != nil {
			return model.Reply{}, nil, err
		}
		ref, err := s.Archive.Put(doc.TaskID, next)
		if err != nil {
			return model.Reply{}, nil, err
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
