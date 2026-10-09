package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

func legacySession(t *testing.T, s *Session) *Session {
	t.Helper()
	directory := s.directory
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(directory, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("DROP TABLE format_manifest"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err = database.Exec("PRAGMA user_version=1"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { next.Close() })
	return next
}
func migrationFixture(t *testing.T) (*Session, StartOptions) {
	t.Helper()
	s, options, _ := startFixture(t, []Turn{{Text: "Offline candidate complete", UsageKnown: true, InputTokens: 3, OutputTokens: 1}}, true)
	if _, err := s.Run(context.Background(), options.TaskID); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return legacySession(t, s), options
}
func TestApplicationMigrationValidatesBackupAndPreservesFullHistory(t *testing.T) {
	ctx := context.Background()
	s, options := migrationFixture(t)
	before, doc, err := s.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "before")
	receipt, err := s.Migrate(ctx, "upgrade-one", backup)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Target.SchemaVersion != 2 || receipt.Intent.BackupDigest == "" || receipt.Status != "COMMITTED" {
		t.Fatal(receipt)
	}
	manifest, err := migrationBackupManifest(backup)
	if err != nil || manifest.Store.SchemaVersion != 1 {
		t.Fatal(manifest, err)
	}
	digest, _ := c.Digest(manifest)
	if digest != receipt.Intent.BackupDigest {
		t.Fatal("backup binding changed")
	}
	state, after, err := s.Load(ctx, options.TaskID)
	if err != nil || !reflect.DeepEqual(before, state) || !reflect.DeepEqual(doc, after) {
		t.Fatal("migration changed task bytes", err)
	}
	// Old-format backup remains independently restorable and is never upgraded.
	oldRestore := filepath.Join(t.TempDir(), "old")
	if _, err = RestoreBackup(ctx, backup, oldRestore); err != nil {
		t.Fatal(err)
	}
	old, err := OpenExisting(ctx, oldRestore)
	if err != nil {
		t.Fatal(err)
	}
	info, err := old.Journal.SnapshotInfo(ctx)
	old.Close()
	if err != nil || info != manifest.Store {
		t.Fatal(info, err)
	}
	// A v2 backup retains the migration markers and exact format lineage.
	afterBackup := filepath.Join(t.TempDir(), "after")
	newer, err := s.Backup(ctx, afterBackup)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, f := range newer.Files {
		if f.Path == "metadata/migrations/v1-v2-intent.json" || f.Path == "metadata/migrations/v1-v2-complete.json" {
			count++
		}
	}
	if count != 2 {
		t.Fatal("migration markers absent from backup")
	}
	restoredPath := filepath.Join(t.TempDir(), "new")
	if _, err = RestoreBackup(ctx, afterBackup, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	info, err = restored.Journal.SnapshotInfo(ctx)
	if err != nil || info != receipt.Target {
		t.Fatal(info, err)
	}
	restoredState, restoredDoc, err := restored.Load(ctx, options.TaskID)
	if err != nil || !reflect.DeepEqual(before, restoredState) || !reflect.DeepEqual(doc, restoredDoc) {
		t.Fatal(err)
	}
	same, err := s.Migrate(ctx, "upgrade-one", backup)
	if err != nil || same != receipt {
		t.Fatal(same, err)
	}
	if _, err = s.Migrate(ctx, "other", backup); err == nil {
		t.Fatal("migration identity changed")
	}
}
func TestApplicationMigrationRejectsActiveUnknownAndCorruptEvidence(t *testing.T) {
	for _, kind := range []string{"active", "unknown", "corrupt-backup", "missing-raw-input", "invalid-id", "source-overlap"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, options := migrationFixture(t)
			output := filepath.Join(t.TempDir(), "backup")
			id := "upgrade-one"
			switch kind {
			case "active":
				options.TaskID = "another"
				if _, err := s.Create(ctx, options); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				// An unknown inference retains its reservation even after cancellation.
				unknown, opts, _ := startFixture(t, []Turn{{UsageKnown: false}}, true)
				if _, err := unknown.Run(ctx, opts.TaskID); err != nil {
					t.Fatal(err)
				}
				if _, err := unknown.Controls(ctx, opts.TaskID, "cancel"); err != nil {
					t.Fatal(err)
				}
				s = legacySession(t, unknown)
			case "corrupt-backup":
				m, err := s.Backup(ctx, output)
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range m.Files {
					if f.Path == "database/state.sqlite" {
						if err = os.WriteFile(filepath.Join(output, filepath.FromSlash(f.Path)), []byte("corrupt"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			case "missing-raw-input":
				_, doc, err := s.Load(ctx, options.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				digest := doc.Spec.Inputs[0].Digest
				// Find the task-scoped immutable object without assuming CAS path layout.
				var name string
				if err = s.Archive.VisitImmutable(ctx, func(path string, raw []byte) error {
					if c.HashBytes(raw) == digest {
						name = path
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if name == "" {
					t.Fatal("raw input not found")
				}
				if err = os.Remove(filepath.Join(s.directory, "artifacts", filepath.FromSlash(name))); err != nil {
					t.Fatal(err)
				}
			case "invalid-id":
				id = ""
			case "source-overlap":
				output = filepath.Join(options.Root, "backup")
			}
			if _, err := s.Migrate(ctx, id, output); err == nil {
				t.Fatal("invalid application migration admitted")
			}
			if _, err := os.Stat(filepath.Join(s.directory, store.MigrationIntentFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("migration intent exists after failed preflight", err)
			}
		})
	}
}
func TestPendingMigrationStopsAdmissionBeforeArtifactOrFixtureWork(t *testing.T) {
	ctx := context.Background()
	s, options := migrationFixture(t)
	backup, err := fileguard.ResolveProspective(filepath.Join(t.TempDir(), "backup"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Backup(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := c.Digest(m)
	intent := store.MigrationIntent{SchemaVersion: 1, ID: "upgrade-one", FromVersion: 1, ToVersion: 2, BackupPath: backup, BackupDigest: digest, Source: m.Store}
	directory := s.directory
	generation := s.Journal.Generation()
	s.Close()
	if err = os.MkdirAll(filepath.Join(directory, ".migrations"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(intent)
	if err = os.WriteFile(filepath.Join(directory, store.MigrationIntentFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Journal.Generation() != generation || s.Journal.ReadOnlyReason() == "" {
		t.Fatal("pending migration admitted generation")
	}
	count := func() int {
		n := 0
		if err := s.Archive.VisitImmutable(ctx, func(string, []byte) error { n++; return nil }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	objects := count()
	options.TaskID = "blocked-create"
	if _, err = s.Create(ctx, options); err == nil {
		t.Fatal("create while migrating")
	}
	if _, err = s.Run(ctx, "task-one"); err == nil {
		t.Fatal("run while migrating")
	}
	if _, err = s.RecordSteering(ctx, SteeringInput{CommandID: "blocked-input", TaskID: "task-one", Text: "new"}); err == nil {
		t.Fatal("steer while migrating")
	}
	if count() != objects {
		t.Fatal("blocked admission published artifacts")
	}
	if _, _, err = s.Load(ctx, "task-one"); err != nil {
		t.Fatal("read-only inspection unavailable", err)
	}
	receipt, err := s.Migrate(ctx, intent.ID, backup)
	if err != nil || receipt.Status != "COMMITTED" || s.Journal.Writable() != nil {
		t.Fatal(receipt, err)
	}
}
