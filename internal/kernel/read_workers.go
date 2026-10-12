package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
)

func applyReadWorkerAdmission(state *c.TaskState, next *c.TaskState, event c.Event, payload c.EventPayload) error {
	if state == nil || event.Type != "ReadWorkersAdmitted" || event.Actor != "kernel" ||
		payload.ReadWorkerAdmission == nil || payload.WorkBudget == nil || !c.ValidDigest(payload.DocumentDigest) {
		return c.Fail(c.PolicyDenied, "read-worker admission requires a typed, budget-bearing kernel event")
	}
	a := *payload.ReadWorkerAdmission
	if err := a.Validate(); err != nil {
		return err
	}
	if state.Deletion != nil || state.InputBarrier || len(state.PendingInputIDs) != 0 ||
		state.DocumentDigest != a.SourceDocumentDigest || state.CandidateDigest != a.CandidateDigest ||
		state.TaskSeq != a.SourceTaskSeq || state.SpecVersion != a.SpecVersion ||
		state.PolicyEpoch != a.PolicyEpoch || state.KernelGeneration != a.KernelGeneration ||
		event.KernelGeneration != a.KernelGeneration {
		return c.Fail(c.StaleAuthority, "read-worker admission source, policy, or generation is stale")
	}
	if state.WorkBudget == nil || len(state.ReadWorkerAdmissions) >= c.MaxReadWorkerAdmissions {
		return c.Fail(c.BudgetLimitReached, "task has no work-budget witness or has reached its read-worker admission history bound")
	}
	if hasUnsettledWorkExposure(*state) {
		return c.Fail(c.PolicyDenied, "read workers cannot be admitted while model or native work exposure is unsettled")
	}
	for _, prior := range state.ReadWorkerAdmissions {
		if prior.OperationID == a.OperationID {
			return c.Fail(c.CommandIDConflict, "read-worker operation ID was already charged; inspect the task journal")
		}
	}
	if payload.Deletion != nil || payload.DeliveryEffect != nil || payload.Goal != nil || payload.Verification != nil ||
		payload.QueueID != "" || payload.Resources != nil || payload.Tokens != nil || payload.CommandError != nil ||
		payload.SnapshotDigest != "" || payload.InputDigest != "" || payload.InputBytes != 0 || payload.InputID != "" ||
		payload.SpecVersion != 0 || payload.State != "" || payload.Outcome != "" || payload.Quality != "" ||
		payload.Fulfillment != "" || payload.PolicyEpoch != 0 || payload.RequiredObligations != 0 || payload.Reason != "" {
		return c.Fail(c.InvalidArgument, "read-worker admission event contains unrelated authority")
	}
	prior := *state.WorkBudget
	updated := *payload.WorkBudget
	if err := updated.Validate(); err != nil {
		return c.Fail(c.StoreIntegrityError, "read-worker admission work budget is malformed")
	}
	if updated.Limits != prior.Limits || updated.Steps-prior.Steps != a.ChargedSteps ||
		updated.ToolCalls-prior.ToolCalls != a.ChargedToolCalls ||
		updated.ActiveMillis-prior.ActiveMillis != a.ChargedActiveMillis ||
		updated.UsedInput != prior.UsedInput || updated.UsedOutput != prior.UsedOutput ||
		updated.ReservedInput != prior.ReservedInput || updated.ReservedOutput != prior.ReservedOutput ||
		updated.EffectPending != prior.EffectPending {
		return c.Fail(c.StoreIntegrityError, "read-worker admission did not apply its exact conservative budget charge")
	}
	if updated.Steps > updated.Limits.MaxSteps || updated.ToolCalls > updated.Limits.MaxToolCalls ||
		updated.ActiveMillis > updated.Limits.MaxActiveMillis {
		return c.Fail(c.BudgetLimitReached, "read-worker admission exceeds the sealed task work budget")
	}
	held, legacy, err := goalPoolUsage(state.GoalQueue)
	if err != nil {
		return err
	}
	if legacy {
		return c.Fail(c.BudgetLimitReached, "legacy child allocation blocks read-worker admission")
	}
	charge := c.BudgetAllocation{MaxSteps: a.ChargedSteps, MaxToolCalls: a.ChargedToolCalls, MaxActiveMillis: a.ChargedActiveMillis}
	if !allocationFitsPool(prior, held, charge) {
		return c.Fail(c.BudgetLimitReached, "read-worker charge exceeds parent headroom after child holds")
	}
	if next.ReadWorkerAdmissions == nil {
		next.ReadWorkerAdmissions = []c.ReadWorkerAdmission{}
	}
	next.ReadWorkerAdmissions = append(next.ReadWorkerAdmissions, a)
	return nil
}
