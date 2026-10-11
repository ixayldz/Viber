package contracts

import "strings"

const (
	DeliveryApplyPhase   = "APPLY"
	DeliveryRestorePhase = "RESTORE"
	DeliveryUnknown      = "UNKNOWN"
	DeliveryApplied      = "APPLIED"
	DeliveryRestored     = "RESTORED"
)

// DeliveryEffect is a typed durable cursor for one keyless live-target effect.
// Package digests bind the private managed intent and receipt; raw source bytes
// and filesystem identifiers remain in those registered packages.
type DeliveryEffect struct {
	OperationID       string `json:"operation_id"`
	ParentOperationID string `json:"parent_operation_id,omitempty"`
	Phase             string `json:"phase"`
	Status            string `json:"status"`
	PlanDigest        string `json:"plan_digest"`
	ReceiptDigest     string `json:"receipt_digest,omitempty"`
	SourceDocument    string `json:"source_document_digest"`
	SourceTaskSeq     int64  `json:"source_task_seq"`
	SpecVersion       int64  `json:"spec_version"`
	PolicyEpoch       int64  `json:"policy_epoch"`
	Generation        int64  `json:"kernel_generation"`
	CandidateDigest   string `json:"candidate_digest"`
	TargetDigest      string `json:"target_digest"`
	ResultDigest      string `json:"result_digest"`
}

func validDeliveryOperationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (effect DeliveryEffect) Validate() error {
	if !validDeliveryOperationID(effect.OperationID) ||
		(effect.ParentOperationID != "" && !validDeliveryOperationID(effect.ParentOperationID)) ||
		(effect.Phase != DeliveryApplyPhase && effect.Phase != DeliveryRestorePhase) ||
		(effect.Status != DeliveryUnknown && effect.Status != DeliveryApplied && effect.Status != DeliveryRestored) ||
		(effect.Phase == DeliveryApplyPhase && effect.Status == DeliveryRestored) ||
		(effect.Phase == DeliveryRestorePhase && effect.Status == DeliveryApplied) ||
		(effect.Phase == DeliveryApplyPhase && effect.ParentOperationID != "") ||
		(effect.Phase == DeliveryRestorePhase && effect.ParentOperationID == "") ||
		!ValidDigest(effect.PlanDigest) || effect.SourceTaskSeq < 1 ||
		!ValidDigest(effect.SourceDocument) || effect.SpecVersion < 1 ||
		effect.PolicyEpoch < 1 || effect.Generation < 1 ||
		!ValidDigest(effect.CandidateDigest) || !ValidDigest(effect.TargetDigest) ||
		!ValidDigest(effect.ResultDigest) ||
		(effect.Status == DeliveryUnknown && effect.ReceiptDigest != "") ||
		(effect.Status != DeliveryUnknown && !ValidDigest(effect.ReceiptDigest)) {
		return Fail(InvalidArgument, "invalid durable delivery effect binding")
	}
	return nil
}
