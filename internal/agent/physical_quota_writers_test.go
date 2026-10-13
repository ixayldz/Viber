package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
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

// A begun support reservation must not be silently reused when its report
// snapshot changes across owner reopen. PrivacyMetadata.AvailableBytes is an
// observation, so this test records whether it remains byte-stable and checks
// that any changed report is rejected before an output root is created.
func TestSupportRetryAfterReopenUsesExactOriginalReportEnvelope(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	original, err := s.Support(ctx)
	if err != nil {
		t.Fatal(err)
	}
	originalRaw, err := c.CanonicalV1(original)
	if err != nil {
		t.Fatal(err)
	}
	upperBytes, upperEntries, err := exportPhysicalEstimate(nil, originalRaw)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "support-retry")
	request := resource.Request{Writer: "agent", Family: "SUPPORT", RequestDigest: c.HashBytes(originalRaw), TargetRoot: output, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload}
	allocation, err := s.PhysicalQuota.Reserve(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(ctx); err != nil {
		t.Fatal(err)
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
	current, err := reopened.Support(ctx)
	if err != nil {
		t.Fatal(err)
	}
	currentRaw, err := c.CanonicalV1(current)
	if err != nil {
		t.Fatal(err)
	}
	digestChanged := c.HashBytes(currentRaw) != request.RequestDigest
	if original.PrivacyMetadata != nil && current.PrivacyMetadata != nil {
		t.Logf("reopened support available bytes: %d -> %d; report digest changed=%v", original.PrivacyMetadata.AvailableBytes, current.PrivacyMetadata.AvailableBytes, digestChanged)
	} else {
		t.Logf("reopened support report digest changed=%v", digestChanged)
	}
	before, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reopened.ExportSupport(ctx, output)
	if digestChanged {
		var typed *c.Error
		if err == nil || !errors.As(err, &typed) || typed.Code != c.StaleAuthority {
			t.Fatalf("changed support report did not fail closed against its original allocation: %v", err)
		}
		if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
			t.Fatal("changed report created output before rejecting the retry")
		}
		after, capacityErr := reopened.PhysicalQuota.Capacity(ctx)
		if capacityErr != nil || after.Allocations != before.Allocations || after.OutstandingItems != before.OutstandingItems {
			t.Fatalf("changed retry replaced or released its original charge: before=%+v after=%+v err=%v", before, after, capacityErr)
		}
		return
	}
	if err != nil {
		t.Fatal("unchanged support report did not resume its exact WRITING allocation", err)
	}
	stored, err := os.ReadFile(filepath.Join(output, "support.json"))
	if err != nil || c.HashBytes(stored) != request.RequestDigest {
		t.Fatal("resumed support output differs from its reserved original report", err)
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
	upperBytes, upperEntries, err := gcPhysicalEstimate(intent, len(plan.Objects))
	if err != nil {
		t.Fatal(err)
	}
	crashed, err := s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes(intent), TargetRoot: crashedTarget, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Control})
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

// A process may die after the durable completion record is published but
// before Commit changes its matching allocation from WRITING to RETAINED.
// Reopening the owner and retrying must settle that exact charge, then validate
// the retained receipt on later retries without charging it again.
func TestCollectGarbageSettlesCompletedWritingAllocationAfterReopen(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
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
	command := GCCommand{CommandID: "gc-complete-before-commit", Plan: plan}
	commandRaw, err := c.CanonicalV1(command)
	if err != nil {
		t.Fatal(err)
	}
	upperBytes, upperEntries, err := gcPhysicalEstimate(commandRaw, len(plan.Objects))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(s.directory, "maintenance", "gc", c.HashBytes([]byte(command.CommandID)))
	allocation, err := s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes(commandRaw), TargetRoot: target, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Control})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := freshPrivate(target)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Publish(root, "intent.json", commandRaw); err != nil {
		root.Close()
		t.Fatal(err)
	}
	completed := GCResult{SchemaVersion: 1, CommandID: command.CommandID, PlanDigest: plan.Digest, Objects: []GCObjectReceipt{}}
	for _, object := range plan.Objects {
		removed, removeErr := s.Archive.RemoveOrphan(object)
		if removeErr != nil {
			root.Close()
			t.Fatal(removeErr)
		}
		receipt := GCObjectReceipt{Object: object, Status: "ALREADY_ABSENT_AFTER_INTENT"}
		if removed {
			receipt.Status = "UNLINKED_AND_DIRECTORY_SYNCED"
			completed.BytesRemoved += object.Size
		}
		receiptRaw, encodeErr := c.CanonicalV1(receipt)
		if encodeErr != nil {
			root.Close()
			t.Fatal(encodeErr)
		}
		if publishErr := fileguard.Publish(root, c.HashBytes([]byte(object.Path))+".json", receiptRaw); publishErr != nil {
			root.Close()
			t.Fatal(publishErr)
		}
		completed.Objects = append(completed.Objects, receipt)
	}
	completeRaw, err := c.CanonicalV1(completed)
	if err == nil {
		err = fileguard.Publish(root, "complete.json", completeRaw)
	}
	closeErr := root.Close()
	if err = errors.Join(err, closeErr); err != nil {
		t.Fatal(err)
	}
	reserved, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.OutstandingItems == 0 {
		t.Fatal("pre-commit crash setup has no outstanding allocation", reserved)
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
	beforeRetry, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := reopened.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(first, completed) {
		t.Fatal("completed retry did not recover exact result", first, err, completed)
	}
	settled, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Allocations != beforeRetry.Allocations || settled.OutstandingItems >= beforeRetry.OutstandingItems || settled.RetainedBytes <= 0 {
		t.Fatal("completed retry did not settle the existing charge", beforeRetry, settled)
	}
	second, err := reopened.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(second, completed) {
		t.Fatal("retained GC retry did not validate idempotently", second, err, completed)
	}
	retried, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if retried != settled {
		t.Fatal("retained GC retry changed shared quota capacity", settled, retried)
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
