package store

import (
	"context"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestPrivacyAuthorityWriteOnceCASSurvivesReplayAndRejectsCorruption(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw := []byte(`{"schema_version":1,"authority":"metadata only"}`)
	if err = s.BindPrivacyAuthority(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err = s.BindPrivacyAuthority(ctx, raw); err != nil {
		t.Fatal("idempotent pin", err)
	}
	if err = s.BindPrivacyAuthority(ctx, []byte("different")); err == nil {
		t.Fatal("authority replaced")
	}
	actual, err := s.PrivacyAuthority(ctx)
	if err != nil || string(actual) != string(raw) {
		t.Fatal(actual, err)
	}
	if _, err = s.DocumentReferences(ctx); err != nil {
		t.Fatal("metadata confused with event payload", err)
	}
	if _, err = s.SnapshotInfo(ctx); err != nil {
		t.Fatal("snapshot changed journal semantics", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE payloads SET body=? WHERE task_id='' AND digest=?", []byte("corrupt"), c.HashBytes(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrivacyAuthority(ctx); err == nil {
		t.Fatal("corrupt authority accepted")
	}
}
