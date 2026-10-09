package agent

import (
	"context"
	"errors"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/store"
)

type NativeRiskCommand struct {
	CommandID       string `json:"command_id"`
	TaskID          string `json:"task_id"`
	ExpectedTaskSeq int64  `json:"expected_task_seq"`
	LeaseID         string `json:"lease_id"`
	ReservationID   string `json:"reservation_id"`
	RequestDigest   string `json:"request_digest"`
	ProfileDigest   string `json:"profile_digest"`
	Decision        string `json:"decision"`
}

const nativeRiskDecision = "FENCE_OLD_SUBJECTS_AND_ACCOUNT_FULL_UPPER_BOUND"
const nativeRiskMeter = "KERNEL_FENCED_NATIVE_UPPER_BOUND"

type NativeRiskReceipt struct {
	SchemaVersion    int                   `json:"schema_version"`
	TaskID           string                `json:"task_id"`
	CommandDigest    string                `json:"command_digest"`
	PreviousDocument string                `json:"previous_document"`
	ReservationID    string                `json:"reservation_id"`
	RequestDigest    string                `json:"request_digest"`
	ProfileDigest    string                `json:"profile_digest"`
	Decision         string                `json:"decision"`
	AssumedResources c.ResourceVector      `json:"assumed_resources"`
	Cleanup          runner.CleanupReceipt `json:"cleanup"`
	Complete         bool                  `json:"complete"`
	AcceptedOutput   bool                  `json:"accepted_output"`
}
type NativeRiskView struct {
	SchemaVersion   int                  `json:"schema_version"`
	EngineBinding   *NativeEngineBinding `json:"engine_binding,omitempty"`
	TaskID          string               `json:"task_id"`
	TaskSeq         int64                `json:"task_seq"`
	Generation      int64                `json:"current_owner_generation"`
	Lease           *runner.Lease        `json:"pending_lease,omitempty"`
	SubjectCount    int                  `json:"subject_count"`
	InstanceMatches bool                 `json:"physical_instance_matches"`
	CanFence        bool                 `json:"can_fence"`
	Reason          string               `json:"reason"`
	Command         *NativeRiskCommand   `json:"command_template,omitempty"`
	CleanupReceipts []string             `json:"cleanup_receipts"`
}

func (s *Session) NativeRiskInfo(ctx context.Context, task string) (NativeRiskView, error) {
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return NativeRiskView{}, err
	}
	view := NativeRiskView{SchemaVersion: 1, TaskID: task, TaskSeq: state.TaskSeq, Generation: s.Journal.Generation(), CleanupReceipts: append([]string{}, doc.NativeCleanupAttempts...), Reason: "NO_OWNED_UNKNOWN_NATIVE_INTENT"}
	binding, err := s.engineBinding()
	if err != nil {
		return view, err
	}
	view.EngineBinding = binding
	if doc.Pending == nil || doc.Pending.NativeLease == nil {
		return view, nil
	}
	lease := *doc.Pending.NativeLease
	view.Lease = &lease
	view.SubjectCount = len(doc.Pending.NativeSubjects)
	view.InstanceMatches = lease.InstanceID == s.instance.ID
	r := unresolvedResource(state)
	view.CanFence = view.InstanceMatches && view.Generation > lease.Generation && doc.UnknownEffect && doc.Pending.Status == "UNKNOWN" && r != nil && r.Kind == "NATIVE_TOOL" && r.Status == "UNKNOWN" && nativeRiskState(state)
	view.Reason = "OLD_GENERATION_AND_PHYSICAL_INSTANCE_REQUIRED"
	if binding == nil {
		view.CanFence = false
		view.Reason = "ENGINE_PIN_MISSING_OUTCOME_UNKNOWN"
	}
	if view.CanFence {
		view.Reason = "EXPLICIT_BOUND_FENCE_AND_FULL_CHARGE_REQUIRED"
		view.Command = &NativeRiskCommand{CommandID: "", TaskID: task, ExpectedTaskSeq: state.TaskSeq, LeaseID: lease.ID, ReservationID: r.ID, RequestDigest: r.RequestDigest, ProfileDigest: r.ProfileDigest, Decision: nativeRiskDecision}
	}
	return view, nil
}
func nativeRiskState(s c.TaskState) bool {
	return s.Execution == c.Blocked || s.Execution == c.Paused || s.Execution == c.Recovering || s.Execution == c.WaitingResource || s.Execution == c.Terminated
}

type processReconciler interface {
	Reconcile(context.Context, runner.Lease, int64, []runner.Target) (runner.CleanupReceipt, error)
}

func (s *Session) ReconcileNativeRisk(ctx context.Context, command NativeRiskCommand) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileNativeRisk(ctx, command, func() (processReconciler, func() error, error) {
		_, retained, err := s.Load(ctx, command.TaskID)
		if err != nil || retained.Pending == nil || retained.Pending.NativeLease == nil {
			return nil, nil, c.Fail(c.StaleAuthority, "native cleanup lease unavailable")
		}
		leaseForCleanup := *retained.Pending.NativeLease
		broker, err := runner.OpenDocker()
		if err != nil {
			return nil, nil, err
		}
		if err = s.bindEngine(ctx, broker, leaseForCleanup, false); err != nil {
			broker.Close()
			return nil, nil, err
		}
		return broker, broker.Close, nil
	})
}
func (s *Session) reconcileNativeRisk(ctx context.Context, command NativeRiskCommand, open func() (processReconciler, func() error, error)) (c.TaskState, error) {
	hash, err := c.Digest(command)
	if err != nil {
		return c.TaskState{}, err
	}
	if command.CommandID == "" || len(command.CommandID) > 128 || command.TaskID == "" || command.ExpectedTaskSeq < 1 || command.LeaseID == "" || command.ReservationID == "" || !c.ValidDigest(command.RequestDigest) || !c.ValidDigest(command.ProfileDigest) || command.Decision != nativeRiskDecision {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "bound native process fence/full-charge decision required")
	}
	prior, event, payload, found, err := s.Journal.CommandReceipt(ctx, command.CommandID)
	if err != nil {
		return prior, err
	}
	if found {
		if event.TaskID != command.TaskID || event.Actor != "user" || event.Type != "NativeRiskReconciled" && event.Type != "NativeCleanupObserved" || payload.Reason != hash {
			return prior, c.Fail(c.CommandIDConflict, "native cleanup command ID conflict")
		}
		if event.Type == "NativeCleanupObserved" {
			return prior, c.Fail(c.UnknownOutcome, "cleanup was incomplete; inspect current cursor and issue a new command")
		}
		return prior, nil
	}
	state, doc, err := s.Load(ctx, command.TaskID)
	if err != nil {
		return state, err
	}
	if state.TaskSeq != command.ExpectedTaskSeq {
		return state, c.Fail(c.StaleAuthority, "native cleanup cursor changed")
	}
	r := unresolvedResource(state)
	if !nativeRiskState(state) || !doc.UnknownEffect || doc.Pending == nil || doc.Pending.Kind != "NATIVE_TOOL" || doc.Pending.Status != "UNKNOWN" || doc.Pending.NativeLease == nil || r == nil || r.Status != "UNKNOWN" || r.Kind != "NATIVE_TOOL" || r.ID != command.ReservationID || r.RequestDigest != command.RequestDigest || r.ProfileDigest != command.ProfileDigest || doc.Pending.NativeLease.ID != command.LeaseID {
		return state, c.Fail(c.PolicyDenied, "only a retained UNKNOWN owned native operation can be fenced")
	}
	lease := *doc.Pending.NativeLease
	if lease.InstanceID != s.instance.ID || s.Journal.Generation() <= lease.Generation {
		return state, c.Fail(c.StaleAuthority, "cleanup cannot cross physical store instances or run in the old owner generation")
	}
	if len(doc.NativeCleanupAttempts) >= 256 {
		return state, c.Fail(c.BudgetLimitReached, "native cleanup receipt quota exhausted")
	}
	call, err := pendingCheck(doc)
	if err != nil {
		return state, err
	}
	targets, capsules, err := ownedCheckTargets(s, doc, call, lease)
	if err != nil {
		return state, err
	}
	if len(capsules) != len(doc.Pending.NativeSubjects) {
		return state, c.Fail(c.StoreIntegrityError, "pending process target set changed")
	}
	for i, capsule := range capsules {
		if capsule != doc.Pending.NativeSubjects[i] {
			return state, c.Fail(c.StaleAuthority, "physical mount differs from retained process intent")
		}
	}
	broker, closeBroker, err := open()
	if err != nil {
		return state, err
	}
	defer closeBroker()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	cleanup, cleanupErr := broker.Reconcile(cleanupCtx, lease, s.Journal.Generation(), targets)
	complete := cleanupErr == nil && runner.ValidateCleanupCapsules(cleanup, lease, s.Journal.Generation(), capsules) == nil
	assumed := r.Upper
	assumed.Children = 0
	proof := NativeRiskReceipt{SchemaVersion: 1, TaskID: doc.TaskID, CommandDigest: hash, PreviousDocument: state.DocumentDigest, ReservationID: r.ID, RequestDigest: r.RequestDigest, ProfileDigest: r.ProfileDigest, Decision: command.Decision, AssumedResources: assumed, Cleanup: cleanup, Complete: complete, AcceptedOutput: false}
	raw, err := c.CanonicalV1(proof)
	if err != nil {
		return state, err
	}
	receiptDigest, err := s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		return state, err
	}
	doc.NativeCleanupAttempts = append(doc.NativeCleanupAttempts, receiptDigest)
	payload = c.EventPayload{Reason: hash}
	kind := "NativeCleanupObserved"
	if complete {
		rr := *r
		rr.Status = "SETTLED"
		rr.Used = assumed
		rr.Meter = nativeRiskMeter
		doc.Budget.ActiveMillis = max(doc.Budget.ActiveMillis, rr.StartedActiveMillis+rr.Upper.WallMillis)
		reply := model.Reply{CallID: doc.Pending.ID, Content: "OLD_NATIVE_SUBJECTS_FENCED; lost computation output was discarded; full resource allocation charged", IsError: true}
		resourceProof := ResourceReceipt{SchemaVersion: 1, TaskID: doc.TaskID, OperationID: rr.ID, RequestDigest: rr.RequestDigest, ProfileDigest: rr.ProfileDigest, StartedActiveMillis: rr.StartedActiveMillis, EndedActiveMillis: doc.Budget.ActiveMillis, Candidate: doc.Candidate.SnapshotDigest, Measurement: rr.Meter, SourceReceipt: receiptDigest, ToolReply: &reply}
		raw, err = c.CanonicalV1(resourceProof)
		if err != nil {
			return state, err
		}
		rr.ReceiptDigest, err = s.Archive.PutControlBytes(doc.TaskID, raw, true)
		if err != nil {
			return state, err
		}
		doc.PendingReplies = append(doc.PendingReplies, reply)
		doc.ToolCursor++
		doc.Pending = nil
		doc.UnknownEffect = false
		doc.Blocker = "OLD_NATIVE_SUBJECTS_FENCED; no result or verification accepted"
		payload.Resources = &c.ResourceMutation{Action: "SETTLE", Reservation: rr}
		kind = "NativeRiskReconciled"
	}
	predicted := state
	if payload.Resources != nil {
		predicted.Resources, err = c.ApplyResources(state.Resources, *payload.Resources, state)
		if err != nil {
			return state, err
		}
	}
	if err = s.validateResourceDocument(predicted, doc); err != nil {
		return state, err
	}
	raw, err = c.CanonicalV1(doc)
	if err != nil {
		return state, err
	}
	payload.DocumentDigest, err = s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		return state, err
	}
	state, err = s.Journal.Execute(cleanupCtx, store.Command{ID: command.CommandID, TaskID: doc.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: kind, Payload: payload})
	if err != nil {
		return state, err
	}
	if !complete {
		return state, errors.Join(c.Fail(c.UnknownOutcome, "native cleanup incomplete; intent and risk retained"), cleanupErr)
	}
	return state, nil
}
func (s *Session) readNativeRisk(doc Document, digest string) (NativeRiskReceipt, error) {
	var proof NativeRiskReceipt
	raw, err := s.Archive.GetBytes(doc.TaskID, digest)
	if err != nil {
		return proof, err
	}
	if c.DecodeStrict(raw, &proof) != nil || proof.SchemaVersion != 1 || proof.TaskID != doc.TaskID || !c.ValidDigest(proof.CommandDigest) || !c.ValidDigest(proof.PreviousDocument) || proof.Decision != nativeRiskDecision || proof.AcceptedOutput || !c.ValidDigest(proof.RequestDigest) || !c.ValidDigest(proof.ProfileDigest) {
		return proof, c.Fail(c.StoreIntegrityError, "native cleanup receipt invalid")
	}
	raw, err = s.Archive.GetBytes(doc.TaskID, proof.PreviousDocument)
	if err != nil {
		return proof, err
	}
	var prior Document
	if c.DecodeStrict(raw, &prior) != nil || prior.TaskID != doc.TaskID || prior.Pending == nil || prior.Pending.NativeLease == nil || !prior.UnknownEffect || prior.Pending.Status != "UNKNOWN" || prior.Pending.ArgumentsDigest != proof.RequestDigest || nativeResourceID(prior) != proof.ReservationID || resourceProfile(prior) != proof.ProfileDigest || validateNativeLease(prior) != nil {
		return proof, c.Fail(c.StoreIntegrityError, "native cleanup lacks original bound UNKNOWN intent")
	}
	if proof.Complete {
		if err = runner.ValidateCleanupCapsules(proof.Cleanup, *prior.Pending.NativeLease, proof.Cleanup.ReconcilerGeneration, prior.Pending.NativeSubjects); err != nil {
			return proof, err
		}
	}
	return proof, nil
}
func (s *Session) validateNativeCleanup(doc Document) error {
	if len(doc.NativeCleanupAttempts) > 256 {
		return c.Fail(c.StoreIntegrityError, "native cleanup receipt quota exceeded")
	}
	seen := map[string]bool{}
	for _, digest := range doc.NativeCleanupAttempts {
		if !c.ValidDigest(digest) || seen[digest] {
			return c.Fail(c.StoreIntegrityError, "duplicate native cleanup receipt")
		}
		seen[digest] = true
		if _, err := s.readNativeRisk(doc, digest); err != nil {
			return err
		}
	}
	return nil
}
