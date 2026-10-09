package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func (s *Session) creationTokenLimits(ctx context.Context, requested *c.TokenLimits) (c.TokenLimits, error) {
	view, err := s.Journal.TokenLedger(ctx)
	if err != nil {
		return c.TokenLimits{}, err
	}
	if view.Coverage == "LEGACY_UNTRACKED" {
		return c.TokenLimits{}, c.Fail(c.UnsupportedCapability, "new token-accounted tasks require a fresh store; legacy tasks remain readable/resumable")
	}
	limits := view.Limits
	if view.Coverage == "EMPTY" {
		limits = c.DefaultTokenLimits()
	}
	if requested != nil {
		if err := requested.Validate(); err != nil {
			return limits, err
		}
		if view.Coverage != "EMPTY" && *requested != limits {
			return limits, c.Fail(c.PolicyDenied, "store token limits are immutable; use the existing limits or a fresh store")
		}
		limits = *requested
	}
	return limits, nil
}

func tokenProfile(doc Document) string {
	provider, capacity, estimator := contextProfile(doc)
	modelID, output := "offline-fixture-v1", int64(512)
	if doc.Runtime != nil {
		modelID, output = doc.Runtime.Model, doc.Runtime.OutputLimit
	}
	digest, _ := c.Digest(struct {
		Provider  string        `json:"provider"`
		Model     string        `json:"model"`
		Capacity  int64         `json:"context"`
		Output    int64         `json:"output"`
		Estimator string        `json:"estimator"`
		Runtime   *LocalRuntime `json:"runtime,omitempty"`
	}{provider, modelID, capacity, output, estimator, doc.Runtime})
	return digest
}

func recordTokenMutation(state c.TaskState, doc Document, kind string, extra *c.EventPayload) error {
	if kind == "TaskCreated" {
		return nil
	}
	if state.Tokens == nil {
		if doc.StoreTokens != nil {
			return c.Fail(c.StoreIntegrityError, "task lost its token ledger")
		}
		return nil // Legacy documents retain their original accounting contract.
	}
	if doc.StoreTokens == nil || *doc.StoreTokens != state.Tokens.Limits {
		return c.Fail(c.StoreIntegrityError, "document token limits changed")
	}
	if kind != "SessionRecorded" {
		return nil
	}
	var unresolved *c.TokenReservation
	for i := range state.Tokens.Reservations {
		r := &state.Tokens.Reservations[i]
		if r.Status == "RESERVED" || r.Status == "UNKNOWN" {
			unresolved = r
			break
		}
	}
	if doc.Pending != nil && doc.Pending.Kind == "MODEL" {
		if unresolved == nil {
			if doc.Pending.Status != "ADMITTED" || doc.Context == nil {
				return c.Fail(c.StoreIntegrityError, "unaccounted model intent")
			}
			extra.Tokens = &c.TokenMutation{Action: "RESERVE", Reservation: c.TokenReservation{ID: doc.Pending.ID, RequestDigest: doc.Context.RequestDigest, ProfileDigest: tokenProfile(doc), SpecVersion: doc.Spec.Version, PolicyEpoch: state.PolicyEpoch, Generation: state.KernelGeneration, Upper: c.TokenLimits{Input: doc.Budget.ReservedInput, Output: doc.Budget.ReservedOutput}, Status: "RESERVED"}}
		} else {
			if unresolved.ID != doc.Pending.ID {
				return c.Fail(c.StoreIntegrityError, "pending reservation identity mismatch")
			}
			if doc.Pending.Status == "UNKNOWN" && unresolved.Status == "RESERVED" {
				r := *unresolved
				r.Status = "UNKNOWN"
				extra.Tokens = &c.TokenMutation{Action: "UNKNOWN", Reservation: r}
			}
		}
	} else if unresolved != nil {
		if doc.Budget.ReservedInput != 0 || doc.Budget.ReservedOutput != 0 {
			return c.Fail(c.StoreIntegrityError, "reservation cannot disappear without a settlement")
		}
		r := *unresolved
		r.Status, r.ResponseDigest, r.UsageSource = "SETTLED", doc.LastResponseBlob, "PROVIDER_REPORTED"
		if doc.Runtime == nil {
			r.UsageSource = "FIXTURE_REPORTED"
		}
		if doc.LastNoDispatch {
			r.UsageSource = "KERNEL_NO_DISPATCH"
		}
		r.Used = c.TokenLimits{Input: doc.Budget.UsedInput - state.Tokens.Used.Input, Output: doc.Budget.UsedOutput - state.Tokens.Used.Output}
		extra.Tokens = &c.TokenMutation{Action: "SETTLE", Reservation: r}
	}
	return nil
}

func validateTokenDocument(state c.TaskState, doc Document) error {
	account := state.Tokens
	if account == nil {
		if doc.StoreTokens != nil {
			return c.Fail(c.StoreIntegrityError, "document token account missing from journal")
		}
		return nil
	}
	if doc.StoreTokens == nil || *doc.StoreTokens != account.Limits || account.Used.Input != doc.Budget.UsedInput || account.Used.Output != doc.Budget.UsedOutput || account.Reserved.Input != doc.Budget.ReservedInput || account.Reserved.Output != doc.Budget.ReservedOutput {
		return c.Fail(c.StoreIntegrityError, "document token counters differ from authoritative journal")
	}
	unresolved := 0
	for _, r := range account.Reservations {
		if r.Status != "RESERVED" && r.Status != "UNKNOWN" {
			continue
		}
		unresolved++
		if doc.Pending == nil || doc.Pending.Kind != "MODEL" || doc.Pending.ID != r.ID || doc.Context == nil || r.RequestDigest != doc.Context.RequestDigest || r.ProfileDigest != tokenProfile(doc) || r.Upper.Input != doc.Budget.ReservedInput || r.Upper.Output != doc.Budget.ReservedOutput || r.SpecVersion != doc.Spec.Version || r.PolicyEpoch != doc.Pending.Epoch || r.Generation != doc.Pending.Generation || (r.Status == "UNKNOWN" && (doc.Pending.Status != "UNKNOWN" || !doc.UnknownEffect)) {
			return c.Fail(c.StoreIntegrityError, "pending model and token reservation lineage differ")
		}
	}
	if unresolved == 0 && doc.Pending != nil && doc.Pending.Kind == "MODEL" {
		return c.Fail(c.StoreIntegrityError, "model pending without a token reservation")
	}
	return nil
}

func (s *Session) validateTokenReceipts(state c.TaskState, doc Document) error {
	if state.Tokens == nil {
		return nil
	}
	for _, r := range state.Tokens.Reservations {
		receiptDoc, profileErr := profileDocument(doc, r.ProfileDigest)
		if profileErr != nil {
			return profileErr
		}
		provider, capacity, estimator := contextProfile(receiptDoc)
		raw, err := s.Archive.GetBytes(doc.TaskID, r.RequestDigest)
		if err != nil {
			return c.Fail(c.StoreIntegrityError, "token reservation request unavailable")
		}
		var request model.Request
		if c.DecodeStrict(raw, &request) != nil || model.ValidateRequest(request, provider) != nil || request.ID != r.ID || request.Stream != (receiptDoc.Runtime != nil && receiptDoc.Runtime.Stream) || request.MaxOutputTokens != r.Upper.Output || r.ProfileDigest != tokenProfile(receiptDoc) {
			return c.Fail(c.StoreIntegrityError, "token request/profile lineage invalid")
		}
		_, manifest, countErr := boundedContext(request, capacity, estimator)
		expectedModel, expectedContext := "offline-fixture-v1", int64(0)
		if receiptDoc.Runtime != nil {
			expectedModel, expectedContext = receiptDoc.Runtime.Model, requestContextWindow(receiptDoc.Runtime)
		}
		if countErr != nil || manifest.InputTokens+4096 != r.Upper.Input || request.Model != expectedModel || request.ContextWindow != expectedContext {
			return c.Fail(c.StoreIntegrityError, "token upper bound or model profile differs from retained request")
		}
		if r.Status != "SETTLED" {
			continue
		}
		raw, err = s.Archive.GetBytes(doc.TaskID, r.ResponseDigest)
		if err != nil {
			return c.Fail(c.StoreIntegrityError, "token usage receipt unavailable")
		}
		var usage model.Usage
		if r.UsageSource == "OPERATOR_ASSUMED_UPPER_BOUND" {
			if err := s.validateRiskReceipt(receiptDoc, r, raw); err != nil {
				return err
			}
			usage = model.Usage{Known: true, Input: r.Used.Input, Output: r.Used.Output}
		} else if r.UsageSource == "KERNEL_NO_DISPATCH" {
			var proof model.NoDispatchReceipt
			if receiptDoc.Runtime == nil || c.DecodeStrict(raw, &proof) != nil || proof.SchemaVersion != 1 || proof.Decision != "KERNEL_PREFLIGHT_DECLINED" || proof.RequestID != r.ID || proof.RequestDigest != r.RequestDigest || proof.ProfileDigest != r.ProfileDigest || proof.Provider != provider || proof.Model != request.Model || r.Used != (c.TokenLimits{}) {
				return c.Fail(c.StoreIntegrityError, "kernel preflight receipt binding invalid")
			}
			usage = model.Usage{Known: true}
		} else if receiptDoc.Runtime == nil {
			var turn Turn
			if c.DecodeStrict(raw, &turn) != nil || r.UsageSource != "FIXTURE_REPORTED" {
				return c.Fail(c.StoreIntegrityError, "fixture usage receipt invalid")
			}
			usage = model.Usage{Known: turn.UsageKnown, Input: turn.InputTokens, Output: turn.OutputTokens}
		} else {
			result, err := model.DecodeReceipt(request, provider, raw)
			if err != nil || r.UsageSource != "PROVIDER_REPORTED" {
				return c.Fail(c.StoreIntegrityError, "provider usage receipt invalid")
			}
			usage = result.Usage
		}
		if !usage.Known || usage.Input != r.Used.Input || usage.Output != r.Used.Output {
			return c.Fail(c.StoreIntegrityError, "observed usage differs from journal charge")
		}
	}
	return nil
}
