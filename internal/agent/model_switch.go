package agent

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/store"
)

type RuntimeProfileRef struct {
	Digest  string   `json:"digest"`
	Profile *Runtime `json:"runtime,omitempty"`
}
type ModelSwitch struct {
	CommandID       string  `json:"command_id"`
	TaskID          string  `json:"task_id"`
	ExpectedTaskSeq int64   `json:"expected_task_seq"`
	ExpectedProfile string  `json:"expected_profile"`
	Runtime         Runtime `json:"runtime"`
}

func cloneRuntime(r *Runtime) *Runtime {
	if r == nil {
		return nil
	}
	copy := *r
	return &copy
}
func profileDocument(doc Document, digest string) (Document, error) {
	if tokenProfile(doc) == digest {
		return doc, nil
	}
	for _, ref := range doc.ProfileHistory {
		if ref.Digest == digest {
			next := doc
			next.Runtime = cloneRuntime(ref.Profile)
			return next, nil
		}
	}
	return doc, c.Fail(c.StoreIntegrityError, "retained token model profile unavailable")
}
func (s *Session) validateProfileHistory(doc Document) error {
	if len(doc.ProfileHistory) > 16 {
		return c.Fail(c.StoreIntegrityError, "model switch quota exceeded")
	}
	seen := map[string]bool{}
	for _, ref := range doc.ProfileHistory {
		previous := doc
		previous.Runtime = ref.Profile
		if !c.ValidDigest(ref.Digest) || seen[ref.Digest] || ref.Profile.Validate() != nil || tokenProfile(previous) != ref.Digest {
			return c.Fail(c.StoreIntegrityError, "invalid historical model profile")
		}
		seen[ref.Digest] = true
		if ref.Profile != nil && ref.Profile.Provider == "chatgpt" {
			base, err := s.Archive.Get(doc.Baseline)
			if err != nil {
				return err
			}
			if !fileguard.Disjoint(ref.Profile.AuthDirectory, base.Snapshot.Root) || !fileguard.Disjoint(ref.Profile.AuthDirectory, s.directory) {
				return c.Fail(c.PolicyDenied, "historical auth store overlaps source/task store")
			}
		}
	}
	return nil
}
func switchMessages(messages []model.Message) []model.Message {
	result := make([]model.Message, len(messages))
	for i, m := range messages {
		result[i] = m
		// Even same-provider model changes start a fresh canonical request. Opaque
		// reasoning/signatures belong to the old exact profile; never transplant them.
		result[i].Continuation = nil
		result[i].Provider = ""
		result[i].Model = ""
	}
	return result
}
func (s *Session) SwitchModel(ctx context.Context, command ModelSwitch) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	digest, err := c.Digest(command)
	if err != nil {
		return c.TaskState{}, err
	}
	if command.CommandID == "" || len(command.CommandID) > 128 || command.TaskID == "" || command.ExpectedTaskSeq < 1 || !c.ValidDigest(command.ExpectedProfile) || command.Runtime.Validate() != nil {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "bound valid model switch required")
	}
	prior, event, payload, found, err := s.Journal.CommandReceipt(ctx, command.CommandID)
	if err != nil {
		return prior, err
	}
	if found {
		if event.TaskID != command.TaskID || event.Type != "ContinuityRevised" || event.Actor != "user" || payload.Reason != digest {
			return prior, c.Fail(c.CommandIDConflict, "model switch command conflict")
		}
		return prior, nil
	}
	state, doc, err := s.Load(ctx, command.TaskID)
	if err != nil {
		return state, err
	}
	if state.TaskSeq != command.ExpectedTaskSeq || tokenProfile(doc) != command.ExpectedProfile {
		return state, c.Fail(c.StaleAuthority, "model switch task/profile changed")
	}
	// The initial task's privacy/provider choice is an immutable lock here.
	// Expanding allowed providers requires a separately authorized policy revision;
	// this command cannot turn an offline/local task into remote inference.
	if doc.Runtime == nil || command.Runtime.Provider != doc.Runtime.Provider || command.Runtime.AllowRemote != doc.Runtime.AllowRemote || command.Runtime.DeclaredLocal != doc.Runtime.DeclaredLocal || command.Runtime.AuthDirectory != doc.Runtime.AuthDirectory || command.Runtime.AuthProfile != doc.Runtime.AuthProfile || command.Runtime.Endpoint != doc.Runtime.Endpoint || command.Runtime.SecretHandle != doc.Runtime.SecretHandle {
		return state, c.Fail(c.PolicyDenied, "model switch cannot expand provider/privacy/account/endpoint lock")
	}
	state, err = s.quiescentRevision(ctx, state, doc)
	if err != nil {
		return state, err
	}
	probe := doc
	probe.Runtime = &command.Runtime
	if err = validateTaskConfig(probe); err != nil {
		return state, err
	}
	probe.Messages = switchMessages(doc.Messages)
	probe.Context = nil
	// Validate the old complete tool boundary before removing opaque state.
	provider, _, _ := contextProfile(doc)
	validation := model.Request{SchemaVersion: 1, ID: "switch-boundary", Model: doc.Runtime.Model, Messages: doc.Messages, Tools: Tools(), Stream: doc.Runtime.Stream, MaxOutputTokens: doc.Runtime.OutputLimit, ContextWindow: requestContextWindow(doc.Runtime)}
	if err = model.ValidateRequest(validation, provider); err != nil {
		return state, err
	}
	nextProfile := tokenProfile(probe)
	if nextProfile == tokenProfile(doc) {
		return state, c.Fail(c.InvalidArgument, "model profile is unchanged")
	}
	if len(doc.ProfileHistory) >= 16 {
		return state, c.Fail(c.BudgetLimitReached, "model profile history quota exhausted")
	}
	oldProfile := tokenProfile(doc)
	known := false
	for _, ref := range probe.ProfileHistory {
		known = known || ref.Digest == oldProfile
	}
	if !known {
		probe.ProfileHistory = append(append([]RuntimeProfileRef{}, doc.ProfileHistory...), RuntimeProfileRef{oldProfile, cloneRuntime(doc.Runtime)})
	}
	// Raw archived history is preserved under its source profile; the new context
	// has no old opaque state. All token receipts remain charged under old profiles.
	if probe.FinalReady {
		return state, c.Fail(c.Conflict, "final delivery boundary cannot switch models")
	}
	probe.Messages = append(probe.Messages, model.Message{Role: "user", Text: "Kernel model profile changed at a settled protocol boundary. Goal, constraints, candidate, checks, plan, budget and historical receipts are unchanged. Old opaque continuation was discarded; historical observations are not current evidence."})
	request, _, manifest, err := compileOfflineRequest(probe, state, s.layers(state, probe))
	if err != nil {
		compacted, _, compactErr := s.compactDocument(probe, 8)
		if compactErr == nil {
			probe = compacted
			request, _, manifest, err = compileOfflineRequest(probe, state, s.layers(state, probe))
		}
	}
	if err != nil {
		return state, err
	}
	if manifest.InputTokens+4096 > probe.Budget.MaxInputTokens-probe.Budget.UsedInput || request.MaxOutputTokens > probe.Budget.MaxOutputTokens-probe.Budget.UsedOutput {
		return state, c.Fail(c.BudgetLimitReached, "new model request does not fit remaining task budget")
	}
	if state.Tokens != nil {
		view, err := s.Journal.TokenLedger(ctx)
		if err != nil {
			return state, err
		}
		if manifest.InputTokens+4096 > view.Limits.Input-view.Used.Input-view.Reserved.Input || request.MaxOutputTokens > view.Limits.Output-view.Used.Output-view.Reserved.Output {
			return state, c.Fail(c.BudgetLimitReached, "new model profile exceeds remaining global budget")
		}
	}
	if state.Resources != nil {
		price, err := probe.ResourcePolicy.Price(command.Runtime.Provider, command.Runtime.Model)
		if err != nil {
			return state, err
		}
		upper := resourceUpper(probe, true)
		if price != nil {
			upper.MoneyMicros, err = price.Cost(manifest.InputTokens+4096, request.MaxOutputTokens)
			if err != nil {
				return state, err
			}
		}
		view, err := s.Journal.ResourceLedger(ctx)
		if err != nil {
			return state, err
		}
		if !view.Used.Add(view.Reserved).Add(upper).Fits(probe.ResourcePolicy.Limits) {
			return state, c.Fail(c.BudgetLimitReached, "new model does not fit global resource budget")
		}
	}
	if err = s.validateContinuity(probe); err != nil {
		return state, err
	}
	if err = s.validateTokenReceipts(state, probe); err != nil {
		return state, err
	}
	raw, err := c.CanonicalV1(probe)
	if err != nil {
		return state, err
	}
	blob, err := s.Archive.PutBytes(doc.TaskID, raw)
	if err != nil {
		return state, err
	}
	return s.Journal.Execute(ctx, store.Command{ID: command.CommandID, TaskID: doc.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "ContinuityRevised", Payload: c.EventPayload{DocumentDigest: blob, Reason: digest}})
}
