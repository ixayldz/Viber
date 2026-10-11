package evaluation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func registrationFixture(t *testing.T, cutoff, start time.Time) (string, string, string, string, Protocol) {
	t.Helper()
	root := t.TempDir()
	dataset := filepath.Join(root, "dataset")
	if err := os.Mkdir(dataset, 0700); err != nil {
		t.Fatal(err)
	}
	common := []byte("frozen protocol asset\n")
	commonDigest := c.HashBytes(common)
	commonPath := filepath.Join(dataset, "shared.bin")
	if err := os.WriteFile(commonPath, common, 0600); err != nil {
		t.Fatal(err)
	}
	recipe := nativeRecipe("golang@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "acceptance", []byte("IN\n"), []byte("IN\n"))
	recipeRaw, err := c.CanonicalV1(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dataset, "acceptance.json"), recipeRaw, 0600); err != nil {
		t.Fatal(err)
	}
	acceptanceDigest := c.HashBytes(recipeRaw)
	files := []AssetFile{{Path: "acceptance.json", Digest: acceptanceDigest, Size: int64(len(recipeRaw))}, {Path: "shared.bin", Digest: commonDigest, Size: int64(len(common))}}
	datasetManifest := DatasetManifest{SchemaVersion: 1, Files: files, Directories: []string{}}
	datasetManifestRaw, err := c.CanonicalV1(datasetManifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := "dataset-manifest.json"
	if err = os.WriteFile(filepath.Join(dataset, manifestPath), datasetManifestRaw, 0600); err != nil {
		t.Fatal(err)
	}
	protocol := Protocol{
		SchemaVersion: 1, Version: ProtocolVersion, ID: "registered-fixture", Kind: "HARNESS_ABLATION",
		DatasetDigest: c.HashBytes(datasetManifestRaw), EvaluatorDigest: commonDigest,
		Repeats: 1, PrimaryRepeat: 1, Seed: 7, BootstrapSamples: 1000,
		UncertaintyMethod: "REPOSITORY_CLUSTER_BOOTSTRAP_PCG_V1", Confidence: "0.95",
		StoppingRule: "FIXED_ASSIGNMENT_NO_BEST_RUN", HoldoutRule: "REPOSITORY_DISJOINT_HISTORY_CUTOFF",
		MinRepositoryClusters: 5, MaxLatencyPPM: 1_100_000, MaxReworkPPM: 1_100_000,
		Tasks: []Task{{ID: "task-a", RepositoryDigest: commonDigest, Split: SplitHoldout, Cohort: "SOLUTION", RequirementsDigest: commonDigest, InitialSnapshot: commonDigest, AcceptanceDigest: acceptanceDigest, CheckSetDigest: commonDigest, StartUTC: start.UTC().Format(time.RFC3339), HistoryCutoffUTC: cutoff.UTC().Format(time.RFC3339)}},
		Arms:  []Arm{{ID: "base", HarnessDigest: commonDigest, Conditions: Conditions{Provider: "fixture", ModelProfileDigest: commonDigest, DecodingDigest: commonDigest, ToolCapabilitiesDigest: commonDigest, EnvironmentDigest: commonDigest, BudgetDigest: commonDigest, PrivacyDigest: commonDigest, SafetyBoundaryDigest: commonDigest, AdapterDigest: commonDigest, PromptDigest: commonDigest, KernelDigest: commonDigest, DependencyDigest: commonDigest, PricingDigest: commonDigest, Currency: "USD"}}},
	}
	protocolRaw, err := c.CanonicalV1(protocol)
	if err != nil {
		t.Fatal(err)
	}
	protocolFile := filepath.Join(root, "protocol.json")
	if err = os.WriteFile(protocolFile, protocolRaw, 0600); err != nil {
		t.Fatal(err)
	}
	bindings := []AssetBinding{{Role: "evaluator", Path: "shared.bin"}}
	for role := range map[string]bool{"repository": true, "requirements": true, "initial_snapshot": true, "check_set": true} {
		bindings = append(bindings, AssetBinding{Role: role, Task: "task-a", Path: "shared.bin"})
	}
	bindings = append(bindings, AssetBinding{Role: "acceptance", Task: "task-a", Path: "acceptance.json"})
	for role := range map[string]string{"harness": commonDigest, "model_profile": commonDigest, "decoding": commonDigest, "tool_capabilities": commonDigest, "environment": commonDigest, "budget": commonDigest, "privacy": commonDigest, "safety_boundary": commonDigest, "adapter": commonDigest, "prompt": commonDigest, "kernel": commonDigest, "dependency": commonDigest, "pricing": commonDigest} {
		bindings = append(bindings, AssetBinding{Role: role, Arm: "base", Path: "shared.bin"})
	}
	plan := AssetPlan{SchemaVersion: 1, DatasetManifestPath: manifestPath, Bindings: bindings}
	planRaw, err := c.CanonicalV1(plan)
	if err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(root, "assets.json")
	if err = os.WriteFile(planFile, planRaw, 0600); err != nil {
		t.Fatal(err)
	}
	return root, dataset, protocolFile, planFile, protocol
}

func TestRegistrationFreezesExactAssetsAndRejectsPosthocOrCutoffLeakage(t *testing.T) {
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	root, dataset, protocolFile, planFile, protocol := registrationFixture(t, now.Add(-time.Hour), now.Add(time.Hour))
	output := filepath.Join(root, "registration")
	if _, err := registerDatasetAt(context.Background(), protocolFile, dataset, planFile, output, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistration(output); err != nil {
		t.Fatalf("frozen registration did not validate: %v", err)
	}
	if _, err := registerDatasetAt(context.Background(), protocolFile, dataset, planFile, filepath.Join(root, "posthoc"), nil, now.Add(2*time.Hour)); err == nil {
		t.Fatal("post-hoc registration after the declared task start was accepted")
	}
	if err := os.WriteFile(filepath.Join(output, "assets", "unbound.txt"), []byte("late extra"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistration(output); err == nil {
		t.Fatal("unmanifested late asset was accepted")
	}
	if err := os.Remove(filepath.Join(output, "assets", "unbound.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "assets", "shared.bin"), []byte("edited after freeze"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistration(output); err == nil {
		t.Fatal("changed frozen asset was accepted")
	}
	cutoffRoot, cutoffDataset, cutoffProtocol, cutoffPlan, invalid := registrationFixture(t, now.Add(30*time.Minute), now.Add(time.Hour))
	if err := invalid.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := registerDatasetAt(context.Background(), cutoffProtocol, cutoffDataset, cutoffPlan, filepath.Join(cutoffRoot, "cutoff"), nil, now); err == nil {
		t.Fatal("registration before declared history cutoff was accepted")
	}

	leaked := protocol
	leaked.Tasks = append(leaked.Tasks, leaked.Tasks[0])
	leaked.Tasks[1].ID = "task-b"
	leaked.Tasks[1].Split = SplitDevelopment
	if err := leaked.Validate(); err == nil {
		t.Fatal("same repository in holdout and development was accepted")
	}
}

func TestStartedAssignmentIsSingleUseAndCrashRemainsInterrupted(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	root, dataset, protocolFile, planFile, _ := registrationFixture(t, now.Add(-time.Hour), now.Add(time.Hour))
	owner, err := agent.Open(ctx, filepath.Join(root, "owner-store"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	registrationDir := filepath.Join(root, "registration")
	if _, err = registerDatasetAt(ctx, protocolFile, dataset, planFile, registrationDir, owner.PhysicalQuota, now); err != nil {
		t.Fatal(err)
	}
	registration, err := LoadRegistration(registrationDir)
	if err != nil {
		t.Fatal(err)
	}
	assignment := registration.Assignments[0]
	_, recipeDigest, acceptance, err := RegisteredRecipe(registration, assignment.Task)
	if err != nil {
		t.Fatal(err)
	}
	binding := agent.EvaluationAssignmentBinding{ProtocolDigest: registration.ProtocolDigest, RegistrationDigest: registration.Manifest.RegistrationDigest, AssignmentID: assignment.ID, Arm: assignment.Arm, Task: assignment.Task, Repeat: assignment.Repeat, SourceDigest: c.HashBytes([]byte("source")), SourceTreeDigest: c.HashBytes([]byte("source tree")), RecipeDigest: recipeDigest, RecipeAssetDigest: acceptance.Digest, BuildDigest: assignment.HarnessDigest}
	bundle := filepath.Join(root, "bundle")
	if _, _, err = startAssignmentAt(ctx, owner.PhysicalQuota, registrationDir, binding, assignment, "eval-crash", bundle, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = startAssignmentAt(ctx, owner.PhysicalQuota, registrationDir, binding, assignment, "eval-retry", filepath.Join(root, "retry"), now.Add(2*time.Hour)); err == nil {
		t.Fatal("single-use assignment admitted a retry after a crash")
	}
	status, err := InspectRegistration(registrationDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Planned != 1 || status.Started != 1 || status.Completed != 0 || status.Interrupted != 1 || status.Pending != 0 || len(status.Assignments) != 1 || status.Assignments[0].State != "INTERRUPTED" {
		t.Fatalf("crashed assignment denominator was not retained: %+v", status)
	}
}
