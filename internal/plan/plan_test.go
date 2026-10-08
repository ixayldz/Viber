package plan

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func definition() (Definition, c.TaskSpec) {
	spec := c.TaskSpec{Version: 1, Requirements: []c.Requirement{{ID: "goal", Required: true}}}
	node := func(id string) Node {
		return Node{ID: id, Goal: "implement " + id, InputContract: "current candidate", OutputContract: "bound work product", ReadScope: []string{"**"}, WriteScope: []string{"src/**"}, RequirementIDs: []string{"goal"}, RequiredChecks: []string{"check"}, Mutex: []string{"source"}}
	}
	return Definition{SchemaVersion: 1, ID: "plan-one", SpecVersion: 1, PolicyEpoch: 1, BaseCandidate: c.HashBytes([]byte("before")), Nodes: []Node{node("b"), node("a")}, Relations: []Relation{{From: "b", To: "a", Kind: "DEPENDS_ON"}, {From: "a", To: "b", Kind: "VERIFIES"}}}, spec
}
func TestDependencySchedulerUsesContractsAndCannotCompleteOutOfOrder(t *testing.T) {
	d, spec := definition()
	state, err := New(d, spec, []string{"check"}, d.BaseCandidate, 1)
	if err != nil {
		t.Fatal(err)
	}
	d.Nodes[0].WriteScope[0] = "**"
	if state.Definition.Nodes[0].WriteScope[0] != "src/**" {
		t.Fatal("caller owns retained plan")
	}
	node, err := state.Next()
	if err != nil || node.ID != "a" {
		t.Fatal(node, err)
	}
	repeated, _ := state.Next()
	if repeated.ID != "a" {
		t.Fatal("second worker scheduled")
	}
	if state.Finish("b", c.HashBytes([]byte("out")), state.CurrentCandidate) == nil {
		t.Fatal("dependency bypass")
	}
	if state.AdmitWrites([]c.Change{{Path: "tests/check.go"}}) == nil {
		t.Fatal("scope bypass")
	}
	if err = state.AdmitWrites([]c.Change{{Path: "src/a.go"}}); err != nil {
		t.Fatal(err)
	}
	if err = state.Finish("a", c.HashBytes([]byte("a-output")), state.CurrentCandidate); err != nil {
		t.Fatal(err)
	}
	node, err = state.Next()
	if err != nil || node.ID != "b" {
		t.Fatal(node, err)
	}
	if err = state.Finish("b", c.HashBytes([]byte("b-output")), state.CurrentCandidate); err != nil {
		t.Fatal(err)
	}
	if !state.Complete() {
		t.Fatal("plan did not complete")
	}
	node, err = state.Next()
	if err != nil || node != nil {
		t.Fatal(node, err)
	}
	if err = state.Validate(spec, []string{"check"}, state.CurrentCandidate, 1); err != nil {
		t.Fatal(err)
	}
}
func TestPlanRejectsCyclesUnknownBindingsCoverageAndUnsafeScopes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Definition)
	}{
		{"cycle", func(d *Definition) {
			d.Relations = append(d.Relations, Relation{From: "a", To: "b", Kind: "DEPENDS_ON"})
		}},
		{"unknown-node", func(d *Definition) { d.Relations[0].To = "missing" }},
		{"unsafe-scope", func(d *Definition) { d.Nodes[0].ReadScope = []string{"../secret/**"} }},
		{"unknown-check", func(d *Definition) { d.Nodes[0].RequiredChecks = []string{"not-origin-check"} }},
		{"unknown-criterion", func(d *Definition) { d.Nodes[0].RequirementIDs = []string{"missing"} }},
		{"missing-goal", func(d *Definition) {
			for i := range d.Nodes {
				d.Nodes[i].RequirementIDs = []string{"optional"}
			}
		}},
		{"version", func(d *Definition) { d.SchemaVersion = 2 }},
		{"spec", func(d *Definition) { d.SpecVersion = 2 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			d, spec := definition()
			spec.Requirements = append(spec.Requirements, c.Requirement{ID: "optional"})
			test.mutate(&d)
			if d.Validate(spec, []string{"check"}) == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	d, spec := definition()
	if _, err := New(d, spec, []string{"check"}, c.HashBytes([]byte("changed")), 1); err == nil {
		t.Fatal("stale candidate")
	}
	if _, err := New(d, spec, []string{"check"}, d.BaseCandidate, 2); err == nil {
		t.Fatal("stale epoch")
	}
}
func TestRestoredProgressCannotForgeDependencyOrVerification(t *testing.T) {
	d, spec := definition()
	state, _ := New(d, spec, []string{"check"}, d.BaseCandidate, 1)
	state.Progress[0].Status = "RUNNING"
	if state.Validate(spec, []string{"check"}, state.CurrentCandidate, 1) == nil {
		t.Fatal("dependency bypass on replay")
	}
	state.Progress[0].Status = "VERIFIED"
	if state.Validate(spec, []string{"check"}, state.CurrentCandidate, 1) == nil {
		t.Fatal("model plan claims verification")
	}
}

func TestPlanReadSetRejectsUndeclaredDependencies(t *testing.T) {
	d, spec := definition()
	for i := range d.Nodes {
		d.Nodes[i].ReadScope = []string{"src/a.go"}
		d.Nodes[i].WriteScope = []string{"src/a.go"}
	}
	state, err := New(d, spec, []string{"check"}, d.BaseCandidate, 1)
	if err != nil {
		t.Fatal(err)
	}
	state.Next()
	if err = state.AdmitReadSet([]c.ReadCondition{{Path: "private/key", Kind: "FILE"}}); err == nil {
		t.Fatal("undeclared dependency admitted")
	}
	if err = state.AdmitReadSet([]c.ReadCondition{{Path: ".", Kind: "LISTING"}}); err == nil {
		t.Fatal("broad listing admitted")
	}
	if err = state.AdmitReadSet([]c.ReadCondition{{Path: "src/a.go", Kind: "FILE"}}); err != nil {
		t.Fatal(err)
	}
}
