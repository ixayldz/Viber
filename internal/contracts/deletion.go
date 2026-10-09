package contracts

// TaskDeletion retains only a scope/plan/content identity, not deleted bytes.
// PURGED denotes managed local task artifacts, never physical-media erasure,
// provider-side deletion or destruction of arbitrary external copies.
type TaskDeletion struct {
	SchemaVersion    int    `json:"schema_version"`
	PlanDigest       string `json:"plan_digest"`
	OriginalDocument string `json:"original_document_digest"`
	ObjectsDigest    string `json:"objects_digest"`
	Scope            string `json:"scope"`
	Status           string `json:"status"`
	Watermark        int64  `json:"deletion_watermark"`
}

func (deletion TaskDeletion) Validate() error {
	if deletion.SchemaVersion != 1 || !ValidDigest(deletion.PlanDigest) || !ValidDigest(deletion.OriginalDocument) || !ValidDigest(deletion.ObjectsDigest) || deletion.Scope != "TASK_CONTENT" || deletion.Watermark < 1 || deletion.Status != "PENDING" && deletion.Status != "PURGED" {
		return Fail(InvalidArgument, "bounded task content tombstone required")
	}
	return nil
}
