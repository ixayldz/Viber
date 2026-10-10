package store

import (
	"context"
	"path/filepath"
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

func TestPrivacyPendingAllocationReopensAndBindingAtomicallyPromotesExactCAS(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "owner")
	s, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	raw := []byte(`{"schema_version":1,"allocation":"physical binding"}`)
	before, err := s.SnapshotInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.ReservePrivacyAuthority(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.ReservePrivacyAuthority(ctx, []byte("different")); err == nil {
		t.Fatal("pending allocation replaced")
	}
	if err = s.BindPrivacyAuthority(ctx, []byte("different")); err == nil {
		t.Fatal("wrong authority adopted pending allocation")
	}
	if active, err := s.PrivacyAuthority(ctx); err != nil || active != nil {
		t.Fatal("allocation became active before bind", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingPrivacyAuthority(ctx); err != nil || string(pending) != string(raw) {
		t.Fatal("lost pending CAS on reopen", err)
	}
	if after, err := s.SnapshotInfo(ctx); err != nil || before != after {
		t.Fatal("allocation changed task journal semantics", err)
	}
	if err = s.BindPrivacyAuthority(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingPrivacyAuthority(ctx); err != nil || pending != nil {
		t.Fatal("committed bind retained pending marker", err)
	}
	if active, err := s.PrivacyAuthority(ctx); err != nil || string(active) != string(raw) {
		t.Fatal("bind lost allocation CAS", err)
	}
	if err = s.ReservePrivacyAuthority(ctx, raw); err == nil {
		t.Fatal("bound owner allocated another root")
	}
	if err = s.BindPrivacyAuthority(ctx, raw); err != nil {
		t.Fatal("binding retry not idempotent", err)
	}
	if _, err = s.DocumentReferences(ctx); err != nil {
		t.Fatal("allocation treated as a task document", err)
	}
}

func TestPrivacyPendingAllocationCorruptionCannotPublishCurrentAuthority(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw := []byte(`{"allocation":"original"}`)
	if err = s.ReservePrivacyAuthority(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE payloads SET body=? WHERE task_id='' AND digest=?", []byte("corrupt"), c.HashBytes(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PendingPrivacyAuthority(ctx); err == nil {
		t.Fatal("corrupt allocation accepted")
	}
	if err = s.BindPrivacyAuthority(ctx, raw); err == nil {
		t.Fatal("corrupt pending allocation committed")
	}
	if active, err := s.PrivacyAuthority(ctx); err != nil || active != nil {
		t.Fatal("failed bind wrote active marker", err)
	}
}
