package agent

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// DeliveryApplyResult is a live-apply preflight paired with a fresh review
// package. It is not a write receipt: this build has no backend that can
// exclude hostile writers to a user's live workspace.
type DeliveryApplyResult struct {
	SchemaVersion    int                      `json:"schema_version"`
	TaskID           string                   `json:"task_id"`
	TaskSeq          int64                    `json:"task_seq"`
	Status           string                   `json:"status"`
	Backend          string                   `json:"backend"`
	TargetWritten    bool                     `json:"target_written"`
	TargetDigest     string                   `json:"target_digest"`
	PackageDirectory string                   `json:"package_directory"`
	ManifestDigest   string                   `json:"manifest_digest"`
	Decisions        []delivery.MergeDecision `json:"per_file_decisions"`
	Changeset        *delivery.Changeset      `json:"changeset,omitempty"`
	Reason           string                   `json:"reason"`
}

// DeliveryApply prepares the exact current B/C/U merge, changeset and per-file
// preconditions in a fresh managed package. It deliberately does not modify
// the live workspace. Hash checks cannot close a write race against existing
// descriptors or mmap writers, and this binary has no hostile-writer exclusion
// backend to make such a write safe.
func (s *Session) DeliveryApply(ctx context.Context, task, output string) (DeliveryApplyResult, error) {
	result := DeliveryApplyResult{SchemaVersion: 1, TaskID: task, Backend: delivery.LiveBackendUnsupported, TargetWritten: false, Decisions: []delivery.MergeDecision{}}
	if task == "" || output == "" {
		return result, c.Fail(c.InvalidArgument, "task and fresh review package output are required")
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
	result.Status = "UNSUPPORTED_STRONG_EXCLUSIVITY"
	result.Reason = "live apply is unavailable because this backend cannot exclude existing or concurrent workspace writers; use the fresh changeset and patch for an explicit user-controlled apply"
	return result, nil
}
