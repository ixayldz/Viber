package agent

import (
	"context"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/ixayldz/Viber/internal/diskguard"
)

func TestPrivacyReserveRemediationReplenishesPhysicalBytesWithoutChangingAuthorityOrLedger(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := readPrivacy(root)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := s.Journal.PrivacyAuthority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := s.Journal.ResourceLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, err := root.OpenFile("control.reserve", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(diskguard.ReserveBytes / 2); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	capacity, err := s.PrivacyMetadataCapacity(ctx)
	if err != nil || capacity.WorkReady || capacity.PhysicalReserveBytes != diskguard.ReserveBytes/2 || !capacity.ObservationOnly || !slices.Contains(capacity.WorkBlockers, "CONTROL_RESERVE_DEPLETED") || !slices.Contains(capacity.NextActions, "REPLENISH_CONTROL_RESERVE") {
		t.Fatal(capacity, err)
	}
	for retry := 0; retry < 2; retry++ {
		capacity, err = s.ReplenishPrivacyControlReserve(ctx)
		if err != nil || capacity.PhysicalReserveBytes != diskguard.ReserveBytes || !capacity.WorkReady || len(capacity.WorkBlockers) != 0 || slices.Contains(capacity.NextActions, "REPLENISH_CONTROL_RESERVE") {
			t.Fatal("physical replenishment/readback", capacity, err)
		}
	}
	after, err := readPrivacy(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("reserve maintenance changed registry", err)
	}
	current, err := s.Journal.SnapshotInfo(ctx)
	if err != nil || current != info {
		t.Fatal("reserve maintenance changed kernel snapshot", err)
	}
	currentLedger, err := s.Journal.ResourceLedger(ctx)
	if err != nil || !reflect.DeepEqual(ledger, currentLedger) {
		t.Fatal("reserve maintenance changed resource ledger", err)
	}
	currentAuthority, err := s.Journal.PrivacyAuthority(ctx)
	if err != nil || string(authority) != string(currentAuthority) {
		t.Fatal("reserve maintenance rebound authority", err)
	}
	// A corrupted genesis is not permission to replenish or allocate a substitute.
	f, err = root.OpenFile("control.reserve", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(4096); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if err = root.WriteFile("authority.json", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReplenishPrivacyControlReserve(ctx); err == nil {
		t.Fatal("invalid authority allowed reserve maintenance")
	}
	stat, err := root.Stat("control.reserve")
	if err != nil || stat.Size() != 4096 {
		t.Fatal("denial allocated bytes", err)
	}
}
