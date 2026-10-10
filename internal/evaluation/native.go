package evaluation

import (
	"context"
	"fmt"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/verify"
	"os"
	"path/filepath"
	"time"
)

const NativeMode = "NATIVE_SEPARATE_OWNER_STDIO_V1; POST_HOC_CANDIDATE_VALIDATION"
const evaluatorTask = "independent"

var evaluatorPrompt = []byte("Execute the fixed operator check IDs once; do not change the immutable candidate. This is independent candidate validation, not goal fulfillment.")

type NativeRecipe struct {
	SchemaVersion int                `json:"schema_version"`
	Plan          verify.CheckPlan   `json:"plan"`
	Runtime       agent.CheckRuntime `json:"runtime"`
}

func (recipe NativeRecipe) Validate() error {
	if recipe.SchemaVersion != 1 || recipe.Plan.Validate() != nil || recipe.Runtime.SchemaVersion != 1 || recipe.Runtime.Profile.Validate() != nil || len(recipe.Plan.Checks) < 1 || len(recipe.Plan.Checks) > 16 || len(recipe.Runtime.ObserverSuites) != len(recipe.Plan.Checks) {
		return c.Fail(c.InvalidArgument, "bounded independent STDIO recipe required")
	}
	profile, _ := c.Digest(recipe.Runtime.Profile)
	seen := map[string]bool{}
	for _, check := range recipe.Plan.Checks {
		if check.RunnerDigest != profile || check.Kind != "TEST" || len(check.RequirementIDs) != 1 || check.RequirementIDs[0] != "user-goal" {
			return c.Fail(c.PolicyDenied, "independent check must be profile-bound and analysis-only")
		}
		found := false
		for _, suite := range recipe.Runtime.ObserverSuites {
			if suite.CheckID == check.ID {
				if found || seen[suite.CheckID] {
					return c.Fail(c.InvalidArgument, "duplicate independent oracle")
				}
				seen[suite.CheckID] = true
				found = true
				if err := suite.Validate(check, recipe.Runtime.Profile); err != nil {
					return err
				}
			}
		}
		if !found {
			return c.Fail(c.PolicyDenied, "independent oracle missing")
		}
	}
	return nil
}
func ReadNativeRecipe(path string) (NativeRecipe, error) {
	var recipe NativeRecipe
	raw, err := regularInput(path, 1<<20)
	if err != nil {
		return recipe, err
	}
	if err = c.DecodeStrict(raw, &recipe); err != nil {
		return recipe, err
	}
	return recipe, recipe.Validate()
}
func evaluatorSourcePrompt(source agent.EvaluationSource) ([]byte, error) {
	digest, err := c.Digest(source)
	if err != nil {
		return nil, err
	}
	return []byte(string(evaluatorPrompt) + "\nFrozen original source binding sha256: " + digest), nil
}
func evaluatorFixture(recipe NativeRecipe) ([]byte, error) {
	turns := []agent.Turn{}
	for ordinal, check := range recipe.Plan.Checks {
		args, _ := c.CanonicalV1(struct {
			Check string `json:"check_id"`
		}{check.ID})
		turns = append(turns, agent.Turn{Calls: []model.Call{{ID: fmt.Sprintf("evaluate-%02d", ordinal), Name: "check_run", Arguments: args}}, UsageKnown: true})
	}
	turns = append(turns, agent.Turn{Text: "Independent observation complete. No source edits.", UsageKnown: true})
	return c.CanonicalV1(agent.Fixture{SchemaVersion: 1, Turns: turns})
}

type NativeCheck struct {
	ID                       string    `json:"id"`
	ReceiptDigest            string    `json:"receipt_digest"`
	Verdict                  c.Verdict `json:"verdict"`
	Complete                 bool      `json:"complete"`
	Flaky                    bool      `json:"flaky"`
	Attempts                 int       `json:"attempts"`
	BaselineOverheadAttempts int       `json:"baseline_overhead_attempts"`
	NativeOwnership          bool      `json:"native_ownership"`
}
type NativeResult struct {
	EvaluatorTask        string                 `json:"evaluator_task,omitempty"`
	SchemaVersion        int                    `json:"schema_version"`
	EvidenceMode         string                 `json:"evidence_mode"`
	Scope                string                 `json:"scope"`
	Source               agent.EvaluationSource `json:"source"`
	EvaluatorCandidate   string                 `json:"evaluator_candidate"`
	RecipeDigest         string                 `json:"recipe_digest"`
	OriginalClaim        Claim                  `json:"original_claim"`
	IndependentVerdict   c.Verdict              `json:"independent_verdict"`
	FalseVerified        bool                   `json:"false_verified"`
	UnknownVerifiedClaim bool                   `json:"unknown_verified_claim"`
	FalseSuccess         bool                   `json:"false_success"`
	Checks               []NativeCheck          `json:"checks"`
	EvaluatorResources   *c.ResourceAccount     `json:"evaluator_resources"`
	EvaluatorBudget      agent.Budget           `json:"evaluator_budget"`
	EvaluatorQuiescent   bool                   `json:"evaluator_quiescent"`
	Measurement          string                 `json:"measurement"`
	SafetyVerdict        c.Verdict              `json:"safety_verdict"`
	AdoptionAllowed      bool                   `json:"adoption_allowed"`
	ReleaseEvidence      bool                   `json:"release_evidence"`
}
type NativeManifest struct {
	SchemaVersion int          `json:"schema_version"`
	EvidenceMode  string       `json:"evidence_mode"`
	SourceDigest  string       `json:"source_digest"`
	RecipeDigest  string       `json:"recipe_digest"`
	Files         []OutputFile `json:"files"`
}

func publishNativeFile(root *os.Root, name string, value any) (OutputFile, error) {
	raw, err := c.CanonicalV1(value)
	if err != nil {
		return OutputFile{}, err
	}
	if err = fileguard.Publish(root, name, raw); err != nil {
		return OutputFile{}, err
	}
	return OutputFile{name, c.HashBytes(raw), int64(len(raw))}, nil
}

// RunCandidate keeps the original owner open and runs only a generated local
// fixture in a separate private owner store. Every subject uses the ordinary
// durable admission, native lease, budget and orphan-reconciliation protocol.
func RunCandidate(ctx context.Context, original *agent.Session, task, recipeFile, output string) (NativeResult, error) {
	var result NativeResult
	recipe, err := ReadNativeRecipe(recipeFile)
	if err != nil {
		return result, err
	}
	source, _, err := original.FreezeEvaluationSource(ctx, task, recipeFile, output)
	if err != nil {
		return result, err
	}
	destination, err := fileguard.ResolveProspective(output)
	if err != nil {
		return result, err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return result, c.Fail(c.Conflict, "fresh independent evaluator directory required")
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return result, err
	}
	defer root.Close()
	if err = fileguard.Private(root); err != nil {
		return result, err
	}
	owner, err := original.OpenIndependentEvaluator(ctx, filepath.Join(destination, "owner"))
	if err != nil {
		return result, err
	}
	taskID := owner.EvaluationTaskID()
	defer owner.Close()
	fixture, err := evaluatorFixture(recipe)
	if err != nil {
		return result, err
	}
	planRaw, err := c.CanonicalV1(recipe.Plan)
	if err != nil {
		return result, err
	}
	budget := agent.DefaultBudget()
	budget.MaxSteps = int64(len(recipe.Plan.Checks) + 1)
	budget.MaxActiveMillis = 900000
	boundPrompt, err := evaluatorSourcePrompt(source)
	if err != nil {
		return result, err
	}
	if _, err = owner.CreateIndependentEvaluator(ctx, original, source, agent.StartOptions{TaskID: taskID, TaskKind: "ANALYSIS", Prompt: boundPrompt, Budget: budget, Autonomy: "guided", AllowUnverified: true, Fixture: fixture, CheckPlan: planRaw, CheckRuntime: &recipe.Runtime}); err != nil {
		return result, err
	}
	if err = original.RegisterEvaluationReport(ctx, owner, root, source); err != nil {
		return result, err
	}
	sourceFile, err := publishNativeFile(root, "source.json", source)
	if err != nil {
		return result, err
	}
	// Freeze all evaluator inputs before the first possible native dispatch.
	_, doc, err := owner.Load(ctx, taskID)
	if err != nil {
		return result, err
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
		return result, c.Fail(c.Conflict, "independent capture differs from frozen original candidate")
	}
	recipeDigest, _ := c.Digest(recipe)
	registration := NativeRegistration{SchemaVersion: 2, Source: sourceFile.Digest, Recipe: recipeDigest, Candidate: doc.Candidate.SnapshotDigest, Scope: NativeMode, Task: taskID}
	registrationFile, err := publishNativeFile(root, "registration.json", registration)
	if err != nil {
		return result, err
	}
	_, runErr := owner.Run(ctx, taskID)
	// A failing check may leave the generic coding loop waiting for repair.
	// Independent evaluation never repairs or chooses another candidate.
	closureCtx, closureCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer closureCancel()
	ownerState, closureErr := owner.State(closureCtx, taskID)
	if closureErr != nil {
		return result, closureErr
	}
	if ownerState.Execution != c.Terminated {
		if _, closureErr = owner.ControlsWithID(closureCtx, taskID, "cancel", "independent-finalize"); closureErr != nil {
			return result, closureErr
		}
	}
	result, err = deriveNativeResultForTask(ctx, owner, taskID, source, recipe)
	if err != nil {
		return result, err
	}
	after, _, err := original.FreezeEvaluationSource(ctx, task, recipeFile, output)
	if err != nil {
		return result, err
	}
	beforeDigest, _ := c.Digest(source)
	afterDigest, _ := c.Digest(after)
	if beforeDigest != afterDigest {
		return result, c.Fail(c.Conflict, "original source binding changed during independent evaluation")
	}
	resultFile, err := publishNativeFile(root, "result.json", result)
	if err != nil {
		return result, err
	}
	manifest := NativeManifest{1, NativeMode, sourceFile.Digest, recipeDigest, []OutputFile{sourceFile, registrationFile, resultFile}}
	if _, err = publishNativeFile(root, "manifest.json", manifest); err != nil {
		return result, err
	}
	if err = fileguard.SyncParents(root, "."); err != nil {
		return result, err
	}
	// UNKNOWN/FAIL are inspectable results; admission/infrastructure errors never
	// masquerade as a passing computation and retain the owner for reconciliation.
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

// Schema-1 bundles retain their original call-ID recipe for durable inspection.
// This compatibility path does not grant the old bundle a privacy lineage.
func legacyEvaluatorFixture(recipe NativeRecipe) ([]byte, error) {
	raw, err := evaluatorFixture(recipe)
	if err != nil {
		return nil, err
	}
	var fixture agent.Fixture
	if err = c.DecodeStrict(raw, &fixture); err != nil {
		return nil, err
	}
	for i, check := range recipe.Plan.Checks {
		fixture.Turns[i].Calls[0].ID = "evaluate-" + check.ID
	}
	return c.CanonicalV1(fixture)
}
