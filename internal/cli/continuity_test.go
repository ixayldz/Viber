package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestCLIContinuityBoundPinRetryAndUnpin(t *testing.T) {
	source, directory := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("exact bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := agent.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	state, err := s.Create(context.Background(), agent.StartOptions{Root: source, TaskID: "pins", Prompt: []byte("Read a.txt"), Fixture: fixture, Budget: agent.DefaultBudget(), Autonomy: "guided"})
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), "pins")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	command := agent.ContinuityCommand{CommandID: "pin-command", TaskID: "pins", ExpectedTaskSeq: state.TaskSeq, Action: "pin", Pin: &agent.ContextPin{ID: "important", Path: "a.txt", Candidate: doc.Candidate.SnapshotDigest, SourceDigest: c.HashBytes([]byte("exact bytes\n")), Start: 0, End: 12}}
	file := filepath.Join(t.TempDir(), "command.json")
	raw, _ := json.Marshal(command)
	os.WriteFile(file, raw, 0600)
	invoke := func(want int, args ...string) []byte {
		t.Helper()
		var out, errout bytes.Buffer
		code := Execute(args, &out, &errout)
		if code != want {
			t.Fatalf("%v exit%d want%d %s %s", args, code, want, out.String(), errout.String())
		}
		if !json.Valid(out.Bytes()) {
			t.Fatal("invalid structured output", out.String())
		}
		return out.Bytes()
	}
	first := invoke(0, "context-edit", "pins", "--store", directory, "--command-file", file, "--json")
	second := invoke(0, "context-edit", "pins", "--store", directory, "--command-file", file, "--json")
	var a, b c.TaskState
	json.Unmarshal(first, &a)
	json.Unmarshal(second, &b)
	if a.TaskSeq != b.TaskSeq {
		t.Fatal("CLI retry duplicated pin")
	}
	info := invoke(0, "continuity-info", "pins", "--store", directory, "--json")
	var parsed struct {
		State c.TaskState        `json:"state"`
		Pins  []agent.ContextPin `json:"pins"`
	}
	if err = json.Unmarshal(info, &parsed); err != nil || len(parsed.Pins) != 1 || string(parsed.Pins[0].Data) != "exact bytes\n" {
		t.Fatal("pin info", err)
	}
	command = agent.ContinuityCommand{CommandID: "unpin-command", TaskID: "pins", ExpectedTaskSeq: parsed.State.TaskSeq, Action: "unpin", PinID: "important"}
	raw, _ = json.Marshal(command)
	os.WriteFile(file, raw, 0600)
	invoke(0, "context-edit", "pins", "--store", directory, "--command-file", file, "--json")
	invoke(4, "history-page", "pins", "--store", directory, "--history-digest", c.HashBytes([]byte("arbitrary")), "--json")
	invoke(4, "continuity-info", "pins", "--store", directory, "--offset", "1", "--json")
	invoke(4, "model-switch", "pins", "--store", directory, "--command-file", file, "--json")
}
