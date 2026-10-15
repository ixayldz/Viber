package agent

import (
	"bytes"
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
	intentTarget, err := canonicalExistingDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	committedIntent, found, err := session.PhysicalQuota.FindReservationIntent(ctx, intentTarget, "agent", "RESTORE_OWNER")
	if err != nil || !found || committedIntent.Match.Status != "RETAINED" {
		t.Fatalf("committed restore lost its original writer intent: intent=%+v found=%v err=%v", committedIntent, found, err)
	}
	metadataBefore, _, err := session.PhysicalQuota.MetadataCatalogSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreBackup(ctx, backup, target); err != nil {
		t.Fatalf("exact retry did not finish privacy owner registration: %v", err)
	}
	afterRetry, err := session.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retriedIntent, found, intentErr := session.PhysicalQuota.FindReservationIntent(ctx, intentTarget, "agent", "RESTORE_OWNER")
	if intentErr != nil || !found || retriedIntent.Match.Status != "RETAINED" || retriedIntent.Match.Allocation.ID != committedIntent.Match.Allocation.ID ||
		retriedIntent.Match.RequestDigest != committedIntent.Match.RequestDigest || retriedIntent.Match.UpperBytes != committedIntent.Match.UpperBytes ||
		retriedIntent.Match.UpperEntries != committedIntent.Match.UpperEntries || !bytes.Equal(retriedIntent.Binding, committedIntent.Binding) {
		t.Fatalf("privacy registration retry changed the exact restore writer allocation: committed=%+v retry=%+v found=%v err=%v", committedIntent, retriedIntent, found, intentErr)
	}
	// Owner registration publishes one immutable privacy metadata receipt. The
	// restore writer itself must remain the same retained allocation and must
	// not be charged again alongside that independently measured record. Owner
	// registration may also grow the already-bound mutable SQLite component.
	metadataCharge := addedPrivacyMetadataCharge(t, session, metadataBefore)
	if afterRetry.Allocations != committed.Allocations+1 || afterRetry.MetadataBytes != committed.MetadataBytes+metadataCharge ||
		afterRetry.MetadataEntries != committed.MetadataEntries+1 || afterRetry.RetainedBytes < committed.RetainedBytes+metadataCharge ||
		afterRetry.RetainedEntries < committed.RetainedEntries+1 ||
		afterRetry.OutstandingBytes != committed.OutstandingBytes || afterRetry.OutstandingItems != committed.OutstandingItems {
		t.Fatalf("privacy registration retry changed restore accounting beyond its one immutable metadata receipt: committed=%+v retry=%+v err=%v", committed, afterRetry, err)
	}
	if _, err = RestoreBackup(ctx, backup, target); err != nil {
		t.Fatalf("repeat of completed exact restore was not idempotent: %v", err)
	}
	afterRepeat, err := session.PhysicalQuota.Capacity(ctx)
	if err != nil || afterRepeat != afterRetry {
		t.Fatalf("completed restore repeat charged the owner again: firstRetry=%+v repeat=%+v err=%v", afterRetry, afterRepeat, err)
	}
	repeatedIntent, found, intentErr := session.PhysicalQuota.FindReservationIntent(ctx, intentTarget, "agent", "RESTORE_OWNER")
	if intentErr != nil || !found || repeatedIntent.Match.Allocation.ID != committedIntent.Match.Allocation.ID ||
		repeatedIntent.Match.RequestDigest != committedIntent.Match.RequestDigest || repeatedIntent.Match.UpperBytes != committedIntent.Match.UpperBytes ||
		repeatedIntent.Match.UpperEntries != committedIntent.Match.UpperEntries || !bytes.Equal(repeatedIntent.Binding, committedIntent.Binding) {
		t.Fatalf("completed restore repeat lost the original writer allocation: original=%+v repeated=%+v found=%v err=%v", committedIntent, repeatedIntent, found, intentErr)
	}
	restored, err := Open(ctx, target)
	if err != nil {
		t.Fatalf("restored owner was not registered after retry: %v", err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
}
