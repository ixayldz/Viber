package evaluation

import (
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeBundleSourceClaimCannotBeRehashedAwayFromOwnerJournal(t *testing.T) {
	ctx := context.Background()
	original, _, _ := producer(t, nil)
	if _, err := original.Run(ctx, "producer"); err != nil {
		t.Fatal(err)
	}
	source, _, err := original.FreezeEvaluationSource(ctx, "producer")
	if err != nil {
		t.Fatal(err)
	}
	recipe := nativeRecipe("golang@sha256:"+strings.Repeat("a", 64), "hidden", []byte("private"), []byte("private"))
	bundle := t.TempDir()
	owner, err := agent.Open(ctx, filepath.Join(bundle, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := evaluatorFixture(recipe)
	plan, _ := c.CanonicalV1(recipe.Plan)
	prompt, _ := evaluatorSourcePrompt(source)
	if _, err = owner.CreateIndependentEvaluator(ctx, original, source, agent.StartOptions{TaskID: evaluatorTask, TaskKind: "ANALYSIS", Prompt: prompt, Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture, CheckPlan: plan, CheckRuntime: &recipe.Runtime}); err != nil {
		owner.Close()
		t.Fatal(err)
	}
	result, err := deriveNativeResult(ctx, owner, source, recipe)
	if err != nil {
		owner.Close()
		t.Fatal(err)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sourceFile, err := publishNativeFile(root, "source.json", source)
	if err != nil {
		t.Fatal(err)
	}
	registration := NativeRegistration{1, sourceFile.Digest, result.RecipeDigest, result.EvaluatorCandidate, NativeMode}
	registrationFile, err := publishNativeFile(root, "registration.json", registration)
	if err != nil {
		t.Fatal(err)
	}
	resultFile, err := publishNativeFile(root, "result.json", result)
	if err != nil {
		t.Fatal(err)
	}
	manifest := NativeManifest{1, NativeMode, sourceFile.Digest, result.RecipeDigest, []OutputFile{sourceFile, registrationFile, resultFile}}
	if _, err = publishNativeFile(root, "manifest.json", manifest); err != nil {
		t.Fatal(err)
	}
	got, err := ReadNativeResult(ctx, bundle)
	if err != nil || got.IndependentVerdict != c.Unknown {
		t.Fatal("valid incomplete bundle rejected", got, err)
	}
	// Simulate an attacker changing all mutable report-envelope hashes together.
	// The original owner raw input/protected origin remains fixed before dispatch.
	source.Quality = c.Verified
	result.Source = source
	result.OriginalClaim = sourceClaim(source)
	result.UnknownVerifiedClaim = true
	rewrite := func(name string, value any) OutputFile {
		raw, err := c.CanonicalV1(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(bundle, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		return OutputFile{name, c.HashBytes(raw), int64(len(raw))}
	}
	sourceFile = rewrite("source.json", source)
	registration.Source = sourceFile.Digest
	registrationFile = rewrite("registration.json", registration)
	resultFile = rewrite("result.json", result)
	manifest.SourceDigest = sourceFile.Digest
	manifest.Files = []OutputFile{sourceFile, registrationFile, resultFile}
	rewrite("manifest.json", manifest)
	if _, err = ReadNativeResult(ctx, bundle); err == nil {
		t.Fatal("rehashed original quality claim escaped immutable owner source binding")
	}
}
