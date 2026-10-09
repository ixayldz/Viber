package evaluation

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/verify"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeRecipe(image, id string, input, expected []byte) NativeRecipe {
	profile := runner.DefaultProfile(image)
	profile.TimeoutSeconds = 5
	profile.MaxOutputBytes = 4096
	suite := verify.StdioSuite{SchemaVersion: 1, CheckID: id, Level: "V4", Protocol: verify.ObserverProtocol, Repeats: 2, Cases: []verify.StdioCase{{ID: "exact", Input: input, Stdout: expected}}}
	oracle, _ := c.Digest(suite)
	profileHash, _ := c.Digest(profile)
	plan := verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{ID: id, Kind: "TEST", RequirementIDs: []string{"user-goal"}, ExpectedTests: verify.SuiteCaseIDs(suite), ObserverDigest: oracle, RunnerDigest: profileHash, Argv: []string{"/bin/sh", "/workspace/app.sh"}, Selection: "all", Closure: []verify.CheckScope{{Path: "checks", Recursive: true}}}}}
	return NativeRecipe{1, plan, agent.CheckRuntime{SchemaVersion: 1, Profile: profile, ObserverSuites: []verify.StdioSuite{suite}}}
}
func recipeFile(t *testing.T, recipe NativeRecipe) string {
	t.Helper()
	raw, err := c.CanonicalV1(recipe)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hidden.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func producer(t *testing.T, public *NativeRecipe, prompts ...string) (*agent.Session, string, string) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "app.sh"), []byte("cat\n"), 0644); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	session, err := agent.Open(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	options := agent.StartOptions{TaskID: "producer", Root: source, Prompt: []byte("Echo supplied bytes exactly, with no stderr."), Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true}
	if len(prompts) > 0 {
		options.Prompt = []byte(prompts[0])
	}
	fixture := agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "Candidate is ready.", UsageKnown: true}}}
	if public != nil {
		args, _ := c.CanonicalV1(struct {
			Check string `json:"check_id"`
		}{public.Plan.Checks[0].ID})
		fixture.Turns = append([]agent.Turn{{Calls: []model.Call{{ID: "public-check", Name: "check_run", Arguments: args}}, UsageKnown: true}}, fixture.Turns...)
		options.CheckPlan, _ = c.CanonicalV1(public.Plan)
		options.CheckRuntime = &public.Runtime
		options.GoalReview = &verify.GoalReview{SchemaVersion: 1, InputDigests: []string{c.HashBytes(options.Prompt)}, Coverage: []verify.GoalMapping{{RequirementID: "user-goal", CheckIDs: []string{public.Plan.Checks[0].ID}}}, DependencyChecks: []string{public.Plan.Checks[0].ID}, Acknowledgement: verify.CoverageAcknowledgement}
	}
	options.Fixture, _ = c.CanonicalV1(fixture)
	if _, err = session.Create(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	return session, source, store
}
func TestEvaluationFreezeUsesImmutableCandidateAndRejectsOverlaps(t *testing.T) {
	ctx := context.Background()
	session, source, _ := producer(t, nil)
	if _, _, err := session.FreezeEvaluationSource(ctx, "producer"); err == nil {
		t.Fatal("nonterminal source admitted")
	}
	if _, err := session.Run(ctx, "producer"); err != nil {
		t.Fatal(err)
	}
	frozen, path, err := session.FreezeEvaluationSource(ctx, "producer")
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := session.Load(ctx, "producer")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "app.sh"), []byte("printf mutated\n"), 0644); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(path, "app.sh"))
	if err != nil || string(raw) != "cat\n" {
		t.Fatal("live source became evaluator input", string(raw), err)
	}
	for _, overlap := range []string{filepath.Join(source, "hidden.json"), filepath.Dir(path), source} {
		if _, _, err = session.FreezeEvaluationSource(ctx, "producer", overlap); err == nil {
			t.Fatal("hidden input/output overlap admitted", overlap)
		}
	}
	after, _, err := session.Load(ctx, "producer")
	if err != nil || after.DocumentDigest != before.DocumentDigest || frozen.CandidateDigest != after.CandidateDigest {
		t.Fatal(after, err)
	}
}
func TestIndependentRecipeCannotDowngradeOrWeakenOracle(t *testing.T) {
	recipe := nativeRecipe("golang@sha256:"+strings.Repeat("a", 64), "hidden", []byte("private"), []byte("private"))
	if err := recipe.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"unbound", "oracle", "level", "missing", "ordinary", "duplicate", "requirements"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := c.CanonicalV1(recipe)
			var altered NativeRecipe
			c.DecodeStrict(raw, &altered)
			switch mode {
			case "unbound":
				altered.Plan.Checks[0].RunnerDigest = ""
			case "oracle":
				altered.Runtime.ObserverSuites[0].Cases[0].Stdout = []byte("weaker")
			case "level":
				altered.Runtime.ObserverSuites[0].Level = "V0"
			case "missing":
				altered.Runtime.ObserverSuites = nil
			case "ordinary":
				altered.Plan.Checks[0].Kind = "BUILD"
			case "duplicate":
				altered.Plan.Checks = append(altered.Plan.Checks, altered.Plan.Checks[0])
				altered.Runtime.ObserverSuites = append(altered.Runtime.ObserverSuites, altered.Runtime.ObserverSuites[0])
			case "requirements":
				altered.Plan.Checks[0].RequirementIDs = []string{"other"}
			}
			if err := altered.Validate(); err == nil {
				t.Fatal("invalid hidden recipe admitted")
			}
		})
	}
}
func TestActualIndependentHiddenFailureOverridesSelfVerifiedOnlyInEvaluation(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned Docker image required")
	}
	ctx := context.Background()
	public := nativeRecipe(image, "public", []byte("PUBLIC-CASE\n"), []byte("PUBLIC-CASE\n"))
	session, source, _ := producer(t, &public, "Transform supplied ASCII bytes to uppercase, with no stderr.")
	state, err := session.Run(ctx, "producer")
	if err != nil || state.Quality != c.Verified {
		t.Fatal(state, err)
	}
	original, originalDoc, err := session.Load(ctx, "producer")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name     string
		input    []byte
		expected []byte
		verdict  c.Verdict
	}{
		{"passing", []byte("HIDDEN-SIGNAL-DO-NOT-LEAK\n"), []byte("HIDDEN-SIGNAL-DO-NOT-LEAK\n"), c.Pass},
		{"failing", []byte("hidden-signal-do-not-leak\n"), []byte("HIDDEN-SIGNAL-DO-NOT-LEAK\n"), c.FailVerdict},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			hidden := nativeRecipe(image, "hidden", scenario.input, scenario.expected)
			bundle := filepath.Join(t.TempDir(), "evaluation")
			result, err := RunCandidate(ctx, session, "producer", recipeFile(t, hidden), bundle)
			if err != nil || result.IndependentVerdict != scenario.verdict || !result.EvaluatorQuiescent || len(result.Checks) != 1 || !result.Checks[0].NativeOwnership || result.Checks[0].Attempts != 2 || result.Checks[0].BaselineOverheadAttempts != 2 {
				t.Fatal(result, err)
			}
			if result.FalseVerified != (scenario.verdict == c.FailVerdict) || result.FalseSuccess != result.FalseVerified || result.AdoptionAllowed || result.ReleaseEvidence || result.SafetyVerdict != c.Unknown {
				t.Fatal(result)
			}
			inspected, err := ReadNativeResult(ctx, bundle)
			first, _ := c.Digest(result)
			second, _ := c.Digest(inspected)
			if err != nil || first != second {
				t.Fatal("durable independent evidence did not reproduce", inspected, err)
			}
			// Rehashing a forged result cannot replace the durable evaluator facts.
			result.IndependentVerdict = c.Pass
			result.FalseVerified = false
			raw, _ := c.CanonicalV1(result)
			if err = os.WriteFile(filepath.Join(bundle, "result.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			manifestRaw, _ := os.ReadFile(filepath.Join(bundle, "manifest.json"))
			var manifest NativeManifest
			if err = c.DecodeStrict(manifestRaw, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Files[2] = OutputFile{"result.json", c.HashBytes(raw), int64(len(raw))}
			manifestRaw, _ = c.CanonicalV1(manifest)
			os.WriteFile(filepath.Join(bundle, "manifest.json"), manifestRaw, 0600)
			if scenario.verdict == c.FailVerdict {
				if _, err = ReadNativeResult(ctx, bundle); err == nil {
					t.Fatal("forged self-report replaced independent facts")
				}
			}
		})
	}
	after, doc, err := session.Load(ctx, "producer")
	{ // TaskState contains slices; compare canonical digests.
		beforeHash, _ := c.Digest(original)
		afterHash, _ := c.Digest(after)
		if err != nil || beforeHash != afterHash {
			t.Fatal("evaluation mutated producer", after, err)
		}
	}
	raw, _ := json.Marshal(doc.Messages)
	if bytes.Contains(raw, []byte("hidden-signal")) {
		t.Fatal("hidden feedback entered original model")
	}
	beforeHash, _ := c.Digest(originalDoc)
	afterHash, _ := c.Digest(doc)
	if beforeHash != afterHash {
		t.Fatal("original task document changed")
	}
	raw, err = os.ReadFile(filepath.Join(source, "app.sh"))
	if err != nil || string(raw) != "cat\n" {
		t.Fatal("independent evaluator wrote live source", string(raw), err)
	}
}
