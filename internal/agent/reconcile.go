package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

type ModelRiskCommand struct {
	CommandID       string `json:"command_id"`
	TaskID          string `json:"task_id"`
	ExpectedTaskSeq int64  `json:"expected_task_seq"`
	ReservationID   string `json:"reservation_id"`
	RequestDigest   string `json:"request_digest"`
	ProfileDigest   string `json:"profile_digest"`
	Decision        string `json:"decision"`
}
type ModelRiskReceipt struct {
	SchemaVersion       int              `json:"schema_version"`
	TaskID              string           `json:"task_id"`
	CommandDigest       string           `json:"command_digest"`
	PreviousDocument    string           `json:"previous_document"`
	RequestDigest       string           `json:"request_digest"`
	ProfileDigest       string           `json:"profile_digest"`
	ReservationID       string           `json:"reservation_id"`
	Decision            string           `json:"decision"`
	AssumedTokens       c.TokenLimits    `json:"assumed_tokens"`
	AssumedResources    c.ResourceVector `json:"assumed_resources"`
	AcceptedModelOutput bool             `json:"accepted_model_output"`
}

func (s *Session) ReconcileModelRisk(ctx context.Context, command ModelRiskCommand) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := c.Digest(command)
	if err != nil {
		return c.TaskState{}, err
	}
	if command.CommandID == "" || len(command.CommandID) > 128 || command.TaskID == "" || command.ExpectedTaskSeq < 1 || command.Decision != "ACCOUNT_FULL_UPPER_BOUND_WITHOUT_OUTPUT" || command.ReservationID == "" || !c.ValidDigest(command.RequestDigest) || !c.ValidDigest(command.ProfileDigest) {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "explicit bound model-risk accounting decision required")
	}
	prior, event, payload, found, err := s.Journal.CommandReceipt(ctx, command.CommandID)
	if err != nil {
		return prior, err
	}
	if found {
		if event.TaskID != command.TaskID || event.Type != "ModelRiskReconciled" || event.Actor != "user" || payload.Reason != hash {
			return prior, c.Fail(c.CommandIDConflict, "model risk command ID conflict")
		}
		return prior, nil
	}
	state, doc, err := s.Load(ctx, command.TaskID)
	if err != nil {
		return state, err
	}
	if state.TaskSeq != command.ExpectedTaskSeq {
		return state, c.Fail(c.StaleAuthority, "model-risk task cursor changed")
	}
	if state.InputBarrier || (state.Execution != c.Blocked && state.Execution != c.WaitingResource && state.Execution != c.Paused && state.Execution != c.Recovering && state.Execution != c.Terminated) || doc.Pending == nil || doc.Pending.Kind != "MODEL" || doc.Pending.Status != "UNKNOWN" || !doc.UnknownEffect || doc.Pending.ID != command.ReservationID || doc.Pending.ArgumentsDigest != command.RequestDigest || tokenProfile(doc) != command.ProfileDigest || state.Resources == nil || state.Tokens == nil {
		return state, c.Fail(c.PolicyDenied, "only a quiescent UNKNOWN model request can be accounted; native process risk cannot be released")
	}
	var tokens *c.TokenReservation
	for i := range state.Tokens.Reservations {
		r := state.Tokens.Reservations[i]
		if r.ID == command.ReservationID && r.Status == "UNKNOWN" && r.RequestDigest == command.RequestDigest && r.ProfileDigest == command.ProfileDigest {
			tokens = &r
		}
	}
	resources := unresolvedResource(state)
	if tokens == nil || resources == nil || resources.ID != command.ReservationID || resources.Kind != "MODEL" || resources.Status != "UNKNOWN" || resources.RequestDigest != command.RequestDigest || resources.ProfileDigest != command.ProfileDigest {
		return state, c.Fail(c.StoreIntegrityError, "paired unknown model reservation missing")
	}
	tr, rr := *tokens, *resources
	tr.Status = "SETTLED"
	tr.Used = tr.Upper
	tr.UsageSource = "OPERATOR_ASSUMED_UPPER_BOUND"
	rr.Status = "SETTLED"
	rr.Used = rr.Upper
	rr.Used.Children = 0
	rr.TokenUsed = tr.Used
	rr.Meter = "OPERATOR_ASSUMED_UPPER_BOUND"
	proof := ModelRiskReceipt{1, doc.TaskID, hash, state.DocumentDigest, tr.RequestDigest, tr.ProfileDigest, tr.ID, command.Decision, tr.Used, rr.Used, false}
	raw, err := c.CanonicalV1(proof)
	if err != nil {
		return state, err
	}
	tr.ResponseDigest, err = s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		return state, err
	}
	resourceProof := ResourceReceipt{SchemaVersion: 1, TaskID: doc.TaskID, OperationID: rr.ID, RequestDigest: rr.RequestDigest, ProfileDigest: rr.ProfileDigest, StartedActiveMillis: rr.StartedActiveMillis, EndedActiveMillis: doc.Budget.ActiveMillis, SourceReceipt: tr.ResponseDigest, Candidate: doc.Candidate.SnapshotDigest, Measurement: rr.Meter}
	raw, err = c.CanonicalV1(resourceProof)
	if err != nil {
		return state, err
	}
	rr.ReceiptDigest, err = s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		return state, err
	}
	doc.Budget.UsedInput += tr.Used.Input
	doc.Budget.UsedOutput += tr.Used.Output
	doc.Budget.ReservedInput = 0
	doc.Budget.ReservedOutput = 0
	doc.Pending = nil
	doc.UnknownEffect = false
	doc.Context = nil
	doc.LastResponseBlob = tr.ResponseDigest
	doc.LastNoDispatch = false
	doc.Blocker = "MODEL_RISK_ACCOUNTED_AT_FULL_UPPER_BOUND; no response/output/verification was accepted"
	predicted := state
	predicted.Tokens, err = c.ApplyTokens(state.Tokens, c.TokenMutation{Action: "SETTLE", Reservation: tr}, state)
	if err != nil {
		return state, err
	}
	predicted.Resources, err = c.ApplyResources(state.Resources, c.ResourceMutation{Action: "SETTLE", Reservation: rr}, state)
	if err != nil {
		return state, err
	}
	if err = validateTokenDocument(predicted, doc); err != nil {
		return state, err
	}
	if err = s.validateTokenReceipts(predicted, doc); err != nil {
		return state, err
	}
	if err = s.validateResourceDocument(predicted, doc); err != nil {
		return state, err
	}
	raw, err = c.CanonicalV1(doc)
	if err != nil {
		return state, err
	}
	digest, err := s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		return state, err
	}
	return s.Journal.Execute(ctx, store.Command{ID: command.CommandID, TaskID: doc.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "ModelRiskReconciled", Payload: c.EventPayload{DocumentDigest: digest, Reason: hash, Tokens: &c.TokenMutation{Action: "SETTLE", Reservation: tr}, Resources: &c.ResourceMutation{Action: "SETTLE", Reservation: rr}}})
}
func (s *Session) validateRiskReceipt(doc Document, r c.TokenReservation, raw []byte) error {
	var proof ModelRiskReceipt
	if c.DecodeStrict(raw, &proof) != nil || proof.SchemaVersion != 1 || proof.TaskID != doc.TaskID || !c.ValidDigest(proof.CommandDigest) || !c.ValidDigest(proof.PreviousDocument) || proof.Decision != "ACCOUNT_FULL_UPPER_BOUND_WITHOUT_OUTPUT" || proof.AcceptedModelOutput || proof.ReservationID != r.ID || proof.RequestDigest != r.RequestDigest || proof.ProfileDigest != r.ProfileDigest || proof.AssumedTokens != r.Used || r.Used != r.Upper {
		return c.Fail(c.StoreIntegrityError, "operator risk accounting receipt invalid")
	}
	originalRaw, err := s.Archive.GetBytes(doc.TaskID, proof.PreviousDocument)
	if err != nil {
		return err
	}
	var original Document
	if c.DecodeStrict(originalRaw, &original) != nil || original.TaskID != doc.TaskID || original.Pending == nil || original.Pending.Kind != "MODEL" || original.Pending.Status != "UNKNOWN" || original.Pending.ID != r.ID || original.Pending.ArgumentsDigest != r.RequestDigest || !original.UnknownEffect || tokenProfile(original) != r.ProfileDigest {
		return c.Fail(c.StoreIntegrityError, "risk accounting lacks original UNKNOWN model intent")
	}
	return nil
}
