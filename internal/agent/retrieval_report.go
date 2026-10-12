package agent

import (
	"bytes"
	"context"
	"sort"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/retrieval"
	"github.com/ixayldz/Viber/internal/workspace"
)

const capturedRetrievalReportKind = "VIBER_CAPTURED_FS_SEARCH_V1"

const (
	maxCapturedSearchArguments = 64 << 10
	maxCapturedSearchReply     = 1 << 20
	maxCapturedSearchMessages  = 10000
)

// CapturedRetrievalReport records exact bytes retained by the current task
// document. It does not attest to a build, run condition, timestamp, or model
// usage; those must be supplied separately by an evaluation operator.
type CapturedRetrievalReport struct {
	SchemaVersion        int    `json:"schema_version"`
	Kind                 string `json:"kind"`
	TaskID               string `json:"task_id"`
	TaskSequence         int64  `json:"task_seq"`
	JournalSequence      int64  `json:"journal_seq"`
	DocumentDigest       string `json:"document_digest"`
	KernelGeneration     int64  `json:"kernel_generation"`
	PolicyEpoch          int64  `json:"policy_epoch"`
	CallID               string `json:"call_id"`
	CandidateDigest      string `json:"candidate_digest"`
	PolicyDigest         string `json:"policy_digest"`
	AuthorizedSourceSet  string `json:"authorized_source_set_digest"`
	Query                string `json:"query"`
	Intent               string `json:"intent"`
	ArgumentsJSON        string `json:"arguments_json"`
	ArgumentsSHA256      string `json:"arguments_sha256"`
	FSSearchReply        string `json:"fs_search_reply"`
	ReplySHA256          string `json:"reply_sha256"`
	AuthorityDescription string `json:"authority_description"`
}

type capturedSearchPage struct {
	Retrieval retrieval.Result `json:"retrieval"`
	Coverage  pageCoverage     `json:"coverage"`
	Scope     string           `json:"scope"`
}

// CapturedRetrievalReport exports one exact, still-retained fs_search
// call/reply pair. It is intentionally current-document-only: compaction,
// stale bindings, current policy denial, and uncertain task effects prevent
// export rather than triggering a broad historical scan.
func (s *Session) CapturedRetrievalReport(ctx context.Context, task, callID string) (CapturedRetrievalReport, error) {
	var empty CapturedRetrievalReport
	if len(callID) == 0 || len(callID) > 256 {
		return empty, c.Fail(c.InvalidArgument, "bounded fs_search call ID required")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return empty, err
	}
	if err = currentOperatorEffect(doc, "context.read"); err != nil {
		return empty, err
	}
	if state.Deletion != nil || doc.Pending != nil || doc.UnknownEffect || len(doc.PendingReplies) != 0 || state.InputBarrier || len(state.PendingInputIDs) != 0 || state.Execution == c.Running || state.Execution == c.Recovering || unresolvedResource(state) != nil || !tokensQuiescent(state) {
		return empty, c.Fail(c.Conflict, "retrieval export requires a current settled task without pending or unknown effects")
	}
	if len(doc.Compactions) != 0 || len(doc.Messages) > maxCapturedSearchMessages {
		return empty, c.Fail(c.UnsupportedCapability, "retrieval export is limited to bounded, uncompacted current messages")
	}
	for _, message := range doc.Messages {
		if message.HistoryDigest != "" {
			return empty, c.Fail(c.UnsupportedCapability, "retrieval export does not traverse compacted history")
		}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}

	callIndex := -1
	var capturedCall model.Call
	callMatches := 0
	replyMatches := 0
	var capturedReply model.Reply
	replyIndex := -1
	for i, message := range doc.Messages {
		for _, call := range message.Calls {
			if call.ID == callID {
				callMatches++
				callIndex = i
				capturedCall = call
			}
		}
		for _, reply := range message.Replies {
			if reply.CallID == callID {
				replyMatches++
				replyIndex = i
				capturedReply = reply
			}
		}
	}
	if callMatches != 1 || replyMatches != 1 || capturedCall.Name != "fs_search" || callIndex < 0 || replyIndex != callIndex+1 || replyIndex >= len(doc.Messages) || doc.Messages[callIndex].Role != "assistant" || doc.Messages[replyIndex].Role != "tool" {
		return empty, c.Fail(c.InvalidArgument, "call ID does not identify one retained paired fs_search exchange")
	}
	for _, call := range doc.Messages[callIndex].Calls {
		if call.ID == "" || call.ID == callID && call.Name != "fs_search" {
			return empty, c.Fail(c.StoreIntegrityError, "retained assistant call identity is invalid")
		}
	}
	if capturedReply.IsError {
		return empty, c.Fail(c.UnsupportedCapability, "failed fs_search replies cannot be used as ranked retrieval reports")
	}
	if len(capturedCall.Arguments) == 0 || len(capturedCall.Arguments) > maxCapturedSearchArguments || len(capturedReply.Content) == 0 || len(capturedReply.Content) > maxCapturedSearchReply {
		return empty, c.Fail(c.UnsupportedCapability, "retained fs_search exchange exceeds export bounds")
	}

	candidate, layers, policyDigest, err := s.hydrateAuthorizedRetrievalCandidate(ctx, state, doc)
	if err != nil {
		return empty, err
	}
	baseAction := policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "snapshot.read"}
	documents, err := capturedAuthorizedDocuments(ctx, candidate, layers, baseAction)
	if err != nil {
		return empty, err
	}
	globalSourceSet, err := retrievalSourceSet(documents)
	if err != nil {
		return empty, err
	}
	binding := retrieval.Binding{Candidate: candidate.Snapshot.Digest, Policy: policyDigest}
	rankingToken := ""
	if doc.Ranking != nil {
		var args pageArgs
		if err = c.DecodeStrict(capturedCall.Arguments, &args); err != nil {
			return empty, err
		}
		if args.Mode == "ranked" {
			decision, decisionErr := doc.Ranking.Current(candidate.Snapshot.Digest, args.Query)
			if decisionErr != nil {
				return empty, decisionErr
			}
			rankingToken, err = c.Digest(decision)
			if err != nil {
				return empty, err
			}
		}
	}
	args, cursor, err := parsePageWithRanking(capturedCall, candidate, layers, rankingToken)
	if err != nil {
		return empty, err
	}
	if args.Mode != "ranked" || args.Cursor != "" {
		return empty, c.Fail(c.UnsupportedCapability, "retrieval reports require an initial ranked fs_search page")
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}

	var page capturedSearchPage
	if err = c.DecodeStrict([]byte(capturedReply.Content), &page); err != nil {
		return empty, c.Fail(c.StoreIntegrityError, "retained fs_search reply is not a supported exact ranked page")
	}
	result := page.Retrieval
	if page.Scope != "CAPTURED_POLICY_SCOPE; RANKED_POOL_IS_NOT_ABSENCE_PROOF" || result.SchemaVersion != 1 || result.Trust != "UNTRUSTED_SOURCE_BOUND_RANKING_HINT" || result.Fallback || result.Manifest.Version != retrieval.Version || result.Manifest.Binding != binding || result.Query != args.Query || result.Intent != args.Intent || result.QueryMicros < 0 || result.Total < 0 || len(result.Hits) > int(args.Limit) {
		return empty, c.Fail(c.StoreIntegrityError, "retained fs_search reply does not match current query, candidate, policy, or scope")
	}

	selected := documents
	var expectedPartition *retrieval.PartitionEvidence
	sourceBytes := int64(0)
	for _, document := range documents {
		sourceBytes += int64(len(document.Bytes))
	}
	if len(documents) > retrieval.PartitionMaxDocuments || sourceBytes > retrieval.PartitionMaxBytes || args.Partition != 0 {
		plan, planErr := retrieval.PlanPartitions(ctx, binding, documents)
		if planErr != nil {
			return empty, planErr
		}
		var evidence retrieval.PartitionEvidence
		selected, evidence, planErr = plan.Select(args.Partition)
		if planErr != nil {
			return empty, planErr
		}
		expectedPartition = &evidence
	}
	if !samePartitionEvidence(result.Partition, expectedPartition) {
		return empty, c.Fail(c.StoreIntegrityError, "retained fs_search partition differs from current authorized source plan")
	}
	selectedSourceSet, err := retrievalSourceSet(selected)
	if err != nil {
		return empty, err
	}
	selectedBytes := int64(0)
	for _, document := range selected {
		selectedBytes += int64(len(document.Bytes))
	}
	if result.Manifest.SourceSet != selectedSourceSet || result.Manifest.Documents != len(selected) || result.Manifest.SourceBytes != selectedBytes || result.Manifest.SourceBytes > retrieval.MaxBytes || result.Manifest.IndexedDocuments < 0 || result.Manifest.IndexedDocuments > result.Manifest.Documents {
		return empty, c.Fail(c.StoreIntegrityError, "retained fs_search index does not match the current authorized source set")
	}
	if int64(page.Coverage.End) != int64(len(result.Hits)) || page.Coverage.Start != 0 || page.Coverage.Total != int64(result.Total) || page.Coverage.HasMore != (page.Coverage.Next != "") {
		return empty, c.Fail(c.StoreIntegrityError, "retained fs_search page coverage is inconsistent")
	}
	expectedCoverage, err := coverage(cursor, int64(len(result.Hits)), int64(result.Total))
	if err != nil || expectedCoverage != page.Coverage {
		return empty, c.Fail(c.StoreIntegrityError, "retained fs_search continuation is not bound to the exact call")
	}
	selectedByPath := make(map[string]retrieval.Document, len(selected))
	for _, document := range selected {
		selectedByPath[document.Path] = document
	}
	for _, hit := range result.Hits {
		if err = ctx.Err(); err != nil {
			return empty, err
		}
		document, ok := selectedByPath[hit.Path]
		if !ok || hit.FileDigest != document.Digest || hit.Start < 0 || hit.End <= hit.Start || hit.End > len(document.Bytes) || hit.Line < 1 || hit.Line != bytes.Count(document.Bytes[:hit.Start], []byte{'\n'})+1 || len(hit.Bytes) != hit.End-hit.Start || !bytes.Equal(hit.Bytes, document.Bytes[hit.Start:hit.End]) {
			return empty, c.Fail(c.StoreIntegrityError, "retained fs_search hit is not exact current authorized source")
		}
	}
	// Re-read task authority before returning. A concurrent task mutation or
	// operator-policy change must not leave this report labeled current.
	currentState, currentDoc, err := s.Load(ctx, task)
	if err != nil {
		return empty, err
	}
	if currentState.TaskSeq != state.TaskSeq || currentState.DocumentDigest != state.DocumentDigest || currentState.CandidateDigest != state.CandidateDigest || currentState.PolicyEpoch != state.PolicyEpoch || currentState.KernelGeneration != state.KernelGeneration || currentState.InputBarrier || currentDoc.Pending != nil || currentDoc.UnknownEffect || len(currentDoc.PendingReplies) != 0 || currentState.Deletion != nil {
		return empty, c.Fail(c.StaleAuthority, "task authority changed while preparing the retrieval export")
	}
	if err = currentOperatorEffect(currentDoc, "context.read"); err != nil {
		return empty, err
	}
	currentLayers := s.layers(currentState, currentDoc)
	if currentDoc.Plan != nil {
		if node := currentDoc.Plan.Active(); node != nil {
			currentLayers = append(currentLayers, policy.Policy{SchemaVersion: 1, Epoch: currentState.PolicyEpoch, Generation: currentState.KernelGeneration, Effects: []string{"snapshot.read"}, Paths: node.ReadScope})
		}
	}
	currentPolicyDigest, err := c.Digest(currentLayers)
	if err != nil || currentPolicyDigest != policyDigest {
		return empty, c.Fail(c.StaleAuthority, "current retrieval read scope changed while preparing the export")
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	return CapturedRetrievalReport{
		SchemaVersion: 1, Kind: capturedRetrievalReportKind, TaskID: task,
		TaskSequence: currentState.TaskSeq, JournalSequence: currentState.StoreSeq,
		DocumentDigest: state.DocumentDigest, KernelGeneration: state.KernelGeneration,
		PolicyEpoch: state.PolicyEpoch, CallID: callID,
		CandidateDigest: candidate.Snapshot.Digest, PolicyDigest: policyDigest,
		AuthorizedSourceSet: globalSourceSet, Query: args.Query, Intent: args.Intent,
		ArgumentsJSON: string(capturedCall.Arguments), ArgumentsSHA256: c.HashBytes(capturedCall.Arguments),
		FSSearchReply: capturedReply.Content, ReplySHA256: c.HashBytes([]byte(capturedReply.Content)),
		AuthorityDescription: "CURRENT_RETAINED_TASK_CALL_AND_PAIRED_REPLY; CURRENT_CANDIDATE_POLICY_AND_AUTHORIZED_SOURCE_SET_RECHECKED; NOT_BUILD_OR_RUN_ATTESTATION",
	}, nil
}

// hydrateAuthorizedRetrievalCandidate rehydrates the current candidate and
// the exact policy layers that govern snapshot reads, including an active
// plan node's read scope. Only policy-admitted entries are hydrated, so a
// privacy-purged or denied blob is never read; the snapshot manifest and its
// digest stay original to preserve the retained reply binding. It returns
// the digest of those layers so callers can detect authority drift against a
// later re-check.
func (s *Session) hydrateAuthorizedRetrievalCandidate(ctx context.Context, state c.TaskState, doc Document) (workspace.Capture, []policy.Policy, string, error) {
	if err := ctx.Err(); err != nil {
		return workspace.Capture{}, nil, "", err
	}
	manifest, err := s.Archive.ReadManifest(doc.Candidate)
	if err != nil {
		return workspace.Capture{}, nil, "", err
	}
	layers := s.layers(state, doc)
	if doc.Plan != nil {
		if node := doc.Plan.Active(); node != nil {
			layers = append(layers, policy.Policy{SchemaVersion: 1, Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Effects: []string{"snapshot.read"}, Paths: node.ReadScope})
		}
	}
	policyDigest, err := c.Digest(layers)
	if err != nil {
		return workspace.Capture{}, nil, "", err
	}
	action := policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "snapshot.read"}
	capture := workspace.Capture{Snapshot: manifest.Snapshot, Contents: make(map[string][]byte, len(manifest.Snapshot.Entries))}
	var total int64
	for i, entry := range manifest.Snapshot.Entries {
		if i&127 == 0 {
			if err = ctx.Err(); err != nil {
				return workspace.Capture{}, nil, "", err
			}
		}
		pathAction := action
		pathAction.Path = entry.Path
		if policy.Admit(layers, pathAction) != nil {
			continue
		}
		data, err := s.Archive.GetBytes(doc.TaskID, entry.Hash)
		if err != nil {
			return workspace.Capture{}, nil, "", err
		}
		if int64(len(data)) != entry.Size || c.HashBytes(data) != entry.Hash {
			return workspace.Capture{}, nil, "", c.Fail(c.StoreIntegrityError, "authorized candidate bytes failed integrity checks during retrieval report hydration")
		}
		total += int64(len(data))
		if total > retrieval.PartitionMaxCount*retrieval.PartitionMaxBytes {
			return workspace.Capture{}, nil, "", c.Fail(c.UnsupportedCapability, "authorized candidate sources exceed the retrieval report hydration bound")
		}
		capture.Contents[entry.Path] = data
	}
	if err = ctx.Err(); err != nil {
		return workspace.Capture{}, nil, "", err
	}
	return capture, layers, policyDigest, nil
}

func capturedAuthorizedDocuments(ctx context.Context, capture workspace.Capture, layers []policy.Policy, action policy.Action) ([]retrieval.Document, error) {
	entries := append([]c.Entry{}, capture.Snapshot.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	documents := make([]retrieval.Document, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pathAction := action
		pathAction.Path = entry.Path
		if policy.Admit(layers, pathAction) != nil {
			continue
		}
		raw, ok := capture.Contents[entry.Path]
		if !ok || int64(len(raw)) != entry.Size || c.HashBytes(raw) != entry.Hash {
			return nil, c.Fail(c.StoreIntegrityError, "current authorized retrieval source is unavailable or changed")
		}
		documents = append(documents, retrieval.Document{Path: entry.Path, Digest: entry.Hash, Bytes: raw})
	}
	return documents, nil
}

func retrievalSourceSet(documents []retrieval.Document) (string, error) {
	ordered := append([]retrieval.Document{}, documents...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	refs := make([]struct{ Path, Digest string }, 0, len(ordered))
	previous := ""
	for _, document := range ordered {
		if document.Path <= previous || !policy.SafePath(document.Path) || !c.ValidDigest(document.Digest) || c.HashBytes(document.Bytes) != document.Digest {
			return "", c.Fail(c.StoreIntegrityError, "current retrieval source set is invalid")
		}
		refs = append(refs, struct{ Path, Digest string }{document.Path, document.Digest})
		previous = document.Path
	}
	return c.Digest(refs)
}

func samePartitionEvidence(actual, expected *retrieval.PartitionEvidence) bool {
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	a, errA := c.Digest(actual)
	b, errB := c.Digest(expected)
	return errA == nil && errB == nil && a == b
}
