package resource

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/diskguard"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func partitionTestLimits() Limits {
	limits := DefaultLimits()
	limits.LowWaterBytes = 0
	return limits
}

func writePartitionTestFile(t *testing.T, root *os.Root, name string, data []byte) {
	t.Helper()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("create partition member %s: %v", name, err)
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		t.Fatalf("write partition member %s: %v", name, err)
	}
}

func createPartitionTestOwner(t *testing.T, owner string) {
	t.Helper()
	root, err := os.OpenRoot(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(root); err == nil {
		writePartitionTestFile(t, root, "owner.lock", []byte("lock"))
		writePartitionTestFile(t, root, "state.sqlite", make([]byte, 4096))
		writePartitionTestFile(t, root, "runtime-instance.json", []byte("{}\n"))
		if err = os.Mkdir(filepath.Join(owner, "artifacts"), 0700); err == nil {
			var artifacts *os.Root
			artifacts, err = os.OpenRoot(filepath.Join(owner, "artifacts"))
			if err == nil {
				err = fileguard.Private(artifacts)
				err = errors.Join(err, artifacts.Close())
			}
		}
	}
	if err = errors.Join(err, root.Close()); err != nil {
		t.Fatalf("create partition test owner: %v", err)
	}
	disk, err := diskguard.Open(owner)
	if err != nil {
		t.Fatalf("initialize partition control reserve: %v", err)
	}
	if err = disk.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertPartitionUpper(t *testing.T, ledger *Ledger, upper uint64) {
	t.Helper()
	capacity, err := ledger.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != upper || capacity.OutstandingItems != 16384 || capacity.RetainedBytes != 0 || capacity.RetainedEntries != 0 {
		t.Fatalf("unresolved partition lost or reduced its broad charge: %+v", capacity)
	}
}

func TestOwnerPartitionCrashReopenSettlesOnceAndAllowsFreshGCReservation(t *testing.T) {
	ctx := context.Background()
	authority, base := testAuthority(t)
	limits := partitionTestLimits()
	const upperBytes = uint64(128 << 20)
	rootPath := filepath.Join(base, "partition-report")
	ownerPath := filepath.Join(rootPath, "owner")
	ledger := openTestLedger(t, authority, limits, "")
	defer func() { _ = ledger.Close() }()
	allocation, err := ledger.Reserve(ctx, Request{Writer: "evaluation", Family: "EVALUATION_REPORT", RequestDigest: c.HashBytes([]byte("partition-crash-reopen")), TargetRoot: rootPath, UpperBytes: upperBytes, UpperEntries: 16384, Class: Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.PreparePartition(ctx, EvaluationReportPartition); err != nil {
		t.Fatalf("prepare evaluator partition: %v", err)
	}
	ledgerID := ledger.LedgerID
	assertPartitionUpper(t, ledger, upperBytes)
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, limits, ledgerID)
	assertPartitionUpper(t, ledger, upperBytes)
	allocation = Allocation{ledger: ledger, ID: allocation.ID, targetRoot: rootPath}
	if err = allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(root); err == nil {
		err = allocation.BindPartitionRoot(ctx)
	}
	if err = errors.Join(err, root.Close()); err != nil {
		t.Fatalf("bind evaluator report root: %v", err)
	}
	assertPartitionUpper(t, ledger, upperBytes)
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, limits, ledgerID)
	allocation = Allocation{ledger: ledger, ID: allocation.ID, targetRoot: rootPath}
	if err = allocation.BindPartitionRoot(ctx); err != nil {
		t.Fatalf("recover bound report root: %v", err)
	}
	if err = os.Mkdir(ownerPath, 0700); err != nil {
		t.Fatal(err)
	}
	ownerRoot, err := os.OpenRoot(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(ownerRoot); err == nil {
		err = allocation.BindPartitionOwner(ctx, ownerPath)
	}
	if err = errors.Join(err, ownerRoot.Close()); err != nil {
		t.Fatalf("bind evaluator owner: %v", err)
	}
	assertPartitionUpper(t, ledger, upperBytes)
	if err = os.WriteFile(filepath.Join(rootPath, "summary.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	createPartitionTestOwner(t, ownerPath)
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, limits, ledgerID)
	assertPartitionUpper(t, ledger, upperBytes)
	allocation = Allocation{ledger: ledger, ID: allocation.ID, targetRoot: rootPath}
	if err = allocation.CommitPartition(ctx); err != nil {
		t.Fatalf("settle recovered owner partition: %v", err)
	}
	committed, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if committed.OutstandingBytes >= upperBytes || committed.RetainedBytes+committed.OutstandingBytes >= upperBytes || committed.OutstandingItems >= 16384 {
		t.Fatalf("partition settlement retained the broad charge or double-counted its subsets: %+v", committed)
	}
	if err = allocation.CommitPartition(ctx); err != nil {
		t.Fatalf("exact partition settlement retry: %v", err)
	}
	retried, err := ledger.Capacity(ctx)
	if err != nil || retried != committed {
		t.Fatalf("partition retry changed its exact capacity receipt: before=%+v after=%+v err=%v", committed, retried, err)
	}
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("validate committed evaluator owner: %v", err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, limits, ledgerID)
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("validate reopened evaluator owner: %v", err)
	}
	reopenedCapacity, err := ledger.Capacity(ctx)
	if err != nil || reopenedCapacity != committed {
		t.Fatalf("opening the owner charged its bytes twice: before=%+v after=%+v err=%v", committed, reopenedCapacity, err)
	}

	gcTarget := filepath.Join(ownerPath, "maintenance", "gc", "new-object")
	gc, err := ledger.Reserve(ctx, Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes([]byte("new bounded GC target")), TargetRoot: gcTarget, UpperBytes: 64 << 10, UpperEntries: 16, Class: Control})
	if err != nil {
		t.Fatalf("new bounded GC output under reopened owner was not admitted: %v", err)
	}
	if err = gc.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(gcTarget, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(gcTarget, "intent.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pendingCapacity, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gcID := gc.ID
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, limits, ledgerID)
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("reopened owner rejected files inside the exact outstanding GC subtree: %v", err)
	}
	resumedCapacity, err := ledger.Capacity(ctx)
	if err != nil || resumedCapacity != pendingCapacity {
		t.Fatalf("owner reopen released or duplicated the uncertain GC upper charge: before=%+v after=%+v err=%v", pendingCapacity, resumedCapacity, err)
	}
	gc = Allocation{ledger: ledger, ID: gcID, targetRoot: gcTarget}
	gcRoot, err := os.OpenRoot(gcTarget)
	if err != nil {
		t.Fatal(err)
	}
	gcInventory, measureErr := Measure(gcRoot, true)
	closeErr := gcRoot.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = gc.Commit(ctx, gcInventory); err != nil {
		t.Fatalf("commit bounded GC output: %v", err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, limits, ledgerID)
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("validate owner with separately retained GC output: %v", err)
	}
	archiveRoot := filepath.Join(ownerPath, "artifacts")
	if _, err = ledger.Reserve(ctx, Request{Writer: "untrusted", Family: "IMMUTABLE", RequestDigest: c.HashBytes([]byte("wrong archive writer")), TargetRoot: archiveRoot, UpperBytes: 4096, UpperEntries: 1, Class: Payload}); err == nil {
		t.Fatal("archive partition admitted a writer outside its exact immutable family")
	}
	archiveChild := filepath.Join(archiveRoot, "new-exact-member")
	archiveMatch, err := ledger.ReserveOrMatch(ctx, Request{Writer: "artifact", Family: "IMMUTABLE", RequestDigest: c.HashBytes([]byte("exact archive child")), TargetRoot: archiveChild, UpperBytes: 16 << 10, UpperEntries: 4, Class: Payload})
	if err != nil || archiveMatch.Scope != ExactReservationScope {
		t.Fatalf("retained ARCHIVE_BASE subset acted as ancestor authority: match=%+v err=%v", archiveMatch, err)
	}
	if err = archiveMatch.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(archiveChild, 0700); err != nil {
		t.Fatal(err)
	}
	archiveChildRoot, err := os.OpenRoot(archiveChild)
	if err != nil {
		t.Fatal(err)
	}
	writePartitionTestFile(t, archiveChildRoot, "manifest.json", []byte("{}\n"))
	archiveChildInventory, measureErr := Measure(archiveChildRoot, true)
	closeErr = archiveChildRoot.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = archiveMatch.Allocation.Commit(ctx, archiveChildInventory); err != nil {
		t.Fatalf("commit exact new archive member: %v", err)
	}
	pendingTarget := filepath.Join(ownerPath, "maintenance", "gc", "pending-free")
	pendingMatch, err := ledger.ReserveOrMatch(ctx, Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes([]byte("partition pending free")), TargetRoot: pendingTarget, UpperBytes: 64 << 10, UpperEntries: 8, Class: Control})
	if err != nil || pendingMatch.Scope != ExactReservationScope {
		t.Fatalf("reserve exact partition GC target for pending free: match=%+v err=%v", pendingMatch, err)
	}
	if err = pendingMatch.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(pendingTarget, 0700); err != nil {
		t.Fatal(err)
	}
	pendingRoot, err := os.OpenRoot(pendingTarget)
	if err != nil {
		t.Fatal(err)
	}
	writePartitionTestFile(t, pendingRoot, "receipt.json", []byte("completed receipt"))
	pendingInventory, measureErr := Measure(pendingRoot, true)
	closeErr = pendingRoot.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = pendingMatch.Allocation.Commit(ctx, pendingInventory); err != nil {
		t.Fatalf("commit exact partition GC target: %v", err)
	}
	file := pendingInventory.Files[0]
	pendingRoot, err = os.OpenRoot(pendingTarget)
	if err != nil {
		t.Fatal(err)
	}
	intent := freeIntent{Path: file.Path, Digest: file.Digest, Identity: file.Identity, Logical: file.LogicalBytes, Allocated: file.Allocated, Charged: file.ChargedBytes, Before: math.MaxInt64, Volume: pendingInventory.VolumeIdentity, Root: pendingInventory.RootIdentity}
	err = ledger.beginFreeIntent(ctx, pendingMatch.Allocation.ID, intent)
	if err == nil {
		err = pendingRoot.Remove(filepath.FromSlash(file.Path))
	}
	if err == nil {
		err = fileguard.SyncDirectory(pendingRoot, ".")
	}
	if err = errors.Join(err, pendingRoot.Close()); err != nil {
		t.Fatal(err)
	}
	pendingCapacity, err = ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pendingCapacity.Unknown != 1 || pendingCapacity.RetainedBytes == 0 || pendingCapacity.RetainedEntries == 0 {
		t.Fatalf("exact pending free did not remain charged and UNKNOWN: %+v", pendingCapacity)
	}
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("owner partition rejected the exact missing FREE_PENDING member: %v", err)
	}
	ledgerID = ledger.LedgerID
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger = openTestLedger(t, authority, limits, ledgerID)
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("reopened owner rejected the exact pending-free retry state: %v", err)
	}
	resumedCapacity, capacityErr := ledger.Capacity(ctx)
	if capacityErr != nil || resumedCapacity != pendingCapacity {
		t.Fatalf("pending-free reopen changed retained or UNKNOWN charge: before=%+v after=%+v err=%v", pendingCapacity, resumedCapacity, capacityErr)
	}
	if _, err = ledger.Reserve(ctx, Request{Writer: "agent", Family: "BAD_REPORT_OVERLAP", RequestDigest: c.HashBytes([]byte("overlaps report remainder")), TargetRoot: filepath.Join(rootPath, "unowned-report-file"), UpperBytes: 4096, UpperEntries: 1, Class: Payload}); err == nil {
		t.Fatal("fresh reservation overlapped the retained report remainder")
	}
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("owner validation rejected the new exact archive receipt: %v", err)
	}
	rogueTarget := filepath.Join(ownerPath, "maintenance", "gc", "untracked-missing")
	rogueMatch, err := ledger.ReserveOrMatch(ctx, Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes([]byte("untracked missing file")), TargetRoot: rogueTarget, UpperBytes: 64 << 10, UpperEntries: 8, Class: Control})
	if err != nil {
		t.Fatal(err)
	}
	if err = rogueMatch.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(rogueTarget, 0700); err != nil {
		t.Fatal(err)
	}
	rogueRoot, err := os.OpenRoot(rogueTarget)
	if err != nil {
		t.Fatal(err)
	}
	writePartitionTestFile(t, rogueRoot, "untracked.json", []byte("missing without a free intent"))
	rogueInventory, measureErr := Measure(rogueRoot, true)
	closeErr = rogueRoot.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = rogueMatch.Allocation.Commit(ctx, rogueInventory); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(rogueTarget, "untracked.json")); err != nil {
		t.Fatal(err)
	}
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err == nil {
		t.Fatal("owner partition accepted a missing retained file without its exact pending-free intent")
	}
	afterRejectedMissing, err := ledger.Capacity(ctx)
	if err != nil || afterRejectedMissing.Unknown != 1 || afterRejectedMissing.RetainedBytes <= pendingCapacity.RetainedBytes {
		t.Fatalf("rejected unmatched disappearance changed or lost conservative charges: before=%+v after=%+v err=%v", pendingCapacity, afterRejectedMissing, err)
	}
}

func TestOwnerPartitionBindsOptionalNativeEngineFile(t *testing.T) {
	ctx := context.Background()
	authority, base := testAuthority(t)
	limits := partitionTestLimits()
	ledger := openTestLedger(t, authority, limits, "")
	defer func() { _ = ledger.Close() }()
	if used, err := ledger.NativeEnginePartitionFeatureInUse(ctx); err != nil || used {
		t.Fatalf("fresh physical quota ledger reported native engine partition use: used=%v err=%v", used, err)
	}

	rootPath := filepath.Join(base, "native-partition-report")
	ownerPath := filepath.Join(rootPath, "owner")
	allocation, err := ledger.Reserve(ctx, Request{Writer: "evaluation", Family: "EVALUATION_REPORT", RequestDigest: c.HashBytes([]byte("native-engine-partition")), TargetRoot: rootPath, UpperBytes: 128 << 20, UpperEntries: 16384, Class: Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.PreparePartition(ctx, EvaluationReportPartition); err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(root); err == nil {
		err = allocation.BindPartitionRoot(ctx)
	}
	if err = errors.Join(err, root.Close()); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(ownerPath, 0700); err != nil {
		t.Fatal(err)
	}
	ownerRoot, err := os.OpenRoot(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(ownerRoot); err == nil {
		err = allocation.BindPartitionOwner(ctx, ownerPath)
	}
	if err = errors.Join(err, ownerRoot.Close()); err != nil {
		t.Fatal(err)
	}
	createPartitionTestOwner(t, ownerPath)
	ownerRoot, err = os.OpenRoot(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	writePartitionTestFile(t, ownerRoot, "native-engine.json", make([]byte, 1025))
	if err = ownerRoot.Close(); err != nil {
		t.Fatal(err)
	}
	root, err = os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	writePartitionTestFile(t, root, "summary.json", []byte("{}\n"))
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	if err = allocation.CommitPartition(ctx); err == nil {
		t.Fatal("oversized native engine receipt entered the bootstrap owner pin")
	}
	pending, err := ledger.Capacity(ctx)
	if err != nil || pending.OutstandingBytes != 128<<20 || pending.RetainedBytes != 0 {
		t.Fatalf("oversized native engine receipt released its broad bootstrap charge: %+v err=%v", pending, err)
	}
	if err = os.WriteFile(filepath.Join(ownerPath, "native-engine.json"), []byte("{\"engine\":\"bound\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = allocation.CommitPartition(ctx); err != nil {
		t.Fatalf("settle owner with the initial native engine receipt: %v", err)
	}
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("validate owner with its exact optional native engine file: %v", err)
	}
	if used, err := ledger.NativeEnginePartitionFeatureInUse(ctx); err != nil || !used {
		t.Fatalf("durable native engine partition feature was not detected: used=%v err=%v", used, err)
	}
	committed, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(ownerPath, "native-engine.json"), []byte("{\"engine\":\"changed\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = ledger.ValidateOwnerPartition(ctx, ownerPath); err == nil {
		t.Fatal("owner partition accepted a changed native engine receipt")
	}
	after, err := ledger.Capacity(ctx)
	if err != nil || after != committed {
		t.Fatalf("rejecting changed native engine bytes altered their conservative charge: before=%+v after=%+v err=%v", committed, after, err)
	}
}

func TestRetainedReopenKeepsPurgedBytesChargedWithoutGenericPresenceRequirement(t *testing.T) {
	ctx := context.Background()
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer func() { _ = ledger.Close() }()
	owner := filepath.Join(base, "purge-owner")
	output := filepath.Join(owner, "tasks", "purged-task")
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "content.bin"), []byte("private content"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(output)
	if err != nil {
		t.Fatal(err)
	}
	inventory, measureErr := Measure(root, true)
	closeErr := root.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = ledger.ImportRetained(ctx, "privacy", "TASK_CONTENT", inventory); err != nil {
		t.Fatal(err)
	}
	charged, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(output, "content.bin")); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(output); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Dir(output)); err != nil {
		t.Fatal(err)
	}
	root, err = os.OpenRoot(owner)
	if err != nil {
		t.Fatal(err)
	}
	current, measureErr := Measure(root, true)
	closeErr = root.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	tx, err := ledger.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	validationErr := validateRetainedReopen(ctx, tx, current)
	rollbackErr := tx.Rollback()
	if err = errors.Join(validationErr, rollbackErr); err != nil {
		t.Fatalf("generic privacy-purged receipt should remain conservatively charged and reopenable: %v", err)
	}
	after, err := ledger.Capacity(ctx)
	if err != nil || after != charged || after.RetainedBytes == 0 {
		t.Fatalf("purging bytes released or changed their retained charge: before=%+v after=%+v err=%v", charged, after, err)
	}
}

func TestOwnerPartitionKeepsBootstrapChargeWhenOutputExceedsUpperBound(t *testing.T) {
	ctx := context.Background()
	authority, base := testAuthority(t)
	limits := partitionTestLimits()
	ledger := openTestLedger(t, authority, limits, "")
	defer func() { _ = ledger.Close() }()
	ownerPath := filepath.Join(base, "undersized-owner")
	allocation, err := ledger.Reserve(ctx, Request{Writer: "evaluation", Family: "EVALUATION_REPORT", RequestDigest: c.HashBytes([]byte("under-reserved-partition")), TargetRoot: ownerPath, UpperBytes: directoryCharge, UpperEntries: 1, Class: Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.PreparePartition(ctx, IndependentOwnerPartition); err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(ownerPath, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(root); err == nil {
		err = allocation.BindPartitionRoot(ctx)
	}
	if err = errors.Join(err, root.Close()); err != nil {
		t.Fatal(err)
	}
	createPartitionTestOwner(t, ownerPath)
	if err = allocation.CommitPartition(ctx); err == nil {
		t.Fatal("partition settlement accepted output beyond the bootstrap upper bound")
	}
	capacity, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingBytes != directoryCharge || capacity.OutstandingItems != 1 || capacity.RetainedBytes != 0 || capacity.RetainedEntries != 0 {
		t.Fatalf("over-bound partition did not preserve the original broad charge: %+v", capacity)
	}
}
