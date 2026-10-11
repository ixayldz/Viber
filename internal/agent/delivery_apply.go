package agent

import (
	"context"
	"errors"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// DeliveryApplyResult distinguishes a candidate package from an exact set of
// live file writes. Whole-workspace quality remains UNKNOWN after native apply.
type DeliveryApplyResult struct {
	SchemaVersion    int                      `json:"schema_version"`
	TaskID           string                   `json:"task_id"`
	TaskSeq          int64                    `json:"task_seq"`
	Status           string                   `json:"status"`
	Backend          string                   `json:"backend"`
	TargetWritten    bool                     `json:"target_written"`
	OperationID      string                   `json:"operation_id,omitempty"`
	PlanDigest       string                   `json:"plan_manifest_digest,omitempty"`
	PlanDirectory    string                   `json:"plan_package_directory,omitempty"`
	ReceiptDigest    string                   `json:"receipt_manifest_digest,omitempty"`
	TouchedScope     string                   `json:"touched_scope,omitempty"`
	WorkspaceQuality string                   `json:"whole_workspace_quality,omitempty"`
	TargetDigest     string                   `json:"target_digest"`
	PackageDirectory string                   `json:"package_directory"`
	ManifestDigest   string                   `json:"manifest_digest"`
	Decisions        []delivery.MergeDecision `json:"per_file_decisions"`
	Changeset        *delivery.Changeset      `json:"changeset,omitempty"`
	Reason           string                   `json:"reason"`
}

// DeliveryApply always creates a fresh B/C/U review package. A live write is
// available only when the caller explicitly opts into the Windows fixed-NTFS
// exclusive-handle profile; other hosts and unsupported file sets stay
// candidate/patch-only.
func (s *Session) DeliveryApply(ctx context.Context, task, output string, options ...DeliveryApplyOptions) (DeliveryApplyResult, error) {
	result := DeliveryApplyResult{SchemaVersion: 1, TaskID: task, Backend: delivery.LiveBackendUnsupported, TargetWritten: false, Decisions: []delivery.MergeDecision{}}
	if task == "" || output == "" || len(options) > 1 {
		return result, c.Fail(c.InvalidArgument, "task and fresh review package output are required; provide at most one native backend option")
	}
	exclusiveNTFS := len(options) == 1 && options[0].ExclusiveNTFS
	if exclusiveNTFS {
		s.mu.Lock()
		_, _, err := s.rebindDeliveryAuthorityLocked(ctx, task, nil)
		s.mu.Unlock()
		if err != nil {
			return result, err
		}
	}
	preview, err := s.DeliveryPreview(ctx, task, output)
	if err != nil {
		return result, err
	}
	result.TaskSeq = preview.TaskSeq
	result.TargetDigest = preview.Preview.Target
	result.Decisions = append(result.Decisions, preview.Preview.Decisions...)
	result.Changeset = preview.Changeset
	result.ManifestDigest = preview.Digest
	result.PackageDirectory, err = fileguard.ResolveProspective(output)
	if err != nil {
		return result, err
	}
	if preview.Preview.Status == "CONFLICT" {
		result.Status = "CONFLICT"
		result.Reason = "the current workspace has unresolved merge conflicts; no live write was attempted; review preview.json in the package"
		return result, nil
	}
	if preview.Preview.Status != "READY" || preview.Preview.LiveWorkspaceWritten || result.Changeset == nil {
		return result, c.Fail(c.StoreIntegrityError, "delivery preview did not produce a complete no-write preflight")
	}
	if !exclusiveNTFS {
		result.Status = "UNSUPPORTED_STRONG_EXCLUSIVITY"
		result.Reason = "live apply was not opted into; use the fresh changeset and patch for an explicit user-controlled apply"
		return result, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return result, err
	}
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return result, err
	}
	if state.Execution != c.Terminated || state.InputBarrier || len(state.PendingInputIDs) != 0 || state.TaskSeq != preview.TaskSeq || state.DocumentDigest != preview.Document || state.SpecVersion != preview.SpecVersion || state.PolicyEpoch != preview.PolicyEpoch || state.Quality != preview.HistoricalCandidateQuality || state.CandidateDigest != preview.Preview.Candidate || state.KernelGeneration != s.Journal.Generation() || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(state) != nil || !tokensQuiescent(state) {
		return result, c.Fail(c.StaleRequest, "fresh preview no longer binds the current terminal task cursor")
	}
	for _, effect := range state.DeliveryEffects {
		if effect.Status == c.DeliveryUnknown {
			return result, c.Fail(c.Conflict, "a prior delivery effect is UNKNOWN and must be reconciled before another apply")
		}
	}
	exactVerifiedCandidate := state.Quality == c.Verified && state.CandidateDigest == preview.Preview.Result
	if !exactVerifiedCandidate && !doc.AllowUnverified {
		return result, c.Fail(c.PolicyDenied, "the merged live result differs from the verified candidate; fresh merged-candidate reverification or an explicit unverified-result policy is required")
	}
	if state.Quality != c.Verified && state.Quality != c.Unverified && state.Quality != c.AcceptedWithWaiver {
		return result, c.Fail(c.PolicyDenied, "task quality is outside the native delivery profile")
	}
	if result.Changeset == nil || len(result.Changeset.Changes) == 0 {
		result.Status = "NO_LIVE_CHANGES"
		result.Backend = "WINDOWS_FIXED_NTFS_EXCLUSIVE_HANDLES_V1"
		result.TouchedScope = "NONE"
		result.WorkspaceQuality = "UNKNOWN"
		result.Reason = "the fresh merge contains no file changes to apply"
		return result, nil
	}
	for _, change := range result.Changeset.Changes {
		if change.Kind != "MODIFIED" || change.BeforeMode != change.AfterMode {
			result.Status = "UNSUPPORTED_NATIVE_CHANGESET"
			result.Backend = "WINDOWS_FIXED_NTFS_EXCLUSIVE_HANDLES_V1"
			result.WorkspaceQuality = "UNKNOWN"
			result.Reason = "the restricted backend accepts only same-mode modifications to existing regular files; additions, deletions and mode changes remain in the review package"
			return result, nil
		}
	}
	if err = currentOperatorEffect(doc, "workspace.apply"); err != nil {
		return result, err
	}
	if s.Journal.Generation() != state.KernelGeneration {
		return result, c.Fail(c.StaleAuthority, "native apply requires the current journal generation")
	}
	names := make([]string, 0, len(result.Changeset.Changes)*2)
	for _, change := range result.Changeset.Changes {
		names = append(names, "current/"+change.Path, "merged/"+change.Path)
	}
	manifest, contents, err := s.readRegisteredDeliveryPreviewFiles(ctx, output, task, preview.Digest, names)
	if err != nil {
		return result, err
	}
	if manifest.TaskSeq != state.TaskSeq || manifest.Document != state.DocumentDigest || manifest.SpecVersion != state.SpecVersion || manifest.PolicyEpoch != state.PolicyEpoch || manifest.Preview.Digest != preview.Preview.Digest || manifest.Preview.Result != preview.Preview.Result || manifest.HistoricalCandidateQuality != state.Quality {
		return result, c.Fail(c.StaleBase, "registered review package no longer binds the current task and target preview")
	}
	baseline, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		return result, err
	}
	operationID := newID("")
	plan, packageFiles, err := buildDeliveryOperationPlan(state, manifest, baseline.Snapshot.Root, contents, operationID)
	if err != nil {
		var failure *c.Error
		if errors.As(err, &failure) && failure.Code == c.UnsupportedCapability {
			result.Status = "UNSUPPORTED_NATIVE_PROFILE"
			result.Backend = "WINDOWS_FIXED_NTFS_EXCLUSIVE_HANDLES_V1"
			result.WorkspaceQuality = "UNKNOWN"
			result.Reason = "the current host, volume or file set does not satisfy the exclusive native profile: " + err.Error()
			return result, nil
		}
		if errors.As(err, &failure) && failure.Code == c.StaleBase {
			result.Status = "CONFLICT_STALE_TARGET"
			result.Reason = "the live target changed after preview; no target write was attempted"
			return result, nil
		}
		return result, err
	}
	owner, err := privacyOperation(ctx, *s.privacy)
	if err != nil {
		return result, err
	}
	defer func() { _ = owner.Close() }()
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return result, err
	}
	lease, err := delivery.AcquireNativeTarget(ctx, plan.Seal, stringForPlan(plan))
	if err != nil {
		var failure *c.Error
		if errors.As(err, &failure) && failure.Code == c.UnsupportedCapability {
			result.Status = "EXCLUSIVE_HANDLE_DENIED"
			result.Backend = plan.Seal.Profile
			result.WorkspaceQuality = "UNKNOWN"
			result.Reason = "exclusive native handles could not be acquired; no target write was attempted: " + err.Error()
			return result, nil
		}
		if errors.As(err, &failure) && failure.Code == c.StaleBase {
			result.Status = "CONFLICT_STALE_TARGET"
			result.Reason = "the live target changed before exclusive acquisition; no target write was attempted"
			return result, nil
		}
		return result, err
	}
	defer func() { _ = lease.Close() }()
	if state.TaskSeq != manifest.TaskSeq || state.KernelGeneration != s.Journal.Generation() {
		return result, c.Fail(c.StaleAuthority, "delivery cursor changed before native target acquisition")
	}
	if err = currentOperatorEffect(doc, "workspace.apply"); err != nil {
		return result, err
	}
	applied, applyErr := s.writeDeliveryPlan(ctx, output, state, doc, plan, packageFiles, "", lease)
	applied.PackageDirectory = result.PackageDirectory
	applied.ManifestDigest = result.ManifestDigest
	applied.Decisions = result.Decisions
	applied.Changeset = result.Changeset
	return applied, applyErr
}

func stringForPlan(plan deliveryOperationPlan) map[string]string {
	expected := make(map[string]string, len(plan.Files))
	for _, file := range plan.Files {
		expected[file.Path] = file.BeforeDigest
	}
	return expected
}
