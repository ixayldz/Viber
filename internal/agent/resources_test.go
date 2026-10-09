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

func resourceFixture(t *testing.T, policy c.ResourcePolicy, turns []Turn) (*Session, StartOptions) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	raw, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	opts := StartOptions{Root: root, TaskID: "resource-task", Prompt: []byte("Inspect captured source"), Budget: DefaultBudget(), Autonomy: "guided", ResourcePolicy: &policy, Fixture: raw}
	if _, err = s.Create(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	return s, opts
}
func TestResourceAdmissionFailureNeverDispatchesOrRetainsSpeculativeStep(t *testing.T) {
	policy := c.DefaultResourcePolicy()
	policy.Limits.CPUMillis = 1000
	s, opts := resourceFixture(t, policy, []Turn{{Text: "done", UsageKnown: true, InputTokens: 1, OutputTokens: 1}})
	state, err := s.Run(context.Background(), opts.TaskID)
	if err != nil || state.Execution != c.WaitingResource {
		t.Fatal(state, err)
	}
	_, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil || doc.Pending != nil || doc.FixtureCursor != 0 || doc.Budget.Steps != 0 || len(state.Tokens.Reservations) != 0 || len(state.Resources.Reservations) != 0 {
		t.Fatal("speculative reservation escaped", err)
	}
	disk, err := s.Journal.DiskStatus()
	if err != nil || !disk.WorkReady || disk.RetainedReserveBytes != disk.TargetReserveBytes {
		t.Fatal(disk, err)
	}
}
func TestNativeResourceRefusalKeepsCompleteModelBoundaryWithoutToolExecution(t *testing.T) {
	policy := c.DefaultResourcePolicy()
	policy.Limits.DiskBytes = 9 << 20
	s, opts := resourceFixture(t, policy, []Turn{{Calls: []model.Call{{ID: "read", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}, UsageKnown: true, InputTokens: 2, OutputTokens: 1}})
	state, err := s.Run(context.Background(), opts.TaskID)
	if err != nil || state.Execution != c.WaitingResource {
		t.Fatal(state, err)
	}
	_, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil || doc.Pending != nil || doc.Budget.ToolCalls != 0 || doc.FixtureCursor != 1 || len(state.Resources.Reservations) != 1 || state.Resources.Reservations[0].Status != "SETTLED" || len(doc.PendingReplies) != 0 {
		t.Fatal("native operation partially admitted", err)
	}
}
func TestResourceReceiptsBindUsageAndRejectMutation(t *testing.T) {
	s, opts := resourceFixture(t, c.DefaultResourcePolicy(), []Turn{{Calls: []model.Call{{ID: "read", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}, UsageKnown: true, InputTokens: 2, OutputTokens: 1}, {Text: "done", UsageKnown: true, InputTokens: 2, OutputTokens: 1}})
	state, err := s.Run(context.Background(), opts.TaskID)
	if err != nil || state.Execution != c.WaitingUser {
		t.Fatal(state, err)
	}
	_, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Resources.Reservations) != 3 || state.Resources.Reserved != (c.ResourceVector{}) || state.Resources.Used.DiskBytes != 80<<20 {
		t.Fatal("conservative allocation charge missing", state.Resources)
	}
	copied := *state.Resources
	copied.Reservations = append([]c.ResourceReservation{}, copied.Reservations...)
	state.Resources = &copied
	state.Resources.Reservations[0].Used.DiskBytes++
	if err = s.validateResourceDocument(state, doc); err == nil {
		t.Fatal("resource receipt altered")
	}
}
func TestPaidModelWithoutCatalogCannotDispatch(t *testing.T) {
	s, opts := resourceFixture(t, c.DefaultResourcePolicy(), []Turn{{Text: "done", UsageKnown: true}})
	state, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{SchemaVersion: 1, Provider: "openai", Endpoint: "https://api.openai.com", Model: "unpriced", AllowRemote: true, SecretHandle: "OPENAI_API_KEY", ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 1000}
	doc.Runtime = &runtime
	doc.FixtureDigest = ""
	state, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Run(context.Background(), opts.TaskID); err == nil {
		t.Fatal("unpriced paid inference admitted")
	}
	state, doc, err = s.Load(context.Background(), opts.TaskID)
	if err != nil || len(state.Tokens.Reservations) != 0 || doc.Pending != nil || doc.Budget.Steps != 0 {
		t.Fatal("unpriced operation reserved risk", err)
	}
}

func TestNativeResourceIdentityIsTaskScoped(t *testing.T) {
	s, first := resourceFixture(t, c.DefaultResourcePolicy(), []Turn{{Calls: []model.Call{{ID: "read", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}, UsageKnown: true, InputTokens: 2, OutputTokens: 1}, {Text: "done", UsageKnown: true, InputTokens: 2, OutputTokens: 1}})
	second := first
	second.TaskID = "resource-task-two"
	if _, err := s.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, task := range []string{first.TaskID, second.TaskID} {
		state, err := s.Run(context.Background(), task)
		if err != nil || state.Execution != c.WaitingUser {
			t.Fatal(state, err)
		}
		found := false
		for _, r := range state.Resources.Reservations {
			if r.Kind != "NATIVE_TOOL" {
				continue
			}
			if r.Status != "SETTLED" || r.CallDigest != c.HashBytes([]byte("read")) || ids[r.ID] {
				t.Fatal("native calls collided across tasks", r)
			}
			ids[r.ID], found = true, true
		}
		if !found {
			t.Fatal("missing native resource charge")
		}
	}
}
func TestNativeResourceIdentityBindsOperationOrdinal(t *testing.T) {
	doc := Document{TaskID: "task", Pending: &Pending{ID: "read", ArgumentsDigest: c.HashBytes([]byte("args"))}}
	first := nativeResourceID(doc)
	if first == "" || first != nativeResourceID(doc) {
		t.Fatal("identity not deterministic")
	}
	doc.Budget.ToolCalls++
	if first == nativeResourceID(doc) {
		t.Fatal("tool ordinal omitted")
	}
	second := nativeResourceID(doc)
	doc.Budget.Steps++
	if second == nativeResourceID(doc) {
		t.Fatal("model step omitted")
	}
}
func TestResourcePolicyWithoutPricesCreatesAndReplays(t *testing.T) {
	policy := c.DefaultResourcePolicy()
	policy.Prices = nil
	s, opts := resourceFixture(t, policy, []Turn{{Text: "done", UsageKnown: true, InputTokens: 1, OutputTokens: 1}})
	state, err := s.Run(context.Background(), opts.TaskID)
	if err != nil || state.Execution != c.WaitingUser {
		t.Fatal(state, err)
	}
	if _, _, err = s.Load(context.Background(), opts.TaskID); err != nil {
		t.Fatal(err)
	}
	changed := policy
	changed.Limits.DiskBytes++
	if _, err = s.creationResourcePolicy(context.Background(), &changed); err == nil {
		t.Fatal("changed store policy accepted")
	}
}
