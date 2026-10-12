package contracts

import "strings"

const ReadWorkerAdmissionCharged = "CHARGED"
const MaxReadWorkerAdmissions = 256

// ReadWorkerAdmission is the durable, conservative parent-budget charge for
// one fixed read-only worker request. The entire active-time ceiling is charged
// before dispatch, so a crash or cancellation cannot create free retries.
type ReadWorkerAdmission struct {
	OperationID          string `json:"operation_id"`
	RequestDigest        string `json:"request_digest"`
	SourceDocumentDigest string `json:"source_document_digest"`
	CandidateDigest      string `json:"candidate_digest"`
	PolicyDigest         string `json:"policy_digest"`
	AdmissionDigest      string `json:"admission_digest"`
	SourceTaskSeq        int64  `json:"source_task_seq"`
	SpecVersion          int64  `json:"spec_version"`
	PolicyEpoch          int64  `json:"policy_epoch"`
	KernelGeneration     int64  `json:"kernel_generation"`
	WorkerCount          int    `json:"worker_count"`
	ChargedSteps         int64  `json:"charged_steps"`
	ChargedToolCalls     int64  `json:"charged_tool_calls"`
	ChargedActiveMillis  int64  `json:"charged_active_millis"`
	Status               string `json:"status"`
}

func ValidReadWorkerOperationID(value string) bool {
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

func (a ReadWorkerAdmission) Digest() (string, error) {
	return Digest(struct {
		OperationID          string `json:"operation_id"`
		RequestDigest        string `json:"request_digest"`
		SourceDocumentDigest string `json:"source_document_digest"`
		CandidateDigest      string `json:"candidate_digest"`
		PolicyDigest         string `json:"policy_digest"`
		SourceTaskSeq        int64  `json:"source_task_seq"`
		SpecVersion          int64  `json:"spec_version"`
		PolicyEpoch          int64  `json:"policy_epoch"`
		KernelGeneration     int64  `json:"kernel_generation"`
		WorkerCount          int    `json:"worker_count"`
		ChargedSteps         int64  `json:"charged_steps"`
		ChargedToolCalls     int64  `json:"charged_tool_calls"`
		ChargedActiveMillis  int64  `json:"charged_active_millis"`
		Status               string `json:"status"`
	}{a.OperationID, a.RequestDigest, a.SourceDocumentDigest, a.CandidateDigest, a.PolicyDigest, a.SourceTaskSeq, a.SpecVersion, a.PolicyEpoch, a.KernelGeneration, a.WorkerCount, a.ChargedSteps, a.ChargedToolCalls, a.ChargedActiveMillis, a.Status})
}

func (a ReadWorkerAdmission) Validate() error {
	if !ValidReadWorkerOperationID(a.OperationID) || !ValidDigest(a.RequestDigest) ||
		!ValidDigest(a.SourceDocumentDigest) || !ValidDigest(a.CandidateDigest) ||
		!ValidDigest(a.PolicyDigest) || !ValidDigest(a.AdmissionDigest) ||
		a.SourceTaskSeq < 1 || a.SpecVersion < 1 || a.PolicyEpoch < 1 || a.KernelGeneration < 1 ||
		a.WorkerCount < 2 || a.WorkerCount > 4 || a.ChargedSteps != int64(a.WorkerCount) ||
		a.ChargedToolCalls != int64(a.WorkerCount) || a.ChargedActiveMillis < 250 ||
		a.ChargedActiveMillis > 30000 || a.Status != ReadWorkerAdmissionCharged {
		return Fail(InvalidArgument, "invalid bounded read-worker budget admission")
	}
	digest, err := a.Digest()
	if err != nil || digest != a.AdmissionDigest {
		return Fail(StoreIntegrityError, "read-worker admission digest mismatch")
	}
	return nil
}
