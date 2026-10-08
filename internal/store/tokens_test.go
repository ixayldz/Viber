package store

import (
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"reflect"
	"sync"
	"testing"
)

func tokenTask(t *testing.T, s *Store, id string, limits c.TokenLimits) c.TaskState {
	t.Helper()
	ctx := context.Background()
	st, err := s.Execute(ctx, Command{ID: id + "-create", TaskID: id, Actor: "kernel", Type: "TaskCreated", Payload: c.EventPayload{SpecVersion: 1, DocumentDigest: c.HashBytes([]byte("doc")), SnapshotDigest: c.HashBytes([]byte("snapshot")), Tokens: &c.TokenMutation{Action: "INIT", Limits: limits}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []c.ExecutionState{c.Scoping, c.Ready, c.Running} {
		st, err = s.Execute(ctx, Command{ID: id + "-" + string(next), TaskID: id, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "StateTransitioned", Payload: c.EventPayload{State: next}})
		if err != nil {
			t.Fatal(err)
		}
	}
	return st
}
func tokenReserve(st c.TaskState) Command {
	return Command{ID: st.TaskID + "-reserve", TaskID: st.TaskID, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "SessionRecorded", Payload: c.EventPayload{DocumentDigest: c.HashBytes([]byte("admitted")), Tokens: &c.TokenMutation{Action: "RESERVE", Reservation: c.TokenReservation{ID: st.TaskID + "-operation", RequestDigest: c.HashBytes([]byte("request")), ProfileDigest: c.HashBytes([]byte("profile")), SpecVersion: st.SpecVersion, PolicyEpoch: st.PolicyEpoch, Generation: st.KernelGeneration, Upper: c.TokenLimits{Input: 6000, Output: 300}, Status: "RESERVED"}}}}
}
func tokenUpdate(st c.TaskState, id string, r c.TokenReservation, action string) Command {
	return Command{ID: id, TaskID: st.TaskID, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "SessionRecorded", Payload: c.EventPayload{DocumentDigest: c.HashBytes([]byte(id)), Tokens: &c.TokenMutation{Action: action, Reservation: r}}}
}
func TestGlobalTokensConcurrentReserveIsAtomicAndDeduplicated(t *testing.T) {
	s := open(t, t.TempDir())
	limits := c.TokenLimits{Input: 10000, Output: 512}
	a, b := tokenTask(t, s, "a", limits), tokenTask(t, s, "b", limits)
	ctx := context.Background()
	var wg sync.WaitGroup
	type result struct {
		command Command
		state   c.TaskState
		err     error
	}
	results := make(chan result, 2)
	for _, st := range []c.TaskState{a, b} {
		wg.Add(1)
		go func(st c.TaskState) {
			defer wg.Done()
			command := tokenReserve(st)
			next, err := s.Execute(ctx, command)
			results <- result{command, next, err}
		}(st)
	}
	wg.Wait()
	close(results)
	var admitted result
	pass, deny := 0, 0
	for result := range results {
		if result.err == nil {
			pass++
			admitted = result
		} else {
			var typed *c.Error
			if !errors.As(result.err, &typed) || typed.Code != c.BudgetLimitReached {
				t.Fatal(result.err)
			}
			deny++
		}
	}
	if pass != 1 || deny != 1 {
		t.Fatal("overcommit", pass, deny)
	}
	info, err := s.SnapshotInfo(ctx)
	if err != nil || info.StoreSeq != 9 {
		t.Fatal("failed admission mutated journal", info, err)
	}
	same, err := s.Execute(ctx, admitted.command)
	if err != nil || !reflect.DeepEqual(same, admitted.state) {
		t.Fatal("reserve dedup changed accounting", err)
	}
	ledger, err := s.TokenLedger(ctx)
	if err != nil || ledger.Reserved.Input != 6000 || ledger.Reserved.Output != 300 || ledger.Used.Input != 0 || ledger.Tasks != 2 {
		t.Fatal(ledger, err)
	}
	r := admitted.state.Tokens.Reservations[0]
	r.Status, r.ResponseDigest, r.UsageSource = "SETTLED", c.HashBytes([]byte("raw-provider")), "PROVIDER_REPORTED"
	r.Used = c.TokenLimits{Input: 120, Output: 30}
	settle := tokenUpdate(admitted.state, "settle", r, "SETTLE")
	_, err = s.Execute(ctx, settle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, settle); err != nil {
		t.Fatal("settle dedup", err)
	}
	ledger, err = s.TokenLedger(ctx)
	if err != nil || ledger.Used != r.Used || ledger.Reserved != (c.TokenLimits{}) {
		t.Fatal("double charge", ledger, err)
	}
	states, err := s.Replay(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for id, st := range states {
		if id != admitted.state.TaskID {
			if _, err = s.Execute(ctx, tokenReserve(st)); err != nil {
				t.Fatal("settled unused capacity not reusable", err)
			}
		}
	}
}
func TestGlobalTokensUnknownSurvivesCancelRestartAndCannotRelease(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	st := tokenTask(t, s, "unknown", c.TokenLimits{Input: 10000, Output: 512})
	ctx := context.Background()
	st, err := s.Execute(ctx, tokenReserve(st))
	if err != nil {
		t.Fatal(err)
	}
	r := st.Tokens.Reservations[0]
	r.Status = "UNKNOWN"
	st, err = s.Execute(ctx, tokenUpdate(st, "unknown-receipt", r, "UNKNOWN"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, tokenUpdate(st, "unsafe-release", r, "RELEASE")); err == nil {
		t.Fatal("unknown release admitted")
	}
	st, err = s.Execute(ctx, Command{ID: "cancel", TaskID: st.TaskID, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "StateTransitioned", Payload: c.EventPayload{State: c.Terminated, Outcome: c.Cancelled}})
	if err != nil {
		t.Fatal("control budget unavailable", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, root)
	ledger, err := s.TokenLedger(ctx)
	if err != nil || ledger.Unknown != 1 || ledger.Reserved.Input != 6000 || ledger.Used.Input != 0 {
		t.Fatal(ledger, err)
	}
	replayed, err := s.Replay(ctx, st.TaskID)
	if err != nil || replayed[st.TaskID].Outcome != c.Cancelled {
		t.Fatal(err)
	}
}
func TestGlobalTokensOverageIsChargedAndLimitsCannotChange(t *testing.T) {
	s := open(t, t.TempDir())
	st := tokenTask(t, s, "overage", c.TokenLimits{Input: 10000, Output: 512})
	ctx := context.Background()
	st, err := s.Execute(ctx, tokenReserve(st))
	if err != nil {
		t.Fatal(err)
	}
	r := st.Tokens.Reservations[0]
	r.Status, r.ResponseDigest, r.UsageSource = "SETTLED", c.HashBytes([]byte("raw")), "PROVIDER_REPORTED"
	r.Used = c.TokenLimits{Input: 11000, Output: 600}
	st, err = s.Execute(ctx, tokenUpdate(st, "observed-overage", r, "SETTLE"))
	if err != nil {
		t.Fatal("overage receipt lost", err)
	}
	command := tokenReserve(st)
	command.ID = "new-operation"
	command.Payload.Tokens.Reservation.ID = "new-operation"
	if _, err = s.Execute(ctx, command); err == nil {
		t.Fatal("new work admitted after overage")
	}
	changed := Command{ID: "changed-limits", TaskID: "other", Actor: "kernel", Type: "TaskCreated", Payload: c.EventPayload{SpecVersion: 1, DocumentDigest: c.HashBytes([]byte("doc")), SnapshotDigest: c.HashBytes([]byte("snapshot")), Tokens: &c.TokenMutation{Action: "INIT", Limits: c.DefaultTokenLimits()}}}
	if _, err = s.Execute(ctx, changed); err == nil {
		t.Fatal("immutable global limits changed")
	}
	ledger, err := s.TokenLedger(ctx)
	if err != nil || ledger.Used != r.Used || ledger.Reserved != (c.TokenLimits{}) {
		t.Fatal(ledger, err)
	}
}
func TestGlobalTokensLegacyMixAndForgedLineageAreRejected(t *testing.T) {
	s := open(t, t.TempDir())
	st := tokenTask(t, s, "guard", c.TokenLimits{Input: 10000, Output: 512})
	ctx := context.Background()
	if _, err := s.Execute(ctx, cmd("legacy", "legacy", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err == nil {
		t.Fatal("untracked task joined ledger")
	}
	command := tokenReserve(st)
	command.Payload.Tokens.Reservation.PolicyEpoch++
	if _, err := s.Execute(ctx, command); err == nil {
		t.Fatal("stale reservation admitted")
	}
	st, err := s.Execute(ctx, tokenReserve(st))
	if err != nil {
		t.Fatal(err)
	}
	r := st.Tokens.Reservations[0]
	r.Status, r.ResponseDigest, r.UsageSource = "SETTLED", c.HashBytes([]byte("raw")), "PROVIDER_REPORTED"
	r.ProfileDigest = c.HashBytes([]byte("other"))
	if _, err = s.Execute(ctx, tokenUpdate(st, "forged-profile", r, "SETTLE")); err == nil {
		t.Fatal("changed profile settled")
	}
	s2 := open(t, t.TempDir())
	if _, err = s2.Execute(ctx, cmd("legacy-first", "legacy", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
		t.Fatal(err)
	}
	init := Command{ID: "new-init", TaskID: "new", Actor: "kernel", Type: "TaskCreated", Payload: c.EventPayload{SpecVersion: 1, DocumentDigest: c.HashBytes([]byte("doc")), SnapshotDigest: c.HashBytes([]byte("snapshot")), Tokens: &c.TokenMutation{Action: "INIT", Limits: c.DefaultTokenLimits()}}}
	if _, err = s2.Execute(ctx, init); err == nil {
		t.Fatal("legacy usage silently omitted")
	}
}
