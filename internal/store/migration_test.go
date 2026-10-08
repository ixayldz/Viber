package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func legacyStore(t *testing.T, root string) *Store {
	t.Helper()
	s := open(t, root)
	// The exact v1 layout is v2 minus its additive format table.
	if _, err := s.db.Exec("DROP TABLE format_manifest"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	s.schema = 1
	return s
}
func migrationIntent(t *testing.T, s *Store) MigrationIntent {
	t.Helper()
	source, err := s.SnapshotInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return MigrationIntent{SchemaVersion: 1, ID: "upgrade-one", FromVersion: 1, ToVersion: 2, BackupPath: filepath.Join(t.TempDir(), "backup"), BackupDigest: c.HashBytes([]byte("validated complete application backup")), Source: source}
}
func terminalTask(t *testing.T, s *Store) c.TaskState {
	t.Helper()
	ctx := context.Background()
	state, err := s.Execute(ctx, cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1}))
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.Execute(ctx, cmd("cancel", "t", "StateTransitioned", state.TaskSeq, c.EventPayload{State: c.Terminated, Outcome: c.Cancelled}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Checkpoint(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	return state
}
func TestLegacyOpenIsNeverAutomaticMigration(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	s := legacyStore(t, directory)
	before := terminalTask(t, s)
	s.Close()
	s = open(t, directory)
	info, err := s.SnapshotInfo(ctx)
	if err != nil || info.SchemaVersion != 1 || info.FormatDigest != "" {
		t.Fatal(info, err)
	}
	actual, err := s.Replay(ctx, "t")
	if err != nil || !reflect.DeepEqual(before, actual["t"]) {
		t.Fatal(actual, err)
	}
}
func TestMigrationPreservesJournalCheckpointDedupAndBindsLineage(t *testing.T) {
	ctx := context.Background()
	s := legacyStore(t, t.TempDir())
	before := terminalTask(t, s)
	intent := migrationIntent(t, s)
	result, err := s.Migrate(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "COMMITTED" || result.Target.SchemaVersion != 2 || !c.ValidDigest(result.Target.FormatDigest) || result.Target.StoreSeq != intent.Source.StoreSeq || result.Target.TailHash != intent.Source.TailHash || s.Writable() != nil {
		t.Fatal(result)
	}
	actual, err := s.Replay(ctx, "t")
	if err != nil || !reflect.DeepEqual(actual["t"], before) {
		t.Fatal(actual, err)
	}
	checkpoint, err := s.InspectCheckpoint(ctx, "t")
	if err != nil || !reflect.DeepEqual(checkpoint, before) {
		t.Fatal(checkpoint, err)
	}
	same, err := s.Execute(ctx, cmd("cancel", "t", "StateTransitioned", 1, c.EventPayload{State: c.Terminated, Outcome: c.Cancelled}))
	if err != nil || !reflect.DeepEqual(same, before) {
		t.Fatal("command dedup changed", same, err)
	}
	repeated, err := s.Migrate(ctx, intent)
	if err != nil || repeated != result {
		t.Fatal("migration not idempotent", repeated, err)
	}
	changed := intent
	changed.ID = "another"
	if _, err = s.Migrate(ctx, changed); err == nil {
		t.Fatal("conflicting migration admitted")
	}
	// Idempotence refers to the original receipt, even after new journal events.
	if _, err = s.Execute(ctx, cmd("second", "u", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
		t.Fatal(err)
	}
	repeated, err = s.Migrate(ctx, intent)
	if err != nil || repeated != result {
		t.Fatal(repeated, err)
	}
}
func TestMigrationPreflightDoesNotPublishIntent(t *testing.T) {
	for _, kind := range []string{"active-task", "stale-journal", "disk-space", "invalid-id", "overlapping-backup"} {
		t.Run(kind, func(t *testing.T) {
			s := legacyStore(t, t.TempDir())
			intent := migrationIntent(t, s)
			switch kind {
			case "active-task":
				s.Execute(context.Background(), cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1}))
				intent = migrationIntent(t, s)
			case "stale-journal":
				terminalTask(t, s)
			case "disk-space":
				s.migrationSpace = func() uint64 { return 0 }
			case "invalid-id":
				intent.ID = ""
			case "overlapping-backup":
				intent.BackupPath = filepath.Join(s.directory, "backup")
			}
			if _, err := s.Migrate(context.Background(), intent); err == nil {
				t.Fatal("invalid migration admitted")
			}
			if _, err := os.Stat(filepath.Join(s.directory, MigrationIntentFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("intent published before preflight", err)
			}
			info, err := s.SnapshotInfo(context.Background())
			if err != nil || info.SchemaVersion != 1 || s.Writable() != nil {
				t.Fatal(info, err)
			}
		})
	}
}
func TestMigrationAbruptProcessRecovery(t *testing.T) {
	ctx := context.Background()
	for _, stage := range []string{"after-intent", "before-commit", "after-commit", "before-marker"} {
		t.Run(stage, func(t *testing.T) {
			directory := t.TempDir()
			s := legacyStore(t, directory)
			before := terminalTask(t, s)
			intent := migrationIntent(t, s)
			generation := s.Generation()
			s.Close()
			raw, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			worker := exec.Command(os.Args[0], "-test.run=^TestMigrationCrashWorker$")
			worker.Env = append(os.Environ(), "VIBER_MIGRATION_CRASH_STAGE="+stage, "VIBER_MIGRATION_CRASH_STORE="+directory, "VIBER_MIGRATION_CRASH_INTENT="+string(raw))
			output, err := worker.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("worker did not crash at %s: %v %s", stage, err, output)
			}
			recovered := open(t, directory)
			if recovered.Generation() != generation+1 || recovered.ReadOnlyReason() == "" {
				t.Fatal("recovery mutated generation or admitted writes", recovered.Generation())
			}
			info, err := recovered.SnapshotInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			expectedVersion := 1
			if stage == "after-commit" || stage == "before-marker" {
				expectedVersion = 2
			}
			if info.SchemaVersion != expectedVersion || info.StoreSeq != intent.Source.StoreSeq || info.TailHash != intent.Source.TailHash {
				t.Fatal("transaction boundary changed journal", info)
			}
			if _, err = recovered.Execute(ctx, cmd("forbidden", "u", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err == nil {
				t.Fatal("write during recovery")
			}
			if _, err = recovered.Checkpoint(ctx, "t"); err == nil {
				t.Fatal("checkpoint write during recovery")
			}
			replay, err := recovered.Replay(ctx, "t")
			if err != nil || !reflect.DeepEqual(replay["t"], before) {
				t.Fatal(replay, err)
			}
			changed := intent
			changed.BackupDigest = c.HashBytes([]byte("different backup"))
			if _, err = recovered.Migrate(ctx, changed); err == nil {
				t.Fatal("retry changed immutable backup")
			}
			receipt, err := recovered.Migrate(ctx, intent)
			if err != nil || receipt.Status != "COMMITTED" || recovered.Writable() != nil {
				t.Fatal(receipt, err)
			}
			recovered.Close()
			reopened := open(t, directory)
			if reopened.Generation() != generation+2 || reopened.Writable() != nil {
				t.Fatal("completed migration not writable")
			}
			same, err := reopened.Migrate(ctx, intent)
			if err != nil || same != receipt {
				t.Fatal(same, err)
			}
		})
	}
}
func TestMigrationCrashWorker(t *testing.T) {
	stage := os.Getenv("VIBER_MIGRATION_CRASH_STAGE")
	if stage == "" {
		return
	}
	s, err := Open(context.Background(), os.Getenv("VIBER_MIGRATION_CRASH_STORE"))
	if err != nil {
		t.Fatal(err)
	}
	var intent MigrationIntent
	if err = json.Unmarshal([]byte(os.Getenv("VIBER_MIGRATION_CRASH_INTENT")), &intent); err != nil {
		t.Fatal(err)
	}
	s.migrationFault = func(actual string) error {
		if actual == stage {
			os.Exit(73)
		}
		return nil
	}
	if _, err = s.Migrate(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash stage was not reached")
}
func TestFormatAndMigrationTamperingCannotOpen(t *testing.T) {
	for _, kind := range []string{"format-digest", "schema-downgrade", "intent-mismatch", "completion-mismatch", "missing-sidecars", "orphan-marker", "pending-tail-change"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			s := legacyStore(t, directory)
			intent := migrationIntent(t, s)
			if kind == "pending-tail-change" {
				s.migrationFault = func(string) error { return errors.New("injected interruption") }
				if _, err := s.Migrate(context.Background(), intent); err == nil {
					t.Fatal("fault not injected")
				}
				s.readOnlyReason = ""
				if _, err := s.Execute(context.Background(), cmd("forbidden", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.Migrate(context.Background(), intent); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "format-digest":
					s.db.Exec("UPDATE format_manifest SET digest='corrupt'")
				case "schema-downgrade":
					s.db.Exec("PRAGMA user_version=1")
				case "intent-mismatch":
					intent.ID = "tampered"
					raw, _ := c.CanonicalV1(intent)
					os.WriteFile(filepath.Join(directory, MigrationIntentFile), raw, 0600)
				case "completion-mismatch":
					os.WriteFile(filepath.Join(directory, MigrationCompleteFile), []byte("{}"), 0600)
				case "missing-sidecars":
					os.Remove(filepath.Join(directory, MigrationIntentFile))
					os.Remove(filepath.Join(directory, MigrationCompleteFile))
				case "orphan-marker":
					os.Remove(filepath.Join(directory, MigrationIntentFile))
				}
			}
			s.Close()
			opened, err := Open(context.Background(), directory)
			if err == nil {
				opened.Close()
				t.Fatal("tampered format/migration admitted")
			}
		})
	}
}

func TestMigrationRejectsRehashedFalseCheckpoint(t *testing.T) {
	s := legacyStore(t, t.TempDir())
	state := terminalTask(t, s)
	intent := migrationIntent(t, s)
	state.Outcome = c.Finished
	raw, _ := c.CanonicalV1(state)
	digest, _ := c.Digest(state)
	if _, err := s.db.Exec("UPDATE checkpoints SET state=?,state_digest=? WHERE task_id='t'", raw, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(context.Background(), intent); err == nil {
		t.Fatal("rehashed false checkpoint migrated")
	}
	if _, err := os.Stat(filepath.Join(s.directory, MigrationIntentFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("intent before checkpoint validation", err)
	}
}

func TestStoreRejectsHardlinkedMetadataBeforeSQLiteOrLock(t *testing.T) {
	for _, name := range []string{"owner.lock", "state.sqlite", "state.sqlite-wal", "state.sqlite-shm"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			outside := filepath.Join(t.TempDir(), "unrelated")
			original := []byte("unrelated file must not be accessed as metadata")
			if err := os.WriteFile(outside, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(outside, filepath.Join(directory, name)); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(context.Background(), directory)
			if err == nil {
				opened.Close()
				t.Fatal("hardlinked metadata accepted")
			}
			var typed *c.Error
			if !errors.As(err, &typed) || typed.Code != c.UnsupportedCapability {
				t.Fatal("metadata library reached before guard", err)
			}
			after, err := os.ReadFile(outside)
			if err != nil || !reflect.DeepEqual(after, original) {
				t.Fatal("unrelated metadata changed", err)
			}
		})
	}
}

func TestMigrationCancelledCommitStaysReadOnlyAndCanReconcile(t *testing.T) {
	s := legacyStore(t, t.TempDir())
	terminalTask(t, s)
	intent := migrationIntent(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.migrationFault = func(stage string) error {
		if stage == "before-commit" {
			cancel()
		}
		return nil
	}
	if _, err := s.Migrate(ctx, intent); err == nil {
		t.Fatal("cancelled commit reported success")
	}
	if s.Writable() == nil {
		t.Fatal("cancelled commit reopened admission")
	}
	s.migrationFault = nil
	receipt, err := s.Migrate(context.Background(), intent)
	if err != nil || receipt.Status != "COMMITTED" || s.Writable() != nil {
		t.Fatal(receipt, err)
	}
}
func TestMigrationCommandIDNamespaceCannotAliasJournalCommand(t *testing.T) {
	t.Run("existing-journal-id", func(t *testing.T) {
		s := legacyStore(t, t.TempDir())
		terminalTask(t, s)
		intent := migrationIntent(t, s)
		intent.ID = "cancel"
		if _, err := s.Migrate(context.Background(), intent); err == nil {
			t.Fatal("existing journal command ID accepted")
		}
		if _, err := os.Stat(filepath.Join(s.directory, MigrationIntentFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("conflicting intent published", err)
		}
	})
	t.Run("committed-migration-id", func(t *testing.T) {
		s := legacyStore(t, t.TempDir())
		intent := migrationIntent(t, s)
		if _, err := s.Migrate(context.Background(), intent); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Execute(context.Background(), cmd(intent.ID, "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err == nil {
			t.Fatal("migration command ID reused in journal")
		}
		info, err := s.SnapshotInfo(context.Background())
		if err != nil || info.StoreSeq != 0 {
			t.Fatal(info, err)
		}
	})
}
