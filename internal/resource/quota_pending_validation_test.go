package resource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestValidateFreePendingAllowsOnlyExactIntentMemberMissing(t *testing.T) {
	ctx := context.Background()
	authority, base := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()
	target := filepath.Join(base, "pending-validation")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	allocation, err := ledger.Reserve(ctx, Request{Writer: "test", Family: "PENDING_VALIDATION", RequestDigest: c.HashBytes([]byte("pending-validation")), TargetRoot: target, UpperBytes: 64 << 10, UpperEntries: 4, Class: Control})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Publish(root, "a-member.json", []byte("pending member")); err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Publish(root, "z-member.json", []byte("unchanged data")); err != nil {
		t.Fatal(err)
	}
	saved, err := Measure(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Commit(ctx, saved); err != nil {
		t.Fatal(err)
	}
	member := saved.Files[0]
	available, err := fileguard.AvailableBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	intent := freeIntent{Path: member.Path, Digest: member.Digest, Identity: member.Identity, Logical: member.LogicalBytes, Allocated: member.Allocated, Charged: member.ChargedBytes, Before: available, Volume: saved.VolumeIdentity, Root: saved.RootIdentity}
	if err = ledger.beginFreeIntent(ctx, allocation.ID, intent); err != nil {
		t.Fatal(err)
	}
	if err = allocation.ValidateFreePending(ctx, saved); err != nil {
		t.Fatalf("exact retained inventory was rejected while member remains: %v", err)
	}
	if err = root.Remove(filepath.FromSlash(member.Path)); err != nil {
		t.Fatal(err)
	}
	if err = fileguard.SyncDirectory(root, "."); err != nil {
		t.Fatal(err)
	}
	withoutMember, err := Measure(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.ValidateFreePending(ctx, withoutMember); err != nil {
		t.Fatalf("inventory missing the exact pending member was rejected: %v", err)
	}
	if err = os.WriteFile(filepath.Join(target, "z-member.json"), []byte("changed bytes!"), 0600); err != nil {
		t.Fatal(err)
	}
	tampered, err := Measure(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.ValidateFreePending(ctx, tampered); err == nil {
		t.Fatal("pending inventory accepted a changed non-intent member")
	}
	if _, found, err := ledger.readFreeIntent(ctx, allocation.ID); err != nil || !found {
		t.Fatalf("read-only validation resolved or lost the pending free intent: found=%v err=%v", found, err)
	}
}
