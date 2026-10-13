package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/resource"
)

// Support and delivery-preview exports must charge the durable shared
// physical ledger before their first write, settle the charge as retained on
// completion, and keep it visible across owner reopen.
func TestSupportAndDeliveryPreviewReserveSharedPhysicalQuota(t *testing.T) {
	ctx := context.Background()
	s, options, source := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	if _, err := s.Run(ctx, options.TaskID); err != nil {
		t.Fatal(err)
	}
	if s.PhysicalQuota == nil {
		t.Fatal("session owner has no shared physical quota ledger")
	}
	if err := os.WriteFile(filepath.Join(source, "user.txt"), []byte("independent user file"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	support := filepath.Join(t.TempDir(), "support")
	if _, err = s.ExportSupport(ctx, support); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(support, "support.json")); err != nil {
		t.Fatal("support export missing its final marker", err)
	}
	preview := filepath.Join(t.TempDir(), "preview")
	manifest, err := s.DeliveryPreview(ctx, options.TaskID, preview)
	if err != nil || manifest.Preview.Status != "READY" {
		t.Fatal("delivery preview not ready", manifest, err)
	}
	if _, err = os.Stat(filepath.Join(preview, "manifest.json")); err != nil {
		t.Fatal("delivery preview missing its final marker", err)
	}
	after, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.RetainedBytes <= before.RetainedBytes || after.RetainedEntries <= before.RetainedEntries {
		t.Fatal("operator exports did not settle durable retained physical charge", before, after)
	}
	directory := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RetainedBytes < after.RetainedBytes {
		t.Fatal("retained physical charge did not survive owner reopen", after, persisted)
	}
}

// Filling the shared payload entry budget must make both writers refuse to
// create any output while the failed writer charge stays visible (fail
// closed, no silent release).
func TestSupportAndDeliveryPreviewFailClosedOnExhaustedPhysicalQuota(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	if _, err := s.Run(ctx, options.TaskID); err != nil {
		t.Fatal(err)
	}
	if s.PhysicalQuota == nil {
		t.Fatal("session owner has no shared physical quota ledger")
	}
	// Exhaust the payload entry budget with tiny never-committed allocations;
	// entry admission has no environmental free-space dependency. Halve the
	// chunk after each denial until a single-entry reservation is refused.
	exhaustTarget := filepath.Join(t.TempDir(), "exhaust")
	for chunk := uint64(1 << 16); chunk > 0; {
		_, err := s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "test", Family: "QUOTA_EXHAUST", RequestDigest: c.HashBytes([]byte("exhaust")), TargetRoot: exhaustTarget, UpperBytes: 4096, UpperEntries: chunk, Class: resource.Payload})
		if err == nil {
			continue
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
			t.Fatal(err)
		}
		chunk /= 2
	}
	capacity, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capacity.OutstandingItems == 0 {
		t.Fatal("test setup failed to exhaust the shared physical quota", capacity)
	}
	support := filepath.Join(t.TempDir(), "support")
	_, err = s.ExportSupport(ctx, support)
	if err == nil {
		t.Fatal("support export ignored an exhausted shared physical quota")
	}
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(support); !os.IsNotExist(statErr) {
		t.Fatal("failed support export created output before its reservation")
	}
	preview := filepath.Join(t.TempDir(), "preview")
	if _, err = s.DeliveryPreview(ctx, options.TaskID, preview); err == nil {
		t.Fatal("delivery preview ignored an exhausted shared physical quota")
	}
	if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(preview); !os.IsNotExist(statErr) {
		t.Fatal("failed delivery preview created output before its reservation")
	}
	settled, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settled.OutstandingItems < capacity.OutstandingItems {
		t.Fatal("failed writers silently released the exhausted physical charge", capacity, settled)
	}
}

// GC control records must be charged to the durable control budget before the
// first write, an exact retry must not double-charge the retained records, and
// a crashed attempt's allocation must be adopted instead of duplicated.
func TestCollectGarbageReservesControlQuotaAndAdoptsCrashedAttempts(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive.PutBytes(opts.TaskID, []byte("unreferenced interrupted write")); err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command := GCCommand{CommandID: "gc-quota", Plan: plan}
	before, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed attempt: its allocation is already WRITING over the
	// exact record directory this command will use.
	intent, err := c.CanonicalV1(command)
	if err != nil {
		t.Fatal(err)
	}
	crashedTarget := filepath.Join(s.directory, "maintenance", "gc", c.HashBytes([]byte(command.CommandID)))
	crashed, err := s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes(intent), TargetRoot: crashedTarget, UpperBytes: 1 << 20, UpperEntries: 16, Class: resource.Control})
	if err != nil {
		t.Fatal(err)
	}
	if err = crashed.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	reserved, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Allocations != before.Allocations+1 {
		t.Fatal("crashed GC allocation was not registered", before, reserved)
	}
	first, err := s.CollectGarbage(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Allocations != reserved.Allocations {
		t.Fatal("GC duplicated the crashed attempt's physical allocation instead of adopting it", reserved, adopted)
	}
	if adopted.OutstandingItems >= reserved.OutstandingItems || adopted.RetainedBytes <= before.RetainedBytes {
		t.Fatal("GC control records did not settle from outstanding to retained", before, reserved, adopted)
	}
	second, err := s.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("GC retry not idempotent", err)
	}
	retried, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if retried != adopted {
		t.Fatal("exact GC retry double-charged retained control records", adopted, retried)
	}
}

// Filling the control budget must refuse GC admission before the durable
// intent exists while the failed charge stays visible.
func TestCollectGarbageFailsClosedOnExhaustedControlQuota(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive.PutBytes(opts.TaskID, []byte("unreferenced interrupted write")); err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exhaustTarget := filepath.Join(t.TempDir(), "exhaust")
	for chunk := uint64(512); chunk > 0; {
		_, err := s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "test", Family: "CONTROL_EXHAUST", RequestDigest: c.HashBytes([]byte("exhaust")), TargetRoot: exhaustTarget, UpperBytes: 4096, UpperEntries: chunk, Class: resource.Control})
		if err == nil {
			continue
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
			t.Fatal(err)
		}
		chunk /= 2
	}
	command := GCCommand{CommandID: "gc-denied", Plan: plan}
	_, err = s.CollectGarbage(ctx, command)
	if err == nil {
		t.Fatal("GC ignored an exhausted control budget")
	}
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(s.directory, "maintenance", "gc", c.HashBytes([]byte(command.CommandID)), "intent.json")); !os.IsNotExist(statErr) {
		t.Fatal("denied GC wrote its durable intent before reservation")
	}
}
