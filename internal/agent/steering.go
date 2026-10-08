package agent

import (
	"context"
	"errors"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/store"
)

type SteeringInput struct {
	CommandID string `json:"command_id"`
	TaskID    string `json:"task_id"`
	Text      string `json:"text"`
}
type ScopeRevision struct {
	CommandID           string `json:"command_id"`
	TaskID              string `json:"task_id"`
	InputID             string `json:"input_id"`
	ExpectedSpecVersion int64  `json:"expected_spec_version"`
	ExpectedPolicyEpoch int64  `json:"expected_policy_epoch"`
	ExpectedCandidate   string `json:"expected_candidate"`
	Fixture             []byte `json:"fixture_bytes"`
}

func steeringDigest(input SteeringInput) string { digest, _ := c.Digest(input); return digest }
func (s *Session) SteeringReceipt(ctx context.Context, input SteeringInput) (c.TaskState, bool, error) {
	state, event, payload, found, err := s.Journal.CommandReceipt(ctx, input.CommandID)
	if err != nil || !found {
		return state, found, err
	}
	if event.TaskID != input.TaskID || event.Type != "InputRecorded" || event.Actor != "user" || payload.Reason != steeringDigest(input) || payload.InputDigest != c.HashBytes([]byte(input.Text)) || payload.InputBytes != int64(len(input.Text)) {
		return state, true, c.Fail(c.CommandIDConflict, "steering command ID reused with different input")
	}
	return state, true, nil
}

// RecordSteering does not acquire the work-loop mutex. Raw bytes are published
// first; the journal barrier fences an in-flight candidate pointer transaction.
// Only the offline/native profile currently exposes this concurrent boundary.
func (s *Session) RecordSteering(ctx context.Context, input SteeringInput) (c.TaskState, error) {
	if err := s.Journal.Writable(); err != nil {
		return c.TaskState{}, err
	}
	if input.CommandID == "" || len(input.CommandID) > 128 || input.TaskID == "" || len(input.Text) == 0 || len(input.Text) > 64<<10 || !utf8.ValidString(input.Text) {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "bounded UTF-8 steering input required")
	}
	if state, found, err := s.SteeringReceipt(ctx, input); found || err != nil {
		return state, err
	}
	digest, err := s.Archive.PutBytes(input.TaskID, []byte(input.Text))
	if err != nil {
		return c.TaskState{}, err
	}
	for attempt := 0; attempt < 32; attempt++ {
		state, err := s.State(ctx, input.TaskID)
		if err != nil {
			return state, err
		}
		if state.Execution == c.Terminated {
			return state, c.Fail(c.InvalidArgument, "terminal task requires a new attempt")
		}
		if state.KernelGeneration != s.Journal.Generation() {
			state, err = s.recover(ctx, state)
			if err != nil {
				return state, err
			}
		}
		result, err := s.Journal.Execute(ctx, store.Command{ID: input.CommandID, TaskID: input.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "InputRecorded", Payload: c.EventPayload{InputID: input.CommandID, InputDigest: digest, InputBytes: int64(len(input.Text)), Reason: steeringDigest(input)}})
		if err == nil {
			return result, nil
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.Conflict {
			return result, err
		}
		if recorded, found, lookupErr := s.SteeringReceipt(ctx, input); found || lookupErr != nil {
			return recorded, lookupErr
		}
	}
	return c.TaskState{}, c.Fail(c.Conflict, "steering admission could not reach a journal boundary")
}
func (s *Session) pendingInput(ctx context.Context, task, id string) ([]byte, error) {
	_, event, payload, found, err := s.Journal.CommandReceipt(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found || event.TaskID != task || event.Actor != "user" || event.Type != "InputRecorded" || payload.InputID != id || !c.ValidDigest(payload.InputDigest) || payload.InputBytes <= 0 || payload.InputBytes > 64<<10 {
		return nil, c.Fail(c.StoreIntegrityError, "pending raw steering input unavailable")
	}
	raw, err := s.Archive.GetBytes(task, payload.InputDigest)
	if err != nil || int64(len(raw)) != payload.InputBytes || !utf8.Valid(raw) {
		return nil, c.Fail(c.StoreIntegrityError, "pending raw steering input corrupted")
	}
	return raw, nil
}
func (s *Session) Revise(ctx context.Context, revision ScopeRevision) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Journal.Writable(); err != nil {
		return c.TaskState{}, err
	}
	if revision.CommandID == "" || len(revision.CommandID) > 128 || revision.TaskID == "" || revision.InputID == "" || revision.ExpectedSpecVersion < 1 || revision.ExpectedPolicyEpoch < 1 || !c.ValidDigest(revision.ExpectedCandidate) {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "bound scope revision required")
	}
	if _, err := ParseFixture(revision.Fixture); err != nil {
		return c.TaskState{}, err
	}
	digest, err := c.Digest(revision)
	if err != nil {
		return c.TaskState{}, err
	}
	receipt, event, payload, found, err := s.Journal.CommandReceipt(ctx, revision.CommandID)
	if err != nil {
		return receipt, err
	}
	if found {
		if event.Actor != "user" || event.TaskID != revision.TaskID || event.Type != "SpecRevised" || payload.Reason != digest {
			return receipt, c.Fail(c.CommandIDConflict, "revision command ID reused with different input")
		}
		return receipt, nil
	}
	state, doc, err := s.Load(ctx, revision.TaskID)
	if err != nil {
		return state, err
	}
	if state.Execution == c.Terminated || state.SpecVersion != revision.ExpectedSpecVersion || state.PolicyEpoch != revision.ExpectedPolicyEpoch || state.CandidateDigest != revision.ExpectedCandidate || !state.InputBarrier {
		return state, c.Fail(c.StaleRequest, "scope revision binding changed or no pending input")
	}
	if doc.Pending != nil || doc.UnknownEffect {
		return state, c.Fail(c.PolicyDenied, "unknown effects must be reconciled before a scope revision")
	}
	if len(state.PendingInputIDs) < 1 || state.PendingInputIDs[0] != revision.InputID {
		return state, c.Fail(c.StaleRequest, "resolve the oldest pending input with explicit current bindings")
	}
	raw, err := s.pendingInput(ctx, revision.TaskID, revision.InputID)
	if err != nil {
		return state, err
	}
	if len(doc.Spec.Inputs) >= 32 {
		return state, c.Fail(c.InvalidArgument, "raw input history quota exceeded")
	}
	state, err = s.recover(ctx, state)
	if err != nil {
		return state, err
	}
	inputDigest := c.HashBytes(raw)
	doc.Spec.Version++
	doc.Spec.Goal += "\n" + string(raw)
	doc.Spec.Inputs = append(doc.Spec.Inputs, c.InputSource{ID: revision.InputID, PayloadRef: "blob://" + revision.TaskID + "/" + inputDigest, Digest: inputDigest, ByteLength: int64(len(raw)), Integrity: c.Intact})
	doc.Spec.Requirements = append(doc.Spec.Requirements, c.Requirement{ID: "steering-" + revision.InputID, Source: c.SourceSpan{InputID: revision.InputID, Start: 0, End: int64(len(raw))}, Required: true, Risk: "NORMAL", VerificationMethod: "UNRESOLVED_GOAL_COVERAGE"})
	if err = doc.Spec.Validate(); err != nil {
		return state, err
	}
	fixtureDigest, err := s.Archive.PutBytes(revision.TaskID, revision.Fixture)
	if err != nil {
		return state, err
	}
	for i := range doc.Requests {
		if doc.Requests[i].Status == "PENDING" || doc.Requests[i].Status == "APPROVED" {
			doc.Requests[i].Status = "STALE"
		}
	}
	doc.Context = nil
	doc.FixtureDigest = fixtureDigest
	doc.FixtureCursor = 0
	doc.Messages = []model.Message{}
	for _, input := range doc.Spec.Inputs {
		content, readErr := s.Archive.GetBytes(doc.TaskID, input.Digest)
		if readErr != nil {
			return state, readErr
		}
		doc.Messages = append(doc.Messages, model.Message{Role: "user", Text: string(content)})
	}
	doc.ToolCursor = 0
	doc.PendingReplies = nil
	doc.FinalReady = false
	doc.FinalSummary = ""
	doc.Blocker = ""
	doc.AllowUnverified = false
	documentRaw, err := c.CanonicalV1(doc)
	if err != nil {
		return state, err
	}
	documentDigest, err := s.Archive.PutBytes(doc.TaskID, documentRaw)
	if err != nil {
		return state, err
	}
	required := 0
	for _, requirement := range doc.Spec.Requirements {
		if requirement.Required {
			required++
		}
	}
	return s.Journal.Execute(ctx, store.Command{ID: revision.CommandID, TaskID: revision.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "SpecRevised", Payload: c.EventPayload{InputID: revision.InputID, DocumentDigest: documentDigest, SpecVersion: doc.Spec.Version, PolicyEpoch: state.PolicyEpoch + 1, RequiredObligations: required, Reason: digest}})
}
