package evaluation

import (
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/verify"
)

func sourceClaim(source agent.EvaluationSource) Claim {
	return Claim{source.Execution, source.Outcome, source.Quality, source.Fulfillment, source.OpenRequired, source.InputBarrier}
}
func validateEvaluationSource(source agent.EvaluationSource) error {
	if source.SchemaVersion != 1 || source.TaskID == "" || source.TaskSequence < 1 || source.Execution != c.Terminated || source.InputBarrier || !validClaim(sourceClaim(source)) || source.Budget.Validate() != nil || source.Scope != "FROZEN_CAPTURED_CANDIDATE_ONLY; NO_PREREGISTRATION_OR_LIVE_APPLY_CLAIM" {
		return c.Fail(c.StoreIntegrityError, "invalid frozen source binding")
	}
	for _, hash := range []string{source.DocumentDigest, source.SpecDigest, source.InitialSnapshot, source.CandidateDigest, source.ContentDigest, source.CheckSetDigest, source.ProviderProfileDigest} {
		if !c.ValidDigest(hash) {
			return c.Fail(c.StoreIntegrityError, "frozen source digest missing")
		}
	}
	if source.FinalArtifact != "" && !c.ValidDigest(source.FinalArtifact) {
		return c.Fail(c.StoreIntegrityError, "invalid final artifact reference")
	}
	if source.Resources != nil {
		if source.Resources.Policy.Validate() != nil || source.Resources.Reserved != (c.ResourceVector{}) {
			return c.Fail(c.StoreIntegrityError, "frozen source has unresolved resource risk")
		}
		for _, r := range source.Resources.Reservations {
			if r.Status != "SETTLED" {
				return c.Fail(c.StoreIntegrityError, "frozen source resource risk unresolved")
			}
		}
	}
	return nil
}
func deriveNativeResult(ctx context.Context, owner *agent.Session, source agent.EvaluationSource, recipe NativeRecipe) (NativeResult, error) {
	return deriveNativeResultForTask(ctx, owner, owner.EvaluationTaskID(), source, recipe)
}
func deriveNativeResultForTask(ctx context.Context, owner *agent.Session, task string, source agent.EvaluationSource, recipe NativeRecipe) (NativeResult, error) {
	var result NativeResult
	if err := validateEvaluationSource(source); err != nil {
		return result, err
	}
	if err := recipe.Validate(); err != nil {
		return result, err
	}
	state, doc, runs, err := owner.EvaluationChecks(ctx, task)
	if err != nil {
		return result, err
	}
	expectedFixture, err := evaluatorFixture(recipe)
	if doc.EvaluationOrigin == nil {
		expectedFixture, err = legacyEvaluatorFixture(recipe)
	}
	if err != nil {
		return result, err
	}
	if doc.Protection == nil || doc.CheckRuntime == nil {
		return result, c.Fail(c.StoreIntegrityError, "independent owner recipe unavailable")
	}
	boundPrompt, err := evaluatorSourcePrompt(source)
	if err != nil {
		return result, err
	}
	actualRecipe := NativeRecipe{1, doc.Protection.Plan, *doc.CheckRuntime}
	actualRecipeDigest, _ := c.Digest(actualRecipe)
	recipeDigest, _ := c.Digest(recipe)
	if doc.TaskKind != "ANALYSIS" || doc.Runtime != nil || doc.FixtureDigest != c.HashBytes(expectedFixture) || doc.Spec.Goal != string(boundPrompt) || doc.GoalCoverage != nil || doc.Autonomy != "guided" || actualRecipeDigest != recipeDigest || doc.Candidate != doc.Baseline {
		return result, c.Fail(c.StoreIntegrityError, "independent evaluator inputs or immutable candidate changed")
	}
	capture, err := owner.Archive.Get(doc.Candidate)
	if err != nil {
		return result, err
	}
	tree, err := agent.CapturedTreeDigest(capture)
	if err != nil {
		return result, err
	}
	if tree != source.ContentDigest {
		return result, c.Fail(c.StoreIntegrityError, "independent evaluator source content changed")
	}
	result = NativeResult{SchemaVersion: 1, EvidenceMode: NativeMode, Scope: "EXACT_OPERATOR_STDIO_CASES_ON_FROZEN_CAPTURED_TREE; NO_GENERIC_SEMANTIC_OR_CAUSAL_HARNESS_CLAIM", Source: source, EvaluatorCandidate: doc.Candidate.SnapshotDigest, RecipeDigest: recipeDigest, OriginalClaim: sourceClaim(source), IndependentVerdict: c.Unknown, Checks: []NativeCheck{}, EvaluatorResources: state.Resources, EvaluatorBudget: doc.Budget, Measurement: "CONSERVATIVE_KERNEL_RESOURCE_LEDGER; FIXTURE_TOKENS_ARE_NOT_MODEL_USAGE; LOCAL_COMPUTE_MONEY_AND_HUMAN_REWORK_UNKNOWN", SafetyVerdict: c.Unknown}
	if doc.EvaluationOrigin != nil {
		result.EvaluatorTask = task
	}
	quiescent := state.Execution == c.Terminated && !state.InputBarrier && len(state.PendingInputIDs) == 0 && doc.Pending == nil && !doc.UnknownEffect && state.Resources != nil && state.Resources.Reserved == (c.ResourceVector{})
	if state.Resources != nil {
		for _, r := range state.Resources.Reservations {
			quiescent = quiescent && r.Status == "SETTLED"
		}
	}
	if state.Tokens != nil {
		for _, r := range state.Tokens.Reservations {
			quiescent = quiescent && r.Status == "SETTLED"
		}
	}
	result.EvaluatorQuiescent = quiescent
	if len(runs) > len(recipe.Plan.Checks) {
		return result, c.Fail(c.StoreIntegrityError, "independent owner added unregistered checks")
	}
	seen := map[string]bool{}
	allComplete := len(runs) == len(recipe.Plan.Checks) && quiescent
	failed := false
	for i, run := range runs {
		if seen[run.CheckID] {
			return result, c.Fail(c.StoreIntegrityError, "independent evaluation contains duplicate selected runs")
		}
		seen[run.CheckID] = true
		if run.CheckID != recipe.Plan.Checks[i].ID {
			return result, c.Fail(c.StoreIntegrityError, "independent fixed check order changed")
		}
		summary := NativeCheck{ID: run.CheckID, ReceiptDigest: doc.CheckRuns[i].Digest, Verdict: c.Unknown}
		e := run.Observer
		if e != nil && e.Current != nil && e.Baseline != nil {
			summary.Verdict = e.Current.Verdict
			summary.Complete = e.Current.Complete && e.Baseline.Complete && e.Baseline.Verdict != c.Unknown
			summary.Flaky = e.Current.Flaky || e.Baseline.Flaky || e.Current.Verdict != e.Baseline.Verdict
			summary.Attempts = len(e.Current.Attempts)
			summary.BaselineOverheadAttempts = len(e.Baseline.Attempts)
			summary.NativeOwnership = run.NativeLease != nil
		}
		// Validated ownership is mandatory here; legacy synthetic receipts remain
		// readable in agent tasks but cannot become native independent evidence.
		if e != nil {
			for _, group := range []*verify.ObservedRun{e.Baseline, e.Current} {
				if group == nil {
					summary.NativeOwnership = false
					continue
				}
				for _, attempt := range group.Attempts {
					summary.NativeOwnership = summary.NativeOwnership && attempt.Result.Ownership != nil && !attempt.BrokerError && attempt.Result.SourceReadOnly && attempt.Result.ProtectedResultChannel && attempt.Result.ProcessTreeQuiescent
				}
			}
		}
		if !summary.Complete || summary.Flaky || !summary.NativeOwnership || summary.Verdict == c.Unknown {
			allComplete = false
		}
		if summary.Verdict == c.FailVerdict {
			failed = true
		}
		result.Checks = append(result.Checks, summary)
	}
	if allComplete {
		result.IndependentVerdict = c.Pass
		if failed {
			result.IndependentVerdict = c.FailVerdict
		}
	}
	result.FalseVerified = source.Quality == c.Verified && result.IndependentVerdict == c.FailVerdict
	result.UnknownVerifiedClaim = source.Quality == c.Verified && result.IndependentVerdict == c.Unknown
	result.FalseSuccess = result.OriginalClaim.Strict() && result.IndependentVerdict == c.FailVerdict
	return result, nil
}
