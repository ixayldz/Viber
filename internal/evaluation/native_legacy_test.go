package evaluation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestLegacyIndependentBundleKeepsOldFixtureAndCannotClaimNewLineage(t *testing.T) {
	ctx := context.Background()
	original, live, _ := producer(t, nil)
	if _, err := original.Run(ctx, "producer"); err != nil {
		t.Fatal(err)
	}
	source, _, err := original.FreezeEvaluationSource(ctx, "producer")
	if err != nil {
		t.Fatal(err)
	}
	recipe := nativeRecipe("golang@sha256:"+strings.Repeat("a", 64), "hidden", []byte("private"), []byte("private"))
	fixture, err := legacyEvaluatorFixture(recipe)
	if err != nil {
		t.Fatal(err)
	}
	prompt, _ := evaluatorSourcePrompt(source)
	plan, _ := c.CanonicalV1(recipe.Plan)
	bundle := t.TempDir()
	owner, err := agent.Open(ctx, filepath.Join(bundle, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Create(ctx, agent.StartOptions{TaskID: evaluatorTask, Root: live, TaskKind: "ANALYSIS", Prompt: prompt, Fixture: fixture, CheckPlan: plan, CheckRuntime: &recipe.Runtime, Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true}); err != nil {
		owner.Close()
		t.Fatal(err)
	}
	result, err := deriveNativeResultForTask(ctx, owner, evaluatorTask, source, recipe)
	if err != nil || result.EvaluatorTask != "" {
		owner.Close()
		t.Fatal(result, err)
	}
	owner.Close()
	root, err := os.OpenRoot(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sourceFile, err := publishNativeFile(root, "source.json", source)
	if err != nil {
		t.Fatal(err)
	}
	registration := NativeRegistration{SchemaVersion: 1, Source: sourceFile.Digest, Recipe: result.RecipeDigest, Candidate: result.EvaluatorCandidate, Scope: NativeMode}
	regFile, err := publishNativeFile(root, "registration.json", registration)
	if err != nil {
		t.Fatal(err)
	}
	resultFile, err := publishNativeFile(root, "result.json", result)
	if err != nil {
		t.Fatal(err)
	}
	manifest := NativeManifest{1, NativeMode, sourceFile.Digest, result.RecipeDigest, []OutputFile{sourceFile, regFile, resultFile}}
	if _, err = publishNativeFile(root, "manifest.json", manifest); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadNativeResult(ctx, bundle); err != nil || got.EvaluatorTask != "" {
		t.Fatal("old fixture/result inspection changed", got, err)
	}
	registration.SchemaVersion = 2
	registration.Task = evaluatorTask
	raw, _ := c.CanonicalV1(registration)
	if err = os.WriteFile(filepath.Join(bundle, "registration.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	manifest.Files[1] = OutputFile{"registration.json", c.HashBytes(raw), int64(len(raw))}
	raw, _ = c.CanonicalV1(manifest)
	if err = os.WriteFile(filepath.Join(bundle, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadNativeResult(ctx, bundle); err == nil {
		t.Fatal("rehashing old registration claimed new prepublication lineage")
	}
}
