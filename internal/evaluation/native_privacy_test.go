package evaluation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestActualIndependentEvaluationPrivacyPurgesNativeReceipts(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned Docker image required")
	}
	ctx := context.Background()
	original, live, store := producer(t, nil)
	if _, err := original.Run(ctx, "producer"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(live, "app.sh"))
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "evaluation")
	hidden := nativeRecipe(image, "hidden", []byte("private-oracle-content\n"), []byte("private-oracle-content\n"))
	result, err := RunCandidate(ctx, original, "producer", recipeFile(t, hidden), bundle)
	if err != nil || result.IndependentVerdict != c.Pass || !result.EvaluatorQuiescent {
		t.Fatal(result, err)
	}
	raw, err := os.ReadFile(filepath.Join(bundle, "registration.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registration NativeRegistration
	if err = c.DecodeStrict(raw, &registration); err != nil || registration.SchemaVersion != 2 || registration.Task == "" || result.EvaluatorTask != registration.Task {
		t.Fatal(registration, err)
	}
	if _, err = original.DeletionPreview(ctx, "producer"); err == nil {
		t.Fatal("retained native evaluator did not pin source")
	}
	if err = original.Close(); err != nil {
		t.Fatal(err)
	}
	child, err := agent.OpenExisting(ctx, filepath.Join(bundle, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	resources, err := child.Journal.ResourceLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := child.Journal.TokenLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := child.DeletionPreview(ctx, registration.Task)
	if err != nil || len(plan.Copies) != 1 || len(plan.Copies[0].Files) != 4 || len(plan.Objects) < 10 {
		t.Fatal("native oracle/receipt/report inventory incomplete", len(plan.Copies), len(plan.Objects), err)
	}
	deleted, err := child.DeleteContent(ctx, agent.DeletionCommand{CommandID: "delete-native-evaluator", Plan: plan})
	if err != nil || deleted.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal(deleted, err)
	}
	resourcesAfter, err := child.Journal.ResourceLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tokensAfter, err := child.Journal.TokenLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Digest(resources)
	b, _ := c.Digest(resourcesAfter)
	x, _ := c.Digest(tokens)
	y, _ := c.Digest(tokensAfter)
	if a != b || x != y {
		t.Fatal("native deletion changed charged or reserved accounting")
	}
	inventory, err := child.Archive.TaskInventory(ctx, registration.Task)
	if err != nil || len(inventory) != 0 {
		t.Fatal("native raw receipt/oracle content survived", inventory, err)
	}
	if _, err = ReadNativeResult(ctx, bundle); err == nil {
		t.Fatal("deleted bundle still supplies verdict")
	}
	child.Close()
	reopened, err := agent.OpenExisting(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.DeletionPreview(ctx, "producer"); err != nil {
		t.Fatal("purged evaluator still pins producer", err)
	}
	after, err := os.ReadFile(filepath.Join(live, "app.sh"))
	if err != nil || string(after) != string(before) {
		t.Fatal("native purge touched live source", err)
	}
}
