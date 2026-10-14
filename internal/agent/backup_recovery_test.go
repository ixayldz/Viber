package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreBackupRetriesAfterQuotaCommitBeforePrivacyRegistration(t *testing.T) {
	ctx := context.Background()
	session, _, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer session.Close()
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := session.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	before, err := session.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	faultReached := false
	_, err = restoreBackupWithFault(ctx, backup, target, false, func(phase string) error {
		if phase == "after-owner-commit" {
			faultReached = true
			return errors.New("simulated process stop after physical commit")
		}
		return nil
	})
	if err == nil || !faultReached {
		t.Fatalf("expected the injected post-commit stop: reached=%v err=%v", faultReached, err)
	}
	if _, err = os.Stat(filepath.Join(target, "state.sqlite")); err != nil {
		t.Fatalf("committed restore target was not published before the simulated stop: %v", err)
	}
	committed, err := session.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if committed.RetainedBytes <= before.RetainedBytes {
		t.Fatalf("physical commit did not retain the exact restore owner once: before=%+v after=%+v", before, committed)
	}
	if _, err = RestoreBackup(ctx, backup, target); err != nil {
		t.Fatalf("exact retry did not finish privacy owner registration: %v", err)
	}
	afterRetry, err := session.PhysicalQuota.Capacity(ctx)
	if err != nil || afterRetry.OutstandingBytes != committed.OutstandingBytes || afterRetry.Allocations != committed.Allocations ||
		afterRetry.RetainedBytes < committed.RetainedBytes || afterRetry.RetainedEntries < committed.RetainedEntries {
		t.Fatalf("privacy registration retry added a second physical reservation or lost retained bytes: committed=%+v retry=%+v err=%v", committed, afterRetry, err)
	}
	if _, err = RestoreBackup(ctx, backup, target); err != nil {
		t.Fatalf("repeat of completed exact restore was not idempotent: %v", err)
	}
	afterRepeat, err := session.PhysicalQuota.Capacity(ctx)
	if err != nil || afterRepeat != afterRetry {
		t.Fatalf("completed restore repeat charged the owner again: firstRetry=%+v repeat=%+v err=%v", afterRetry, afterRepeat, err)
	}
	restored, err := Open(ctx, target)
	if err != nil {
		t.Fatalf("restored owner was not registered after retry: %v", err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
}
