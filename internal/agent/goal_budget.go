package agent

import (
	c "github.com/ixayldz/Viber/internal/contracts"
)

const goalBudgetTrancheDenominator int64 = 4

func workBudgetSnapshot(doc Document) *c.TaskWorkBudget {
	b := doc.Budget
	return &c.TaskWorkBudget{
		Limits:         c.BudgetAllocation{MaxSteps: b.MaxSteps, MaxToolCalls: b.MaxToolCalls, MaxInputTokens: b.MaxInputTokens, MaxOutputTokens: b.MaxOutputTokens, MaxActiveMillis: b.MaxActiveMillis},
		Steps:          b.Steps,
		ToolCalls:      b.ToolCalls,
		UsedInput:      b.UsedInput,
		UsedOutput:     b.UsedOutput,
		ReservedInput:  b.ReservedInput,
		ReservedOutput: b.ReservedOutput,
		ActiveMillis:   b.ActiveMillis,
		EffectPending:  doc.Pending != nil || doc.UnknownEffect,
	}
}

func workBudgetSnapshotForState(state c.TaskState, doc Document) *c.TaskWorkBudget {
	if state.WorkBudget == nil {
		return nil
	}
	return workBudgetSnapshot(doc)
}

func workBudgetMatchesDocument(pool c.TaskWorkBudget, doc Document) bool {
	expected := workBudgetSnapshot(doc)
	return expected != nil && pool == *expected
}

// heldGoalBudget derives the pool from the durable queue itself. A cancelled
// queued goal releases its immutable tranche; every other status, including
// UNKNOWN, keeps the full allocation held.
func heldGoalBudget(queue []c.GoalSchedule) (c.BudgetAllocation, bool, error) {
	var held c.BudgetAllocation
	legacyUnallocated := false
	for _, item := range queue {
		allocation := item.BudgetAllocation
		if allocation == (c.BudgetAllocation{}) && item.BudgetAllocationDigest == "" {
			if item.Status != "CANCELLED" {
				// Older schedules have no trustworthy allocation. They cannot be
				// activated under the new contract, and must block new admissions.
				legacyUnallocated = true
			}
			continue
		}
		if allocation.Validate() != nil || !c.ValidDigest(item.BudgetAllocationDigest) {
			return held, false, c.Fail(c.StoreIntegrityError, "goal budget allocation is malformed")
		}
		digest, err := allocation.Digest()
		if err != nil || digest != item.BudgetAllocationDigest {
			return held, false, c.Fail(c.StoreIntegrityError, "goal budget allocation digest changed")
		}
		if item.Status == "CANCELLED" {
			continue
		}
		held.MaxSteps += allocation.MaxSteps
		held.MaxToolCalls += allocation.MaxToolCalls
		held.MaxInputTokens += allocation.MaxInputTokens
		held.MaxOutputTokens += allocation.MaxOutputTokens
		held.MaxActiveMillis += allocation.MaxActiveMillis
	}
	return held, legacyUnallocated, nil
}

// budgetHeadroom is the parent's own remaining budget after actual usage,
// in-flight model reservations, and all durable child holds. When an effect is
// pending, no active-time capacity is available for another child allocation:
// the pending operation may consume the remaining bounded interval.
func budgetHeadroom(b Budget, queue []c.GoalSchedule, effectPending bool) (c.BudgetAllocation, error) {
	if b.Validate() != nil {
		return c.BudgetAllocation{}, c.Fail(c.StoreIntegrityError, "task work budget is malformed")
	}
	held, legacy, err := heldGoalBudget(queue)
	if err != nil {
		return c.BudgetAllocation{}, err
	}
	if legacy {
		return c.BudgetAllocation{}, c.Fail(c.BudgetLimitReached, "legacy goal has no fixed allocation; cancel it while still queued or recover it explicitly")
	}
	remaining := func(maximum, used, reserved, childHeld int64) int64 {
		if used < 0 || reserved < 0 || childHeld < 0 || used >= maximum {
			return 0
		}
		left := maximum - used
		if reserved >= left {
			return 0
		}
		left -= reserved
		if childHeld >= left {
			return 0
		}
		return left - childHeld
	}
	headroom := c.BudgetAllocation{
		MaxSteps:        remaining(b.MaxSteps, b.Steps, 0, held.MaxSteps),
		MaxToolCalls:    remaining(b.MaxToolCalls, b.ToolCalls, 0, held.MaxToolCalls),
		MaxInputTokens:  remaining(b.MaxInputTokens, b.UsedInput, b.ReservedInput, held.MaxInputTokens),
		MaxOutputTokens: remaining(b.MaxOutputTokens, b.UsedOutput, b.ReservedOutput, held.MaxOutputTokens),
		MaxActiveMillis: remaining(b.MaxActiveMillis, b.ActiveMillis, 0, held.MaxActiveMillis),
	}
	if effectPending {
		headroom.MaxActiveMillis = 0
	}
	return headroom, nil
}

func allocateGoalBudget(b Budget, queue []c.GoalSchedule, effectPending bool) (c.BudgetAllocation, error) {
	headroom, err := budgetHeadroom(b, queue, effectPending)
	if err != nil {
		return c.BudgetAllocation{}, err
	}
	tranche := func(maximum, remaining, minimum int64) int64 {
		share := maximum / goalBudgetTrancheDenominator
		if share < minimum {
			share = minimum
		}
		if remaining < share {
			return remaining
		}
		return share
	}
	allocation := c.BudgetAllocation{
		MaxSteps:        tranche(b.MaxSteps, headroom.MaxSteps, 1),
		MaxToolCalls:    tranche(b.MaxToolCalls, headroom.MaxToolCalls, 1),
		MaxInputTokens:  tranche(b.MaxInputTokens, headroom.MaxInputTokens, 4096),
		MaxOutputTokens: tranche(b.MaxOutputTokens, headroom.MaxOutputTokens, 512),
		MaxActiveMillis: tranche(b.MaxActiveMillis, headroom.MaxActiveMillis, 1000),
	}
	if err := allocation.Validate(); err != nil {
		return c.BudgetAllocation{}, c.Fail(c.BudgetLimitReached, "parent has insufficient unreserved work budget for another child")
	}
	return allocation, nil
}

func budgetFromAllocation(allocation c.BudgetAllocation) Budget {
	return Budget{
		MaxSteps: allocation.MaxSteps, MaxToolCalls: allocation.MaxToolCalls,
		MaxInputTokens: allocation.MaxInputTokens, MaxOutputTokens: allocation.MaxOutputTokens,
		MaxActiveMillis: allocation.MaxActiveMillis,
	}
}

func budgetMatchesAllocation(b Budget, allocation c.BudgetAllocation) bool {
	return b.MaxSteps == allocation.MaxSteps && b.MaxToolCalls == allocation.MaxToolCalls &&
		b.MaxInputTokens == allocation.MaxInputTokens && b.MaxOutputTokens == allocation.MaxOutputTokens &&
		b.MaxActiveMillis == allocation.MaxActiveMillis
}
