package cli

import (
	"bytes"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/verify"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func operatorRecipe() operatorChecks {
	p := runner.DefaultProfile("golang@sha256:" + strings.Repeat("a", 64))
	p.MaxOutputBytes = 16 << 10
	suite := verify.StdioSuite{SchemaVersion: 1, CheckID: "echo", Level: "V4", Protocol: verify.ObserverProtocol, Repeats: 2, Cases: []verify.StdioCase{{ID: "one", Input: []byte("case-canary"), Stdout: []byte("case-canary")}}}
	return operatorChecks{SchemaVersion: 1, Plan: verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{ID: "echo", Kind: "TEST", RequirementIDs: []string{"user-goal"}, Argv: []string{"/bin/cat"}, Selection: "all", ExpectedTests: []string{"one"}, Closure: []verify.CheckScope{{Path: "checks", Recursive: true}}}}}, Runtime: agent.CheckRuntime{SchemaVersion: 1, Profile: p, ObserverSuites: []verify.StdioSuite{suite}}}
}
func TestCheckPrepareBindsExplicitOracleAndPreservesCompatibility(t *testing.T) {
	parent := t.TempDir()
	recipe := filepath.Join(parent, "recipe.json")
	output := filepath.Join(parent, "bound.json")
	raw, _ := json.Marshal(operatorRecipe())
	if err := os.WriteFile(recipe, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := Execute([]string{"check-prepare", "--file", recipe, "--output", output}, &out, &errs); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	if strings.Contains(out.String(), "case-canary") {
		t.Fatal("oracle leaked in command diagnostics")
	}
	bound, err := readOperatorChecks(output)
	if err != nil {
		t.Fatal(err)
	}
	if !c.ValidDigest(bound.Plan.Checks[0].ObserverDigest) || !c.ValidDigest(bound.Plan.Checks[0].RunnerDigest) {
		t.Fatal("recipe bindings absent")
	}
	original, _ := os.ReadFile(output)
	out.Reset()
	errs.Reset()
	if code := Execute([]string{"check-prepare", "--file", recipe, "--output", output}, &out, &errs); code != 4 {
		t.Fatal("existing oracle overwritten", code)
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(after, original) {
		t.Fatal("existing binding changed")
	}
	runtimeFile := filepath.Join(parent, "runtime.json")
	planFile := filepath.Join(parent, "plan.json")
	raw, _ = json.Marshal(bound.Runtime)
	os.WriteFile(runtimeFile, raw, 0600)
	raw, _ = json.Marshal(bound.Plan)
	os.WriteFile(planFile, raw, 0600)
	out.Reset()
	errs.Reset()
	if code := Execute([]string{"check-config", "--runtime", runtimeFile, "--plan", planFile, "--json"}, &out, &errs); code != 0 || !strings.Contains(out.String(), bound.Plan.Checks[0].RunnerDigest) {
		t.Fatal("existing command regressed", code, out.String(), errs.String())
	}
	// A conflicting preexisting binding must never be silently replaced.
	bound.Runtime.ObserverSuites[0].Cases[0].Stdout = []byte("weaker")
	raw, _ = json.Marshal(bound)
	os.WriteFile(recipe, raw, 0600)
	out.Reset()
	errs.Reset()
	if code := Execute([]string{"check-prepare", "--file", recipe, "--output", filepath.Join(parent, "weakened.json")}, &out, &errs); code != 4 {
		t.Fatal("oracle swap accepted", code)
	}
}
func TestObserverConfigurationInsideSourceDeniedBeforeStore(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	os.Mkdir(source, 0700)
	recipe := filepath.Join(source, "oracle.json")
	fixture := filepath.Join(parent, "fixture.json")
	store := filepath.Join(parent, "store")
	raw, _ := json.Marshal(operatorRecipe())
	os.WriteFile(recipe, raw, 0600)
	os.WriteFile(fixture, []byte(`{"schema_version":1,"turns":[{"text":"done","usage_known":true}]}`), 0600)
	var out, errs bytes.Buffer
	code := Execute([]string{"run", "echo", "--root", source, "--store", store, "--offline", "--fixture", fixture, "--task", "t", "--check-config", recipe, "--json"}, &out, &errs)
	if code != 4 || !strings.Contains(out.String(), "outside source") {
		t.Fatal(code, out.String(), errs.String())
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatal("oracle denial created store", err)
	}
}
