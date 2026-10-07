package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func open(t *testing.T, root string) *Store {
	t.Helper()
	s, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func cmd(id, task, kind string, seq int64, p c.EventPayload) Command {
	return Command{ID: id, TaskID: task, Actor: "user", Type: kind, ExpectedTaskSeq: seq, Payload: p}
}
func TestAtomicJournalReplayCheckpointAndDedup(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	create := cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})
	first, err := s.Execute(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.Execute(ctx, create)
	if err != nil || !reflect.DeepEqual(same, first) {
		t.Fatal("dedup result changed", err)
	}
	create.Payload.SpecVersion = 2
	if _, err = s.Execute(ctx, create); err == nil {
		t.Fatal("conflicting command ID accepted")
	}
	_, err = s.Execute(ctx, cmd("other", "u", "TaskCreated", 0, c.EventPayload{SpecVersion: 1}))
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Execute(ctx, cmd("scope", "t", "StateTransitioned", 1, c.EventPayload{State: c.Scoping}))
	if err != nil {
		t.Fatal(err)
	}
	if next.TaskSeq != 2 || next.StoreSeq != 3 {
		t.Fatal("global/task sequence conflated")
	}
	states, err := s.Replay(ctx, "t")
	if err != nil || !reflect.DeepEqual(states["t"], next) {
		t.Fatal("replay changed state", err)
	}
	digest, err := s.Checkpoint(ctx, "t")
	if err != nil || !c.ValidDigest(digest) {
		t.Fatal(err)
	}
	checkpoint, err := s.InspectCheckpoint(ctx, "t")
	if err != nil || !reflect.DeepEqual(checkpoint, next) {
		t.Fatal("checkpoint equality", err)
	}
	if _, err = s.Execute(ctx, cmd("invalid", "t", "StateTransitioned", 2, c.EventPayload{State: c.Running})); err == nil {
		t.Fatal("illegal transition persisted")
	}
	states, err = s.Replay(ctx, "t")
	if err != nil || states["t"].TaskSeq != 2 {
		t.Fatal("failed transaction leaked state", err)
	}
}
func TestRealOwnerLockAndGenerationRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := open(t, root)
	_, err := Open(ctx, root)
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != c.StoreOwned {
		t.Fatal("second writer not rejected", err)
	}
	if _, err = s.Execute(ctx, cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
		t.Fatal(err)
	}
	g := s.Generation()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	next := open(t, root)
	if next.Generation() != g+1 {
		t.Fatal("generation not renewed")
	}
	if _, err = next.Execute(ctx, cmd("scope", "t", "StateTransitioned", 1, c.EventPayload{State: c.Scoping})); err == nil {
		t.Fatal("restarted owner skipped recovery")
	}
	state, err := next.Execute(ctx, cmd("recover", "t", "StateTransitioned", 1, c.EventPayload{State: c.Recovering}))
	if err != nil || state.KernelGeneration != next.Generation() {
		t.Fatal("recovery failed", err)
	}
}
func TestConcurrentDedupCreatesOneEvent(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	command := cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})
	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Execute(ctx, command); errors <- err }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	states, err := s.Replay(ctx, "t")
	if err != nil || states["t"].StoreSeq != 1 {
		t.Fatal("duplicate journal append", err)
	}
}
func TestPayloadChainAndTailCorruptionFailClosed(t *testing.T) {
	for _, query := range []string{
		"UPDATE payloads SET body='{}'",
		"UPDATE events SET event_hash='forged'",
		"DELETE FROM events",
	} {
		t.Run(fmt.Sprint(query), func(t *testing.T) {
			s := open(t, t.TempDir())
			ctx := context.Background()
			if _, err := s.Execute(ctx, cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Replay(ctx, "t"); err == nil {
				t.Fatal("corrupt journal replayed")
			}
		})
	}
}
func TestUnknownSchemaDoesNotDowngrade(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := open(t, root)
	if _, err := s.db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, root); err == nil {
		t.Fatal("future schema opened")
	}
}

func TestTransactionFailureAfterEventInsertRollsEverythingBack(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	if _, err := s.db.Exec("CREATE TRIGGER fault BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT,'injected write failure'); END"); err != nil {
		t.Fatal(err)
	}
	command := cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})
	if _, err := s.Execute(ctx, command); err == nil {
		t.Fatal("injected failure absent")
	}
	states, err := s.Replay(ctx, "")
	if err != nil || len(states) != 0 {
		t.Fatal("partial event escaped transaction", err)
	}
	var events, payloads, commands int
	s.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&events)
	s.db.QueryRow("SELECT COUNT(*) FROM payloads").Scan(&payloads)
	s.db.QueryRow("SELECT COUNT(*) FROM commands").Scan(&commands)
	if events != 0 || payloads != 0 || commands != 0 {
		t.Fatal("uncommitted rows leaked")
	}
	if _, err = s.db.Exec("DROP TRIGGER fault"); err != nil {
		t.Fatal(err)
	}
	state, err := s.Execute(ctx, command)
	if err != nil || state.StoreSeq != 1 {
		t.Fatal("retry after rollback failed", err)
	}
}
func TestCrashChild(t *testing.T) {
	root := os.Getenv("VIBER_TEST_CRASH_STORE")
	if root == "" {
		return
	}
	s, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(context.Background(), cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(context.Background(), cmd("scope", "t", "StateTransitioned", 1, c.EventPayload{State: c.Scoping})); err != nil {
		t.Fatal(err)
	}
	os.Exit(23) // Simulated process death: intentionally no DB/owner Close.
}
func TestRealProcessCrashPreservesCommittedState(t *testing.T) {
	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestCrashChild$")
	child.Env = append(os.Environ(), "VIBER_TEST_CRASH_STORE="+root)
	err := child.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatal("crash helper failed", err)
	}
	s := open(t, root)
	states, err := s.Replay(context.Background(), "t")
	if err != nil || states["t"].Execution != c.Scoping || states["t"].TaskSeq != 2 {
		t.Fatal("committed state lost on crash", err)
	}
	_, err = s.Execute(context.Background(), cmd("recover", "t", "StateTransitioned", 2, c.EventPayload{State: c.Recovering}))
	if err != nil {
		t.Fatal("crashed OS owner lock not released", err)
	}
}
func TestProjectionCorruptionStopsAdmission(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	if _, err := s.Execute(ctx, cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE tasks SET state='{}'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(ctx, cmd("scope", "t", "StateTransitioned", 1, c.EventPayload{State: c.Scoping})); err == nil {
		t.Fatal("corrupt projection admitted")
	}
}
