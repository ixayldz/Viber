package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// CreateIndependentEvaluator imports the already verified CAS capture directly.
// Recapturing a materialized directory would change original permission/Git
// metadata and could silently omit files. No current live bytes are hydrated.
func (s *Session) CreateIndependentEvaluator(ctx context.Context, original *Session, source EvaluationSource, options StartOptions) (c.TaskState, error) {
	return s.createIndependentEvaluator(ctx, original, source, options, nil)
}

// CreateRegisteredIndependentEvaluator binds a prestarted fixed assignment
// into the durable owner document before any call to Run can dispatch work.
func (s *Session) CreateRegisteredIndependentEvaluator(ctx context.Context, original *Session, source EvaluationSource, options StartOptions, assignment EvaluationAssignmentBinding) (c.TaskState, error) {
	if err := assignment.Validate(); err != nil || assignment.Task != source.TaskID {
		return c.TaskState{}, c.Fail(c.PolicyDenied, "registered evaluation assignment does not match its frozen source")
	}
	return s.createIndependentEvaluator(ctx, original, source, options, &assignment)
}

func (s *Session) createIndependentEvaluator(ctx context.Context, original *Session, source EvaluationSource, options StartOptions, assignment *EvaluationAssignmentBinding) (c.TaskState, error) {
	if original == nil || s == original || !fileguard.Disjoint(s.directory, original.directory) || options.Runtime != nil || options.TaskKind != "ANALYSIS" || options.Config != nil || options.GoalReview != nil || options.inheritedSpec != nil || options.evaluationCapture != nil || options.Git || options.AttemptOrigin != nil || !samePrivacyAuthority(s.privacy, original.privacy) || options.TaskID != s.EvaluationTaskID() {
		return c.TaskState{}, c.Fail(c.PolicyDenied, "separate local immutable evaluator required")
	}
	if assignment != nil && (assignment.Validate() != nil || assignment.Task != source.TaskID) {
		return c.TaskState{}, c.Fail(c.PolicyDenied, "invalid preregistered evaluator assignment")
	}
	original.mu.Lock()
	defer original.mu.Unlock()
	state, doc, err := original.Load(ctx, source.TaskID)
	if err != nil {
		return state, err
	}
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return state, err
	}
	if state.Execution != c.Terminated || state.InputBarrier || len(state.PendingInputIDs) != 0 || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(state) != nil || !tokensQuiescent(state) || state.TaskSeq != source.TaskSequence || state.DocumentDigest != source.DocumentDigest || doc.Candidate.SnapshotDigest != source.CandidateDigest {
		return c.TaskState{}, c.Fail(c.Conflict, "original evaluator binding changed before capture import")
	}
	capture, err := original.Archive.Get(doc.Candidate)
	if err != nil {
		return c.TaskState{}, err
	}
	expected, err := evaluationSourceBinding(state, doc, capture)
	if err != nil {
		return c.TaskState{}, err
	}
	expectedDigest, err := c.Digest(expected)
	if err != nil {
		return c.TaskState{}, err
	}
	suppliedDigest, err := c.Digest(source)
	if err != nil {
		return c.TaskState{}, err
	}
	if expectedDigest != suppliedDigest {
		return c.TaskState{}, c.Fail(c.Conflict, "original frozen source metadata differs from journal")
	}
	directory, err := fileguard.ResolveProspective(s.directory)
	if err != nil {
		return c.TaskState{}, err
	}
	var digest string
	if assignment == nil {
		digest, err = c.Digest(struct {
			Source  EvaluationSource
			Options StartOptions
		}{source, options})
	} else {
		digest, err = c.Digest(struct {
			Source     EvaluationSource
			Options    StartOptions
			Assignment EvaluationAssignmentBinding
		}{source, options, *assignment})
	}
	if err != nil {
		return c.TaskState{}, err
	}
	bound := privacyAttempt{Scope: privacyScope{InstanceID: s.instance.ID, Directory: directory, PhysicalRoot: s.instance.PhysicalRoot}, ParentTask: source.TaskID, ParentSequence: source.TaskSequence, ParentDocument: source.DocumentDigest, ChildTask: options.TaskID, RequestDigest: digest}
	if err = bound.validate(); err != nil {
		return c.TaskState{}, err
	}
	if err = s.registerEvaluationLineage(ctx, bound); err != nil {
		return c.TaskState{}, err
	}
	options.evaluationOrigin = &bound
	options.evaluationAssignment = assignment
	options.Root = capture.Snapshot.Root
	options.evaluationCapture = &capture
	return s.Create(ctx, options)
}
