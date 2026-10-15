package agent

import (
	"context"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

// Called only at kernel-proven pre-dispatch boundaries. A reserved request
// cannot simply disappear: its paired token/resource accounts need the same
// durable zero-usage receipt, including when a fixture worker's lease expires.
func (s *Session) settleModelNoDispatch(ctx context.Context, state c.TaskState, doc Document, request model.Request, requestDigest, blocker string) (c.TaskState, error) {
	provider, _, _ := contextProfile(doc)
	raw, err := c.CanonicalV1(model.NoDispatchReceipt{SchemaVersion: 1, RequestID: request.ID, RequestDigest: requestDigest, ProfileDigest: tokenProfile(doc), Provider: provider, Model: request.Model, Decision: "KERNEL_PREFLIGHT_DECLINED"})
	if err != nil {
		return state, err
	}
	doc.LastResponseBlob, err = s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		return state, err
	}
	doc.LastNoDispatch = true
	doc.Budget.ReservedInput, doc.Budget.ReservedOutput = 0, 0
	doc.Pending = nil
	controlCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return s.stop(controlCtx, state, doc, blocker, c.WaitingResource)
}
