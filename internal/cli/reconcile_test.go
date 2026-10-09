package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIModelRiskBoundDecisionAndResourceObservation(t *testing.T) {
	root := t.TempDir()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "fixture.json")
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "do not accept", UsageKnown: false}}})
	os.WriteFile(fixture, raw, 0600)
	var out, errout bytes.Buffer
	if code := Execute([]string{"run", "inspect", "--root", root, "--store", directory, "--task", "risk", "--offline", "--fixture", fixture, "--json"}, &out, &errout); code != 3 {
		t.Fatal(code, out.String(), errout.String())
	}
	session, err := agent.OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	state, doc, err := session.Load(context.Background(), "risk")
	if err != nil {
		t.Fatal(err)
	}
	charge := state.Tokens.Reservations[0]
	command := agent.ModelRiskCommand{CommandID: "risk-command", TaskID: "risk", ExpectedTaskSeq: state.TaskSeq, ReservationID: doc.Pending.ID, RequestDigest: doc.Pending.ArgumentsDigest, ProfileDigest: charge.ProfileDigest, Decision: "ACCOUNT_FULL_UPPER_BOUND_WITHOUT_OUTPUT"}
	session.Close()
	raw, _ = json.Marshal(command)
	file := filepath.Join(t.TempDir(), "risk.json")
	os.WriteFile(file, raw, 0600)
	for i := 0; i < 2; i++ {
		out.Reset()
		errout.Reset()
		if code := Execute([]string{"reconcile-model-risk", "risk", "--store", directory, "--command-file", file, "--json"}, &out, &errout); code != 0 {
			t.Fatal(code, out.String(), errout.String())
		}
		var actual c.TaskState
		if c.DecodeStrict(out.Bytes(), &actual) != nil || actual.Tokens.Used != charge.Upper || actual.Quality != c.Unverified {
			t.Fatal(out.String())
		}
	}
	out.Reset()
	errout.Reset()
	if code := Execute([]string{"resources", "risk", "--store", directory, "--json"}, &out, &errout); code != 0 || !strings.Contains(out.String(), "physical_reserve_bytes") || !strings.Contains(out.String(), "ALL_TASKS_CONSERVATIVE_RESOURCE_V1") {
		t.Fatal(code, out.String(), errout.String())
	}
}
