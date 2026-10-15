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
	beforeIntent, found, err := reopened.PhysicalQuota.FindReservationIntent(ctx, output, "agent", "SUPPORT")
	if err != nil || !found || beforeIntent.Match.Status != "WRITING" || beforeIntent.Match.RequestDigest != request.RequestDigest ||
		beforeIntent.Match.UpperBytes != request.UpperBytes || beforeIntent.Match.UpperEntries != request.UpperEntries || !bytes.Equal(beforeIntent.Binding, bindingRaw) {
		t.Fatalf("reopened support writer did not retain its exact original intent: intent=%+v found=%v err=%v", beforeIntent, found, err)
	}
	metadataBefore, _, err := reopened.PhysicalQuota.MetadataCatalogSnapshot(ctx)
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
	if err != nil {
		t.Fatal(err)
	}
	afterIntent, found, err := reopened.PhysicalQuota.FindReservationIntent(ctx, output, "agent", "SUPPORT")
	if err != nil || !found || afterIntent.Match.Status != "RETAINED" || afterIntent.Match.Allocation.ID != beforeIntent.Match.Allocation.ID ||
		afterIntent.Match.RequestDigest != beforeIntent.Match.RequestDigest || afterIntent.Match.UpperBytes != beforeIntent.Match.UpperBytes ||
		afterIntent.Match.UpperEntries != beforeIntent.Match.UpperEntries || !bytes.Equal(afterIntent.Binding, beforeIntent.Binding) {
		t.Fatalf("support retry did not settle its original writer allocation: before=%+v after=%+v found=%v err=%v", beforeIntent, afterIntent, found, err)
	}
	// Registering this one managed output adds one measured immutable privacy
	// metadata receipt. The original support reservation itself must settle
	// in place, with no duplicate writer charge.
	metadataCharge := addedPrivacyMetadataCharge(t, reopened, metadataBefore)
	if after.Allocations != before.Allocations+1 || after.MetadataBytes != before.MetadataBytes+metadataCharge || after.MetadataEntries != before.MetadataEntries+1 ||
		after.OutstandingBytes >= before.OutstandingBytes || after.OutstandingItems >= before.OutstandingItems ||
		after.RetainedBytes-after.MetadataBytes <= before.RetainedBytes-before.MetadataBytes ||
		after.RetainedEntries-after.MetadataEntries <= before.RetainedEntries-before.MetadataEntries {
		t.Fatalf("support retry changed physical quota beyond its one immutable privacy metadata receipt: before=%+v after=%+v", before, after)
	}
	repeated, err := reopened.ExportSupport(ctx, output)
	if err != nil {
		t.Fatalf("completed exact support retry was not idempotent: %v", err)
	}
	repeatedRaw, err := c.CanonicalV1(repeated)
	if err != nil || !bytes.Equal(repeatedRaw, originalRaw) {
		t.Fatalf("completed support retry changed the saved report: %v", err)
	}
	afterRepeat, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil || afterRepeat != after {
		t.Fatalf("completed support retry charged physical metadata or payload twice: first=%+v repeat=%+v err=%v", after, afterRepeat, err)
	}
	repeatedIntent, found, err := reopened.PhysicalQuota.FindReservationIntent(ctx, output, "agent", "SUPPORT")
	if err != nil || !found || repeatedIntent.Match.Allocation.ID != afterIntent.Match.Allocation.ID ||
		repeatedIntent.Match.RequestDigest != afterIntent.Match.RequestDigest || repeatedIntent.Match.UpperBytes != afterIntent.Match.UpperBytes ||
		repeatedIntent.Match.UpperEntries != afterIntent.Match.UpperEntries || !bytes.Equal(repeatedIntent.Binding, afterIntent.Binding) {
		t.Fatalf("completed support retry lost its exact writer allocation: original=%+v repeated=%+v found=%v err=%v", afterIntent, repeatedIntent, found, err)
	}
}

// Once the managed-copy record has durably bound the output root, an exact
// synced Publish temp is sufficient evidence to replay the original payload.
// The test stops after the temp file is synced and before support.json exists.
func TestSupportRetryRecoversExactManagedPublishTemp(t *testing.T) {
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
	output, err := fileguard.ResolveProspective(filepath.Join(t.TempDir(), "support-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	request := resource.Request{Writer: "agent", Family: "SUPPORT", RequestDigest: c.HashBytes(originalRaw), TargetRoot: output, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload}
	reservation, saved, err := s.PhysicalQuota.ReserveOrMatchWithIntent(ctx, request, bindingRaw, originalRaw)
	if err != nil || !reservation.Created || !bytes.Equal(saved, originalRaw) {
		t.Fatal("support reservation did not preserve its initial payload", err)
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
	const partialName = ".publish-0123456789abcdef0123456789abcdef"
	var partial *os.File
	if err == nil {
		partial, err = root.OpenFile(partialName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
	if err == nil {
		_, err = partial.Write(originalRaw)
	}
	if err == nil {
		err = partial.Sync()
	}
	if partial != nil {
		err = errors.Join(err, partial.Close())
	}
	closeRootErr := root.Close()
	if err = errors.Join(err, closeRootErr); err != nil {
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
	before, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := reopened.ExportSupport(ctx, output)
	if err != nil {
		t.Fatal("retry did not recover the exact synced publication temp", err)
	}
	stored, err := os.ReadFile(filepath.Join(output, "support.json"))
	if err != nil || !bytes.Equal(stored, originalRaw) {
		t.Fatal("recovered support output differs from the original intent payload", err)
	}
	if _, err = os.Stat(filepath.Join(output, partialName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("exact interrupted temp was not retired", err)
	}
	exportedRaw, err := c.CanonicalV1(exported)
	if err != nil || !bytes.Equal(exportedRaw, originalRaw) {
		t.Fatal("support retry returned a regenerated report instead of its original intent", err)
	}
	retained, found, err := reopened.PhysicalQuota.FindReservationIntent(ctx, output, "agent", "SUPPORT")
	if err != nil || !found || retained.Match.Status != "RETAINED" || retained.Match.Allocation.ID != reservation.Allocation.ID || retained.Match.RequestDigest != request.RequestDigest || retained.Match.UpperBytes != uint64(upperBytes) || retained.Match.UpperEntries != upperEntries || !bytes.Equal(retained.Binding, bindingRaw) {
		t.Fatalf("support retry did not settle the exact original allocation: %+v found=%v err=%v", retained.Match, found, err)
	}
	retainedRoot, err := os.OpenRoot(output)
	if err != nil {
		t.Fatal(err)
	}
	retainedInventory, measureErr := resource.Measure(retainedRoot, true)
	closeErr := retainedRoot.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err = retained.Match.Allocation.ValidateRetained(ctx, retainedInventory); err != nil {
		t.Fatal("support retry's current files differ from its exact retained receipt", err)
	}
	after, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil || after.Allocations != before.Allocations {
		t.Fatalf("support recovery changed its original charge: before=%+v after=%+v err=%v", before, after, err)
	}

	t.Run("refuses_truncated_temp_without_settling", func(t *testing.T) {
		fixture := prepareSupportPublishTempRetry(t, true, false)
		assertSupportTempRetryRefusedWithoutMutation(t, fixture)
	})
	t.Run("refuses_exact_temp_with_foreign_sibling", func(t *testing.T) {
		fixture := prepareSupportPublishTempRetry(t, false, true)
		assertSupportTempRetryRefusedWithoutMutation(t, fixture)
	})
}

type supportPublishRetryFixture struct {
	session      *Session
	output       string
	original     []byte
	partial      []byte
	partialName  string
	foreignName  string
	foreign      []byte
	request      resource.Request
	binding      []byte
	allocationID string
}

func prepareSupportPublishTempRetry(t *testing.T, truncate, addForeign bool) supportPublishRetryFixture {
	t.Helper()
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	sessionOpen := true
	t.Cleanup(func() {
		if sessionOpen {
			_ = s.Close()
		}
	})
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	report, err := s.Support(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original, upperBytes, upperEntries, err := supportExportEnvelope(report)
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := buildSupportExportBinding(ctx, s, report)
	if err != nil {
		t.Fatal(err)
	}
	output, err := fileguard.ResolveProspective(filepath.Join(t.TempDir(), "support-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	request := resource.Request{Writer: "agent", Family: "SUPPORT", RequestDigest: c.HashBytes(original), TargetRoot: output, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload}
	reservation, saved, err := s.PhysicalQuota.ReserveOrMatchWithIntent(ctx, request, binding, original)
	if err != nil || !reservation.Created || !bytes.Equal(saved, original) {
		t.Fatal("support reservation did not preserve its initial payload", err)
	}
	if err = reservation.Allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := freshPrivate(output)
	if err != nil {
		t.Fatal(err)
	}
	rootOpen := true
	defer func() {
		if rootOpen {
			_ = root.Close()
		}
	}()
	tasks, err := supportTasks(ctx, s)
	if err == nil {
		err = s.allocateManagedCopy(ctx, root, "SUPPORT", tasks)
	}
	if err != nil {
		t.Fatal(err)
	}
	const partialName = ".publish-0123456789abcdef0123456789abcdef"
	partialPayload := original
	if truncate {
		partialPayload = append([]byte(nil), original[:len(original)-1]...)
	}
	partial, err := root.OpenFile(partialName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = partial.Write(partialPayload); err == nil {
		err = partial.Sync()
	}
	if closeErr := partial.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	fixture := supportPublishRetryFixture{session: s, output: output, original: original, partial: partialPayload, partialName: partialName, request: request, binding: binding, allocationID: reservation.Allocation.ID}
	if addForeign {
		fixture.foreignName = "foreign.txt"
		fixture.foreign = []byte("preserve foreign content")
		if err = fileguard.Publish(root, fixture.foreignName, fixture.foreign); err != nil {
			t.Fatal(err)
		}
	}
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	rootOpen = false
	directory := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	sessionOpen = false
	reopened, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	fixture.session = reopened
	t.Cleanup(func() { _ = reopened.Close() })
	intent, found, err := reopened.PhysicalQuota.FindReservationIntent(ctx, output, "agent", "SUPPORT")
	if err != nil || !found || intent.Match.Status != "WRITING" || intent.Match.Allocation.ID != fixture.allocationID || intent.Match.RequestDigest != request.RequestDigest || !bytes.Equal(intent.Binding, binding) {
		t.Fatalf("interrupted support reservation changed before retry: %+v found=%v err=%v", intent.Match, found, err)
	}
	return fixture
}

func assertSupportTempRetryRefusedWithoutMutation(t *testing.T, fixture supportPublishRetryFixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := fixture.session.ExportSupport(ctx, fixture.output); err == nil {
		t.Fatal("support retry accepted an unproven partial publication directory")
	}
	partial, err := os.ReadFile(filepath.Join(fixture.output, fixture.partialName))
	if err != nil || !bytes.Equal(partial, fixture.partial) {
		t.Fatal("refused support retry changed its publication temp", err)
	}
	if fixture.foreignName != "" {
		foreign, readErr := os.ReadFile(filepath.Join(fixture.output, fixture.foreignName))
		if readErr != nil || !bytes.Equal(foreign, fixture.foreign) {
			t.Fatal("refused support retry changed the foreign sibling", readErr)
		}
	}
	if _, err = os.Stat(filepath.Join(fixture.output, "support.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused support retry published a completed report", err)
	}
	intent, found, err := fixture.session.PhysicalQuota.FindReservationIntent(ctx, fixture.output, "agent", "SUPPORT")
	if err != nil || !found || intent.Match.Status != "WRITING" || intent.Match.Allocation.ID != fixture.allocationID || intent.Match.RequestDigest != fixture.request.RequestDigest || intent.Match.UpperBytes != fixture.request.UpperBytes || intent.Match.UpperEntries != fixture.request.UpperEntries || !bytes.Equal(intent.Binding, fixture.binding) {
		t.Fatalf("refused support retry settled or changed its original WRITING allocation: %+v found=%v err=%v", intent.Match, found, err)
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
	// Follow ExportSupport's production ordering: resolve the prospective
	// output before creating/registering its rooted directory. Darwin's temp
	// path may use /var as an alias of /private/var; a raw alias is not the
	// canonical path bound into a support intent.
	output, err := fileguard.ResolveProspective(filepath.Join(base, "interrupted"))
	if err != nil {
		t.Fatal(err)
	}
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
	request := resource.Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes(intent), TargetRoot: crashedTarget, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Control}
	crashed, err := s.PhysicalQuota.Reserve(ctx, request)
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
	verifyCurrentAllocation := func() (resource.Inventory, error) {
		match, matchErr := s.PhysicalQuota.ReserveOrMatch(ctx, request)
		if matchErr != nil {
			return resource.Inventory{}, matchErr
		}
		if (match.Status != "RETAINED" && match.Status != "FREE_PENDING") || match.Allocation.ID != crashed.ID || match.RequestDigest != request.RequestDigest || match.UpperBytes != request.UpperBytes || match.UpperEntries != request.UpperEntries {
			return resource.Inventory{}, errors.New("GC retry did not reuse its exact retained or pending-free allocation")
		}
		gcRoot, openErr := os.OpenRoot(crashedTarget)
		if openErr != nil {
			return resource.Inventory{}, openErr
		}
		inventory, measureErr := resource.Measure(gcRoot, true)
		closeErr := gcRoot.Close()
		if measureErr = errors.Join(measureErr, closeErr); measureErr != nil {
			return resource.Inventory{}, measureErr
		}
		if match.Status == "RETAINED" {
			if measureErr = match.Allocation.ValidateRetained(ctx, inventory); measureErr != nil {
				return resource.Inventory{}, measureErr
			}
		} else if measureErr = match.Allocation.ValidateFreePending(ctx, inventory); measureErr != nil {
			return resource.Inventory{}, measureErr
		}
		return inventory, nil
	}
	retainedInventory, err := verifyCurrentAllocation()
	if err != nil {
		t.Fatal("GC control directory differs from its exact retained or pending-free receipt", err)
	}
	second, err := s.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("GC retry not idempotent", err)
	}
	retriedInventory, err := verifyCurrentAllocation()
	if err != nil {
		t.Fatal("GC retry changed its exact retained or pending-free inventory", err)
	}
	if !reflect.DeepEqual(retainedInventory, retriedInventory) {
		t.Fatal("exact GC retry changed the retained control inventory", retainedInventory, retriedInventory)
	}
	retried, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Allocations != adopted.Allocations {
		t.Fatal("exact GC retry created a new physical allocation", adopted, retried)
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
	request := resource.Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes(commandRaw), TargetRoot: target, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Control}
	verifyExactRetainedAllocation := func() (resource.Inventory, resource.ReservationMatch, error) {
		match, matchErr := reopened.PhysicalQuota.ReserveOrMatch(ctx, request)
		if matchErr != nil {
			return resource.Inventory{}, resource.ReservationMatch{}, matchErr
		}
		if match.Scope != resource.ExactReservationScope || match.Status != "RETAINED" && match.Status != "FREE_PENDING" || match.RequestDigest != request.RequestDigest || match.UpperBytes != upperBytes || match.UpperEntries != upperEntries {
			return resource.Inventory{}, resource.ReservationMatch{}, errors.New("retained GC retry changed its exact allocation binding")
		}
		gcRoot, openErr := os.OpenRoot(target)
		if openErr != nil {
			return resource.Inventory{}, resource.ReservationMatch{}, openErr
		}
		inventory, measureErr := resource.Measure(gcRoot, true)
		closeErr := gcRoot.Close()
		if measureErr = errors.Join(measureErr, closeErr); measureErr != nil {
			return resource.Inventory{}, resource.ReservationMatch{}, measureErr
		}
		var validateErr error
		if match.Status == "FREE_PENDING" {
			validateErr = match.Allocation.ValidateFreePending(ctx, inventory)
		} else {
			validateErr = match.Allocation.ValidateRetained(ctx, inventory)
		}
		if validateErr != nil {
			return resource.Inventory{}, resource.ReservationMatch{}, validateErr
		}
		return inventory, match, nil
	}
	retryInventory, retryMatch, err := verifyExactRetainedAllocation()
	if err != nil {
		t.Fatal("retained GC retry no longer matches its exact receipt:", err)
	}
	retried, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil || retried.Allocations != settled.Allocations || retryMatch.Allocation.ID == "" {
		t.Fatal("retained GC retry created a physical allocation", settled, retried, err)
	}
	if afterInventory, afterMatch, verifyErr := verifyExactRetainedAllocation(); verifyErr != nil || afterMatch.Allocation.ID != retryMatch.Allocation.ID || !reflect.DeepEqual(afterInventory, retryInventory) {
		t.Fatal("retained GC retry changed its exact receipt or current files", retryInventory, retryMatch, afterInventory, afterMatch, verifyErr)
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
