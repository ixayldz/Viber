package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func TestBackupRestoresCandidateRawIntentHistoryAndUnknownReservation(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "unknown"}[unknown], func(t *testing.T) {
			session, options, source := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "final", UsageKnown: !unknown}}, true)
			defer session.Close()
			state, err := session.Run(context.Background(), options.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			_, before, err := session.Load(context.Background(), options.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "backup")
			manifest, err := session.Backup(context.Background(), output)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Store.StoreSeq != state.StoreSeq {
				t.Fatal("inconsistent WAL snapshot", manifest.Store, state)
			}
			restoredPath := filepath.Join(t.TempDir(), "restored")
			if _, err = RestoreBackup(context.Background(), output, restoredPath); err != nil {
				t.Fatal(err)
			}
			restored, err := Open(context.Background(), restoredPath)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			actual, after, err := restored.Load(context.Background(), options.TaskID)
			if err != nil || !reflect.DeepEqual(state, actual) || !reflect.DeepEqual(before, after) {
				t.Fatal("restore changed authoritative state", err)
			}
			candidate, err := restored.Archive.Get(after.Candidate)
			if err != nil || string(candidate.Contents["a.txt"]) != "after\r\n" {
				t.Fatal("candidate absent", err)
			}
			if _, err = restored.Archive.Materialize(after.Candidate); err != nil {
				t.Fatal("materialization not reconstructable", err)
			}
			if unknown {
				if _, err = restored.Run(context.Background(), options.TaskID); err != nil {
					t.Fatal(err)
				}
				_, doc, err := restored.Load(context.Background(), options.TaskID)
				if err != nil || doc.FixtureCursor != 2 || doc.Pending == nil || doc.Budget.ReservedInput != before.Budget.ReservedInput || !doc.UnknownEffect {
					t.Fatal("restore retried unknown model effect", err)
				}
			}
			live, _ := os.ReadFile(filepath.Join(source, "a.txt"))
			if string(live) != "before\r\n" {
				t.Fatal("source modified")
			}
			if _, err = os.Stat(filepath.Join(output, "artifacts", "tasks", options.TaskID, "candidates")); !os.IsNotExist(err) {
				t.Fatal("materialization leaked into backup")
			}
		})
	}
}
func TestBackupRefusesMissingHistoricalEvidenceAndUnsafeDestination(t *testing.T) {
	session, options, source := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer session.Close()
	_, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{filepath.Join(source, "backup"), filepath.Join(session.directory, "backup"), source} {
		if _, err = session.Backup(context.Background(), output); err == nil {
			t.Fatal("overlap accepted", output)
		}
	}
	// Raw intent is a durable prerequisite, not optional backup decoration.
	name := filepath.Join(session.directory, "artifacts", "tasks", options.TaskID, "blobs", doc.Spec.Inputs[0].Digest[:2], doc.Spec.Inputs[0].Digest)
	if err = os.Remove(name); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "backup")
	if _, err = session.Backup(context.Background(), output); err == nil {
		t.Fatal("missing intent backed up")
	}
	if _, err = os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("invalid backup published")
	}
}
func TestRestoreRejectsCorruptionIncompleteBackupAndManifestAttacks(t *testing.T) {
	cases := []string{"corrupt-blob", "missing-file", "unfinished", "traversal", "duplicate", "new-schema", "delete-watermark", "wrong-journal", "missing-reference"}
	for _, attack := range cases {
		t.Run(attack, func(t *testing.T) {
			session, _, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
			defer session.Close()
			output := filepath.Join(t.TempDir(), "backup")
			m, err := session.Backup(context.Background(), output)
			if err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(output, "backup.json")
			first := filepath.Join(output, filepath.FromSlash(m.Files[0].Path))
			switch attack {
			case "corrupt-blob":
				err = os.WriteFile(first, []byte("corrupt"), 0600)
			case "missing-file":
				err = os.Remove(first)
			case "unfinished":
				err = os.Remove(manifestPath)
			case "traversal":
				m.Files[0].Path = "../escape"
			case "duplicate":
				m.Files = append(m.Files, m.Files[0])
			case "new-schema":
				m.SchemaVersion = 99
			case "delete-watermark":
				m.DeletionWatermark = 1
			case "wrong-journal":
				m.Store.StoreSeq++
			case "missing-reference":
				m.Files = m.Files[1:] // Keep a valid ordered manifest but omit referenced CAS bytes.
			}
			if err != nil {
				t.Fatal(err)
			}
			if attack != "unfinished" && attack != "corrupt-blob" && attack != "missing-file" {
				raw, _ := json.Marshal(m)
				if err = os.WriteFile(manifestPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(t.TempDir(), "restored")
			if _, err = RestoreBackup(context.Background(), output, target); err == nil {
				t.Fatal("attack accepted", attack)
			}
			if _, err = os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("failed restore published destination", err)
			}
		})
	}
}
func TestBackupRestoreNeverOverwritesOrWritesIntoSource(t *testing.T) {
	session, _, source := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer session.Close()
	output := filepath.Join(t.TempDir(), "backup")
	if _, err := session.Backup(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Backup(context.Background(), output); err == nil {
		t.Fatal("backup overwritten")
	}
	for _, target := range []string{t.TempDir(), session.directory, output, filepath.Join(source, "restored")} {
		if _, err := RestoreBackup(context.Background(), output, target); err == nil {
			t.Fatal("invalid destination", target)
		}
	}
	if _, err := os.Stat(filepath.Join(source, "restored")); !os.IsNotExist(err) {
		t.Fatal("source mutated by restore")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RestoreBackup(ctx, output, filepath.Join(t.TempDir(), "cancelled")); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestBackupManifestHasNoFalseDeletionAssurance(t *testing.T) {
	m := BackupManifest{SchemaVersion: 1, DeletionPolicy: "UNSUPPORTED_NO_DELETIONS", Files: []BackupFile{{Path: "database/state.sqlite", Digest: c.HashBytes(nil)}}}
	m.Store.SchemaVersion = 1
	m.Store.ReducerVersion = c.ReducerVersion
	if err := validateBackupManifest(m); err != nil {
		t.Fatal(err)
	}
	m.DeletionPolicy = "DELETED"
	if err := validateBackupManifest(m); err == nil {
		t.Fatal("unknown deletion semantics accepted")
	}
}
