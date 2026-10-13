package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// A begun support reservation keeps the original bounded report across owner
// reopen even when its live free-space observation changes.
func TestSupportRetryAfterReopenUsesSavedOriginalReport(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	closed := false
	defer func() {
		if !closed {
			_ = s.Close()
		}
	}()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	original, err := s.Support(ctx)
	if err != nil {
		t.Fatal(err)
	}
	originalRaw, upperBytes, upperEntries, err := supportExportEnvelope(original)
	if err != nil {
		t.Fatal(err)
	}
	_, bindingRaw, err := buildSupportExportBinding(ctx, s, original)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "support-retry")
	request := resource.Request{Writer: "agent", Family: "SUPPORT", RequestDigest: c.HashBytes(originalRaw), TargetRoot: output, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload}
	reservation, saved, err := s.PhysicalQuota.ReserveOrMatchWithIntent(ctx, request, bindingRaw, originalRaw)
	if err != nil {
		t.Fatal(err)
	}
	if !reservation.Created || !bytes.Equal(saved, originalRaw) {
		t.Fatal("support reservation did not save its first payload")
	}
	if err = reservation.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	spaceConsumer := filepath.Join(filepath.Dir(output), "support-space-consumer.bin")
	if err = os.WriteFile(spaceConsumer, make([]byte, 4<<20), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(spaceConsumer)
	directory := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
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
	originalStableDigest, stableErr := supportReportDigest(original)
	if stableErr != nil {
		t.Fatal(stableErr)
	}
	currentStableDigest, stableErr := supportReportDigest(current)
	if stableErr != nil || currentStableDigest != originalStableDigest {
		t.Fatalf("only the free-space observation should vary across reopen: original=%s current=%s err=%v", originalStableDigest, currentStableDigest, stableErr)
	}
	before, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := reopened.ExportSupport(ctx, output)
	if err != nil {
		t.Fatal("support retry did not resume its saved WRITING intent", err)
	}
	stored, err := os.ReadFile(filepath.Join(output, "support.json"))
	if err != nil || c.HashBytes(stored) != request.RequestDigest {
		t.Fatal("resumed support output differs from its reserved original report", err)
	}
	exportedRaw, err := c.CanonicalV1(exported)
	if err != nil || !bytes.Equal(exportedRaw, originalRaw) {
		t.Fatal("resumed support call did not return the original reserved report", err)
	}
	after, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil || after.Allocations != before.Allocations || after.OutstandingItems >= before.OutstandingItems || after.RetainedEntries <= before.RetainedEntries {
		t.Fatalf("support retry changed the original physical charge: before=%+v after=%+v err=%v", before, after, err)
	}
}

// A self-registered copy is the sole privacy-history suffix that an exact
// interrupted support export may adopt. The 9-to-10 transition also changes
// the fresh report's byte estimate, so retry must carry the saved envelope.
func TestSupportRetryOwnRegistrationAcrossSequenceWidthBoundary(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	closed := false
	defer func() {
		if !closed {
			_ = s.Close()
		}
	}()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	var report SupportReport
	var binding supportExportBinding
	for {
		var err error
		report, err = s.Support(ctx)
		if err != nil {
			t.Fatal(err)
		}
		binding, _, err = buildSupportExportBinding(ctx, s, report)
		if err != nil {
			t.Fatal(err)
		}
		if binding.RegistrySequence >= 9 {
			break
		}
		warmup := filepath.Join(base, fmt.Sprintf("warmup-%02d", binding.RegistrySequence))
		if _, err = s.ExportSupport(ctx, warmup); err != nil {
			t.Fatal(err)
		}
	}
	if binding.RegistrySequence != 9 {
		t.Fatalf("expected test to start on sequence 9, got %d", binding.RegistrySequence)
	}
	originalRaw, upperBytes, upperEntries, err := supportExportEnvelope(report)
	if err != nil {
		t.Fatal(err)
	}
	_, bindingRaw, err := buildSupportExportBinding(ctx, s, report)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(base, "interrupted")
	request := resource.Request{Writer: "agent", Family: "SUPPORT", RequestDigest: c.HashBytes(originalRaw), TargetRoot: output, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload}
	reservation, saved, err := s.PhysicalQuota.ReserveOrMatchWithIntent(ctx, request, bindingRaw, originalRaw)
	if err != nil || !reservation.Created || !bytes.Equal(saved, originalRaw) {
		t.Fatal("support reservation did not preserve its initial envelope", err)
	}
	if err = reservation.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := freshPrivate(output)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := supportTasks(ctx, s)
	if err == nil {
		err = s.allocateManagedCopy(ctx, root, "SUPPORT", tasks)
	}
	if err == nil {
		err = fileguard.Publish(root, "support.json", originalRaw)
	}
	if err == nil {
		err = fileguard.Publish(root, "foreign.txt", []byte("preserve foreign content"))
	}
	closeErr := root.Close()
	if err = errors.Join(err, closeErr); err != nil {
		t.Fatal(err)
	}
	directory := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	reopened, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, err := reopened.Support(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, retryUpperBytes, retryUpperEntries, err := supportExportEnvelope(current)
	if err != nil {
		t.Fatal(err)
	}
	if retryUpperBytes == upperBytes && retryUpperEntries == upperEntries {
		t.Fatal("sequence-width boundary did not change the fresh report envelope")
	}
	before, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.ExportSupport(ctx, output); err == nil {
		t.Fatal("support retry adopted foreign content alongside its original report")
	}
	foreign := filepath.Join(output, "foreign.txt")
	if raw, readErr := os.ReadFile(foreign); readErr != nil || string(raw) != "preserve foreign content" {
		t.Fatal("refused retry changed foreign content", readErr)
	}
	after, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil || after.Allocations != before.Allocations || after.OutstandingBytes != before.OutstandingBytes {
		t.Fatal("refused retry altered its original charge", before, after, err)
	}
	// The fixture's external writer removes its own foreign file. Viber never
	// removes or adopts it as part of retry/recovery.
	if err = os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.ExportSupport(ctx, output); err != nil {
		t.Fatal("support retry did not adopt its exact managed-copy suffix using the saved envelope", err)
	}
	stored, err := os.ReadFile(filepath.Join(output, "support.json"))
	if err != nil || c.HashBytes(stored) != c.HashBytes(originalRaw) {
		t.Fatal("support retry did not publish the original intent payload", err)
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
