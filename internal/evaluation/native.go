package evaluation

import (
	"context"
	"fmt"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/resource"
	"github.com/ixayldz/Viber/internal/verify"
	"os"
	"path/filepath"
	"time"
)

const NativeMode = "NATIVE_SEPARATE_OWNER_STDIO_V1; POST_HOC_CANDIDATE_VALIDATION"
const RegisteredNativeMode = "NATIVE_SEPARATE_OWNER_STDIO_V1; REGISTERED_FIXTURE_ONLY; NO_MODEL_OR_HOLDOUT_ATTESTATION"
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
	AssignmentID         string                 `json:"assignment_id,omitempty"`
	RegistrationDigest   string                 `json:"registration_digest,omitempty"`
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
	return runCandidatePrepared(ctx, original, task, recipe, source, output, recipeFile, nil, nil, nil)
}

// RunRegisteredCandidate consumes one fixed preregistered assignment. The
// current runner is still a generated local fixture, so its result remains a
// technical observation rather than model-quality or holdout evidence.
func RunRegisteredCandidate(ctx context.Context, original *agent.Session, task, registrationDir, arm string, repeat int, output string) (NativeResult, error) {
	var result NativeResult
	if original == nil {
		return result, c.Fail(c.InvalidArgument, "original task owner required")
	}
	registration, err := LoadRegistration(registrationDir)
	if err != nil {
		return result, err
	}
	assignment, err := AssignmentFor(registration, arm, task, repeat)
	if err != nil {
		return result, err
	}
	recipe, recipeDigest, recipeAsset, err := RegisteredRecipe(registration, task)
	if err != nil {
		return result, err
	}
	buildDigest, err := CurrentExecutableDigest()
	if err != nil {
		return result, err
	}
	if buildDigest != registration.Protocol.EvaluatorDigest || buildDigest != assignment.HarnessDigest {
		return result, c.Fail(c.PolicyDenied, "running executable differs from preregistered evaluator or arm build")
	}
	source, _, err := original.FreezeEvaluationSource(ctx, task, registration.Directory, output)
	if err != nil {
		return result, err
	}
	binding, err := NativeAssignmentBinding(registration, assignment, source, recipeDigest, recipeAsset.Digest, buildDigest)
	if err != nil {
		return result, err
	}
	return runCandidatePrepared(ctx, original, task, recipe, source, output, registration.Directory, &registration, &assignment, &binding)
}

func runCandidatePrepared(ctx context.Context, original *agent.Session, task string, recipe NativeRecipe, source agent.EvaluationSource, output, protectedPath string, registration *Registration, assignment *RegisteredAssignment, assignmentBinding *agent.EvaluationAssignmentBinding) (NativeResult, error) {
	var result NativeResult
	var err error
	preflight, _, err := original.FreezeEvaluationSource(ctx, task, protectedPath, output)
	if err != nil {
		return result, err
	}
	preflightDigest, _ := c.Digest(preflight)
	sourceDigest, _ := c.Digest(source)
	if preflightDigest != sourceDigest {
		return result, c.Fail(c.Conflict, "source or current export authority changed before evaluation")
	}
	state, sourceDoc, err := original.Load(ctx, task)
	if err != nil {
		return result, err
	}
	if state.TaskSeq != source.TaskSequence || state.DocumentDigest != source.DocumentDigest || sourceDoc.Candidate.SnapshotDigest != source.CandidateDigest {
		return result, c.Fail(c.Conflict, "source changed before evaluation report allocation")
	}
	capture, err := original.Archive.Get(sourceDoc.Candidate)
	if err != nil {
		return result, err
	}
	destination, err := fileguard.ResolveProspective(output)
	if err != nil {
		return result, err
	}
	if original.PhysicalQuota == nil {
		return result, c.Fail(c.UnsupportedCapability, "shared physical quota is required for an evaluation output")
	}
	var candidateBytes uint64
	for _, entry := range capture.Snapshot.Entries {
		if entry.Size < 0 || uint64(entry.Size) > ^uint64(0)-candidateBytes {
			return result, c.Fail(c.UnsupportedCapability, "candidate size exceeds evaluation output bound")
		}
		candidateBytes += uint64(entry.Size)
	}
	if candidateBytes > (resource.DefaultLimits().Bytes-(128<<20))/2 {
		return result, c.Fail(c.UnsupportedCapability, "candidate exceeds bounded evaluation output reservation")
	}
	upperBytes := candidateBytes*2 + (128 << 20)
	upperEntries := uint64(len(capture.Snapshot.Entries))*2 + uint64(len(capture.Snapshot.Directories)) + 4096
	if upperEntries > resource.DefaultLimits().Entries {
		return result, c.Fail(c.UnsupportedCapability, "candidate exceeds bounded evaluation output entry reservation")
	}
	recipeDigestForQuota, err := c.Digest(recipe)
	if err != nil {
		return result, err
	}
	registrationDigest := ""
	if registration != nil {
		registrationDigest = registration.Manifest.RegistrationDigest
	}
	requestDigest, err := c.Digest(struct {
		Source       string `json:"source_digest"`
		Recipe       string `json:"recipe_digest"`
		Registration string `json:"registration_digest,omitempty"`
		Target       string `json:"target"`
	}{sourceDigest, recipeDigestForQuota, registrationDigest, destination})
	if err != nil {
		return result, err
	}
	allocation, err := original.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "evaluation", Family: "EVALUATION_REPORT", RequestDigest: requestDigest, TargetRoot: destination, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload})
	if err != nil {
		return result, err
	}
	if err = allocation.Begin(ctx); err != nil {
		return result, err
	}
	ownerPath := filepath.Join(destination, "owner")
	if !allocation.Covers(ownerPath) {
		return result, c.Fail(c.StoreIntegrityError, "evaluation report reservation does not enclose its owner")
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
	var start AssignmentStart
	var assignmentAllocation resource.Allocation
	if assignmentBinding != nil {
		if registration == nil || assignment == nil {
			return result, c.Fail(c.InvalidArgument, "incomplete registered assignment state")
		}
		start, assignmentAllocation, err = StartAssignment(ctx, original.PhysicalQuota, registration.Directory, *assignmentBinding, *assignment, taskID, destination)
		if err != nil {
			return result, err
		}
		assignmentBinding.StartReceiptDigest, err = AssignmentStartDigest(start)
		if err != nil {
			return result, err
		}
	}
	options := agent.StartOptions{TaskID: taskID, TaskKind: "ANALYSIS", Prompt: boundPrompt, Budget: budget, Autonomy: "guided", AllowUnverified: true, Fixture: fixture, CheckPlan: planRaw, CheckRuntime: &recipe.Runtime}
	var createdState c.TaskState
	if assignmentBinding == nil {
		createdState, err = owner.CreateIndependentEvaluator(ctx, original, source, options)
	} else {
		createdState, err = owner.CreateRegisteredIndependentEvaluator(ctx, original, source, options, *assignmentBinding)
	}
	if err != nil {
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
	capture, err = owner.Archive.Get(doc.Candidate)
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
	registrationRecord := NativeRegistration{SchemaVersion: 2, Source: sourceFile.Digest, Recipe: recipeDigest, Candidate: doc.Candidate.SnapshotDigest, Scope: NativeMode, Task: taskID}
	manifestSchema := 1
	manifestMode := NativeMode
	if assignmentBinding != nil {
		registrationRecord.SchemaVersion = 3
		registrationRecord.Scope = RegisteredNativeMode
		registrationRecord.Assignment = assignmentBinding
		manifestSchema = 2
		manifestMode = RegisteredNativeMode
	}
	registrationFile, err := publishNativeFile(root, "registration.json", registrationRecord)
	if err != nil {
		return result, err
	}
	preDispatch, _, err := original.FreezeEvaluationSource(ctx, task, protectedPath, output)
	if err != nil {
		return result, err
	}
	preDispatchDigest, _ := c.Digest(preDispatch)
	if sourceDigest, _ := c.Digest(source); preDispatchDigest != sourceDigest {
		return result, c.Fail(c.Conflict, "source or current export authority changed before evaluator dispatch")
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
	after, _, err := original.FreezeEvaluationSource(ctx, task, protectedPath, output)
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
	files := []OutputFile{sourceFile, registrationFile, resultFile}
	if assignmentBinding != nil {
		finalState, _, stateErr := owner.Load(ctx, taskID)
		if stateErr != nil {
			return result, stateErr
		}
		if _, err = CompleteAssignment(ctx, assignmentAllocation, registration.Directory, root, start, createdState, finalState, resultFile.Digest); err != nil {
			return result, err
		}
		receiptRaw, readErr := fileguard.ReadRegular(root, "assignment_receipt.json", 1<<20)
		if readErr != nil {
			return result, readErr
		}
		files = append(files, OutputFile{"assignment_receipt.json", c.HashBytes(receiptRaw), int64(len(receiptRaw))})
	}
	manifest := NativeManifest{manifestSchema, manifestMode, sourceFile.Digest, recipeDigest, files}
	if _, err = publishNativeFile(root, "manifest.json", manifest); err != nil {
		return result, err
	}
	if err = fileguard.SyncParents(root, "."); err != nil {
		return result, err
	}
	inventory, measureErr := resource.Measure(root, true)
	if measureErr != nil {
		return result, measureErr
	}
	if err = allocation.Commit(ctx, inventory); err != nil {
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
