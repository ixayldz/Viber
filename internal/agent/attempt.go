package agent

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type AttemptOrigin struct {
	ParentTask     string    `json:"parent_task"`
	ParentSequence int64     `json:"parent_task_seq"`
	ParentDocument string    `json:"parent_document_digest"`
	ParentOutcome  c.Outcome `json:"parent_outcome"`
	RequestDigest  string    `json:"request_digest"`
}
type AttemptOptions struct {
	ParentTask             string `json:"parent_task"`
	NewTask                string `json:"new_task"`
	ExpectedParentSequence int64  `json:"expected_parent_task_seq"`
	Fixture                []byte `json:"fixture,omitempty"`
	AllowUnverified        bool   `json:"allow_unverified"`
}

// NewAttempt never reopens terminal state or resets its ledger. A stable new
// task ID reconciles lost creation responses. Live source is freshly captured.
func (s *Session) NewAttempt(ctx context.Context, options AttemptOptions) (c.TaskState, error) {
	if options.ParentTask == "" || options.NewTask == "" || options.NewTask == options.ParentTask || options.ExpectedParentSequence < 1 {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "distinct stable new task and exact parent sequence required")
	}
	digest, err := c.Digest(options)
	if err != nil {
		return c.TaskState{}, err
	}
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return c.TaskState{}, err
	}
	if state, found := states[options.NewTask]; found {
		_, doc, err := s.Load(ctx, options.NewTask)
		if err != nil {
			return state, err
		}
		if doc.AttemptOrigin == nil || doc.AttemptOrigin.RequestDigest != digest {
			return state, c.Fail(c.CommandIDConflict, "new task ID already used with different attempt input")
		}
		if state.Execution == c.Created || state.Execution == c.Scoping {
			s.mu.Lock()
			defer s.mu.Unlock()
			state, _, err = s.Load(ctx, options.NewTask)
			if err != nil {
				return state, err
			}
			if state.Execution != c.Created && state.Execution != c.Scoping {
				return state, nil
			}
			state, err = s.recover(ctx, state)
			if err != nil {
				return state, err
			}
			if state.Execution == c.Created || state.Execution == c.Recovering {
				state, err = s.transition(ctx, state, c.Scoping, "", "reconcile durable attempt creation")
				if err != nil {
					return state, err
				}
			}
			if state.Execution == c.Scoping {
				return s.transition(ctx, state, c.Ready, "", "attempt creation reconciled; no effect dispatched")
			}
		}
		return state, nil
	}
	parent, doc, err := s.Load(ctx, options.ParentTask)
	if err != nil {
		return parent, err
	}
	if parent.Execution != c.Terminated || parent.TaskSeq != options.ExpectedParentSequence {
		return parent, c.Fail(c.StaleRequest, "parent must be terminal at the exact requested sequence")
	}
	if doc.UnknownEffect || doc.Pending != nil {
		return parent, c.Fail(c.UnknownOutcome, "unknown parent effects require reconciliation before a new attempt")
	}
	if doc.Runtime == nil {
		if _, err = ParseFixture(options.Fixture); err != nil {
			return parent, err
		}
	} else if len(options.Fixture) != 0 {
		return parent, c.Fail(c.InvalidArgument, "local attempt cannot change runtime into a fixture")
	}
	// Preserve every raw input and source-bound requirement, rather than reducing
	// the old task into a model narrative or carrying old PASS/approvals.
	inherited := doc.Spec
	inherited.TaskID = options.NewTask
	inherited.Version = 1
	inherited.ProtectedOrigin = ""
	inherited.Inputs = append([]c.InputSource(nil), doc.Spec.Inputs...)
	inherited.Requirements = append([]c.Requirement(nil), doc.Spec.Requirements...)
	for i, input := range inherited.Inputs {
		raw, err := s.Archive.GetBytes(options.ParentTask, input.Digest)
		if err != nil {
			return parent, err
		}
		if _, err = s.Archive.PutBytes(options.NewTask, raw); err != nil {
			return parent, err
		}
		inherited.Inputs[i].PayloadRef = "blob://" + options.NewTask + "/" + input.Digest
	}
	raw, err := s.Archive.GetBytes(options.ParentTask, inherited.Inputs[0].Digest)
	if err != nil {
		return parent, err
	}
	var checkPlan []byte
	if doc.Protection != nil {
		checkPlan, err = c.CanonicalV1(doc.Protection.Plan)
		if err != nil {
			return parent, err
		}
	}
	budget := DefaultBudget()
	budget.MaxSteps = doc.Budget.MaxSteps
	budget.MaxToolCalls = doc.Budget.MaxToolCalls
	budget.MaxInputTokens = doc.Budget.MaxInputTokens
	budget.MaxOutputTokens = doc.Budget.MaxOutputTokens
	budget.MaxActiveMillis = doc.Budget.MaxActiveMillis
	origin := &AttemptOrigin{ParentTask: options.ParentTask, ParentSequence: parent.TaskSeq, ParentDocument: parent.DocumentDigest, ParentOutcome: parent.Outcome, RequestDigest: digest}
	base, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		return parent, err
	}
	return s.Create(ctx, StartOptions{Config: doc.Config, Root: base.Snapshot.Root, Git: base.Snapshot.Git != nil, Prompt: raw, TaskID: options.NewTask, Budget: budget, Autonomy: doc.Autonomy, AllowUnverified: options.AllowUnverified, Runtime: doc.Runtime, Fixture: options.Fixture, CheckPlan: checkPlan, CheckRuntime: doc.CheckRuntime, TaskKind: taskKind(doc), MaxRepairs: doc.MaxRepairs, inheritedSpec: &inherited, AttemptOrigin: origin})
}
func (s *Session) validateAttempt(doc Document) error {
	if doc.AttemptOrigin == nil {
		return nil
	}
	origin := doc.AttemptOrigin
	if origin.ParentTask == doc.TaskID || origin.ParentSequence < 1 || !c.ValidDigest(origin.ParentDocument) || !c.ValidDigest(origin.RequestDigest) {
		return c.Fail(c.StoreIntegrityError, "invalid attempt origin")
	}
	state, err := s.Journal.ReplayAt(context.Background(), origin.ParentTask, origin.ParentSequence)
	if err != nil {
		return err
	}
	if state.DocumentDigest != origin.ParentDocument || state.Outcome != origin.ParentOutcome || state.Execution != c.Terminated {
		return c.Fail(c.StoreIntegrityError, "attempt parent is not the bound terminal record")
	}
	raw, err := s.Archive.GetBytes(origin.ParentTask, origin.ParentDocument)
	if err != nil {
		return err
	}
	var parent Document
	if err = c.DecodeStrict(raw, &parent); err != nil {
		return err
	}
	if parent.UnknownEffect || parent.Pending != nil || len(parent.Spec.Inputs) > len(doc.Spec.Inputs) || len(parent.Spec.Requirements) > len(doc.Spec.Requirements) {
		return c.Fail(c.StoreIntegrityError, "attempt loses intent or copies unknown effects")
	}
	for i, input := range parent.Spec.Inputs {
		actual := doc.Spec.Inputs[i]
		if actual.ID != input.ID || actual.Digest != input.Digest || actual.ByteLength != input.ByteLength || actual.Integrity != input.Integrity || actual.PayloadRef != "blob://"+doc.TaskID+"/"+input.Digest {
			return c.Fail(c.StoreIntegrityError, "attempt raw input lineage mismatch")
		}
	}
	for i, requirement := range parent.Spec.Requirements {
		if requirement != doc.Spec.Requirements[i] {
			return c.Fail(c.StoreIntegrityError, "attempt drops source-bound criterion")
		}
	}
	return nil
}
