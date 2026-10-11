package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
)

// DeliveryReconcileResult describes recovery of an UNKNOWN durable delivery
// intent. Reconciliation never writes the live target; it only completes the
// registered receipt when every sealed file is already at its planned postimage.
type DeliveryReconcileResult struct {
	SchemaVersion    int    `json:"schema_version"`
	TaskID           string `json:"task_id"`
	OperationID      string `json:"operation_id"`
	Phase            string `json:"phase,omitempty"`
	Status           string `json:"status"`
	Backend          string `json:"backend"`
	TargetWritten    bool   `json:"target_written"`
	TouchedScope     string `json:"touched_scope,omitempty"`
	WorkspaceQuality string `json:"whole_workspace_quality,omitempty"`
	PlanDigest       string `json:"plan_manifest_digest,omitempty"`
	ReceiptDigest    string `json:"receipt_manifest_digest,omitempty"`
	Reason           string `json:"reason"`
}

// DeliveryReconcile resolves a crash-left UNKNOWN intent only when the exact
// immutable plan can be reacquired under exclusive native handles and every
// file already has its planned postimage. Preimage, mixed, or edited states
// remain UNKNOWN and are never repaired or overwritten here.
func (s *Session) DeliveryReconcile(ctx context.Context, task, operationID string) (DeliveryReconcileResult, error) {
	result := DeliveryReconcileResult{SchemaVersion: 1, TaskID: task, OperationID: operationID, Status: "UNKNOWN_EFFECT_PENDING", Backend: delivery.LiveBackendUnsupported, TargetWritten: false, Reason: "the exact UNKNOWN effect could not be reconciled without changing live files"}
	if task == "" || !validDeliveryOperationID(operationID) {
		return result, c.Fail(c.InvalidArgument, "task and exact 32-character lowercase delivery operation ID are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return result, err
	}
	effect, found := findDeliveryEffect(state, operationID)
	if !found || effect.Status != c.DeliveryUnknown {
		result.Status = "NO_UNKNOWN_EFFECT"
		result.Reason = "the exact operation is not a crash-left UNKNOWN effect owned by this task"
		return result, nil
	}
	result.Phase = effect.Phase
	result.PlanDigest = effect.PlanDigest
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return result, err
	}
	if state.Execution != c.Terminated || state.InputBarrier || len(state.PendingInputIDs) != 0 || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(state) != nil || !tokensQuiescent(state) || state.TaskSeq != effect.SourceTaskSeq+1 || state.DocumentDigest != effect.SourceDocument || state.SpecVersion != effect.SpecVersion || state.PolicyEpoch != effect.PolicyEpoch || state.KernelGeneration != effect.Generation || s.Journal.Generation() < state.KernelGeneration || state.CandidateDigest != effect.CandidateDigest {
		return result, c.Fail(c.StaleAuthority, "UNKNOWN delivery reconciliation requires the exact current terminal task authority")
	}
	for _, prior := range state.DeliveryEffects {
		if prior.Status == c.DeliveryUnknown && prior.OperationID != effect.OperationID {
			return result, c.Fail(c.Conflict, "another UNKNOWN delivery effect blocks reconciliation")
		}
	}
	planArtifact, plan, err := s.readDeliveryPlan(ctx, task, operationID)
	if err != nil {
		return result, err
	}
	if plan.Phase != effect.Phase || plan.ParentOperationID != effect.ParentOperationID || plan.SourceDocument != effect.SourceDocument || plan.SourceTaskSeq != effect.SourceTaskSeq || plan.SpecVersion != effect.SpecVersion || plan.PolicyEpoch != effect.PolicyEpoch || plan.Generation != effect.Generation || plan.CandidateDigest != effect.CandidateDigest || plan.TargetDigest != effect.TargetDigest || plan.ResultDigest != effect.ResultDigest || planArtifact.Manifest.Digest != effect.PlanDigest {
		return result, c.Fail(c.StoreIntegrityError, "UNKNOWN delivery plan differs from its durable effect binding")
	}
	if effect.Phase == c.DeliveryRestorePhase {
		parent, ok := findDeliveryEffect(state, effect.ParentOperationID)
		if !ok || parent.Phase != c.DeliveryApplyPhase || parent.Status != c.DeliveryApplied {
			return result, c.Fail(c.StaleAuthority, "UNKNOWN restore reconciliation requires its exact still-owned APPLIED parent")
		}
	}
	result.Backend = plan.Seal.Profile
	result.TouchedScope = "EXACT_TOUCHED_FILES"
	result.WorkspaceQuality = "UNKNOWN"
	owner, err := privacyOperation(ctx, *s.privacy)
	if err != nil {
		return result, err
	}
	defer func() { _ = owner.Close() }()
	if err = s.checkDeliveryReconcileAuthority(ctx, task, effect); err != nil {
		return result, err
	}
	expected := make(map[string]string, len(plan.Files))
	for _, file := range plan.Files {
		expected[file.Path] = file.AfterDigest
	}
	lease, err := delivery.AcquireNativeTarget(ctx, plan.Seal, expected)
	if err != nil {
		result.Reason = "UNKNOWN remains pending because exact exclusive reacquisition of every planned postimage failed; no target file was written"
		return result, nil
	}
	defer func() { _ = lease.Close() }()

	output, err := deliveryPlanOutputBase(planArtifact.Directory, operationID)
	if err != nil {
		return result, err
	}
	existing, err := s.readDeliveryArtifacts(ctx, task, operationID, "FILE_RECEIPT")
	if err != nil {
		return result, err
	}
	receiptsByPath := make(map[string]deliveryFileReceipt, len(existing))
	digestsByPath := make(map[string]string, len(existing))
	planIndex := make(map[string]int, len(plan.Files))
	for i, file := range plan.Files {
		planIndex[file.Path] = i
	}
	for _, artifact := range existing {
		var receipt deliveryFileReceipt
		if err = c.DecodeStrict(artifact.Report, &receipt); err != nil {
			return result, err
		}
		i, ok := planIndex[receipt.Path]
		if !ok || receipt.SchemaVersion != 1 || receipt.OperationID != operationID || receipt.ParentOperationID != effect.ParentOperationID || receipt.Phase != effect.Phase || receipt.PlanDigest != effect.PlanDigest || receipt.Ordinal != i+1 || receipt.BeforeDigest != plan.Files[i].BeforeDigest || receipt.AfterDigest != plan.Files[i].AfterDigest || receipt.FileIdentity != plan.Seal.Files[i].Identity || receipt.ObservedAfter != receipt.AfterDigest || receipt.Observation != "SYNCED_READBACK" {
			return result, c.Fail(c.StoreIntegrityError, "registered per-file receipt does not match the UNKNOWN plan")
		}
		if _, duplicate := receiptsByPath[receipt.Path]; duplicate {
			return result, c.Fail(c.StoreIntegrityError, "duplicate per-file receipt for UNKNOWN operation")
		}
		receiptsByPath[receipt.Path] = receipt
		digestsByPath[receipt.Path] = artifact.Manifest.Digest
	}
	fileReceipts := make([]deliveryFileReceipt, 0, len(plan.Files))
	receiptDigests := make([]string, 0, len(plan.Files))
	for i, file := range plan.Files {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		readback, readErr := lease.ReadFile(file.Path, 64<<20)
		if readErr != nil || c.HashBytes(readback) != file.AfterDigest {
			result.Reason = "UNKNOWN remains pending because at least one sealed file is not already at its planned postimage; no target file was written"
			return result, nil
		}
		receipt, ok := receiptsByPath[file.Path]
		if !ok {
			receipt = deliveryFileReceipt{SchemaVersion: 1, OperationID: operationID, ParentOperationID: effect.ParentOperationID, Phase: effect.Phase, PlanDigest: effect.PlanDigest, Ordinal: i + 1, Path: file.Path, BeforeDigest: file.BeforeDigest, AfterDigest: file.AfterDigest, FileIdentity: plan.Seal.Files[i].Identity, ObservedAfter: file.AfterDigest, Observation: "SYNCED_READBACK"}
			receiptRaw, encodeErr := c.CanonicalV1(receipt)
			if encodeErr != nil {
				return result, encodeErr
			}
			directory, dirErr := deliveryArtifactDirectory(output, operationID, fmt.Sprintf("file-%03d", i+1))
			if dirErr != nil {
				return result, dirErr
			}
			artifact, publishErr := s.publishDeliveryArtifact(ctx, task, directory, "FILE_RECEIPT", operationID, receiptRaw, nil, plan.TargetRoot)
			if publishErr != nil {
				return result, publishErr
			}
			digestsByPath[file.Path] = artifact.Manifest.Digest
		}
		fileReceipts = append(fileReceipts, receipt)
		receiptDigests = append(receiptDigests, digestsByPath[file.Path])
	}
	aggregate := deliveryAggregateReceipt{SchemaVersion: 1, OperationID: operationID, ParentOperationID: effect.ParentOperationID, Phase: effect.Phase, PlanDigest: effect.PlanDigest, Files: fileReceipts, ReceiptDigests: receiptDigests, TouchedScope: "EXACT_TOUCHED_FILES", WorkspaceQuality: "UNKNOWN", VerificationClaim: "CANDIDATE_VERIFICATION_NOT_TRANSFERRED_TO_LIVE_WORKSPACE"}
	aggregateRaw, err := c.CanonicalV1(aggregate)
	if err != nil {
		return result, err
	}
	aggregates, err := s.readDeliveryArtifacts(ctx, task, operationID, "AGGREGATE_RECEIPT")
	if err != nil {
		return result, err
	}
	var aggregateDigest string
	if len(aggregates) > 1 {
		return result, c.Fail(c.StoreIntegrityError, "UNKNOWN operation has multiple aggregate receipts")
	}
	if len(aggregates) == 1 {
		if string(aggregates[0].Report) != string(aggregateRaw) {
			return result, c.Fail(c.StoreIntegrityError, "existing aggregate receipt differs from reconciled exact postimages")
		}
		aggregateDigest = aggregates[0].Manifest.Digest
	} else {
		directory, dirErr := deliveryArtifactDirectory(output, operationID, "receipt")
		if dirErr != nil {
			return result, dirErr
		}
		aggregateArtifact, publishErr := s.publishDeliveryArtifact(ctx, task, directory, "AGGREGATE_RECEIPT", operationID, aggregateRaw, nil, plan.TargetRoot)
		if publishErr != nil {
			return result, publishErr
		}
		aggregateDigest = aggregateArtifact.Manifest.Digest
	}
	if err = s.checkDeliveryReconcileAuthority(ctx, task, effect); err != nil {
		return result, err
	}
	current, err := s.State(ctx, task)
	if err != nil {
		return result, err
	}
	if current.TaskSeq != effect.SourceTaskSeq+1 || current.KernelGeneration != effect.Generation || s.Journal.Generation() < current.KernelGeneration {
		return result, c.Fail(c.StaleAuthority, "task cursor changed while reconciling UNKNOWN delivery")
	}
	effect.ReceiptDigest = aggregateDigest
	if effect.Phase == c.DeliveryApplyPhase {
		effect.Status = c.DeliveryApplied
		result.Status = "RECONCILED_APPLIED_EXACT_TOUCHED_FILES"
	} else {
		effect.Status = c.DeliveryRestored
		result.Status = "RECONCILED_RESTORED_EXACT_TOUCHED_FILES"
	}
	if s.Journal.Generation() > current.KernelGeneration {
		_, err = s.recordReconciledDeliveryEffect(ctx, current, effect)
	} else {
		_, err = s.recordDeliveryEffect(ctx, current, effect)
	}
	if err != nil {
		return result, err
	}
	result.ReceiptDigest = aggregateDigest
	result.Reason = "the exact sealed postimages were reacquired and verified; only durable receipts were completed, and no live file was written"
	return result, nil
}

func (s *Session) checkDeliveryReconcileAuthority(ctx context.Context, task string, effect c.DeliveryEffect) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return err
	}
	if state.Execution != c.Terminated || state.InputBarrier || len(state.PendingInputIDs) != 0 || state.TaskSeq != effect.SourceTaskSeq+1 || state.DocumentDigest != effect.SourceDocument || state.SpecVersion != effect.SpecVersion || state.PolicyEpoch != effect.PolicyEpoch || state.KernelGeneration != effect.Generation || s.Journal.Generation() < state.KernelGeneration || state.CandidateDigest != effect.CandidateDigest || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(state) != nil || !tokensQuiescent(state) {
		return c.Fail(c.StaleAuthority, "task cursor or current session authority changed during UNKNOWN reconciliation")
	}
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return err
	}
	found := false
	for _, prior := range state.DeliveryEffects {
		if prior.OperationID == effect.OperationID && prior.Status == c.DeliveryUnknown && prior.Phase == effect.Phase && prior.PlanDigest == effect.PlanDigest {
			found = true
		}
		if prior.Status == c.DeliveryUnknown && prior.OperationID != effect.OperationID {
			return c.Fail(c.Conflict, "another UNKNOWN delivery effect became active")
		}
	}
	if !found {
		return c.Fail(c.StaleAuthority, "UNKNOWN delivery intent is no longer the active cursor")
	}
	return nil
}

func deliveryPlanOutputBase(planDirectory, operationID string) (string, error) {
	suffix := ".delivery-" + operationID + ".plan"
	if !strings.HasSuffix(planDirectory, suffix) {
		return "", c.Fail(c.StoreIntegrityError, "delivery plan directory does not bind its operation ID")
	}
	output := strings.TrimSuffix(planDirectory, suffix)
	if output == "" || filepath.Clean(output) != output {
		return "", errors.New("delivery plan output base is invalid")
	}
	return output, nil
}
