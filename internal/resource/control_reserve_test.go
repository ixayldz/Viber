package resource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/diskguard"
)

func TestControlReserveIsPayloadHighWaterAndCrashReconciles(t *testing.T) {
	ctx := context.Background()
	authority, parent := testAuthority(t)
	limits := Limits{Bytes: 48 << 20, Entries: 128, ControlBytes: 1 << 20, ControlEntries: 8, LowWaterBytes: 0}
	owner := createMutableTestOwner(t, parent, "reserve-owner", 4<<10)
	disk, err := diskguard.Open(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = disk.Close(); err != nil {
		t.Fatal(err)
	}
	ledger := openTestLedger(t, authority, limits, "")
	ledgerID := ledger.LedgerID
	reserve, err := ledger.AttachControlReserve(ctx, owner)
	if err != nil || reserve == nil {
		t.Fatalf("attach owner control reserve: reserve=%v err=%v", reserve, err)
	}
	initial, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.RetainedBytes != directoryCharge || initial.OutstandingBytes != controlReserveCeiling()-directoryCharge || initial.RetainedEntries != 1 || initial.OutstandingItems != 0 || initial.Unknown != 0 {
		t.Fatalf("reserve high-water was not durably charged as PAYLOAD: %+v", initial)
	}
	acceptedPath := filepath.Join(parent, "payload-accepted")
	if err = os.Mkdir(acceptedPath, 0700); err != nil {
		t.Fatal(err)
	}
	accepted, err := ledger.Reserve(ctx, Request{Writer: "payload", Family: "AFTER_RESERVE", RequestDigest: c.HashBytes([]byte("payload within aggregate quota")), TargetRoot: acceptedPath, UpperBytes: 14 << 20, UpperEntries: 1, Class: Payload})
	if err != nil {
		t.Fatalf("ordinary payload could not use remaining PAYLOAD capacity: %v", err)
	}
	if _, err = ledger.Reserve(ctx, Request{Writer: "payload", Family: "OVER_RESERVE", RequestDigest: c.HashBytes([]byte("payload beyond aggregate quota")), TargetRoot: filepath.Join(parent, "payload-denied"), UpperBytes: 2 << 20, UpperEntries: 1, Class: Payload}); err == nil {
		t.Fatal("ordinary payload consumed the physical control reserve's aggregate PAYLOAD charge")
	}
	if err = accepted.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	acceptedRoot, err := os.OpenRoot(acceptedPath)
	if err != nil {
		t.Fatal(err)
	}
	acceptedInventory, measureErr := Measure(acceptedRoot, true)
	closeErr := acceptedRoot.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = accepted.Commit(ctx, acceptedInventory); err != nil {
		t.Fatal(err)
	}
	if err = reserve.Prepare(ctx, c.HashBytes([]byte("diskguard refill"))); err != nil {
		t.Fatalf("prepare bounded reserve refill: %v", err)
	}
	root, err := os.OpenRoot(owner)
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("control.reserve", os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	_, writeErr := file.Write(make([]byte, 1<<20))
	syncErr := file.Sync()
	closeErr = errors.Join(file.Close(), root.Close())
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestLedger(t, authority, limits, ledgerID)
	defer reopened.Close()
	recovered, err := reopened.AttachControlReserve(ctx, owner)
	if err != nil || recovered == nil {
		t.Fatalf("reconcile admitted reserve growth after crash: reserve=%v err=%v", recovered, err)
	}
	afterGrowth, err := reopened.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterGrowth.Unknown != 0 || afterGrowth.RetainedBytes < (1<<20)+lockedFileSlack || afterGrowth.RetainedBytes+afterGrowth.OutstandingBytes < controlReserveCeiling() {
		t.Fatalf("crash reconciliation lost measured reserve high-water: %+v", afterGrowth)
	}
	root, err = os.OpenRoot(owner)
	if err != nil {
		t.Fatal(err)
	}
	file, err = root.OpenFile("control.reserve", os.O_WRONLY, 0)
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	err = errors.Join(file.Truncate(0), file.Sync(), file.Close(), root.Close())
	if err != nil {
		t.Fatal(err)
	}
	if err = recovered.Refresh(ctx); err != nil {
		t.Fatalf("refresh after emergency reserve consumption: %v", err)
	}
	afterShrink, err := reopened.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterShrink.RetainedBytes != afterGrowth.RetainedBytes || afterShrink.RetainedBytes+afterShrink.OutstandingBytes < controlReserveCeiling() {
		t.Fatalf("reserve shrink falsely released high-water charge: before=%+v after=%+v", afterGrowth, afterShrink)
	}
}

func TestControlReserveRejectsReplacementInode(t *testing.T) {
	ctx := context.Background()
	authority, parent := testAuthority(t)
	owner := createMutableTestOwner(t, parent, "reserve-replaced", 4<<10)
	disk, err := diskguard.Open(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = disk.Close(); err != nil {
		t.Fatal(err)
	}
	ledger := openTestLedger(t, authority, Limits{Bytes: 128 << 20, Entries: 128, ControlBytes: 1 << 20, ControlEntries: 8, LowWaterBytes: 0}, "")
	defer ledger.Close()
	if _, err = ledger.AttachControlReserve(ctx, owner); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(owner, "control.reserve")
	if err = os.Rename(path, filepath.Join(owner, "old.reserve")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.AttachControlReserve(ctx, owner); err == nil {
		t.Fatal("replacement control reserve inode silently adopted")
	}
}

func TestControlReserveFamilyNameDoesNotHideOrdinaryReservation(t *testing.T) {
	authority, parent := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()
	target := filepath.Join(parent, "ordinary-control-family")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	allocation, err := ledger.Reserve(context.Background(), Request{Writer: "ordinary", Family: controlReserveFamily, RequestDigest: c.HashBytes([]byte("ordinary family text")), TargetRoot: target, UpperBytes: 16 << 10, UpperEntries: 2, Class: Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	nested, covered, err := ledger.EnclosingReservation(context.Background(), filepath.Join(target, "nested"))
	if err != nil || !covered || nested.ID != allocation.ID {
		t.Fatalf("ordinary family-name spoof was hidden from compound admission: covered=%v allocation=%+v err=%v", covered, nested, err)
	}
}
