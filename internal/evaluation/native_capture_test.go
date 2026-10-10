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

func TestIndependentCaptureImportsExactArchiveMetadataAndNeverRecapturesLiveSource(t *testing.T) {
	ctx := context.Background()
	original, live, _ := producer(t, nil)
	if _, err := original.Run(ctx, "producer"); err != nil {
		t.Fatal(err)
	}
	source, _, err := original.FreezeEvaluationSource(ctx, "producer")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(live, "app.sh"), []byte("live edit must survive\n"), 0600); err != nil {
		t.Fatal(err)
	}
	recipe := nativeRecipe("golang@sha256:"+strings.Repeat("a", 64), "hidden", []byte("secret"), []byte("secret"))
	owner, err := original.OpenIndependentEvaluator(ctx, filepath.Join(t.TempDir(), "owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	fixture, _ := evaluatorFixture(recipe)
	plan, _ := c.CanonicalV1(recipe.Plan)
	boundPrompt, err := evaluatorSourcePrompt(source)
	if err != nil {
		t.Fatal(err)
	}
	options := agent.StartOptions{TaskID: owner.EvaluationTaskID(), TaskKind: "ANALYSIS", Prompt: boundPrompt, Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture, CheckPlan: plan, CheckRuntime: &recipe.Runtime}
	if _, err = owner.CreateIndependentEvaluator(ctx, original, source, options); err != nil {
		t.Fatal(err)
	}
	_, doc, err := owner.Load(ctx, owner.EvaluationTaskID())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Candidate.SnapshotDigest != source.CandidateDigest || doc.Baseline != doc.Candidate {
		t.Fatal("original modes or root metadata changed", doc.Candidate, source)
	}
	capture, err := owner.Archive.Get(doc.Candidate)
	if err != nil || string(capture.Contents["app.sh"]) != "cat\n" {
		t.Fatal(capture, err)
	}
	tree, err := agent.CapturedTreeDigest(capture)
	if err != nil || tree != source.ContentDigest {
		t.Fatal(tree, err)
	}
	raw, err := os.ReadFile(filepath.Join(live, "app.sh"))
	if err != nil || string(raw) != "live edit must survive\n" {
		t.Fatal("live edit changed", string(raw), err)
	}
	if result, err := deriveNativeResult(ctx, owner, source, recipe); err != nil || result.IndependentVerdict != c.Unknown {
		t.Fatal("valid pre-dispatch metadata rejected", result, err)
	}
	forged := source
	forged.Quality = c.Verified
	if _, err = deriveNativeResult(ctx, owner, forged, recipe); err == nil {
		t.Fatal("source claim rewrite accepted without bound journal input")
	}
	fresh, err := original.OpenIndependentEvaluator(ctx, filepath.Join(t.TempDir(), "owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err = fresh.CreateIndependentEvaluator(ctx, original, forged, options); err == nil {
		t.Fatal("forged source claim imported before dispatch")
	}
	source.TaskSequence++
	if _, err = fresh.CreateIndependentEvaluator(ctx, original, source, options); err == nil {
		t.Fatal("stale source binding admitted")
	}
	if _, err = original.CreateIndependentEvaluator(ctx, original, source, options); err == nil {
		t.Fatal("same owner accepted as independent")
	}
}
