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
)

func TestCLIRealOfflineTaskRunStatusDiffAndResume(t *testing.T) {
	source := t.TempDir()
	directory := t.TempDir()
	inputs := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(struct {
		Read    []c.ReadCondition `json:"read_set"`
		Changes []c.Change        `json:"changes"`
	}{[]c.ReadCondition{{Path: "a.txt", Kind: "FILE", Digest: c.HashBytes([]byte("base"))}}, []c.Change{{Path: "a.txt", BeforeDigest: c.HashBytes([]byte("base")), After: []byte("candidate")}}})
	fixture := agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Calls: []model.Call{{ID: "patch", Name: "candidate_propose", Arguments: args}}, UsageKnown: true}, {Text: "\x1b]52;c;fake-clipboard\a", UsageKnown: true}}}
	raw, _ := json.Marshal(fixture)
	fixturePath := filepath.Join(inputs, "fixture.json")
	if err := os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	run := []string{"run", "Change a.txt", "--root", source, "--store", directory, "--task", "cli-task", "--offline", "--fixture", fixturePath, "--allow-unverified", "--json"}
	if exit := Execute(run, &out, &stderr); exit != 2 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	var result struct {
		State        c.TaskState `json:"state"`
		ReleaseReady bool        `json:"release_ready"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State.Quality != c.Unverified || result.ReleaseReady || bytes.Contains(out.Bytes(), []byte("\x1b")) {
		t.Fatal("false assurance or OSC output")
	}
	live, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(live) != "base" {
		t.Fatal("live edited")
	}
	for _, command := range []string{"status", "inspect", "replay", "diff"} {
		out.Reset()
		if exit := Execute([]string{command, "cli-task", "--store", directory, "--json"}, &out, &stderr); exit != 0 || !json.Valid(out.Bytes()) {
			t.Fatal(command, exit, out.String(), stderr.String())
		}
	}
	out.Reset()
	if exit := Execute([]string{"resume", "cli-task", "--store", directory, "--json"}, &out, &stderr); exit == 0 {
		t.Fatal("terminal attempt silently resumed")
	}
}
func TestCLIWaitingVerificationAndCancelKeepsHistoricalQuality(t *testing.T) {
	source := t.TempDir()
	directory := t.TempDir()
	fixturePath := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	if err := os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if exit := Execute([]string{"run", "Review source", "--root", source, "--store", directory, "--task", "waiting", "--offline", "--fixture", fixturePath, "--json"}, &out, &stderr); exit != 3 {
		t.Fatal(exit, out.String())
	}
	out.Reset()
	if exit := Execute([]string{"pause", "waiting", "--store", directory, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String())
	}
	out.Reset()
	if exit := Execute([]string{"cancel", "waiting", "--store", directory, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String())
	}
	var result struct {
		State c.TaskState `json:"state"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State.Outcome != c.Cancelled || result.State.Quality != c.Unverified {
		t.Fatal(result.State)
	}
}
