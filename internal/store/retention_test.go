package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func cursorFixture(t *testing.T, s *Store) c.RetentionCursor {
	t.Helper()
	raw, err := s.PrivacyAuthority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		raw = []byte(`{"schema_version":1,"test_authority":"cursor-metadata"}`)
		if err = s.BindPrivacyAuthority(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
	digest, _ := c.Digest(json.RawMessage(raw))
	return c.RetentionCursor{SchemaVersion: 1, Protocol: "RETENTION_SCHEDULING_ONLY_V1", AuthorityDigest: digest, PhysicalRoot: c.HashBytes([]byte("fixture source root")), TaskID: "task", Deadline: "2026-10-10T09:00:00Z", PolicyDigest: c.HashBytes([]byte("fixture consent"))}
}

func TestRetentionCursorCASIsBoundedAcrossReopenAndCannotChangeAuthorityOrLedger(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	s, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	cursor := cursorFixture(t, s)
	authority, _ := s.PrivacyAuthority(ctx)
	before, _ := s.SnapshotInfo(ctx)
	ledger, _ := s.ResourceLedger(ctx)
	var expected []byte
	for i := 0; i < 100; i++ {
		cursor.TaskID = fmt.Sprintf("task-%03d", i)
		if err = s.SetRetentionCursor(ctx, expected, cursor); err != nil {
			t.Fatal(err)
		}
		actual, raw, err := s.RetentionCursor(ctx)
		if err != nil || actual == nil || *actual != cursor {
			t.Fatal(actual, err)
		}
		if i > 0 && s.SetRetentionCursor(ctx, nil, cursor) == nil {
			t.Fatal("stale cursor replaced current priority")
		}
		expected = raw
		if i%10 == 0 {
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM payloads WHERE task_id=''").Scan(&count); err != nil || count != 2 {
		t.Fatal("priority updates leaked append-only metadata payloads", count, err)
	}
	if err = s.SetRetentionCursor(ctx, expected, cursor); err != nil {
		t.Fatal("exact metadata dedup", err)
	}
	foreign := cursor
	foreign.AuthorityDigest = c.HashBytes([]byte("foreign authority"))
	if s.SetRetentionCursor(ctx, expected, foreign) == nil {
		t.Fatal("foreign authority changed priority")
	}
	after, _ := s.SnapshotInfo(ctx)
	finalLedger, _ := s.ResourceLedger(ctx)
	finalAuthority, _ := s.PrivacyAuthority(ctx)
	if before != after || !reflect.DeepEqual(ledger, finalLedger) || !bytes.Equal(authority, finalAuthority) {
		t.Fatal("scheduling changed kernel state/risk/authority")
	}
	// Even a rehashed foreign projection cannot become a scheduling cursor.
	if _, err = s.db.ExecContext(ctx, "DELETE FROM meta WHERE key LIKE ?", retentionCursorPrefix+"%"); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"schema_version":1,"actor":"retention","deadline":"2026-10-10T09:00:00Z"}`)
	if _, err = s.db.ExecContext(ctx, "INSERT INTO payloads(task_id,digest,body) VALUES('',?,?)", c.HashBytes(bad), bad); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,1)", retentionCursorPrefix+c.HashBytes(bad)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.RetentionCursor(ctx); err == nil {
		t.Fatal("rehashed foreign cursor accepted")
	}
}

func TestRetentionCursorActualChildDeathRetainsCommittedPriority(t *testing.T) {
	directory := t.TempDir()
	s, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	cursorFixture(t, s)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(exe, "-test.run=^TestRetentionCursorCrashHelper$")
	child.Env = append(os.Environ(), "VIBER_CURSOR_TEST_STORE="+directory)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 85 {
		t.Fatal("actual child did not exit after commit", err, string(output))
	}
	s, err = Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cursor, _, err := s.RetentionCursor(context.Background())
	if err != nil || cursor == nil || cursor.TaskID != "crash-cursor" {
		t.Fatal("committed priority disappeared after actual process death", cursor, err)
	}
}

func TestRetentionCursorCrashHelper(t *testing.T) {
	directory := os.Getenv("VIBER_CURSOR_TEST_STORE")
	if directory == "" {
		t.Skip("explicit disposable crash child only")
	}
	s, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	cursor := cursorFixture(t, s)
	cursor.TaskID = "crash-cursor"
	if err = s.SetRetentionCursor(context.Background(), nil, cursor); err != nil {
		t.Fatal(err)
	}
	os.Exit(85)
}
