package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/retrieval"
	"github.com/ixayldz/Viber/internal/workspace"
)

func rankedReadPage(ctx context.Context, candidate workspace.Capture, layers []policy.Policy, action policy.Action, args pageArgs, cursor pageCursor, cache *retrieval.Cache, scope string) (any, error) {
	if args.Query == "" || len(args.Query) > 256 || !utf8.ValidString(args.Query) || strings.ContainsRune(args.Query, 0) {
		return nil, c.Fail(c.InvalidArgument, "bounded UTF-8 retrieval query required")
	}
	if err := policy.Admit(layers, action); err != nil {
		return nil, err
	}
	policyHash, err := c.Digest(layers)
	if err != nil {
		return nil, err
	}
	documents := []retrieval.Document{}
	for _, entry := range candidate.Snapshot.Entries {
		action.Path = entry.Path
		if policy.Admit(layers, action) != nil {
			continue
		}
		raw, ok := candidate.Contents[entry.Path]
		if !ok || int64(len(raw)) != entry.Size || c.HashBytes(raw) != entry.Hash {
			return nil, c.Fail(c.StoreIntegrityError, "retrieval source unavailable or changed")
		}
		documents = append(documents, retrieval.Document{Path: entry.Path, Digest: entry.Hash, Bytes: raw})
	}
	binding := retrieval.Binding{Candidate: candidate.Snapshot.Digest, Policy: policyHash}
	buildCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	var index *retrieval.Index
	var evidence *retrieval.CacheEvidence
	var release func()
	if cache != nil {
		var measured retrieval.CacheEvidence
		index, measured, release, err = cache.Acquire(buildCtx, scope, binding, documents)
		evidence = &measured
	} else {
		index, err = retrieval.New(buildCtx, binding, documents)
		if err == nil {
			release = func() { index.Close() }
		}
	}
	buildFailure := buildCtx.Err()
	cancel()
	if err != nil {
		if buildFailure != nil {
			err = buildFailure
		}
		var failure *c.Error
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &failure) || failure.Code != c.UnsupportedCapability) {
			return nil, err
		}
		// Fallback uses the original parent budget. A bound ranked cursor is never
		// reinterpreted as a lexical offset: restart with a new literal page instead.
		if args.Cursor != "" {
			return nil, c.Fail(c.StaleBase, "ranked index unavailable; start a new literal search")
		}
		raw, _ := json.Marshal(map[string]any{"query": args.Query, "limit": args.Limit, "candidate_digest": args.Candidate})
		literal, err := nativeReadPage(ctx, candidate, layers, c.TaskState{PolicyEpoch: action.Epoch, KernelGeneration: action.Generation, InputBarrier: action.InputBarrier}, model.Call{Name: "fs_search", Arguments: raw})
		if err != nil {
			return nil, err
		}
		return struct {
			Fallback bool   `json:"fallback_used"`
			Reason   string `json:"reason"`
			Literal  any    `json:"literal"`
		}{true, "INDEX_UNAVAILABLE_WITHIN_BOUNDED_PROFILE", literal}, nil
	}
	defer release()
	queryCtx, queryCancel := context.WithTimeout(ctx, 3*time.Second)
	defer queryCancel()
	result, err := index.Search(queryCtx, binding, args.Query, args.Intent, int(cursor.Position), int(args.Limit))
	if err != nil {
		return nil, err
	}
	result.Cache = evidence
	extent, err := coverage(cursor, cursor.Position+int64(len(result.Hits)), int64(result.Total))
	if err != nil {
		return nil, err
	}
	return struct {
		Retrieval retrieval.Result `json:"retrieval"`
		Coverage  pageCoverage     `json:"coverage"`
		Scope     string           `json:"scope"`
	}{result, extent, "CAPTURED_POLICY_SCOPE; RANKED_POOL_IS_NOT_ABSENCE_PROOF"}, nil
}
