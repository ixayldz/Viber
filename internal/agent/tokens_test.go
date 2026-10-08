package agent

import (
	"context"
	"encoding/json"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentGlobalTokenReserveBlocksBeforeFixtureAndPreservesUnknown(t *testing.T) {
	ctx := context.Background()
	session, options, _ := startFixture(t, []Turn{{Text: "usage unknown", UsageKnown: false}}, true)
	defer session.Close()
	unknown, err := session.Run(ctx, options.TaskID)
	if err != nil || unknown.Execution != c.Blocked {
		t.Fatal(unknown, err)
	}
	_, doc, err := session.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	// A second task must share the first task's limits and cannot erase its unknown risk.
	limits := doc.StoreTokens
	options.TaskID = "second"
	raw, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "final", UsageKnown: true, InputTokens: 8, OutputTokens: 2}}})
	options.Fixture = raw
	if _, err = session.Create(ctx, options); err != nil {
		t.Fatal(err)
	}
	second, err := session.Run(ctx, options.TaskID)
	if err != nil || second.Outcome != c.Finished {
		t.Fatal(second, err)
	}
	ledger, err := session.Journal.TokenLedger(ctx)
	if err != nil || ledger.Unknown != 1 || ledger.Used.Input != 8 || ledger.Reserved.Input != doc.Budget.ReservedInput || ledger.Limits != *limits {
		t.Fatal(ledger, err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = session.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenExisting(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	copy, err := fresh.Journal.TokenLedger(ctx)
	if err != nil || copy != ledger {
		t.Fatal("restore altered global accounting", copy, ledger, err)
	}
	if _, err = session.Controls(ctx, "task-one", "cancel"); err != nil {
		t.Fatal(err)
	}
	after, err := session.Journal.TokenLedger(ctx)
	if err != nil || after != ledger {
		t.Fatal("cancel erased unknown reservation", after, err)
	}
}

func TestAgentGlobalTokenLimitStopsBeforeDispatchAndBudgetForgeryFails(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	session, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	raw, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "not dispatched", UsageKnown: true}}})
	limits := c.TokenLimits{Input: 4096, Output: 512}
	options := StartOptions{StoreTokens: &limits, Root: source, Prompt: []byte("Inspect empty source"), TaskID: "small", Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: raw}
	if _, err = session.Create(ctx, options); err != nil {
		t.Fatal(err)
	}
	st, err := session.Run(ctx, options.TaskID)
	if err != nil || st.Execution != c.WaitingResource {
		t.Fatal(st, err)
	}
	_, doc, err := session.Load(ctx, options.TaskID)
	if err != nil || doc.FixtureCursor != 0 || doc.Budget.Steps != 0 || doc.Pending != nil || doc.Budget.ReservedInput != 0 {
		t.Fatal("failed admission changed durable work", doc, err)
	}
	ledger, err := session.Journal.TokenLedger(ctx)
	if err != nil || ledger.Used != (c.TokenLimits{}) || ledger.Reserved != (c.TokenLimits{}) {
		t.Fatal(ledger, err)
	}
	doc.Budget.UsedInput = 1
	if _, err = session.record(ctx, st, doc, "SessionRecorded", c.EventPayload{}); err == nil {
		t.Fatal("document counter forgery committed")
	}
	_, unchanged, err := session.Load(ctx, options.TaskID)
	if err != nil || unchanged.Budget.UsedInput != 0 {
		t.Fatal("failed record damaged committed task", err)
	}
	if _, err = session.Backup(ctx, filepath.Join(t.TempDir(), "valid-backup")); err != nil {
		t.Fatal(err)
	}
}

func TestMissingHistoricalTokenReceiptFailsLoadAndBackup(t *testing.T) {
	ctx := context.Background()
	session, options, _ := startFixture(t, []Turn{{Calls: []model.Call{{ID: "read", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}, UsageKnown: true, InputTokens: 8, OutputTokens: 1}, {Text: "report", UsageKnown: true, InputTokens: 9, OutputTokens: 2}}, true)
	defer session.Close()
	state, err := session.Run(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	digest := state.Tokens.Reservations[0].ResponseDigest
	if digest == state.Tokens.Reservations[1].ResponseDigest {
		t.Fatal("fixture receipts not independent")
	}
	file := filepath.Join(session.directory, "artifacts", "tasks", options.TaskID, "blobs", digest[:2], digest)
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, _, err = session.Load(ctx, options.TaskID); err == nil {
		t.Fatal("missing older token receipt accepted")
	}
	if _, err = session.Backup(ctx, filepath.Join(t.TempDir(), "missing-receipt-backup")); err == nil {
		t.Fatal("incomplete token lineage backed up")
	}
}
