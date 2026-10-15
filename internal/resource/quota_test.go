package resource

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

type testGenesis struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Directory     string `json:"directory"`
	PhysicalRoot  string `json:"physical_root"`
}

// SQLite checkpoint/reopen changes observed DB/WAL/SHM allocation without
// changing a durable payload reservation. Check the fixed allocator envelope
// separately, then compare every accounting field without its volatile usage.
func capacityAccounting(t *testing.T, value Capacity) Capacity {
	t.Helper()
	if value.SelfOverheadBytes == 0 || value.SelfOverheadUsedBytes == 0 || value.SelfOverheadUsedBytes > value.SelfOverheadBytes || value.SelfOverheadUsedEntries == 0 || value.SelfOverheadUsedEntries > value.SelfOverheadEntries {
		t.Fatalf("allocator measurement exceeded or lost its fixed envelope: %+v", value)
	}
	value.SelfOverheadUsedBytes = 0
	value.SelfOverheadUsedEntries = 0
	return value
}

func testAuthority(t *testing.T) (Authority, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "authority")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatalf("create quota authority test root: %T %#v", err, err)
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("canonicalize quota authority test root %q: %T %#v", directory, err, err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		t.Fatalf("absolutize quota authority test root: %T %#v", err, err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("open quota authority test root: %T %#v", err, err)
	}
	if err = fileguard.Private(root); err != nil {
		root.Close()
		t.Fatalf("set private quota authority ACL: %T %#v", err, err)
	}
	physicalRoot, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		root.Close()
		t.Fatalf("identify quota authority test root: %T %#v", err, err)
	}
	id := c.HashBytes([]byte(t.Name()))[:32]
	genesis, err := c.CanonicalV1(testGenesis{SchemaVersion: 1, ID: id, Directory: canonical, PhysicalRoot: physicalRoot})
	if err != nil {
		root.Close()
		t.Fatalf("encode quota authority test genesis: %T %#v", err, err)
	}
	file, err := root.OpenFile("authority.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = file.Write(genesis)
		if err == nil {
			err = file.Sync()
		}
		err = errors.Join(err, file.Close())
	}
	if err == nil {
		err = fileguard.SyncDirectory(root, ".")
	}
	closeErr := root.Close()
	if err = errors.Join(err, closeErr); err != nil {
		t.Fatalf("write quota authority test genesis: %T %#v", err, err)
	}
	return Authority{ID: id, Directory: canonical, PhysicalRoot: physicalRoot}, filepath.Dir(canonical)
}

func smallLimits() Limits {
	return Limits{Bytes: 1 << 20, Entries: 128, ControlBytes: 128 << 10, ControlEntries: 8, LowWaterBytes: 0}
}

func openTestLedger(t *testing.T, authority Authority, limits Limits, expected string) *Ledger {
	t.Helper()
	ledger, err := OpenPinned(authority, limits, expected)
	if err != nil {
		t.Fatalf("open pinned quota ledger: %T %#v", err, err)
	}
	return ledger
}

func TestSharedReservationRaceAndInterruptedWriterStayCharged(t *testing.T) {
	authority, base := testAuthority(t)
	limits := smallLimits()
	first := openTestLedger(t, authority, limits, "")
	second := openTestLedger(t, authority, limits, first.LedgerID)
	defer second.Close()
	for _, name := range []string{"one", "two"} {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for i, ledger := range []*Ledger{first, second} {
		wait.Add(1)
		go func(i int, ledger *Ledger) {
			defer wait.Done()
			<-start
			_, err := ledger.Reserve(context.Background(), Request{Writer: "test", Family: "RACE", RequestDigest: c.HashBytes([]byte{byte(i)}), TargetRoot: filepath.Join(base, []string{"one", "two"}[i]), UpperBytes: 600 << 10, UpperEntries: 1, Class: Payload})
			results <- err
		}(i, ledger)
	}
	close(start)
	wait.Wait()
	close(results)
	accepted := 0
	denied := 0
	for err := range results {
		if err == nil {
			accepted++
			continue
		}
		var typed *c.Error
		if errors.As(err, &typed) && typed.Code == c.BudgetLimitReached {
			denied++
			continue
		}
		t.Fatalf("unexpected racing reservation error: %v", err)
	}
	if accepted != 1 || denied != 1 {
		t.Fatalf("race admitted %d and denied %d; want one each", accepted, denied)
	}
	capacity, err := first.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != 600<<10 || capacity.Unknown != 1 {
		t.Fatalf("unexpected outstanding race charge: %+v", capacity)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestLedger(t, authority, limits, first.LedgerID)
	defer reopened.Close()
	capacity, err = reopened.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != 600<<10 || capacity.Unknown != 1 {
		t.Fatalf("interrupted reservation did not survive reopen: %+v", capacity)
	}
	if _, err = reopened.Reserve(context.Background(), Request{Writer: "test", Family: "OVER", RequestDigest: c.HashBytes([]byte("over")), TargetRoot: filepath.Join(base, "two"), UpperBytes: 400 << 10, UpperEntries: 1, Class: Payload}); err == nil {
		t.Fatal("ordinary reservation consumed bytes already held by interrupted writer")
	}
}

func TestOuterReservationCoversNestedWriterAndChargesOneReceipt(t *testing.T) {
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()
	output := filepath.Join(base, "report")
	allocation, err := ledger.Reserve(context.Background(), Request{Writer: "evaluation", Family: "REPORT", RequestDigest: c.HashBytes([]byte("report")), TargetRoot: output, UpperBytes: 512 << 10, UpperEntries: 32, Class: Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(output, "owner", "artifacts")
	if err = os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	parent, covered, err := ledger.EnclosingReservation(context.Background(), nested)
	if err != nil || !covered || !parent.Covers(nested) || parent.ID != allocation.ID {
		t.Fatalf("nested writer did not find its unique outer allocation: covered=%v parent=%+v err=%v", covered, parent, err)
	}
	root, err := os.OpenRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(nested, "owner.db"), bytes.Repeat([]byte("x"), 16<<10), 0600); err != nil {
		root.Close()
		t.Fatal(err)
	}
	if err = ledger.ImportRetained(context.Background(), "artifact", "NESTED_BASE", mustMeasure(t, root)); err != nil {
		root.Close()
		t.Fatal(err)
	}
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	capacity, err := ledger.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != 512<<10 || capacity.RetainedBytes != 0 || capacity.Allocations != 1 {
		t.Fatalf("nested inventory was double counted alongside outer reservation: %+v", capacity)
	}
	outer, err := os.OpenRoot(output)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := Measure(outer, true)
	if err == nil {
		err = allocation.Commit(context.Background(), inventory)
	}
	closeErr := outer.Close()
	if err = errors.Join(err, closeErr); err != nil {
		t.Fatal(err)
	}
	capacity, err = ledger.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != 0 || capacity.RetainedBytes != inventory.ChargedBytes || capacity.Allocations != 1 {
		t.Fatalf("outer measured receipt did not replace its one reservation: %+v inventory=%+v", capacity, inventory)
	}
}

func TestReserveOrMatchRequiresExactRetryEnvelopeAndSameClassAncestor(t *testing.T) {
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()
	ctx := context.Background()
	target := filepath.Join(base, "retry-root")
	request := Request{Writer: "export", Family: "SUPPORT", RequestDigest: c.HashBytes([]byte("report-v1")), TargetRoot: target, UpperBytes: 128 << 10, UpperEntries: 8, Class: Payload}
	first, err := ledger.ReserveOrMatch(ctx, request)
	if err != nil || !first.Created || first.Scope != ExactReservationScope || first.Status != "RESERVED" {
		t.Fatalf("first reservation did not create an exact envelope: %+v err=%v", first, err)
	}
	reserved, err := ledger.ReserveOrMatch(ctx, request)
	if err != nil || reserved.Created || reserved.Allocation.ID != first.Allocation.ID || reserved.Status != "RESERVED" {
		t.Fatalf("reserved retry did not reuse its allocation: %+v err=%v", reserved, err)
	}
	if err = first.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	writing, err := ledger.ReserveOrMatch(ctx, request)
	if err != nil || writing.Created || writing.Allocation.ID != first.Allocation.ID || writing.Status != "WRITING" {
		t.Fatalf("writing retry did not adopt its exact allocation: %+v err=%v", writing, err)
	}
	changed := []Request{
		func() Request { r := request; r.Writer = "other"; return r }(),
		func() Request { r := request; r.Family = "DELIVERY_PREVIEW"; return r }(),
		func() Request { r := request; r.RequestDigest = c.HashBytes([]byte("report-v2")); return r }(),
		func() Request { r := request; r.UpperBytes++; return r }(),
		func() Request { r := request; r.UpperEntries++; return r }(),
		func() Request { r := request; r.Class = Control; return r }(),
	}
	for _, retry := range changed {
		if _, err = ledger.ReserveOrMatch(ctx, retry); err == nil {
			t.Fatalf("changed exact retry envelope was accepted: %+v", retry)
		} else {
			var typed *c.Error
			if !errors.As(err, &typed) || typed.Code != c.StaleAuthority {
				t.Fatalf("changed exact retry returned the wrong error: %v", err)
			}
		}
	}
	nested := request
	nested.TargetRoot = filepath.Join(target, "nested", "child")
	nested.Family = "DELIVERY_PREVIEW"
	nested.RequestDigest = c.HashBytes([]byte("nested"))
	nested.UpperBytes = 64 << 10
	nested.UpperEntries = 4
	if _, err = ledger.ReserveOrMatch(ctx, nested); err == nil {
		t.Fatal("nested writer inherited arbitrary same-class ancestor without a family grant")
	}
	nested.AncestorFamily = "SUPPORT"
	covered, err := ledger.ReserveOrMatch(ctx, nested)
	if err != nil || covered.Scope != AncestorReservationScope || covered.Allocation.ID != first.Allocation.ID || covered.Status != "WRITING" {
		t.Fatalf("same-class nested writer did not receive outer coverage: %+v err=%v", covered, err)
	}
	nested.UpperBytes = request.UpperBytes + 1
	if _, err = ledger.ReserveOrMatch(ctx, nested); err == nil {
		t.Fatal("nested writer exceeded its enclosing byte envelope")
	}
	nested.UpperBytes = 64 << 10
	nested.Class = Control
	if _, err = ledger.ReserveOrMatch(ctx, nested); err == nil {
		t.Fatal("nested writer with a different class inherited outer coverage")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StaleAuthority {
			t.Fatalf("wrong-class nested writer returned the wrong error: %v", err)
		}
	}
}

func TestReserveOrMatchWithIntentReturnsOriginalAndErasesPayloadAfterCommit(t *testing.T) {
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()
	ctx := context.Background()
	target := filepath.Join(base, "intent-root")
	binding := []byte(`{"schema_version":1,"snapshot":"stable"}`)
	original := []byte(`{"available_bytes":123,"tasks":[]}`)
	observed := []byte(`{"available_bytes":999,"tasks":[]}`)
	request := Request{Writer: "export", Family: "SUPPORT", RequestDigest: c.HashBytes(original), TargetRoot: target, UpperBytes: 128 << 10, UpperEntries: 8, Class: Payload}
	first, saved, err := ledger.ReserveOrMatchWithIntent(ctx, request, binding, original)
	if err != nil || !first.Created || first.Status != "RESERVED" || !bytes.Equal(saved, original) {
		t.Fatalf("first intent did not atomically save its original payload: %+v saved=%q err=%v", first, saved, err)
	}

	retryRequest := request
	retryRequest.RequestDigest = c.HashBytes(observed)
	retry, saved, err := ledger.ReserveOrMatchWithIntent(ctx, retryRequest, binding, observed)
	if err != nil || retry.Created || retry.Allocation.ID != first.Allocation.ID || !bytes.Equal(saved, original) || retry.RequestDigest != c.HashBytes(original) {
		t.Fatalf("retry did not recover its originally reserved payload: %+v saved=%q err=%v", retry, saved, err)
	}
	changedBinding := []byte(`{"schema_version":1,"snapshot":"changed"}`)
	if _, _, err = ledger.ReserveOrMatchWithIntent(ctx, retryRequest, changedBinding, observed); err == nil {
		t.Fatal("changed stable binding inherited the saved support intent")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StaleAuthority {
			t.Fatalf("changed stable binding returned the wrong error: %v", err)
		}
	}

	if err = first.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(target, "support.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	inventory, measureErr := Measure(root, true)
	closeErr := root.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = first.Allocation.Commit(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	retained, saved, err := ledger.ReserveOrMatchWithIntent(ctx, retryRequest, binding, observed)
	if err != nil || retained.Status != "RETAINED" || retained.RequestDigest != c.HashBytes(original) || len(saved) != 0 {
		t.Fatalf("settled intent did not retain only its binding and original digest: %+v saved=%q err=%v", retained, saved, err)
	}
	if err = retained.Allocation.ValidateRetained(ctx, inventory); err != nil {
		t.Fatal("retained inventory failed validation", err)
	}
}

func mustMeasure(t *testing.T, root *os.Root) Inventory {
	t.Helper()
	inventory, err := Measure(root, true)
	if err != nil {
		t.Fatal(err)
	}
	return inventory
}

func TestQuotaAnchorMustRemainCanonical(t *testing.T) {
	authority, _ := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	ledgerID := ledger.LedgerID
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	anchorPath := filepath.Join(authority.Directory, "physical-quota.anchor")
	file, err := os.OpenFile(anchorPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write([]byte(" "))
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenPinned(authority, smallLimits(), ledgerID); err == nil {
		reopened.Close()
		t.Fatal("noncanonical physical quota anchor was accepted")
	}
}

func TestOrdinaryPayloadCannotConsumeControlReserve(t *testing.T) {
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer func() { _ = ledger.Close() }()
	for _, name := range []string{"payload", "control"} {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ledger.Reserve(context.Background(), Request{Writer: "payload", Family: "DATA", RequestDigest: c.HashBytes([]byte("payload")), TargetRoot: filepath.Join(base, "payload"), UpperBytes: 900 << 10, UpperEntries: 1, Class: Payload}); err == nil {
		t.Fatal("ordinary payload borrowed the protected control reserve")
	}
	if _, err := ledger.Reserve(context.Background(), Request{Writer: "control", Family: "CHECKPOINT", RequestDigest: c.HashBytes([]byte("control")), TargetRoot: filepath.Join(base, "control"), UpperBytes: 64 << 10, UpperEntries: 1, Class: Control}); err != nil {
		t.Fatalf("control reservation could not use protected capacity: %v", err)
	}
}

func TestExactFreeReceiptRequiredBeforeCapacityReturns(t *testing.T) {
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer func() { _ = ledger.Close() }()
	target := filepath.Join(base, "output")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	allocation, err := ledger.Reserve(context.Background(), Request{Writer: "test", Family: "FILE", RequestDigest: c.HashBytes([]byte("file")), TargetRoot: target, UpperBytes: 512 << 10, UpperEntries: 2, Class: Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	content := make([]byte, 128<<10)
	for i := range content {
		content[i] = byte(i)
	}
	f, err := root.OpenFile("payload.bin", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.SyncDirectory(root, "."); err != nil {
		t.Fatal(err)
	}
	inventory, err := MeasureFileInventory(root, "payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Commit(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	if removed, err := ledger.ReleaseFile(context.Background(), root, "payload.bin", c.HashBytes([]byte("wrong")), int64(len(content)), 1<<20); err != nil || removed {
		t.Fatalf("wrong digest released retained capacity: removed=%v err=%v", removed, err)
	}
	if _, err = root.Lstat("payload.bin"); err != nil {
		t.Fatalf("wrong receipt removed payload: %v", err)
	}
	before, err := ledger.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.RetainedBytes == 0 || before.RetainedEntries != 1 {
		t.Fatalf("committed output was not retained: %+v", before)
	}
	freeBefore, err := fileguard.AvailableBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	file := inventory.Files[0]
	intent := freeIntent{Path: file.Path, Digest: file.Digest, Identity: file.Identity, Logical: file.LogicalBytes, Allocated: file.Allocated, Charged: file.ChargedBytes, Before: freeBefore, Volume: inventory.VolumeIdentity, Root: inventory.RootIdentity}
	if err = ledger.beginFreeIntent(context.Background(), allocation.ID, intent); err != nil {
		t.Fatal(err)
	}
	if err = root.Remove("payload.bin"); err != nil {
		t.Fatal(err)
	}
	if err = fileguard.SyncDirectory(root, "."); err != nil {
		t.Fatal(err)
	}
	reconciled, err := ledger.ReconcileFreePending(context.Background())
	if err != nil {
		t.Fatalf("reconcile exact pending free after unlink: %+v %v", reconciled, err)
	}
	interrupted, err := ledger.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.Unknown == 1 {
		if interrupted.RetainedBytes == 0 || reconciled.ReclaimedFiles > 0 {
			t.Fatalf("unproven pending free receipt did not remain charged: %+v result=%+v", interrupted, reconciled)
		}
	} else if interrupted.Unknown != 0 || interrupted.RetainedBytes != 0 {
		t.Fatalf("proven pending free receipt did not return capacity: %+v result=%+v", interrupted, reconciled)
	}
	ledgerID := ledger.LedgerID
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, smallLimits(), ledgerID)
	defer ledger.Close()
	recovered, err := ledger.ReconcileFreePending(context.Background())
	if err != nil {
		t.Fatalf("recovered exact measured free receipt failed: result=%+v err=%v", recovered, err)
	}
	if _, err = root.Lstat("payload.bin"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released output still exists: %v", err)
	}
	after, err := ledger.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Unknown == 1 {
		// A shared CI volume can allocate unrelated files between the persisted
		// intent and readback. Namespace absence is not proof of returned disk
		// capacity. The required outcome is conservative charging, never a
		// forged free receipt just to make recovery appear successful.
		if after.RetainedBytes != before.RetainedBytes || after.RetainedEntries != before.RetainedEntries || recovered.ReclaimedFiles != 0 {
			t.Fatalf("unproven free changed retained capacity: before=%+v after=%+v", before, after)
		}
		t.Log("PHYSICAL_FREE_PENDING: exact missing member stays fully charged until volume free proof is available")
	} else if after.Unknown != 0 || after.RetainedBytes != 0 || after.RetainedEntries != 0 {
		t.Fatalf("proven exact free receipt did not return capacity: %+v", after)
	}
}

func TestFreePendingRecoveryCancelsWhenExactFileStillExists(t *testing.T) {
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()
	target := filepath.Join(base, "cancel")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	allocation, err := ledger.Reserve(context.Background(), Request{Writer: "test", Family: "FILE", RequestDigest: c.HashBytes([]byte("cancel")), TargetRoot: target, UpperBytes: 128 << 10, UpperEntries: 1, Class: Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	content := []byte("exact pending member")
	if err = os.WriteFile(filepath.Join(target, "member.bin"), content, 0600); err != nil {
		t.Fatal(err)
	}
	inventory, err := MeasureFileInventory(root, "member.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Commit(context.Background(), inventory); err != nil {
		t.Fatal(err)
	}
	file := inventory.Files[0]
	available, err := fileguard.AvailableBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	intent := freeIntent{Path: file.Path, Digest: file.Digest, Identity: file.Identity, Logical: file.LogicalBytes, Allocated: file.Allocated, Charged: file.ChargedBytes, Before: available, Volume: inventory.VolumeIdentity, Root: inventory.RootIdentity}
	if err = ledger.beginFreeIntent(context.Background(), allocation.ID, intent); err != nil {
		t.Fatal(err)
	}
	ledgerID := ledger.LedgerID
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, smallLimits(), ledgerID)
	defer ledger.Close()
	result, err := ledger.ReconcileFreePending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.PendingFiles != 0 || result.ReclaimedFiles != 0 {
		t.Fatalf("startup did not cancel interrupted delete for exact extant file: %+v", result)
	}
	if _, found, err := ledger.readFreeIntent(context.Background(), allocation.ID); err != nil || found {
		t.Fatalf("startup left a free intent for the unchanged file: found=%v err=%v", found, err)
	}
	capacity, err := ledger.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.Unknown != 0 || capacity.RetainedEntries != inventory.Entries || capacity.RetainedBytes != inventory.ChargedBytes {
		t.Fatalf("cancelled intent did not restore retained accounting: %+v", capacity)
	}
}
