package agent

import (
	"context"
	"errors"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
)

// DeliveryRestoreResult identifies an exact owned operation and reports only
// the files present in its durable aggregate receipt.
type DeliveryRestoreResult struct {
	SchemaVersion       int    `json:"schema_version"`
	TaskID              string `json:"task_id"`
	Status              string `json:"status"`
	Backend             string `json:"backend"`
	TargetWritten       bool   `json:"target_written"`
	OwnedReceiptPresent bool   `json:"owned_receipt_present"`
	OperationID         string `json:"operation_id,omitempty"`
	RestoreOperationID  string `json:"restore_operation_id,omitempty"`
	PlanDigest          string `json:"plan_manifest_digest,omitempty"`
	ReceiptDigest       string `json:"receipt_manifest_digest,omitempty"`
	TouchedScope        string `json:"touched_scope,omitempty"`
	WorkspaceQuality    string `json:"whole_workspace_quality,omitempty"`
	Reason              string `json:"reason"`
}

// DeliveryRestore requires the exact operation ID returned by native apply.
// Calling it without a checkpoint remains a no-write compatibility path.
func (s *Session) DeliveryRestore(ctx context.Context, task string, checkpoint ...string) (DeliveryRestoreResult, error) {
	result := DeliveryRestoreResult{SchemaVersion: 1, TaskID: task, Status: "UNSUPPORTED_NO_APPLIED_RECEIPT", Backend: delivery.LiveBackendUnsupported, TargetWritten: false, OwnedReceiptPresent: false, Reason: "restore requires the exact checkpoint ID from a registered APPLIED operation"}
	if task == "" || len(checkpoint) > 1 {
		return result, c.Fail(c.InvalidArgument, "task ID and at most one exact applied operation checkpoint are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return result, err
	}
	if len(checkpoint) == 0 || checkpoint[0] == "" {
		return result, nil
	}
	applyID := checkpoint[0]
	if !validDeliveryOperationID(applyID) {
		return result, c.Fail(c.InvalidArgument, "checkpoint must be the exact 32-character lowercase operation ID")
	}
	result.OperationID = applyID
	applyEffect, found := findDeliveryEffect(state, applyID)
	if !found || applyEffect.Phase != c.DeliveryApplyPhase || applyEffect.Status != c.DeliveryApplied {
		result.Status = "UNSUPPORTED_NO_APPLIED_RECEIPT"
		result.Reason = "the exact checkpoint is not a durably APPLIED operation owned by this task"
		return result, nil
	}
	result.OwnedReceiptPresent = true
	result.Backend = "WINDOWS_FIXED_NTFS_EXCLUSIVE_HANDLES_V1"
	for _, effect := range state.DeliveryEffects {
		if effect.Status == c.DeliveryUnknown {
			result.Status = "UNKNOWN_EFFECT_PENDING"
			result.Reason = "another delivery operation is UNKNOWN; reconcile it before restore"
			return result, nil
		}
		if effect.Phase == c.DeliveryRestorePhase && effect.ParentOperationID == applyID && effect.Status == c.DeliveryRestored {
			result.Status = "ALREADY_RESTORED"
			result.RestoreOperationID = effect.OperationID
			result.ReceiptDigest = effect.ReceiptDigest
			result.Reason = "this exact applied operation already has a registered RESTORED receipt"
			return result, nil
		}
	}
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return result, err
	}
	if state.Execution != c.Terminated || state.InputBarrier || len(state.PendingInputIDs) != 0 || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(state) != nil || !tokensQuiescent(state) || state.DocumentDigest != applyEffect.SourceDocument || state.SpecVersion != applyEffect.SpecVersion || state.PolicyEpoch != applyEffect.PolicyEpoch || state.CandidateDigest != applyEffect.CandidateDigest {
		return result, c.Fail(c.StaleAuthority, "owned restore requires the current terminal task authority")
	}
	applyArtifact, applyPlan, err := s.readDeliveryPlan(ctx, task, applyID)
	if err != nil {
		return result, err
	}
	if applyPlan.Phase != c.DeliveryApplyPhase || applyPlan.ParentOperationID != "" || applyPlan.SourceDocument != applyEffect.SourceDocument || applyPlan.SourceTaskSeq != applyEffect.SourceTaskSeq || applyPlan.CandidateDigest != applyEffect.CandidateDigest || applyPlan.TargetDigest != applyEffect.TargetDigest || applyPlan.ResultDigest != applyEffect.ResultDigest || !deliveryArtifactDigestMatches(applyArtifact, applyEffect.PlanDigest) {
		return result, c.Fail(c.StoreIntegrityError, "applied checkpoint and immutable plan differ")
	}
	aggregate, _, err := s.loadDeliveryAggregate(ctx, task, applyEffect)
	if err != nil {
		return result, err
	}
	receipts, err := s.loadDeliveryFileReceipts(ctx, task, applyEffect)
	if err != nil {
		return result, err
	}
	if len(aggregate.Files) != len(applyPlan.Files) || len(receipts) != len(applyPlan.Files) {
		return result, c.Fail(c.StoreIntegrityError, "applied receipt does not cover the complete owned changeset")
	}
	if state.KernelGeneration != s.Journal.Generation() {
		state, doc, err = s.rebindDeliveryAuthorityLocked(ctx, task, &applyEffect)
		if err != nil {
			return result, err
		}
		if state.KernelGeneration != s.Journal.Generation() || state.DocumentDigest != applyEffect.SourceDocument || state.SpecVersion != applyEffect.SpecVersion || state.PolicyEpoch != applyEffect.PolicyEpoch || state.CandidateDigest != applyEffect.CandidateDigest {
			return result, c.Fail(c.StaleAuthority, "delivery authority changed while rebinding for owned restore")
		}
	}
	_, afterBytes := planBytesByName(applyPlan, applyArtifact)
	beforeBytes, _ := planBytesByName(applyPlan, applyArtifact)
	reverseFiles := make([]deliveryOperationFile, len(applyPlan.Files))
	intents := make([]delivery.NativeFileIntent, len(applyPlan.Files))
	packageFiles := map[string][]byte{}
	for i, file := range applyPlan.Files {
		receipt, ok := receipts[file.Path]
		if !ok || receipt.Phase != c.DeliveryApplyPhase || receipt.ParentOperationID != "" || receipt.BeforeDigest != file.BeforeDigest || receipt.AfterDigest != file.AfterDigest || receipt.ObservedAfter != file.AfterDigest || receipt.FileIdentity != applyPlan.Seal.Files[i].Identity || receipt.Observation != "SYNCED_READBACK" {
			return result, c.Fail(c.StoreIntegrityError, "applied file receipt does not prove the exact owned postimage")
		}
		reverseFiles[i] = deliveryOperationFile{Path: file.Path, BeforeDigest: file.AfterDigest, AfterDigest: file.BeforeDigest, Mode: file.Mode}
		intents[i] = delivery.NativeFileIntent{Path: file.Path, Before: afterBytes[file.Path], After: beforeBytes[file.Path], Mode: file.Mode}
		packageFiles["before/"+file.Path] = append([]byte{}, afterBytes[file.Path]...)
		packageFiles["after/"+file.Path] = append([]byte{}, beforeBytes[file.Path]...)
	}
	owner, err := privacyOperation(ctx, *s.privacy)
	if err != nil {
		return result, err
	}
	defer func() { _ = owner.Close() }()
	seal, err := delivery.InspectNativeTarget(applyPlan.TargetRoot, intents)
	if err != nil {
		result.Status = "RESTORE_CONFLICT"
		result.Reason = "a touched file no longer matches the owned postimage; no file was restored"
		return result, nil
	}
	if len(seal.Files) != len(applyPlan.Seal.Files) || len(seal.Directories) != len(applyPlan.Seal.Directories) || seal.VolumeIdentity != applyPlan.Seal.VolumeIdentity {
		result.Status = "RESTORE_CONFLICT"
		result.Reason = "the target namespace identity changed since apply; no file was restored"
		return result, nil
	}
	for i := range seal.Files {
		if seal.Files[i].Path != applyPlan.Seal.Files[i].Path || seal.Files[i].Identity != applyPlan.Seal.Files[i].Identity {
			result.Status = "RESTORE_CONFLICT"
			result.Reason = "a touched file was replaced since apply; no file was restored"
			return result, nil
		}
	}
	for i := range seal.Directories {
		if seal.Directories[i] != applyPlan.Seal.Directories[i] {
			result.Status = "RESTORE_CONFLICT"
			result.Reason = "a target directory was replaced since apply; no file was restored"
			return result, nil
		}
	}
	operationID := newID("")
	plan := deliveryOperationPlan{SchemaVersion: 1, OperationID: operationID, ParentOperationID: applyID, Phase: c.DeliveryRestorePhase, TaskID: task, TargetRoot: applyPlan.TargetRoot, SourceDocument: state.DocumentDigest, SourceTaskSeq: state.TaskSeq, SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch, Generation: state.KernelGeneration, CandidateDigest: applyEffect.CandidateDigest, TargetDigest: applyEffect.TargetDigest, ResultDigest: applyEffect.ResultDigest, PreviewDigest: applyPlan.PreviewDigest, PreviewManifestDigest: applyPlan.PreviewManifestDigest, Files: reverseFiles, Seal: seal}
	if !stableDeliveryFiles(plan.Files) {
		return result, c.Fail(c.StoreIntegrityError, "owned restore plan file ordering is invalid")
	}
	lease, err := delivery.AcquireNativeTarget(ctx, plan.Seal, deliveryExpected(plan.Files))
	if err != nil {
		result.Status = "RESTORE_CONFLICT"
		result.Reason = "exclusive native acquisition failed because a touched file is open, replaced or changed; no file was restored"
		return result, nil
	}
	defer func() { _ = lease.Close() }()
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return result, err
	}
	result.WorkspaceQuality = "UNKNOWN"
	result.TouchedScope = "EXACT_TOUCHED_FILES"
	writeResult, writeErr := s.writeDeliveryPlan(ctx, applyArtifact.Directory, state, doc, plan, packageFiles, applyID, lease)
	result.Status = writeResult.Status
	result.TargetWritten = writeResult.TargetWritten
	result.RestoreOperationID = writeResult.OperationID
	result.PlanDigest = writeResult.PlanDigest
	result.ReceiptDigest = writeResult.ReceiptDigest
	result.TouchedScope = writeResult.TouchedScope
	result.WorkspaceQuality = writeResult.WorkspaceQuality
	result.Reason = writeResult.Reason
	if errors.Is(writeErr, context.Canceled) || errors.Is(writeErr, context.DeadlineExceeded) {
		return result, writeErr
	}
	return result, writeErr
}

func deliveryExpected(files []deliveryOperationFile) map[string]string {
	result := make(map[string]string, len(files))
	for _, file := range files {
		result[file.Path] = file.BeforeDigest
	}
	return result
}
