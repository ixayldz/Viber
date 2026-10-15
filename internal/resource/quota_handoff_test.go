package resource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type testHandoff struct {
	allocation Allocation
	stage      string
	final      string
	upperBytes uint64
}

func prepareTestHandoff(t *testing.T, ledger *Ledger, parent, suffix string) testHandoff {
	t.Helper()
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatalf("create handoff parent: %v", err)
	}
	stage := filepath.Join(parent, ".stage-"+suffix)
	final := filepath.Join(parent, "published-"+suffix)
	const upperBytes = uint64(512 << 10)
	allocation, err := ledger.Reserve(context.Background(), Request{
		Writer: "handoff-test", Family: "TEST", RequestDigest: c.HashBytes([]byte("handoff-" + suffix)),
		TargetRoot: stage, UpperBytes: upperBytes, UpperEntries: 32, Class: Payload,
	})
	if err != nil {
		t.Fatalf("reserve handoff stage: %v", err)
	}
	if err = allocation.PrepareHandoff(context.Background(), final); err != nil {
		t.Fatalf("prepare handoff: %v", err)
	}
	if err = allocation.Begin(context.Background()); err != nil {
		t.Fatalf("begin handoff writer: %v", err)
	}
	if err = os.Mkdir(stage, 0700); err != nil {
		t.Fatalf("create handoff stage: %v", err)
	}
	if err = allocation.BindHandoffStage(context.Background()); err != nil {
		t.Fatalf("bind handoff stage: %v", err)
	}
	return testHandoff{allocation: allocation, stage: stage, final: final, upperBytes: upperBytes}
}

func writeTestHandoffPayload(t *testing.T, stage string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stage, "payload.txt"), []byte("durable handoff payload"), 0600); err != nil {
		t.Fatalf("write handoff payload: %v", err)
	}
}

func TestHandoffCrashAfterRenameReopensChargedAndCommitsExactRetry(t *testing.T) {
	authority, base := testAuthority(t)
	ctx := context.Background()
	ledger := openTestLedger(t, authority, smallLimits(), "")
	handoff := prepareTestHandoff(t, ledger, filepath.Join(base, "handoff-recovery"), "recovery")
	writeTestHandoffPayload(t, handoff.stage)

	root, err := os.OpenRoot(handoff.stage)
	if err != nil {
		t.Fatal(err)
	}
	inventory, measureErr := Measure(root, true)
	closeErr := root.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatalf("measure pending handoff: %v", err)
	}
	if err = handoff.allocation.Commit(ctx, inventory); err == nil {
		t.Fatal("ordinary allocation commit settled a prepared handoff")
	}

	intent, found, err := loadHandoffIntent(ledger.db, handoff.allocation.ID)
	if err != nil || !found {
		t.Fatalf("load bound handoff intent: found=%v err=%v", found, err)
	}
	if err = moveHandoffRoot(intent); err != nil {
		t.Fatalf("simulate crash point after durable rename: %v", err)
	}
	capacity, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != handoff.upperBytes || capacity.RetainedBytes != 0 || capacity.Allocations != 1 {
		t.Fatalf("rename settled or lost the WRITING charge before receipt commit: %+v", capacity)
	}
	ledgerID := ledger.LedgerID
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestLedger(t, authority, smallLimits(), ledgerID)
	defer reopened.Close()
	retry, state, err := reopened.HandoffAllocation(ctx, handoff.allocation.ID)
	if err != nil || state != HandoffStageBound {
		t.Fatalf("reopen did not preserve the bound handoff: state=%q err=%v", state, err)
	}
	capacity, err = reopened.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != handoff.upperBytes || capacity.RetainedBytes != 0 {
		t.Fatalf("reopen changed the unfinished handoff charge: %+v", capacity)
	}

	settled, err := retry.CommitHandoff(ctx)
	if err != nil {
		t.Fatalf("commit recovered handoff: %v", err)
	}
	if filepath.Clean(settled.targetRoot) != filepath.Clean(handoff.final) {
		t.Fatalf("settled allocation points at %q, want final root %q", settled.targetRoot, handoff.final)
	}
	if _, err = os.Lstat(handoff.stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage path remains after settlement: %v", err)
	}
	if _, err = os.Lstat(handoff.final); err != nil {
		t.Fatalf("final path missing after settlement: %v", err)
	}
	capacity, err = reopened.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != 0 || capacity.RetainedBytes == 0 || capacity.Allocations != 1 {
		t.Fatalf("settled handoff did not replace one reservation with one receipt: %+v", capacity)
	}

	retried, err := retry.CommitHandoff(ctx)
	if err != nil {
		t.Fatalf("exact committed handoff retry failed: %v", err)
	}
	if filepath.Clean(retried.targetRoot) != filepath.Clean(handoff.final) || retried.ID != settled.ID {
		t.Fatalf("exact retry returned a different allocation: first=%+v retry=%+v", settled, retried)
	}
	capacityAfterRetry, err := reopened.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capacityAccounting(t, capacityAfterRetry) != capacityAccounting(t, capacity) {
		t.Fatalf("exact retry changed quota accounting: before=%+v after=%+v", capacity, capacityAfterRetry)
	}
}

func TestHandoffRejectsForeignFinalInodeOnOpen(t *testing.T) {
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	handoff := prepareTestHandoff(t, ledger, filepath.Join(base, "handoff-foreign"), "foreign")
	intent, found, err := loadHandoffIntent(ledger.db, handoff.allocation.ID)
	if err != nil || !found {
		t.Fatalf("load bound handoff intent: found=%v err=%v", found, err)
	}
	if err = moveHandoffRoot(intent); err != nil {
		t.Fatalf("move bound handoff to final path: %v", err)
	}
	parked := handoff.final + ".bound"
	if err = os.Rename(handoff.final, parked); err != nil {
		t.Fatalf("park bound final inode: %v", err)
	}
	if err = os.Mkdir(handoff.final, 0700); err != nil {
		t.Fatalf("install foreign final inode: %v", err)
	}
	capacity, err := ledger.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != handoff.upperBytes || capacity.RetainedBytes != 0 {
		t.Fatalf("foreign final path altered the pending charge: %+v", capacity)
	}
	ledgerID := ledger.LedgerID
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenPinned(authority, smallLimits(), ledgerID)
	if err == nil {
		reopened.Close()
		t.Fatal("OpenPinned accepted a foreign inode at the bound final path")
	}
}

func TestHandoffRejectsReplacedParentOnOpen(t *testing.T) {
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	parent := filepath.Join(base, "handoff-parent-replaced")
	handoff := prepareTestHandoff(t, ledger, parent, "parent")
	intent, found, err := loadHandoffIntent(ledger.db, handoff.allocation.ID)
	if err != nil || !found {
		t.Fatalf("load bound handoff intent: found=%v err=%v", found, err)
	}
	if err = moveHandoffRoot(intent); err != nil {
		t.Fatalf("move bound handoff to final path: %v", err)
	}
	parked := parent + ".bound"
	if err = os.Rename(parent, parked); err != nil {
		t.Fatalf("park bound handoff parent: %v", err)
	}
	if err = os.Mkdir(parent, 0700); err != nil {
		t.Fatalf("install foreign parent inode: %v", err)
	}
	ledgerID := ledger.LedgerID
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenPinned(authority, smallLimits(), ledgerID)
	if err == nil {
		reopened.Close()
		t.Fatal("OpenPinned accepted a replaced parent for a bound handoff")
	}
}
