package resource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestPartitionHandoffLookupRecoversRenameBeforeSettlement(t *testing.T) {
	ctx := context.Background()
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, partitionTestLimits(), "")
	defer func() { _ = ledger.Close() }()
	parent := filepath.Join(base, "restore-handoff")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(parent, ".stage-owner")
	final := filepath.Join(parent, "restored-owner")
	binding := []byte("restore source inode + manifest + destination v1")
	digest := c.HashBytes(binding)
	const upperBytes = uint64(128 << 20)
	match, payload, err := ledger.ReserveOrMatchWithIntent(ctx, Request{Writer: "agent", Family: "RESTORE_OWNER", RequestDigest: digest,
		TargetRoot: stage, UpperBytes: upperBytes, UpperEntries: 16384, Class: Payload}, binding, binding)
	if err != nil || !match.Created || string(payload) != string(binding) {
		t.Fatalf("admit exact restore intent: match=%+v payload=%q err=%v", match, payload, err)
	}
	if err = match.Allocation.PreparePartitionHandoff(ctx, final); err != nil {
		t.Fatal(err)
	}
	if err = match.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err = match.Allocation.BindPartitionHandoffStage(ctx); err != nil {
		t.Fatal(err)
	}
	createPartitionTestOwner(t, stage)
	intent, found, err := loadHandoffIntent(ledger.db, match.Allocation.ID)
	if err != nil || !found {
		t.Fatalf("load bound restore intent: found=%v err=%v", found, err)
	}
	if err = moveHandoffRoot(intent); err != nil {
		t.Fatalf("simulate crash after rename before ledger settlement: %v", err)
	}
	before, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.OutstandingBytes != upperBytes || before.RetainedBytes != 0 || before.Allocations != 1 {
		t.Fatalf("rename changed the conservative reservation: %+v", before)
	}
	recovered, state, found, err := ledger.FindPartitionHandoff(ctx, final, digest)
	if err != nil || !found || state != HandoffStageBound || recovered.Allocation.ID != match.Allocation.ID || recovered.Status != "WRITING" {
		t.Fatalf("find exact renamed restore handoff: match=%+v state=%q found=%v err=%v", recovered, state, found, err)
	}
	if _, _, _, err = ledger.FindPartitionHandoff(ctx, final, c.HashBytes([]byte("foreign restore intent"))); err == nil {
		t.Fatal("foreign source binding inherited the persisted destination handoff")
	}
	settled, err := recovered.Allocation.CommitPartitionHandoff(ctx)
	if err != nil {
		t.Fatalf("settle exact renamed restore handoff: %v", err)
	}
	if filepath.Clean(settled.targetRoot) != filepath.Clean(final) {
		t.Fatalf("settled allocation root=%q want %q", settled.targetRoot, final)
	}
	if err = ledger.ValidateOwnerPartition(ctx, final); err != nil {
		t.Fatalf("validate committed restore owner: %v", err)
	}
	after, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.OutstandingBytes >= before.OutstandingBytes || after.OutstandingItems >= before.OutstandingItems || after.RetainedBytes <= before.RetainedBytes || after.Allocations <= before.Allocations {
		t.Fatalf("restore settlement did not replace the broad reservation with typed receipts: %+v", after)
	}
	committed, state, found, err := ledger.FindPartitionHandoff(ctx, final, digest)
	if err != nil || !found || state != HandoffCommitted || committed.Status != "RETAINED" || committed.Allocation.ID != match.Allocation.ID {
		t.Fatalf("find committed restore handoff: match=%+v state=%q found=%v err=%v", committed, state, found, err)
	}
	saved, intentFound, err := ledger.FindReservationIntent(ctx, final, "agent", "RESTORE_OWNER")
	if err != nil || !intentFound || saved.Match.Allocation.ID != match.Allocation.ID || string(saved.Binding) != string(binding) {
		t.Fatalf("committed restore lost its immutable exact source binding: intent=%+v found=%v err=%v", saved, intentFound, err)
	}
	if _, err = committed.Allocation.CommitPartitionHandoff(ctx); err != nil {
		t.Fatalf("exact committed retry: %v", err)
	}
	retried, err := ledger.Capacity(ctx)
	if err != nil || retried != after {
		t.Fatalf("committed retry changed quota accounting: before=%+v after=%+v err=%v", after, retried, err)
	}
	if _, _, found, err = ledger.FindPartitionHandoff(ctx, filepath.Join(parent, "foreign-owner"), digest); err != nil || found {
		t.Fatalf("unrelated target unexpectedly found a handoff: found=%v err=%v", found, err)
	}
	if _, statErr := os.Lstat(stage); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stage path remains after settlement: %v", statErr)
	}
}
