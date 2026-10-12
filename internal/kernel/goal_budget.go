package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
)

func hasUnsettledWorkExposure(state c.TaskState) bool {
	if state.Tokens != nil {
		for _, reservation := range state.Tokens.Reservations {
			if reservation.Status != "SETTLED" {
				return true
			}
		}
	}
	if state.Resources != nil {
		for _, reservation := range state.Resources.Reservations {
			if reservation.Status != "SETTLED" {
				return true
			}
		}
	}
	return false
}

func goalPoolUsage(queue []c.GoalSchedule) (c.BudgetAllocation, bool, error) {
	var held c.BudgetAllocation
	legacy := false
	for _, item := range queue {
		if item.Status == "CANCELLED" {
			continue
		}
		allocation := item.BudgetAllocation
		if allocation == (c.BudgetAllocation{}) && item.BudgetAllocationDigest == "" {
			legacy = true
			continue
		}
		if allocation.Validate() != nil || !c.ValidDigest(item.BudgetAllocationDigest) {
			return held, false, c.Fail(c.StoreIntegrityError, "goal budget pool contains a malformed allocation")
		}
		digest, err := allocation.Digest()
		if err != nil || digest != item.BudgetAllocationDigest {
			return held, false, c.Fail(c.StoreIntegrityError, "goal budget pool allocation digest changed")
		}
		held.MaxSteps += allocation.MaxSteps
		held.MaxToolCalls += allocation.MaxToolCalls
		held.MaxInputTokens += allocation.MaxInputTokens
		held.MaxOutputTokens += allocation.MaxOutputTokens
		held.MaxActiveMillis += allocation.MaxActiveMillis
	}
	return held, legacy, nil
}

func allocationFitsPool(pool c.TaskWorkBudget, held, allocation c.BudgetAllocation) bool {
	fits := func(limit int64, used int64, reservations ...int64) bool {
		if used < 0 || used > limit {
			return false
		}
		remaining := limit - used
		for _, reservation := range reservations {
			if reservation < 0 || reservation > remaining {
				return false
			}
			remaining -= reservation
		}
		return true
	}
	return fits(pool.Limits.MaxSteps, pool.Steps, held.MaxSteps, allocation.MaxSteps) &&
		fits(pool.Limits.MaxToolCalls, pool.ToolCalls, held.MaxToolCalls, allocation.MaxToolCalls) &&
		fits(pool.Limits.MaxInputTokens, pool.UsedInput, pool.ReservedInput, held.MaxInputTokens, allocation.MaxInputTokens) &&
		fits(pool.Limits.MaxOutputTokens, pool.UsedOutput, pool.ReservedOutput, held.MaxOutputTokens, allocation.MaxOutputTokens) &&
		fits(pool.Limits.MaxActiveMillis, pool.ActiveMillis, held.MaxActiveMillis, allocation.MaxActiveMillis)
}

func withinConservativeGoalTranche(limits c.BudgetAllocation, allocation c.BudgetAllocation) bool {
	tranche := func(maximum, minimum int64) int64 {
		value := maximum / 4
		if value < minimum {
			return minimum
		}
		return value
	}
	return allocation.MaxSteps <= tranche(limits.MaxSteps, 1) &&
		allocation.MaxToolCalls <= tranche(limits.MaxToolCalls, 1) &&
		allocation.MaxInputTokens <= tranche(limits.MaxInputTokens, 4096) &&
		allocation.MaxOutputTokens <= tranche(limits.MaxOutputTokens, 512) &&
		allocation.MaxActiveMillis <= tranche(limits.MaxActiveMillis, 1000)
}

func goalAllocationAdmitted(state c.TaskState, allocation c.BudgetAllocation) error {
	if state.WorkBudget == nil {
		return c.Fail(c.BudgetLimitReached, "parent task has no sealed work budget pool; new child allocations are unavailable")
	}
	pool := *state.WorkBudget
	if pool.Validate() != nil || pool.EffectPending || hasUnsettledWorkExposure(state) {
		return c.Fail(c.BudgetLimitReached, "parent has pending or malformed work exposure")
	}
	if !withinConservativeGoalTranche(pool.Limits, allocation) {
		return c.Fail(c.BudgetLimitReached, "child allocation exceeds the conservative one-quarter goal tranche")
	}
	held, legacy, err := goalPoolUsage(state.GoalQueue)
	if err != nil {
		return err
	}
	if legacy {
		return c.Fail(c.BudgetLimitReached, "legacy goal lacks a fixed child allocation and blocks new admissions")
	}
	if !allocationFitsPool(pool, held, allocation) {
		return c.Fail(c.BudgetLimitReached, "child allocation exceeds the parent's remaining work budget")
	}
	return nil
}

func taskWorkBudgetUpdateAllowed(event c.Event, state *c.TaskState) bool {
	if state == nil {
		return event.Type == "TaskCreated" && event.Actor == "kernel"
	}
	switch event.Type {
	case "SessionRecorded", "CandidateRecorded":
		return event.Actor == "kernel"
	case "ModelRiskReconciled", "NativeRiskReconciled", "NativeCleanupObserved":
		return event.Actor == "user"
	default:
		return false
	}
}

func applyTaskWorkBudget(state *c.TaskState, next *c.TaskState, event c.Event, payload c.EventPayload) error {
	updateEvent := event.Type == "SessionRecorded" || event.Type == "CandidateRecorded" ||
		event.Type == "ModelRiskReconciled" || event.Type == "NativeRiskReconciled" || event.Type == "NativeCleanupObserved"
	if payload.WorkBudget == nil {
		if state != nil && state.WorkBudget != nil && updateEvent {
			return c.Fail(c.StoreIntegrityError, "recorded document omitted its authoritative task work budget")
		}
		return nil
	}
	if !taskWorkBudgetUpdateAllowed(event, state) || !c.ValidDigest(payload.DocumentDigest) {
		return c.Fail(c.PolicyDenied, "task work budget snapshot is not bound to a supported document event")
	}
	updated := *payload.WorkBudget
	if updated.Validate() != nil {
		return c.Fail(c.StoreIntegrityError, "task work budget snapshot is malformed")
	}
	if state == nil {
		if event.Type != "TaskCreated" || !updated.Initial() {
			return c.Fail(c.PolicyDenied, "task work budget maxima must be sealed with a zero-use TaskCreated snapshot")
		}
		next.WorkBudget = &updated
		return nil
	}
	if state.WorkBudget == nil {
		return c.Fail(c.PolicyDenied, "legacy task cannot add a new work budget witness during replay")
	}
	prior := *state.WorkBudget
	if updated.Limits != prior.Limits || updated.Steps < prior.Steps || updated.ToolCalls < prior.ToolCalls ||
		updated.UsedInput < prior.UsedInput || updated.UsedOutput < prior.UsedOutput || updated.ActiveMillis < prior.ActiveMillis {
		return c.Fail(c.StoreIntegrityError, "task work budget limits changed or observed usage moved backwards")
	}
	if updated.EffectPending == false && hasUnsettledWorkExposure(*next) {
		return c.Fail(c.PolicyDenied, "pending work exposure cannot be cleared from the task budget pool")
	}
	next.WorkBudget = &updated
	return nil
}
