package agent

import (
	"context"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func TestLLMReadWorkerUsesImmutableCandidateAndCompletesAdvisoryAnalysis(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "advisory result", UsageKnown: true}}, true)
	defer s.Close()
	parent, parentDoc, err := s.Load(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	queued, schedule, err := s.ScheduleLLMReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-explore", TaskID: opts.TaskID, Action: ReadWorkerActionExplore, Prompt: "Summarize the captured candidate."})
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Role != c.ReadWorkerRoleExplore || schedule.SourceSnapshotDigest != parentDoc.Candidate.SnapshotDigest || schedule.ReadWorkerLease.ParentTask != opts.TaskID || schedule.ReadWorkerLease.ParentTaskSeq != parent.TaskSeq || schedule.ReadWorkerLease.Validate() != nil {
		t.Fatalf("worker admission lost its source/lease binding: schedule=%+v", schedule)
	}
	if len(queued.GoalQueue) != 1 || queued.GoalQueue[0].ReadWorkerLease != schedule.ReadWorkerLease {
		t.Fatal("worker lease was not durably copied into the parent queue")
	}
	_, activating, err := s.BeginGoalActivation(ctx, GoalCommand{CommandID: "worker-activate", TaskID: opts.TaskID, Action: "activate", GoalID: schedule.ID})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateGoalTask(ctx, opts.TaskID, activating)
	if err != nil {
		t.Fatal(err)
	}
	_, childDoc, err := s.Load(ctx, child.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if childDoc.TaskKind != "ANALYSIS" || childDoc.Baseline.SnapshotDigest != parentDoc.Candidate.SnapshotDigest || childDoc.Candidate.SnapshotDigest != childDoc.Baseline.SnapshotDigest || childDoc.CheckRuntime != nil || childDoc.GoalOrigin == nil || childDoc.GoalOrigin.ReadWorkerLease != schedule.ReadWorkerLease {
		t.Fatalf("read worker was not an immutable analysis child: kind=%q baseline=%s candidate=%s", childDoc.TaskKind, childDoc.Baseline.SnapshotDigest, childDoc.Candidate.SnapshotDigest)
	}
	activated, err := s.CommitGoalActivation(ctx, opts.TaskID, activating, child)
	if err != nil || activated.GoalQueue[0].Status != "ACTIVATED" {
		t.Fatalf("worker activation receipt missing: state=%+v err=%v", activated, err)
	}
	done, err := s.Run(ctx, child.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Execution != c.Terminated || done.Outcome != c.Finished || done.Quality == c.Verified {
		t.Fatalf("advisory child did not finish as unverified analysis: %+v", done)
	}
	result, err := s.ReadWorkerResult(ctx, opts.TaskID, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Advisory || result.Status != "COMPLETE" || result.Trust != "MODEL_AUTHORED_UNREVIEWED_ADVISORY" || result.Summary != "advisory result" || result.SourceCandidateDigest != parentDoc.Candidate.SnapshotDigest {
		t.Fatalf("unexpected advisory result: %+v", result)
	}
}

func TestLLMReadWorkerRevokedAfterModelAdmissionSettlesWithoutDispatch(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "must not dispatch", UsageKnown: true}}, true)
	defer s.Close()
	_, schedule, err := s.ScheduleLLMReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-revoke", TaskID: opts.TaskID, Action: ReadWorkerActionExplore, Prompt: "Inspect the captured candidate."})
	if err != nil {
		t.Fatal(err)
	}
	_, activating, err := s.BeginGoalActivation(ctx, GoalCommand{CommandID: "worker-revoke-start", TaskID: opts.TaskID, Action: "activate", GoalID: schedule.ID})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateGoalTask(ctx, opts.TaskID, activating)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitGoalActivation(ctx, opts.TaskID, activating, child); err != nil {
		t.Fatal(err)
	}
	state, err := s.transition(ctx, child, c.Running, "", "test pre-dispatch boundary")
	if err != nil {
		t.Fatal(err)
	}
	state, doc, err := s.Load(ctx, state.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	request, encoded, manifest, err := s.compileOfflineRequestForCurrentCandidate(ctx, doc, state, s.layers(state, doc), doc.Budget.MaxOutputTokens)
	if err != nil {
		t.Fatal(err)
	}
	requestDigest, err := s.Archive.PutBytes(doc.TaskID, encoded)
	if err != nil {
		t.Fatal(err)
	}
	doc.Context = &ContextAudit{SchemaVersion: 1, Profile: manifest.Estimator, RequestDigest: requestDigest, Manifest: manifest}
	doc.Budget.ReservedInput, doc.Budget.ReservedOutput = manifest.InputTokens+4096, request.MaxOutputTokens
	doc.Budget.Steps++
	doc.Pending = &Pending{ID: request.ID, Kind: "MODEL", Candidate: doc.Candidate.SnapshotDigest, Generation: state.KernelGeneration, Epoch: state.PolicyEpoch, ArgumentsDigest: c.HashBytes(encoded), Status: "ADMITTED"}
	state, err = s.record(ctx, state, doc, "SessionRecorded", c.EventPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordSteering(ctx, SteeringInput{CommandID: "revoke-parent", TaskID: opts.TaskID, Text: "Stop this source analysis."}); err != nil {
		t.Fatal(err)
	}
	if err = s.validateReadWorkerLease(ctx, state, doc); err == nil {
		t.Fatal("steering did not revoke child admission")
	}
	if _, err = s.settleModelNoDispatch(ctx, state, doc, request, requestDigest, "READ_WORKER_LEASE_STALE_BEFORE_DISPATCH"); err != nil {
		t.Fatal(err)
	}
	settled, retained, err := s.Load(ctx, child.TaskID)
	if err != nil {
		t.Fatal("no-dispatch settlement failed durable receipt validation", err)
	}
	if settled.Execution != c.WaitingResource || retained.Pending != nil || retained.UnknownEffect || !retained.LastNoDispatch || retained.FixtureCursor != 0 || retained.Budget.Steps != 1 || settled.Tokens.Reserved != (c.TokenLimits{}) || len(settled.Tokens.Reservations) != 1 {
		t.Fatal("revoked admission lost its receipt or dispatched fixture work", settled, retained.Budget)
	}
	charge := settled.Tokens.Reservations[0]
	if charge.Status != "SETTLED" || charge.UsageSource != "KERNEL_NO_DISPATCH" || charge.Used != (c.TokenLimits{}) {
		t.Fatal("revoked admission invented provider usage or left reserved tokens", charge)
	}
	if len(settled.Resources.Reservations) != 1 || settled.Resources.Reservations[0].Status != "SETTLED" || settled.Resources.Reservations[0].Meter != "KERNEL_NO_DISPATCH" || settled.Resources.Reservations[0].Used != (c.ResourceVector{}) {
		t.Fatal("paired resource admission was not settled by the same no-dispatch proof", settled.Resources)
	}
}

func TestLLMReadWorkerActivationCancellationRetainsUnknownAllocation(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "unused", UsageKnown: true}}, true)
	defer s.Close()
	queued, schedule, err := s.ScheduleLLMReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-review", TaskID: opts.TaskID, Action: ReadWorkerActionReview, Prompt: "Review the captured candidate."})
	if err != nil {
		t.Fatal(err)
	}
	activatedParent, activating, err := s.BeginGoalActivation(ctx, GoalCommand{CommandID: "worker-review-activate", TaskID: opts.TaskID, Action: "activate", GoalID: schedule.ID})
	if err != nil || activating.Status != "ACTIVATING" {
		t.Fatalf("worker activation intent missing: %+v %v", activating, err)
	}
	before, err := budgetHeadroom(opts.Budget, activatedParent.GoalQueue, false)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.CancelReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-review-cancel", TaskID: opts.TaskID, Action: ReadWorkerActionCancel, GoalID: schedule.ID})
	if err != nil {
		t.Fatal(err)
	}
	item := cancelled.GoalQueue[0]
	if item.Status != "UNKNOWN" || item.ActivationCommandID == "" || item.ChildTaskSeq != 0 || item.ChildDocumentDigest != "" {
		t.Fatalf("receipt-free activation did not retain its unresolved allocation: %+v", item)
	}
	remaining, err := budgetHeadroom(opts.Budget, cancelled.GoalQueue, false)
	if err != nil || remaining != before {
		t.Fatalf("cancelling activated worker released its held tranche: before=%+v remaining=%+v err=%v", before, remaining, err)
	}
	result, err := s.ReadWorkerResult(ctx, opts.TaskID, schedule.ID)
	if err != nil || result.Status != "UNKNOWN_OUTCOME" || result.Advisory != true {
		t.Fatalf("retained worker result was not inspectable as unknown: %+v err=%v", result, err)
	}
	if len(queued.GoalQueue) != 1 {
		t.Fatal("initial worker schedule was not durably queued")
	}
}

func TestLLMReadWorkerQueuedCancellationReleasesItsBudget(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "unused", UsageKnown: true}}, true)
	defer s.Close()
	_, schedule, err := s.ScheduleLLMReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-queued-cancel", TaskID: opts.TaskID, Action: ReadWorkerActionExplore, Prompt: "Explore the captured candidate."})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.CancelReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-queued-cancel-request", TaskID: opts.TaskID, Action: ReadWorkerActionCancel, GoalID: schedule.ID})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.GoalQueue[0].Status != "CANCELLED" || cancelled.GoalQueue[0].ActivationCommandID != "" {
		t.Fatalf("queued worker did not become refundable CANCELLED: %+v", cancelled.GoalQueue[0])
	}
	remaining, err := budgetHeadroom(opts.Budget, cancelled.GoalQueue, false)
	expected := c.BudgetAllocation{MaxSteps: opts.Budget.MaxSteps, MaxToolCalls: opts.Budget.MaxToolCalls, MaxInputTokens: opts.Budget.MaxInputTokens, MaxOutputTokens: opts.Budget.MaxOutputTokens, MaxActiveMillis: opts.Budget.MaxActiveMillis}
	if err != nil || remaining != expected {
		t.Fatalf("queued cancellation did not release its exact tranche: remaining=%+v err=%v", remaining, err)
	}
	result, err := s.ReadWorkerResult(ctx, opts.TaskID, schedule.ID)
	if err != nil || result.Status != "CANCELLED" || result.Advisory != true {
		t.Fatalf("queued cancellation result was not inspectable: %+v err=%v", result, err)
	}
}

func TestLLMReadWorkerAdmissionUsesIndependentChildVersionsAndHistoricalParentCapture(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "unused", UsageKnown: true}}, true)
	defer s.Close()

	// Move the parent to a later spec/policy epoch before scheduling. The
	// worker lease must retain those parent authority values while the newly
	// created child remains an independent version-one document.
	input := SteeringInput{CommandID: "worker-version-input", TaskID: opts.TaskID, Text: "retain this source binding"}
	if _, err := s.RecordSteering(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Controls(ctx, opts.TaskID, "pause"); err != nil {
		t.Fatal(err)
	}
	revised, err := s.Revise(ctx, revisionFor(t, s, opts.TaskID, input.CommandID, "worker-version-revision"))
	if err != nil || revised.SpecVersion != 2 || revised.PolicyEpoch != 2 {
		t.Fatalf("parent did not reach the intended authority version: %+v err=%v", revised, err)
	}

	_, schedule, err := s.ScheduleLLMReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-version", TaskID: opts.TaskID, Action: ReadWorkerActionExplore, Prompt: "Inspect the captured candidate."})
	if err != nil {
		t.Fatal(err)
	}
	if schedule.ReadWorkerLease.SpecVersion != revised.SpecVersion || schedule.ReadWorkerLease.PolicyEpoch != revised.PolicyEpoch {
		t.Fatalf("lease lost parent authority versions: %+v", schedule.ReadWorkerLease)
	}
	_, activating, err := s.BeginGoalActivation(ctx, GoalCommand{CommandID: "worker-version-activate", TaskID: opts.TaskID, Action: "activate", GoalID: schedule.ID})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateGoalTask(ctx, opts.TaskID, activating)
	if err != nil {
		t.Fatal(err)
	}
	childState, childDoc, err := s.Load(ctx, child.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if childState.SpecVersion != 1 || childState.PolicyEpoch != 1 || childDoc.Spec.Version != 1 {
		t.Fatalf("worker child inherited parent versions instead of starting independently: state=%+v spec=%d", childState, childDoc.Spec.Version)
	}
	activated, err := s.CommitGoalActivation(ctx, opts.TaskID, activating, child)
	if err != nil || activated.GoalQueue[0].Status != "ACTIVATED" {
		t.Fatalf("worker activation receipt missing: %+v err=%v", activated, err)
	}
	if err := s.ReadWorkerAdmission(ctx, child.TaskID); err != nil {
		t.Fatalf("valid version-one child was rejected by its parent lease: %v", err)
	}
}

func TestLLMReadWorkerActiveCancellationRetainsConsumedOrUnknownAllocation(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "unused", UsageKnown: true}}, true)
	defer s.Close()

	_, schedule, err := s.ScheduleLLMReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-active", TaskID: opts.TaskID, Action: ReadWorkerActionReview, Prompt: "Review the captured candidate."})
	if err != nil {
		t.Fatal(err)
	}
	parentAfterActivation, activating, err := s.BeginGoalActivation(ctx, GoalCommand{CommandID: "worker-active-activate", TaskID: opts.TaskID, Action: "activate", GoalID: schedule.ID})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateGoalTask(ctx, opts.TaskID, activating)
	if err != nil {
		t.Fatal(err)
	}
	// Leave the parent ACTIVATING while the child has already been created. This
	// models a crash between child creation and the activation receipt commit.
	before, err := budgetHeadroom(opts.Budget, parentAfterActivation.GoalQueue, false)
	if err != nil {
		t.Fatal(err)
	}

	// A recorded child step is enough to prove that the child tranche was
	// consumed. The later cancellation still has to preserve the reservation
	// because the child/provider outcome is no longer refundable by assertion.
	childState, childDoc, err := s.Load(ctx, child.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	childDoc.Budget.Steps = 1
	if _, err = s.record(ctx, childState, childDoc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}

	cancelled, err := s.CancelReadWorker(ctx, LLMReadWorkerCommand{CommandID: "worker-active-cancel", TaskID: opts.TaskID, Action: ReadWorkerActionCancel, GoalID: schedule.ID})
	if err != nil {
		t.Fatal(err)
	}
	item := cancelled.GoalQueue[0]
	if item.Status != "UNKNOWN" || item.ChildTaskSeq != 0 || item.ChildDocumentDigest != "" || item.ActivationCommandID == "" {
		t.Fatalf("crashed activation cancellation did not retain the unresolved intent: %+v", item)
	}
	after, err := budgetHeadroom(opts.Budget, cancelled.GoalQueue, false)
	if err != nil || after != before {
		t.Fatalf("active cancellation refunded consumed or unknown allocation: before=%+v after=%+v err=%v", before, after, err)
	}
	result, err := s.ReadWorkerResult(ctx, opts.TaskID, schedule.ID)
	if err != nil || result.Status != "UNKNOWN_OUTCOME" {
		t.Fatalf("retained active cancellation was not surfaced as unknown: %+v err=%v", result, err)
	}
}
