package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
	"github.com/ixayldz/Viber/internal/store"
	"path/filepath"
	"testing"
)

func TestCLITokenLedgerLimitStopsBeforeDispatchAndCannotChange(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "store")
	var out, errout bytes.Buffer
	exit := Execute([]string{"run", "Inspect empty source", "--offline", "--fixture", "../../examples/offline/greeting.json", "--root", t.TempDir(), "--store", directory, "--task", "small", "--store-input-tokens", "4096", "--store-output-tokens", "512", "--json"}, &out, &errout)
	if exit != 3 || !bytes.Contains(out.Bytes(), []byte("GLOBAL_TOKEN_BUDGET_EXHAUSTED")) {
		t.Fatal(exit, out.String(), errout.String())
	}
	out.Reset()
	exit = Execute([]string{"budget", "small", "--store", directory, "--json"}, &out, &errout)
	var ledger store.TokenLedgerView
	if exit != 0 || c.DecodeStrict(out.Bytes(), &ledger) != nil || ledger.Tasks != 1 || ledger.Used != (c.TokenLimits{}) || ledger.Reserved != (c.TokenLimits{}) || ledger.Limits.Input != 4096 {
		t.Fatal(exit, out.String())
	}
	out.Reset()
	exit = Execute([]string{"run", "Inspect empty source", "--offline", "--fixture", "../../examples/offline/greeting.json", "--root", t.TempDir(), "--store", directory, "--task", "other", "--store-input-tokens", "8192", "--store-output-tokens", "512", "--json"}, &out, &errout)
	if exit != 4 || !bytes.Contains(out.Bytes(), []byte("POLICY_DENIED")) {
		t.Fatal(exit, out.String())
	}
	out.Reset()
	if exit = Execute([]string{"cancel", "small", "--store", directory, "--json"}, &out, &errout); exit != 0 {
		t.Fatal("budget blocked cancel", exit, out.String())
	}
}
func TestCLITokenLedgerRoutesToLiveOwnerAndNeverCreatesMissingStore(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	session, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	_, err = session.Create(ctx, agent.StartOptions{Root: t.TempDir(), Prompt: []byte("Inspect"), TaskID: "budget", Budget: agent.DefaultBudget(), Autonomy: "guided", Fixture: raw})
	if err != nil {
		t.Fatal(err)
	}
	host, err := owner.Open(ctx, directory, session)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	var out, errout bytes.Buffer
	exit := Execute([]string{"budget", "budget", "--store", directory, "--json"}, &out, &errout)
	var ledger store.TokenLedgerView
	if exit != 0 || c.DecodeStrict(out.Bytes(), &ledger) != nil || ledger.Coverage != "ALL_TASKS_TOKEN_ONLY_V1" {
		t.Fatal(exit, out.String(), errout.String())
	}
	out.Reset()
	if exit = Execute([]string{"budget", "missing", "--store", filepath.Join(t.TempDir(), "absent"), "--json"}, &out, &errout); exit != 4 {
		t.Fatal(exit, out.String())
	}
}
