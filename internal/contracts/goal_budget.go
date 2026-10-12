package contracts

// Validate bounds one child task's fixed allocation to the same envelope as a
// task Budget. Zero does not mean unlimited: task budgets require every limit
// to be explicitly positive.
func (a BudgetAllocation) Validate() error {
	if a.MaxSteps < 1 || a.MaxSteps > 256 ||
		a.MaxToolCalls < 1 || a.MaxToolCalls > 1024 ||
		a.MaxInputTokens < 4096 || a.MaxInputTokens > 8<<20 ||
		a.MaxOutputTokens < 512 || a.MaxOutputTokens > 1<<20 ||
		a.MaxActiveMillis < 1000 || a.MaxActiveMillis > 900000 {
		return Fail(InvalidArgument, "invalid fixed child budget allocation")
	}
	return nil
}

func (a BudgetAllocation) Digest() (string, error) { return Digest(a) }

func (b TaskWorkBudget) Validate() error {
	if err := b.Limits.Validate(); err != nil {
		return err
	}
	if b.Steps < 0 || b.ToolCalls < 0 || b.UsedInput < 0 || b.UsedOutput < 0 ||
		b.ReservedInput < 0 || b.ReservedOutput < 0 || b.ActiveMillis < 0 ||
		b.ReservedInput > b.Limits.MaxInputTokens || b.ReservedOutput > b.Limits.MaxOutputTokens {
		return Fail(InvalidArgument, "invalid task work budget counters")
	}
	if b.UsedInput <= b.Limits.MaxInputTokens && b.ReservedInput > b.Limits.MaxInputTokens-b.UsedInput ||
		b.UsedOutput <= b.Limits.MaxOutputTokens && b.ReservedOutput > b.Limits.MaxOutputTokens-b.UsedOutput ||
		b.UsedInput > b.Limits.MaxInputTokens && b.ReservedInput != 0 ||
		b.UsedOutput > b.Limits.MaxOutputTokens && b.ReservedOutput != 0 {
		return Fail(InvalidArgument, "task work budget usage and reservations exceed sealed limits")
	}
	return nil
}

func (b TaskWorkBudget) Initial() bool {
	return b.Steps == 0 && b.ToolCalls == 0 && b.UsedInput == 0 && b.UsedOutput == 0 &&
		b.ReservedInput == 0 && b.ReservedOutput == 0 && b.ActiveMillis == 0 && !b.EffectPending
}
