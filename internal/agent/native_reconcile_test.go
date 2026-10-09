package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
)

func unknownNativeBoundary(t *testing.T) (*Session, c.TaskState, Document) {
	t.Helper()
	call := model.Call{ID: "native-lost", Name: "check_run", Arguments: json.RawMessage(`{"check_id":"registered"}`)}
	s, opts := checkFixture(t, "golang@sha256:"+strings.Repeat("a", 64), []Turn{{Calls: []model.Call{call}, UsageKnown: true, InputTokens: 2, OutputTokens: 2}})
	state, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.transition(context.Background(), state, c.Running, "", "test durable admission")
	if err != nil {
		t.Fatal(err)
	}
	doc.Messages = append(doc.Messages, model.Message{Role: "assistant", Calls: []model.Call{call}})
	doc.Budget.ToolCalls++
	doc.Pending = &Pending{ID: call.ID, Kind: "NATIVE_TOOL", Candidate: doc.Candidate.SnapshotDigest, Generation: state.KernelGeneration, Epoch: state.PolicyEpoch, ArgumentsDigest: c.HashBytes(call.Arguments), Status: "ADMITTED"}
	if err = s.prepareNativeLease(state, &doc, call); err != nil {
		t.Fatal(err)
	}
	state, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{})
	if err != nil {
		t.Fatal(err)
	}
	doc.UnknownEffect = true
	doc.Pending.Status = "UNKNOWN"
	state, err = s.stop(context.Background(), state, doc, "test interrupted native effect", c.Blocked)
	if err != nil {
		t.Fatal(err)
	}
	return s, state, doc
}
func reopenNative(t *testing.T, s *Session, task string) (*Session, c.TaskState, Document) {
	t.Helper()
	directory := s.directory
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	state, doc, err := s.Load(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	return s, state, doc
}

type fixtureReconciler struct {
	calls      int
	incomplete bool
	alter      bool
}

func (f *fixtureReconciler) Reconcile(ctx context.Context, lease runner.Lease, generation int64, targets []runner.Target) (runner.CleanupReceipt, error) {
	f.calls++
	proof := runner.CleanupReceipt{SchemaVersion: 1, Lease: lease, ReconcilerGeneration: generation, Items: []runner.CleanupItem{}, NoLiveSubjects: !f.incomplete, FencesHeld: !f.incomplete}
	for i, target := range targets {
		_, capsule, err := runner.PrepareTarget(target)
		if err != nil {
			return proof, err
		}
		hash, _ := c.Digest(capsule)
		proof.Items = append(proof.Items, runner.CleanupItem{CapsuleDigest: hash, RemovedIDs: []string{}, FenceContainerID: fmt.Sprintf("%064x", i+1)})
	}
	if f.alter {
		proof.Items[0].CapsuleDigest = c.HashBytes([]byte("wrong"))
	}
	if f.incomplete {
		return proof, c.Fail(c.UnknownOutcome, "fixture cleanup interrupted")
	}
	return proof, nil
}
func nativeTestCommand(state c.TaskState, doc Document) NativeRiskCommand {
	r := unresolvedResource(state)
	return NativeRiskCommand{CommandID: "native-fence", TaskID: state.TaskID, ExpectedTaskSeq: state.TaskSeq, LeaseID: doc.Pending.NativeLease.ID, ReservationID: r.ID, RequestDigest: r.RequestDigest, ProfileDigest: r.ProfileDigest, Decision: nativeRiskDecision}
}
func (f *fixtureReconciler) open() (processReconciler, func() error, error) {
	return f, func() error { return nil }, nil
}
func TestNativeRiskOnlyFencedOlderGenerationChargesUpperWithoutAcceptingOutput(t *testing.T) {
	s, before, doc := unknownNativeBoundary(t)
	oldInstance := s.instance
	command := nativeTestCommand(before, doc)
	fake := &fixtureReconciler{}
	if _, err := s.reconcileNativeRisk(context.Background(), command, fake.open); err == nil || fake.calls != 0 {
		t.Fatal("same-generation cleanup admitted")
	}
	s, before, doc = reopenNative(t, s, doc.TaskID)
	if s.instance != oldInstance || s.clockDomain == doc.Pending.NativeLease.ClockDomain {
		t.Fatal("instance or monotonic domain restart failed")
	}
	command = nativeTestCommand(before, doc)
	state, err := s.reconcileNativeRisk(context.Background(), command, fake.open)
	if err != nil {
		t.Fatal(err)
	}
	state, after, err := s.Load(context.Background(), doc.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	resource := state.Resources.Reservations[0]
	expected := resource.Upper
	expected.Children = 0
	if after.UnknownEffect || after.Pending != nil || resource.Status != "SETTLED" || resource.Used != expected || resource.Meter != nativeRiskMeter || after.Candidate != doc.Candidate || !reflect.DeepEqual(after.Spec, doc.Spec) || !reflect.DeepEqual(after.Messages, doc.Messages) || state.Quality != before.Quality || state.Execution != c.Blocked || after.ToolCursor != 1 || len(after.PendingReplies) != 1 || !after.PendingReplies[0].IsError || after.Budget.ActiveMillis < resource.Upper.WallMillis {
		t.Fatal("fence changed output or authority", state, after)
	}
	retry, err := s.reconcileNativeRisk(context.Background(), command, fake.open)
	if err != nil || retry.TaskSeq != state.TaskSeq || fake.calls != 1 {
		t.Fatal("cleanup was dispatched twice", err)
	}
	if _, err = s.GCPreview(context.Background()); err != nil {
		t.Fatal("cleanup lineage not rooted", err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenExisting(context.Background(), restored)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if copy.instance.ID == s.instance.ID || copy.instance.PhysicalRoot == s.instance.PhysicalRoot {
		t.Fatal("restore reused physical runtime")
	}
	if _, _, err = copy.Load(context.Background(), doc.TaskID); err != nil {
		t.Fatal("restored cleanup proof failed", err)
	}
	raw, err := s.Archive.GetBytes(doc.TaskID, after.NativeCleanupAttempts[0])
	if err != nil {
		t.Fatal(err)
	}
	var proof NativeRiskReceipt
	if c.DecodeStrict(raw, &proof) != nil {
		t.Fatal("proof missing")
	}
	proof.Cleanup.FencesHeld = false
	raw, _ = c.CanonicalV1(proof)
	bad, err := s.Archive.PutBytes(doc.TaskID, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.readNativeRisk(after, bad); err == nil {
		t.Fatal("missing fence proof accepted")
	}
}
func TestNativeRiskIncompleteOrInvalidProofRetainsRiskAndDeduplicatesFailure(t *testing.T) {
	for _, mode := range []string{"incomplete", "altered"} {
		t.Run(mode, func(t *testing.T) {
			s, _, doc := unknownNativeBoundary(t)
			s, state, doc := reopenNative(t, s, doc.TaskID)
			fake := &fixtureReconciler{incomplete: mode == "incomplete", alter: mode == "altered"}
			command := nativeTestCommand(state, doc)
			failed, err := s.reconcileNativeRisk(context.Background(), command, fake.open)
			if err == nil {
				t.Fatal("invalid cleanup accepted")
			}
			current, after, err := s.Load(context.Background(), doc.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !after.UnknownEffect || after.Pending == nil || current.Resources.Reservations[0].Status != "UNKNOWN" || after.ToolCursor != doc.ToolCursor || len(after.NativeCleanupAttempts) != 1 {
				t.Fatal("risk or cursor released")
			}
			again, err := s.reconcileNativeRisk(context.Background(), command, fake.open)
			if err == nil || fake.calls != 1 || again.TaskSeq != failed.TaskSeq {
				t.Fatal("failed cleanup blindly retried")
			}
		})
	}
}
func TestNativeRiskRestoreAndManualCloneCannotCleanOriginalRuntime(t *testing.T) {
	s, state, doc := unknownNativeBoundary(t)
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackup(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenExisting(context.Background(), restored)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	copiedState, copiedDoc, err := copy.Load(context.Background(), doc.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fixtureReconciler{}
	if _, err = copy.reconcileNativeRisk(context.Background(), nativeTestCommand(copiedState, copiedDoc), fake.open); err == nil || fake.calls != 0 {
		t.Fatal("restore released original runtime risk")
	}
	// Even copying the opaque file cannot turn a different filesystem directory
	// into the original physical instance.
	original, err := os.ReadFile(filepath.Join(s.directory, runtimeInstanceFile))
	if err != nil {
		t.Fatal(err)
	}
	if err = copy.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(restored, runtimeInstanceFile), original, 0600); err != nil {
		t.Fatal(err)
	}
	if clone, err := OpenExisting(context.Background(), restored); err == nil {
		clone.Close()
		t.Fatal("manual runtime identity copy accepted")
	}
	view, err := s.NativeRiskInfo(context.Background(), state.TaskID)
	if err != nil || view.CanFence || !view.InstanceMatches {
		t.Fatal("old-generation view", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(s.directory, runtimeInstanceFile)); err != nil {
		t.Fatal(err)
	}
	if recreated, err := OpenExisting(context.Background(), s.directory); err == nil {
		recreated.Close()
		t.Fatal("lost instance silently replaced")
	}
}
func TestNativeCapsuleRejectsChangedArgsEpochAndSnapshot(t *testing.T) {
	s, _, doc := unknownNativeBoundary(t)
	defer s.Close()
	for _, mutate := range []func(*Document){
		func(d *Document) { d.Pending.NativeSubjects[0].Owner.Lease.Generation++ },
		func(d *Document) { d.Pending.NativeSubjects[0].InvocationDigest = c.HashBytes([]byte("fake")) },
		func(d *Document) { d.Pending.NativeLease.PolicyEpoch++ },
		func(d *Document) { d.Pending.NativeSubjects = nil },
	} {
		raw, _ := c.CanonicalV1(doc)
		var bad Document
		c.DecodeStrict(raw, &bad)
		mutate(&bad)
		if validateNativeLease(bad) == nil {
			t.Fatal("changed native authority accepted")
		}
	}
}
