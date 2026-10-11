package kernel

import (
	"reflect"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func findDeliveryEffect(effects []c.DeliveryEffect, operationID string) (c.DeliveryEffect, bool) {
	for _, effect := range effects {
		if effect.OperationID == operationID {
			return effect, true
		}
	}
	return c.DeliveryEffect{}, false
}

func deliveryStateQuiescent(state c.TaskState) bool {
	if state.Tokens != nil {
		for _, reservation := range state.Tokens.Reservations {
			if reservation.Status == "RESERVED" || reservation.Status == "UNKNOWN" {
				return false
			}
		}
	}
	if state.Resources != nil {
		for _, reservation := range state.Resources.Reservations {
			if reservation.Status == "RESERVED" || reservation.Status == "UNKNOWN" {
				return false
			}
		}
	}
	return true
}

func deliveryGenerationRebindAllowed(state c.TaskState, payload c.EventPayload, event c.Event) bool {
	return event.Actor == "kernel" && state.Execution == c.Terminated && !state.InputBarrier && len(state.PendingInputIDs) == 0 && state.Deletion == nil && deliveryStateQuiescent(state) && event.KernelGeneration > state.KernelGeneration && c.ValidDigest(payload.DocumentDigest) && payload.DocumentDigest == state.DocumentDigest && payload.SnapshotDigest == state.CandidateDigest
}

func applyDeliveryEffect(previous []c.DeliveryEffect, incoming c.DeliveryEffect) ([]c.DeliveryEffect, error) {
	if err := incoming.Validate(); err != nil {
		return nil, err
	}
	index := -1
	for i, prior := range previous {
		if err := prior.Validate(); err != nil {
			return nil, c.Fail(c.StoreIntegrityError, "durable delivery effect history is invalid")
		}
		if prior.OperationID == incoming.OperationID {
			index = i
		}
		if prior.Status == c.DeliveryUnknown && (index < 0 || i != index) {
			return nil, c.Fail(c.Conflict, "another delivery effect is awaiting reconciliation")
		}
	}
	if index < 0 {
		if len(previous) >= 128 {
			return nil, c.Fail(c.UnsupportedCapability, "delivery effect history quota exceeded")
		}
		if incoming.Status != c.DeliveryUnknown {
			return nil, c.Fail(c.PolicyDenied, "delivery effect must durably enter UNKNOWN before completion")
		}
		if incoming.Phase == c.DeliveryRestorePhase {
			parent := -1
			for i, prior := range previous {
				if prior.OperationID == incoming.ParentOperationID {
					parent = i
					break
				}
			}
			if parent < 0 || previous[parent].Phase != c.DeliveryApplyPhase || previous[parent].Status != c.DeliveryApplied ||
				previous[parent].CandidateDigest != incoming.CandidateDigest || previous[parent].TargetDigest != incoming.TargetDigest ||
				previous[parent].ResultDigest != incoming.ResultDigest {
				return nil, c.Fail(c.PolicyDenied, "restore effect must bind one completed owned apply")
			}
			for _, prior := range previous {
				if prior.Phase == c.DeliveryRestorePhase && prior.ParentOperationID == incoming.ParentOperationID {
					return nil, c.Fail(c.Conflict, "owned apply already has a restore operation")
				}
			}
		}
		return append(append([]c.DeliveryEffect{}, previous...), incoming), nil
	}
	prior := previous[index]
	if prior.Status != c.DeliveryUnknown {
		return nil, c.Fail(c.Conflict, "completed delivery effect cannot be replaced")
	}
	prior.Status = c.DeliveryUnknown
	prior.ReceiptDigest = ""
	incomingWithoutReceipt := incoming
	incomingWithoutReceipt.Status = c.DeliveryUnknown
	incomingWithoutReceipt.ReceiptDigest = ""
	if prior.Status != c.DeliveryUnknown || incoming.Status == c.DeliveryUnknown || !reflect.DeepEqual(prior, incomingWithoutReceipt) {
		return nil, c.Fail(c.PolicyDenied, "delivery effect completion changed its durable intent")
	}
	if incoming.Phase == c.DeliveryApplyPhase && incoming.Status != c.DeliveryApplied || incoming.Phase == c.DeliveryRestorePhase && incoming.Status != c.DeliveryRestored {
		return nil, c.Fail(c.PolicyDenied, "delivery effect completion status does not match its phase")
	}
	result := append([]c.DeliveryEffect{}, previous...)
	result[index] = incoming
	return result, nil
}
