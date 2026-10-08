package verify

import (
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
)

func writeOriginFile(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func checkFixture(t *testing.T) (string, workspace.Capture, CheckOrigin, c.TaskSpec) {
	t.Helper()
	root := t.TempDir()
	writeOriginFile(t, root, "src/app.go", "package app\n")
	writeOriginFile(t, root, "tests/oracle.go", "expected: deny reuse\n")
	writeOriginFile(t, root, "tests/helper.txt", "fixture: two concurrent requests\r\n")
	writeOriginFile(t, root, "test.config", "selection: all\n")
	base, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	plan := CheckPlan{SchemaVersion: 1, Checks: []CheckDefinition{{ID: "rotation", Kind: "TEST", RequirementIDs: []string{"goal"}, Argv: []string{"trusted-runner", "--selection", "all"}, RunnerDigest: c.HashBytes([]byte("trusted runner v1")), Selection: "all rotation cases", ExpectedTests: []string{"rotation", "reuse", "concurrent"}, Closure: []CheckScope{{Path: "tests", Recursive: true}, {Path: "test.config"}, {Path: "extra.config"}, {Path: "future-tests", Recursive: true}}}}}
	input := c.HashBytes([]byte("rotation"))
	origin, err := BuildCheckOrigin("task", base, input, plan)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := c.Digest(origin)
	spec := c.TaskSpec{SchemaVersion: 1, TaskID: "task", Version: 1, Goal: "rotation", Inputs: []c.InputSource{{ID: "initial", PayloadRef: "blob://raw", Digest: input, ByteLength: 8, Integrity: c.Intact}}, Requirements: []c.Requirement{{ID: "goal", Required: true, Risk: "NORMAL", VerificationMethod: "protected-test", Source: c.SourceSpan{InputID: "initial", End: 8}}}, ProtectedOrigin: digest, DeliveryPolicy: "CANDIDATE_ONLY"}
	if err := ValidateCheckOrigin(spec, origin, base); err != nil {
		t.Fatal(err)
	}
	return root, base, origin, spec
}

func TestF26ProtectedClosureRejectsWeakeningBeforeFirstVerification(t *testing.T) {
	cases := map[string]func(*testing.T, string){
		"test expectation": func(t *testing.T, r string) { writeOriginFile(t, r, "tests/oracle.go", "always PASS\n") },
		"helper":           func(t *testing.T, r string) { writeOriginFile(t, r, "tests/helper.txt", "single request\n") },
		"discovery config": func(t *testing.T, r string) { writeOriginFile(t, r, "test.config", "selection: none\n") },
		"removed test": func(t *testing.T, r string) {
			if err := os.Remove(filepath.Join(r, "tests/oracle.go")); err != nil {
				t.Fatal(err)
			}
		},
		"added skip helper": func(t *testing.T, r string) { writeOriginFile(t, r, "tests/skip.go", "skip all\n") },
		"negative config":   func(t *testing.T, r string) { writeOriginFile(t, r, "extra.config", "expected: weakened\n") },
		"negative tree":     func(t *testing.T, r string) { writeOriginFile(t, r, "future-tests/skip.go", "skip all\n") },
		"empty directory": func(t *testing.T, r string) {
			if err := os.Mkdir(filepath.Join(r, "tests/empty"), 0700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root, _, origin, _ := checkFixture(t)
			mutate(t, root)
			next, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if err := GuardProtectedCandidate(origin, next); err == nil {
				t.Fatal("protected closure weakening admitted")
			}
		})
	}
}
func TestProtectedOriginPermitsProductionChangeAndFreezeBindsCandidate(t *testing.T) {
	root, base, origin, spec := checkFixture(t)
	env := c.HashBytes([]byte("environment"))
	policy := c.HashBytes([]byte("policy"))
	first, err := FreezeChecks(spec, origin, base, base, env, policy)
	if err != nil {
		t.Fatal(err)
	}
	writeOriginFile(t, root, "src/app.go", "package app\n// rotation implemented\n")
	next, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := FreezeChecks(spec, origin, base, next, env, policy)
	if err != nil {
		t.Fatal(err)
	}
	if first.Binding.CandidateDigest == second.Binding.CandidateDigest || first.Binding.CheckSetDigest != second.Binding.CheckSetDigest || second.ClosureCoverage != "EXPLICIT_UNREVIEWED" || origin.Provenance != "OPERATOR_DECLARED_REPOSITORY_BASELINE" {
		t.Fatal("wrong provenance or freeze bindings", first, second)
	}
	otherPolicy, err := FreezeChecks(spec, origin, base, next, env, c.HashBytes([]byte("new policy")))
	if err != nil || otherPolicy.Binding.PolicyDigest == second.Binding.PolicyDigest {
		t.Fatal("stale policy binding")
	}
	otherEnv, err := FreezeChecks(spec, origin, base, next, c.HashBytes([]byte("new env")), policy)
	if err != nil || otherEnv.Binding.EnvironmentDigest == second.Binding.EnvironmentDigest {
		t.Fatal("stale env binding")
	}
}
func TestF13OriginRevisionCannotSilentlyRemoveChecksOrCoverage(t *testing.T) {
	_, base, origin, spec := checkFixture(t)
	mutations := map[string]func(*CheckOrigin){
		"drop check":      func(o *CheckOrigin) { o.Plan.Checks = nil },
		"drop scope":      func(o *CheckOrigin) { o.Protected = nil },
		"forged observer": func(o *CheckOrigin) { o.Provenance = "INDEPENDENT_OBSERVER" },
		"forged closure":  func(o *CheckOrigin) { o.ClosureCoverage = "COMPLETE" },
		"weak discovery":  func(o *CheckOrigin) { o.Plan.Checks[0].ExpectedTests = []string{"rotation"} },
		"replace runner":  func(o *CheckOrigin) { o.Plan.Checks[0].RunnerDigest = c.HashBytes([]byte("fake")) },
		"change argv":     func(o *CheckOrigin) { o.Plan.Checks[0].Argv = []string{"echo", "PASS"} },
		"foreign task":    func(o *CheckOrigin) { o.TaskID = "other" },
		"wrong baseline":  func(o *CheckOrigin) { o.BaselineDigest = c.HashBytes([]byte("other")) },
		"wrong input":     func(o *CheckOrigin) { o.InputDigest = c.HashBytes([]byte("other")) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			raw, _ := c.CanonicalV1(origin)
			var copy CheckOrigin
			if err := c.DecodeStrict(raw, &copy); err != nil {
				t.Fatal(err)
			}
			mutate(&copy)
			if err := ValidateCheckOrigin(spec, copy, base); err == nil {
				t.Fatal("origin weakening accepted")
			}
		})
	}
	spec.Version++
	spec.Requirements = append(spec.Requirements, c.Requirement{ID: "new-goal", Required: true, Risk: "NORMAL", VerificationMethod: "unresolved", Source: c.SourceSpan{InputID: "initial", End: 8}})
	if err := ValidateCheckOrigin(spec, origin, base); err != nil {
		t.Fatal("origin lost across additive revision", err)
	}
	if _, err := FreezeChecks(spec, origin, base, base, c.HashBytes([]byte("env")), c.HashBytes([]byte("policy"))); err == nil {
		t.Fatal("new requirement inherited old coverage")
	}
}
func TestCheckPlanRejectsMissingDiscoveryIncompleteOrUnsafeClosure(t *testing.T) {
	cases := map[string]func(*CheckPlan){
		"zero tests":     func(p *CheckPlan) { p.Checks[0].ExpectedTests = nil },
		"duplicate test": func(p *CheckPlan) { p.Checks[0].ExpectedTests = []string{"x", "x"} },
		"no closure":     func(p *CheckPlan) { p.Checks[0].Closure = nil },
		"aliased scopes": func(p *CheckPlan) {
			p.Checks[0].Closure = []CheckScope{{Path: "tests", Recursive: true}, {Path: "Tests", Recursive: true}}
		},
		"traversal":                   func(p *CheckPlan) { p.Checks[0].Closure = []CheckScope{{Path: "../tests", Recursive: true}} },
		"uncaptured sensitive":        func(p *CheckPlan) { p.Checks[0].Closure = []CheckScope{{Path: ".env"}} },
		"directory without recursion": func(p *CheckPlan) { p.Checks[0].Closure = []CheckScope{{Path: "tests"}} },
		"root without recursion":      func(p *CheckPlan) { p.Checks[0].Closure = []CheckScope{{Path: "."}} },
		"file as tree":                func(p *CheckPlan) { p.Checks[0].Closure = []CheckScope{{Path: "test.config", Recursive: true}} },
		"aliased absent parent":       func(p *CheckPlan) { p.Checks[0].Closure = []CheckScope{{Path: "Tests/not-yet.go"}} },
		"binary or excluded tree":     func(p *CheckPlan) { p.Checks[0].Closure = []CheckScope{{Path: ".", Recursive: true}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root, _, origin, _ := checkFixture(t)
			writeOriginFile(t, root, ".env", "secret")
			if name == "binary or excluded tree" {
				writeOriginFile(t, root, "tests/binary.dat", "\x00binary")
			}
			base, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			mutate(&origin.Plan)
			if _, err := BuildCheckOrigin("task", base, origin.InputDigest, origin.Plan); err == nil {
				t.Fatal("incomplete or unsafe check plan accepted")
			}
		})
	}
}
func TestCheckOriginOwnsOperatorPlanAndNormalizesUnorderedIDs(t *testing.T) {
	_, base, origin, _ := checkFixture(t)
	raw, _ := c.CanonicalV1(origin.Plan)
	var plan CheckPlan
	if err := c.DecodeStrict(raw, &plan); err != nil {
		t.Fatal(err)
	}
	first, err := BuildCheckOrigin("task", base, origin.InputDigest, plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Checks[0].ExpectedTests = []string{"reuse", "concurrent", "rotation"}
	second, err := BuildCheckOrigin("task", base, origin.InputDigest, plan)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Digest(first)
	b, _ := c.Digest(second)
	if a != b {
		t.Fatal("order changed origin")
	}
	plan.Checks[0].Argv[0] = "forged"
	current, _ := c.Digest(first)
	if current != a {
		t.Fatal("caller alias mutated durable origin")
	}
}
func TestUnresolvedOriginNeverSuppliesVacuousCoverage(t *testing.T) {
	_, base, _, spec := checkFixture(t)
	origin, err := BuildCheckOrigin("task", base, spec.Inputs[0].Digest, CheckPlan{SchemaVersion: 1, Checks: []CheckDefinition{}})
	if err != nil {
		t.Fatal(err)
	}
	spec.ProtectedOrigin, _ = c.Digest(origin)
	if err := ValidateCheckOrigin(spec, origin, base); err != nil {
		t.Fatal(err)
	}
	if origin.ClosureCoverage != "UNRESOLVED" {
		t.Fatal("empty checks pretended complete")
	}
	if _, err := FreezeChecks(spec, origin, base, base, c.HashBytes([]byte("env")), c.HashBytes([]byte("policy"))); err == nil {
		t.Fatal("vacuous freeze")
	}
}
