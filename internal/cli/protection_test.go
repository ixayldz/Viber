package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/verify"
)

func TestCLIOperatorCheckPlanPreventsWeakeningAndReportsHonestProvenance(t *testing.T) {
	root := t.TempDir()
	directory := t.TempDir()
	inputs := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("original expectation"), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(struct {
		Read    []c.ReadCondition `json:"read_set"`
		Changes []c.Change        `json:"changes"`
	}{[]c.ReadCondition{{Path: "a.txt", Kind: "FILE", Digest: c.HashBytes([]byte("original expectation"))}}, []c.Change{{Path: "a.txt", BeforeDigest: c.HashBytes([]byte("original expectation")), After: []byte("always PASS")}}})
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Calls: []model.Call{{ID: "weaken", Name: "candidate_propose", Arguments: args}}, UsageKnown: true}, {Text: "VERIFIED", UsageKnown: true}}})
	fixturePath := filepath.Join(inputs, "fixture.json")
	if err := os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	plan := verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{ID: "guard", Kind: "TEST", RequirementIDs: []string{"user-goal"}, Argv: []string{"runner", "private-operator-argv"}, RunnerDigest: c.HashBytes([]byte("runner")), Selection: "all", ExpectedTests: []string{"private-discovery-id"}, Closure: []verify.CheckScope{{Path: "a.txt"}}}}}
	raw, _ := json.Marshal(plan)
	planPath := filepath.Join(inputs, "plan.json")
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	commands := []string{"run", "Keep protected check", "--offline", "--fixture", fixturePath, "--check-plan", planPath, "--root", root, "--store", directory, "--task", "protected-cli", "--allow-unverified", "--json"}
	if code := Execute(commands, &out, &stderr); code != 2 {
		t.Fatal(code, out.String(), stderr.String())
	}
	var result struct {
		State      c.TaskState           `json:"state"`
		Protection *agent.ProtectionInfo `json:"check_protection"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State.CandidateDigest != result.State.BaselineDigest || result.State.Quality != c.Unverified || result.Protection == nil || result.Protection.CheckCount != 1 || result.Protection.StrongVerificationAvailable || result.Protection.ClosureCoverage != "EXPLICIT_UNREVIEWED" {
		t.Fatal("weakening or misleading capability", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("private-operator-argv")) || bytes.Contains(out.Bytes(), []byte("private-discovery-id")) {
		t.Fatal("status leaks raw operator config")
	}
	if err := os.WriteFile(planPath, []byte(`{"schema_version":1,"checks":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Execute([]string{"status", "protected-cli", "--store", directory, "--json"}, &out, &stderr); code != 0 {
		t.Fatal(code, out.String())
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Protection.CheckCount != 1 {
		t.Fatal("later plan file rewrote origin", err, out.String())
	}
	source, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(source) != "original expectation" {
		t.Fatal("source overwritten")
	}
}
func TestCLIInvalidCheckPlanFailsBeforeStoreInitialization(t *testing.T) {
	root := t.TempDir()
	inputs := t.TempDir()
	directory := filepath.Join(t.TempDir(), "must-not-exist")
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	fixturePath := filepath.Join(inputs, "fixture.json")
	planPath := filepath.Join(inputs, "plan.json")
	if err := os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte{}, []byte(`{"schema_version":1,"checks":[],"verified":true}`)} {
		if err := os.WriteFile(planPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		if code := Execute([]string{"run", "task", "--offline", "--fixture", fixturePath, "--check-plan", planPath, "--root", root, "--store", directory, "--json"}, &out, &stderr); code != 4 {
			t.Fatal(code, out.String())
		}
		if _, err := os.Lstat(directory); !os.IsNotExist(err) {
			t.Fatal("invalid config created store", err)
		}
	}
}
