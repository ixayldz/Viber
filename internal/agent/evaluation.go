package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/workspace"
)

// EvaluationSource contains only frozen operator metadata. Hidden evaluator
// inputs are never added to the original task or its model context.
type EvaluationSource struct {
	SchemaVersion         int                `json:"schema_version"`
	TaskID                string             `json:"task_id"`
	TaskSequence          int64              `json:"task_sequence"`
	DocumentDigest        string             `json:"document_digest"`
	SpecDigest            string             `json:"spec_digest"`
	InitialSnapshot       string             `json:"initial_snapshot"`
	CandidateDigest       string             `json:"candidate_digest"`
	ContentDigest         string             `json:"captured_tree_digest"`
	CheckSetDigest        string             `json:"check_set_digest"`
	FinalArtifact         string             `json:"final_artifact_digest,omitempty"`
	Execution             c.ExecutionState   `json:"execution"`
	Outcome               c.Outcome          `json:"outcome"`
	Quality               c.Quality          `json:"quality"`
	Fulfillment           c.Fulfillment      `json:"fulfillment"`
	OpenRequired          int                `json:"open_required"`
	InputBarrier          bool               `json:"input_barrier"`
	Budget                Budget             `json:"budget"`
	Resources             *c.ResourceAccount `json:"resource_account,omitempty"`
	ProviderProfileDigest string             `json:"provider_profile_digest"`
	Scope                 string             `json:"scope"`
}

func CapturedTreeDigest(capture workspace.Capture) (string, error) {
	if err := workspace.VerifyCapture(capture); err != nil {
		return "", err
	}
	return c.Digest(struct {
		Entries     []c.Entry `json:"entries"`
		Directories []string  `json:"directories"`
		Scope       string    `json:"scope"`
	}{capture.Snapshot.Entries, capture.Snapshot.Directories, "CAPTURED_FILES_AND_DIRECTORIES_V1; NO_LIVE_OR_GIT_STATE_CLAIM"})
}

// FreezeEvaluationSource requires a terminal, quiescent original task. It
// materializes only its immutable candidate, never reads today's live root.
// The caller must hold this session until the independent capture completes.
func (s *Session) FreezeEvaluationSource(ctx context.Context, task string, protectedPaths ...string) (EvaluationSource, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var binding EvaluationSource
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return binding, "", err
	}
	if state.Execution != c.Terminated || state.InputBarrier || len(state.PendingInputIDs) != 0 || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(state) != nil || !tokensQuiescent(state) {
		return binding, "", c.Fail(c.PolicyDenied, "independent evaluator requires a terminal, quiescent source task")
	}
	capture, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return binding, "", err
	}
	for _, path := range protectedPaths {
		if !fileguard.Disjoint(s.directory, path) || !fileguard.Disjoint(capture.Snapshot.Root, path) {
			return binding, "", c.Fail(c.PolicyDenied, "evaluator inputs/output must stay outside original source and store")
		}
	}
	materialized, err := s.Archive.Materialize(doc.Candidate)
	if err != nil {
		return binding, "", err
	}
	binding, err = evaluationSourceBinding(state, doc, capture)
	if err != nil {
		return binding, "", err
	}
	return binding, materialized.SourceDirectory, nil
}

// EvaluationChecks returns fully validated operator receipts. It does not
// expose hidden oracle/output data through the agent's model tool surface.
func (s *Session) EvaluationChecks(ctx context.Context, task string) (c.TaskState, Document, []CheckRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return state, doc, nil, err
	}
	runs := make([]CheckRun, 0, len(doc.CheckRuns))
	for _, ref := range doc.CheckRuns {
		run, err := s.readCheckRun(doc, ref)
		if err != nil {
			return state, doc, nil, err
		}
		runs = append(runs, run)
	}
	return state, doc, runs, nil
}

func evaluationSourceBinding(state c.TaskState, doc Document, capture workspace.Capture) (EvaluationSource, error) {
	tree, err := CapturedTreeDigest(capture)
	if err != nil {
		return EvaluationSource{}, err
	}
	spec, err := c.Digest(doc.Spec)
	if err != nil {
		return EvaluationSource{}, err
	}
	binding := EvaluationSource{SchemaVersion: 1, TaskID: doc.TaskID, TaskSequence: state.TaskSeq, DocumentDigest: state.DocumentDigest, SpecDigest: spec, InitialSnapshot: doc.Baseline.SnapshotDigest, CandidateDigest: doc.Candidate.SnapshotDigest, ContentDigest: tree, CheckSetDigest: checkSetDigest(doc), FinalArtifact: doc.FinalArtifactDigest, Execution: state.Execution, Outcome: state.Outcome, Quality: state.Quality, Fulfillment: state.Fulfillment, OpenRequired: state.OpenRequiredObligations, InputBarrier: state.InputBarrier, Budget: doc.Budget, Resources: state.Resources, ProviderProfileDigest: tokenProfile(doc), Scope: "FROZEN_CAPTURED_CANDIDATE_ONLY; NO_PREREGISTRATION_OR_LIVE_APPLY_CLAIM"}
	return binding, nil
}
