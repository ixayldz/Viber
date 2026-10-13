package kernel

import c "github.com/ixayldz/Viber/internal/contracts"

// ApplyProjectMemoryRecorded applies the narrow, metadata-only task projection
// for a user-authored memory event. The caller is the event reducer; this does
// not change task execution, policy, quality, budget, tokens or resources.
func ApplyProjectMemoryRecorded(state *c.TaskState, next *c.TaskState, event c.Event, payload c.EventPayload) error {
	if next == nil || !validProjectMemoryMutation(state, event, payload, false) {
		return c.Fail(c.PolicyDenied, "project memory update is not bound to a quiescent task source")
	}
	if state.ProjectMemoryRevision == int64(^uint64(0)>>1) {
		return c.Fail(c.BudgetLimitReached, "project memory revision counter is exhausted")
	}
	if m := payload.ProjectMemoryMutation; m.Action == "COPY" {
		if state.ProjectMemoryCopyCount == int64(^uint64(0)>>1) {
			return c.Fail(c.BudgetLimitReached, "project memory copy count is exhausted")
		}
		next.ProjectMemoryCopyCount = state.ProjectMemoryCopyCount + 1
	}
	next.DocumentDigest = payload.DocumentDigest
	next.ProjectMemoryRevision = state.ProjectMemoryRevision + 1
	return nil
}

// ProjectMemoryGenerationRebindAllowed is the sole old-generation exception
// for ProjectMemoryRecorded. A reopened owner may append metadata to a
// quiescent task without resuming it or settling work exposure.
func ProjectMemoryGenerationRebindAllowed(state *c.TaskState, payload c.EventPayload, event c.Event) bool {
	return state != nil && projectMemoryExecutionQuiescent(state.Execution) && event.KernelGeneration > state.KernelGeneration &&
		validProjectMemoryMutation(state, event, payload, true)
}

func validProjectMemoryMutation(state *c.TaskState, event c.Event, payload c.EventPayload, requireRebind bool) bool {
	m := payload.ProjectMemoryMutation
	if state == nil || m == nil || event.Type != "ProjectMemoryRecorded" || event.Actor != "user" ||
		event.ID == "" || event.ID != event.CausationID || event.ID != m.CommandID ||
		event.TaskID != state.TaskID || event.TaskSeq != state.TaskSeq+1 || event.StoreSeq <= state.StoreSeq ||
		!c.ValidDigest(payload.DocumentDigest) || payload.DocumentDigest == state.DocumentDigest {
		return false
	}
	if m == nil || m.Validate() != nil || m.SourceDocumentDigest != state.DocumentDigest || m.SourceTaskSeq != state.TaskSeq ||
		m.SpecVersion != state.SpecVersion || m.PolicyEpoch != state.PolicyEpoch || m.KernelGeneration != event.KernelGeneration ||
		event.KernelGeneration < state.KernelGeneration || requireRebind && event.KernelGeneration <= state.KernelGeneration {
		return false
	}
	if witness := m.ConflictMetadata; witness != nil &&
		(witness.BaselineDigest != state.BaselineDigest || witness.CandidateDigest != state.CandidateDigest) {
		return false
	}
	if state.Deletion != nil || state.InputBarrier || len(state.PendingInputIDs) != 0 ||
		state.WorkBudget != nil && state.WorkBudget.EffectPending || hasUnsettledWorkExposure(*state) ||
		!projectMemoryExecutionQuiescent(state.Execution) {
		return false
	}
	for _, effect := range state.DeliveryEffects {
		if effect.Status == c.DeliveryUnknown {
			return false
		}
	}
	if payload.Deletion != nil || payload.WorkBudget != nil || payload.ReadWorkerAdmission != nil || payload.DeliveryEffect != nil ||
		payload.Goal != nil || payload.Verification != nil || payload.QueueID != "" || payload.Resources != nil || payload.Tokens != nil ||
		payload.CommandError != nil || payload.SnapshotDigest != "" || payload.InputDigest != "" || payload.InputBytes != 0 ||
		payload.InputID != "" || payload.SpecVersion != 0 || payload.State != "" || payload.Outcome != "" || payload.Quality != "" ||
		payload.Fulfillment != "" || payload.PolicyEpoch != 0 || payload.RequiredObligations != 0 || payload.Reason != "" {
		return false
	}
	return true
}

func projectMemoryExecutionQuiescent(state c.ExecutionState) bool {
	switch state {
	case c.Created, c.Ready, c.WaitingUser, c.WaitingResource, c.Blocked, c.Paused, c.Terminated:
		return true
	default:
		return false
	}
}
