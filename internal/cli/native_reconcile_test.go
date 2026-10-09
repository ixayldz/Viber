package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestCLINativeRuntimeObservationAndBoundCleanupRejectWrongIntent(t *testing.T) {
	source, directory := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := agent.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := c.CanonicalV1(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "Review only.", UsageKnown: true, InputTokens: 1, OutputTokens: 1}}})
	if _, err = s.Create(context.Background(), agent.StartOptions{TaskID: "native-info", Root: source, Prompt: []byte("Review."), Budget: agent.DefaultBudget(), Autonomy: "guided", Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var out, errout bytes.Buffer
	if code := Execute([]string{"runtime-info", "native-info", "--store", directory, "--json"}, &out, &errout); code != 0 {
		t.Fatal(code, errout.String())
	}
	var info agent.NativeRiskView
	if c.DecodeStrict(out.Bytes(), &info) != nil || info.CanFence || info.Command != nil || info.Lease != nil {
		t.Fatal(out.String())
	}
	command := agent.NativeRiskCommand{CommandID: "wrong-native", TaskID: "native-info", ExpectedTaskSeq: info.TaskSeq, LeaseID: "absent", ReservationID: "absent", RequestDigest: c.HashBytes(nil), ProfileDigest: c.HashBytes(nil), Decision: "FENCE_OLD_SUBJECTS_AND_ACCOUNT_FULL_UPPER_BOUND"}
	raw, _ := c.CanonicalV1(command)
	file := filepath.Join(t.TempDir(), "command.json")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errout.Reset()
	if code := Execute([]string{"reconcile-native-risk", "native-info", "--store", directory, "--command-file", file, "--json"}, &out, &errout); code == 0 {
		t.Fatal("non-native intent cleared", out.String())
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][]string{{"runtime-info", "native-info", "--store", missing, "--limit", "1"}, {"reconcile-native-risk", "native-info", "--store", missing}} {
		out.Reset()
		errout.Reset()
		if code := Execute(args, &out, &errout); code == 0 {
			t.Fatal("invalid flags accepted")
		}
		if _, err = os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("bad control initialized missing store")
		}
	}
}
