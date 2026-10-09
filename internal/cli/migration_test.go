package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

func TestCLIMigrationAndRestoreOfOldAndNewFormats(t *testing.T) {
	directory := t.TempDir()
	source := t.TempDir()
	fixturePath := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "candidate result", UsageKnown: true, InputTokens: 2, OutputTokens: 1}}})
	if err := os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if exit := Execute([]string{"run", "Analyze source", "--root", source, "--store", directory, "--task", "t", "--offline", "--fixture", fixturePath, "--allow-unverified", "--json"}, &out, &stderr); exit != 2 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	database, err := sql.Open("sqlite", filepath.Join(directory, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("DROP TABLE format_manifest"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	database.Close()
	backup := filepath.Join(t.TempDir(), "before")
	out.Reset()
	args := []string{"store-migrate", "--store", directory, "--backup-output", backup, "--command-id", "migration-one", "--json"}
	if exit := Execute(args, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	var receipt store.MigrationReceipt
	if err = json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.Status != "COMMITTED" || receipt.Target.SchemaVersion != 2 {
		t.Fatal(receipt, err)
	}
	first := out.String()
	out.Reset()
	if exit := Execute(args, &out, &stderr); exit != 0 || out.String() != first {
		t.Fatal("different idempotent receipt", exit, out.String(), stderr.String())
	}
	out.Reset()
	if exit := Execute([]string{"store-migration-status", "--store", directory, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String())
	}
	var status migrationStatus
	if err = json.Unmarshal(out.Bytes(), &status); err != nil || status.ReadOnlyReason != "" || status.Receipt == nil || status.Snapshot.SchemaVersion != 2 {
		t.Fatal(status, err)
	}
	for _, format := range []string{"before", "after"} {
		path := backup
		expected := receipt.Intent.Source
		if format == "after" {
			path = filepath.Join(t.TempDir(), "after")
			expected = receipt.Target
			out.Reset()
			if exit := Execute([]string{"store-backup", "--store", directory, "--output", path, "--json"}, &out, &stderr); exit != 0 {
				t.Fatal(exit, out.String())
			}
		}
		restored := filepath.Join(t.TempDir(), "restored")
		out.Reset()
		exit := Execute([]string{"store-restore", "--backup", path, "--store", restored, "--json"}, &out, &stderr)
		if format == "before" {
			if exit != 4 || !bytes.Contains(out.Bytes(), []byte("UNSUPPORTED_CAPABILITY")) {
				t.Fatal("legacy restore trusted without authority", exit, out.String())
			}
			if _, err := os.Stat(restored); !os.IsNotExist(err) {
				t.Fatal("legacy restore published", err)
			}
			continue
		}
		if exit != 0 {
			t.Fatal(exit, out.String())
		}
		session, err := agent.OpenExisting(context.Background(), restored)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := session.Journal.SnapshotInfo(context.Background())
		session.Close()
		if err != nil || actual != expected {
			t.Fatal(actual, expected, err)
		}
	}
	out.Reset()
	if exit := Execute([]string{"status", "t", "--store", directory, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String())
	}
	var result struct {
		State c.TaskState `json:"state"`
	}
	if err = json.Unmarshal(out.Bytes(), &result); err != nil || result.State.Quality != c.Unverified {
		t.Fatal("migration fabricated verification", err)
	}
	live, err := os.ReadFile(filepath.Join(source, "a.txt"))
	if err != nil || string(live) != "source" {
		t.Fatal("source changed", err)
	}
}
func TestCLIMigrationDoesNotCreateMissingStoreAndEscapesID(t *testing.T) {
	var out, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "absent")
	if exit := Execute([]string{"store-migrate", "--store", missing, "--backup-output", filepath.Join(t.TempDir(), "backup"), "--command-id", "one", "--json"}, &out, &stderr); exit != 4 {
		t.Fatal(exit)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing store created")
	}
	directory := t.TempDir()
	s, err := agent.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(directory, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TABLE format_manifest"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out.Reset()
	if exit := Execute([]string{"store-migrate", "--store", directory, "--backup-output", filepath.Join(t.TempDir(), "backup"), "--command-id", "\x1b]52;c;fake\a"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String())
	}
	if bytes.ContainsAny(out.Bytes(), "\x1b\a") {
		t.Fatal("raw terminal control in migration output")
	}
}
