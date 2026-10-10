package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/diskguard"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestPrivacyMetadataPressurePreservesDeletionClassAndBoundsTemporaryBytes(t *testing.T) {
	cases := []struct {
		name                         string
		records, retained, temporary int64
		control, allowed             bool
	}{
		{"last-work-slot", privacyWorkRecordLimit - 1, 0, 4096, false, true},
		{"work-slots-exhausted", privacyWorkRecordLimit, 0, 4096, false, false},
		{"deletion-slot-retained", privacyWorkRecordLimit, 0, 4096, true, true},
		{"last-control-slot", privacyRecordLimit - 1, 0, 4096, true, true},
		{"all-slots-exhausted", privacyRecordLimit, 0, 4096, true, false},
		{"temporary-fits-work", 0, privacyWorkMetadataLimit - 4096, 4096, false, true},
		{"temporary-exceeds-work", 0, privacyWorkMetadataLimit - 4095, 4096, false, false},
		{"deletion-bytes-retained", 0, privacyWorkMetadataLimit, 16 << 20, true, true},
		{"control-byte-bound", 0, privacyMetadataLimit - 4096, 4096, true, true},
		{"control-byte-exhausted", 0, privacyMetadataLimit - 4095, 4096, true, false},
		{"invalid-counter", -1, 0, 4096, true, false},
		{"overflow-size", 0, 0, 1 << 62, true, false},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			err := privacyMetadataAdmission(fixture.records, fixture.retained, fixture.temporary, fixture.control)
			if (err == nil) != fixture.allowed {
				t.Fatal(err)
			}
		})
	}
}

func TestPrivacyWorkJournalSaturationStillPublishesRealDeletionIntent(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	defer s.Close()
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	view, err := readPrivacy(root)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	output := t.TempDir()
	copyRoot, err := os.OpenRoot(output)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	physical, err := fileguard.DirectoryIdentity(copyRoot)
	copyRoot.Close()
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	copy := ManagedCopy{Kind: "SUPPORT", Directory: output, PhysicalRoot: physical, Tasks: []string{options.TaskID}, Files: []BackupFile{}}
	for view.Sequence < privacyWorkRecordLimit {
		record := privacyRecord{Type: "MANAGED_COPY", Copy: &copy}
		if err = appendPrivacy(root, view, record); err != nil {
			lock.Close()
			t.Fatal("ordinary record publication failed", view.Sequence, err)
		}
		record.SchemaVersion = 1
		record.Sequence = view.Sequence + 1
		record.Previous = view.Tail
		raw, _ := c.CanonicalV1(record)
		view.Sequence++
		view.Tail = c.HashBytes(raw)
	}
	countBefore, err := privacyMetadataBytes(root)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	if err = appendPrivacy(root, view, privacyRecord{Type: "MANAGED_COPY", Copy: &copy}); err == nil {
		lock.Close()
		t.Fatal("ordinary record consumed deletion journal slot")
	}
	countAfter, err := privacyMetadataBytes(root)
	if err != nil || countBefore != countAfter {
		lock.Close()
		t.Fatal("rejected work published bytes", err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	reserve, err := diskguard.Open(s.privacy.Directory)
	if err != nil {
		t.Fatal(err)
	}
	status, err := reserve.Status()
	reserve.Close()
	if err != nil || status.RetainedReserveBytes != diskguard.ReserveBytes {
		t.Fatal("external authority has no physical control reserve", status, err)
	}
	capacity, err := s.PrivacyMetadataCapacity(ctx)
	if err != nil || capacity.WorkReady || capacity.Records != privacyWorkRecordLimit || capacity.PhysicalReserveBytes != diskguard.ReserveBytes {
		t.Fatal("saturated capacity status is inaccurate", capacity, err)
	}
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil || plan.RegistrySequence != privacyWorkRecordLimit {
		t.Fatal("saturated journal prevented deletion preview", err)
	}
	result, err := s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-after-work-journal-saturation", Plan: plan})
	if err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal("real deletion did not use reserved metadata class", result, err)
	}
	view, err = readPrivacy(root)
	if err != nil || view.Sequence != privacyWorkRecordLimit+1 || view.Watermark != 1 {
		t.Fatal(view.Sequence, view.Watermark, err)
	}
}

func TestPrivacyExternalPhysicalReserveReplacementStopsPublication(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	defer s.Close()
	path := filepath.Join(s.privacy.Directory, "control.reserve")
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) != int(diskguard.ReserveBytes) {
		t.Fatal("missing physical external reserve", len(raw), err)
	}
	// Oversized replacement is not capacity and must fail before an intent is
	// published. This does not erase or pretend to settle any outstanding risk.
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(diskguard.ReserveBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "tampered-privacy-capacity", Plan: plan})
	if err == nil {
		t.Fatal("invalid physical reserve authorized deletion publication")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	view, err := readPrivacy(root)
	if err != nil || view.Watermark != 0 {
		t.Fatal("failed admission wrote deletion intent", view.Watermark, err)
	}
	_, _, err = s.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal("failed admission removed content")
	}
}

func TestPrivacyOwnerCatalogRejectsBeforeLeaseAndExistingOwnerCanReopenAndDelete(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	t.Cleanup(func() { s.Close() })
	directory := s.directory
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	count, err := admitPrivacyCatalog(root)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	for i := 1; count < privacyWorkCatalogLimit; i++ {
		f, createErr := root.OpenFile(filepath.Join("owners", fmt.Sprintf("%032x.lock", i)), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if createErr != nil {
			lock.Close()
			t.Fatal(createErr)
		}
		if createErr = f.Close(); createErr != nil {
			lock.Close()
			t.Fatal(createErr)
		}
		count++
	}
	lock.Close()
	if _, err = privacyOperation(ctx, *s.privacy); err == nil {
		t.Fatal("restore operation created an over-quota owner lease")
	}
	after, err := admitPrivacyCatalog(root)
	if err != nil || after != count {
		t.Fatal("rejected operation left a poison lease", after, err)
	}
	candidate := filepath.Join(t.TempDir(), "evaluator")
	if owner, err := s.OpenIndependentEvaluator(ctx, candidate); err == nil {
		owner.Close()
		t.Fatal("new evaluator entered saturated owner catalog")
	}
	after, err = admitPrivacyCatalog(root)
	if err != nil || after != count {
		t.Fatal("failed owner registration left a poison lease", after, err)
	}
	s.Close()
	s, err = OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal("known physical owner cannot reopen at catalog pressure", err)
	}
	capacity, err := s.PrivacyMetadataCapacity(ctx)
	if err != nil || capacity.WorkReady || capacity.CatalogEntries != privacyWorkCatalogLimit {
		t.Fatal(capacity, err)
	}
	// Historical/foreign pressure can reach the total limit even though current
	// ordinary admission stops at 7680. It must not lock out a known owner.
	lock, err = privacyLock(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for i := privacyWorkCatalogLimit + 1; count < privacyCatalogLimit; i++ {
		f, createErr := root.OpenFile(filepath.Join("owners", fmt.Sprintf("%032x.lock", i)), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if createErr != nil {
			lock.Close()
			t.Fatal(createErr)
		}
		if createErr = f.Close(); createErr != nil {
			lock.Close()
			t.Fatal(createErr)
		}
		count++
	}
	if err = fileguard.SyncDirectory(root, "owners"); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	lock.Close()
	s.Close()
	s, err = OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal("known owner cannot reopen at total catalog limit", err)
	}
	capacity, err = s.PrivacyMetadataCapacity(ctx)
	if err != nil || capacity.WorkReady || capacity.CatalogEntries != privacyCatalogLimit {
		t.Fatal("total catalog capacity observation", capacity, err)
	}
	capacity, err = s.ReplenishPrivacyControlReserve(ctx)
	if err != nil || capacity.WorkReady || len(capacity.WorkBlockers) == 0 || capacity.WorkBlockers[0] != "OWNER_CATALOG_FULL_RETIREMENT_REQUIRED" {
		t.Fatal("reserve maintenance disguised catalog exhaustion", capacity, err)
	}
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal("registered owner cannot preview deletion at catalog pressure", err)
	}
	result, err := s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-at-owner-catalog-pressure", Plan: plan})
	if err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal(result, err)
	}
	after, err = admitPrivacyCatalog(root)
	if err != nil || after != count {
		t.Fatal("deletion unexpectedly altered owner identities", after, err)
	}
}
