package agent

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
)

// DeliveryRestoreResult reports why an owned live restore cannot be issued.
// A preview or patch is not proof that Viber changed the live target.
type DeliveryRestoreResult struct {
	SchemaVersion       int    `json:"schema_version"`
	TaskID              string `json:"task_id"`
	Status              string `json:"status"`
	Backend             string `json:"backend"`
	TargetWritten       bool   `json:"target_written"`
	OwnedReceiptPresent bool   `json:"owned_receipt_present"`
	Reason              string `json:"reason"`
}

// DeliveryRestore checks the task exists, then fails closed. The current
// candidate/patch-only backend cannot create an applied ownership receipt, so
// there are no changes that Viber can safely claim and restore.
func (s *Session) DeliveryRestore(ctx context.Context, task string) (DeliveryRestoreResult, error) {
	result := DeliveryRestoreResult{SchemaVersion: 1, TaskID: task, Status: "UNSUPPORTED_NO_APPLIED_RECEIPT", Backend: delivery.LiveBackendUnsupported, TargetWritten: false, OwnedReceiptPresent: false, Reason: "restore requires a trusted receipt proving which live files Viber applied; live apply is unsupported and a candidate preview is not an ownership receipt"}
	if task == "" {
		return result, c.Fail(c.InvalidArgument, "task ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.State(ctx, task); err != nil {
		return result, err
	}
	return result, nil
}
