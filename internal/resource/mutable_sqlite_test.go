package resource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func mutableTestLimits() Limits {
	return Limits{Bytes: 24 << 20, Entries: 1024, ControlBytes: 1 << 20, ControlEntries: 32, LowWaterBytes: 0}
}

func createMutableTestOwner(t *testing.T, parent, name string, initialDBBytes int) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(root); err == nil {
		for _, file := range []struct {
			name string
			data []byte
		}{{"owner.lock", []byte("lock")}, {"state.sqlite", make([]byte, initialDBBytes)}} {
			var opened *os.File
			opened, err = root.OpenFile(file.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				break
			}
			_, err = opened.Write(file.data)
			if err == nil {
				err = opened.Sync()
			}
			err = errors.Join(err, opened.Close())
			if err != nil {
				break
			}
		}
	}
	err = errors.Join(err, root.Close())
	if err != nil {
		t.Fatalf("create private mutable SQLite owner: %v", err)
	}
	return path
}

func appendMutableTestDB(t *testing.T, path string, bytes int) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(path, "state.sqlite"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(make([]byte, bytes))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		t.Fatal(err)
	}
}

func TestMutableSQLiteStandbyReconcilesAdmittedCrashAndHoldsOverrun(t *testing.T) {
	ctx := context.Background()
	limits := mutableTestLimits()

	t.Run("admitted crash is reconciled and standby is rearmed", func(t *testing.T) {
		authority, parent := testAuthority(t)
		owner := createMutableTestOwner(t, parent, "owner", 4<<10)
		ledger := openTestLedger(t, authority, limits, "")
		ledgerID := ledger.LedgerID
		mutable, err := ledger.AttachMutableSQLite(ctx, owner)
		if err != nil || mutable == nil {
			t.Fatalf("attach mutable owner: mutable=%v err=%v", mutable, err)
		}
		before, err := ledger.Capacity(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if before.OutstandingBytes != mutableStartupHeadroom || before.Unknown != 0 {
			t.Fatalf("initial durable standby missing: %+v", before)
		}
		upper, err := MutableSQLiteUpper(1 << 20)
		if err != nil {
			t.Fatal(err)
		}
		if err = mutable.Prepare(ctx, c.HashBytes([]byte("admitted sqlite mutation")), upper); err != nil {
			t.Fatalf("prepare bounded SQLite write: %v", err)
		}
		if _, covered, coverErr := ledger.EnclosingReservation(ctx, filepath.Join(owner, "artifacts")); coverErr != nil || covered {
			t.Fatalf("mutable SQLite namespace covered a separate nested artifact writer: covered=%v err=%v", covered, coverErr)
		}
		appendMutableTestDB(t, owner, 2<<20)
		measuredRoot, err := os.OpenRoot(owner)
		if err != nil {
			t.Fatal(err)
		}
		measured, err := MeasureMutableSQLite(measuredRoot)
		closeErr := measuredRoot.Close()
		if err = errors.Join(err, closeErr); err != nil {
			t.Fatal(err)
		}
		if err = ledger.Close(); err != nil {
			t.Fatal(err)
		}

		reopened := openTestLedger(t, authority, limits, ledgerID)
		defer reopened.Close()
		reconciled, err := reopened.AttachMutableSQLite(ctx, owner)
		if err != nil || reconciled == nil {
			t.Fatalf("reconcile admitted SQLite write after crash: mutable=%v err=%v", reconciled, err)
		}
		capacity, err := reopened.Capacity(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if capacity.Unknown != 0 || capacity.OutstandingBytes != mutableStartupHeadroom || capacity.RetainedBytes < measured.ChargedBytes {
			t.Fatalf("crash reconciliation did not retain measured bytes and rearm standby: capacity=%+v measured=%+v", capacity, measured)
		}
	})

	t.Run("crashed overrun stays unknown and charged", func(t *testing.T) {
		authority, parent := testAuthority(t)
		owner := createMutableTestOwner(t, parent, "owner", 4<<10)
		ledger := openTestLedger(t, authority, limits, "")
		ledgerID := ledger.LedgerID
		mutable, err := ledger.AttachMutableSQLite(ctx, owner)
		if err != nil || mutable == nil {
			t.Fatalf("attach mutable owner: mutable=%v err=%v", mutable, err)
		}
		upper, err := MutableSQLiteUpper(1 << 20)
		if err != nil {
			t.Fatal(err)
		}
		if err = mutable.Prepare(ctx, c.HashBytes([]byte("admitted sqlite mutation")), upper); err != nil {
			t.Fatalf("prepare bounded SQLite write: %v", err)
		}
		appendMutableTestDB(t, owner, int(mutableStartupHeadroom+(1<<20)))
		if err = ledger.Close(); err != nil {
			t.Fatal(err)
		}

		reopened := openTestLedger(t, authority, limits, ledgerID)
		defer reopened.Close()
		if _, err = reopened.AttachMutableSQLite(ctx, owner); err == nil {
			t.Fatal("reopen accepted SQLite growth beyond the durable standby")
		} else {
			var typed *c.Error
			if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
				t.Fatalf("overrun was not rejected as a quota failure: %v", err)
			}
		}
		capacity, err := reopened.Capacity(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if capacity.Unknown != 1 || capacity.OutstandingBytes != upper || capacity.RetainedBytes == 0 {
			t.Fatalf("failed reconciliation dropped the old receipt or uncertainty: %+v", capacity)
		}
		if _, err = reopened.Reserve(ctx, Request{Writer: "ordinary", Family: "AFTER_OVERRUN", RequestDigest: c.HashBytes([]byte("ordinary payload")), TargetRoot: filepath.Join(parent, "other"), UpperBytes: 8 << 20, UpperEntries: 1, Class: Payload}); err == nil {
			t.Fatal("ordinary payload reservation consumed unresolved SQLite standby capacity")
		}
	})
}

func TestMutableSQLiteFamilyNameDoesNotHideOrdinaryEnclosingReservation(t *testing.T) {
	authority, parent := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()
	target := filepath.Join(parent, "ordinary-writer")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	allocation, err := ledger.Reserve(context.Background(), Request{Writer: "ordinary", Family: mutableBaseFamily, RequestDigest: c.HashBytes([]byte("ordinary family text")), TargetRoot: target, UpperBytes: 16 << 10, UpperEntries: 2, Class: Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(target, "nested")
	parentAllocation, covered, err := ledger.EnclosingReservation(context.Background(), nested)
	if err != nil || !covered || parentAllocation.ID != allocation.ID {
		t.Fatalf("ordinary family-name spoof was hidden from compound admission: covered=%v allocation=%+v err=%v", covered, parentAllocation, err)
	}
}
