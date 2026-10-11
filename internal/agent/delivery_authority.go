package agent

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

// rebindDeliveryAuthorityLocked advances only the terminal task's current
// journal generation. It never changes the document, candidate, policy or
// quality. A prior effect is included only as exact receipt provenance.
func (s *Session) rebindDeliveryAuthorityLocked(ctx context.Context, task string, receipt *c.DeliveryEffect) (c.TaskState, Document, error) {
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return state, doc, err
	}
	if state.KernelGeneration == s.Journal.Generation() {
		return state, doc, nil
	}
	if state.KernelGeneration > s.Journal.Generation() || state.Execution != c.Terminated || state.InputBarrier || len(state.PendingInputIDs) != 0 || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(state) != nil || !tokensQuiescent(state) || state.Deletion != nil {
		return state, doc, c.Fail(c.StaleAuthority, "delivery authority cannot be rebound from a nonquiescent or newer task cursor")
	}
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return state, doc, err
	}
	for _, effect := range state.DeliveryEffects {
		if effect.Status == c.DeliveryUnknown {
			return state, doc, c.Fail(c.Conflict, "UNKNOWN delivery effects must be reconciled before task authority is rebound")
		}
	}
	if receipt == nil && len(state.DeliveryEffects) > 0 {
		latest := state.DeliveryEffects[len(state.DeliveryEffects)-1]
		receipt = &latest
	}
	if receipt != nil {
		recorded, found := findDeliveryEffect(state, receipt.OperationID)
		if !found || recorded != *receipt || (recorded.Status != c.DeliveryApplied && recorded.Status != c.DeliveryRestored) {
			return state, doc, c.Fail(c.StaleAuthority, "delivery authority rebind requires exact completed receipt provenance")
		}
		if _, _, err = s.readDeliveryPlan(ctx, task, recorded.OperationID); err != nil {
			return state, doc, err
		}
		if _, _, err = s.loadDeliveryAggregate(ctx, task, recorded); err != nil {
			return state, doc, err
		}
		if _, err = s.loadDeliveryFileReceipts(ctx, task, recorded); err != nil {
			return state, doc, err
		}
	}
	currentGeneration := s.Journal.Generation()
	digest, err := c.DeliveryAuthorityRebindDigest(state, currentGeneration, receipt)
	if err != nil {
		return state, doc, err
	}
	payload := c.EventPayload{DocumentDigest: state.DocumentDigest, SnapshotDigest: state.CandidateDigest, Reason: digest, DeliveryEffect: receipt}
	updated, err := s.Journal.Execute(ctx, store.Command{ID: newID("delivery-rebind-"), TaskID: task, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "DeliveryAuthorityRebound", Payload: payload})
	if err != nil {
		return state, doc, err
	}
	return updated, doc, nil
}
