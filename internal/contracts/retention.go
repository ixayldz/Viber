package contracts

import (
	"regexp"
	"time"
)

const RetentionAcknowledgement = "DELETE_MANAGED_FAMILY_TASK_CONTENT_AFTER_DEADLINE_V1"

// Retention is explicit user consent for this task's managed family copies.
// It is not permission to waive UNKNOWN risk or erase provider/unmanaged data.
type RetentionPolicy struct {
	SchemaVersion        int    `json:"schema_version"`
	TaskID               string `json:"task_id"`
	Revision             int64  `json:"revision"`
	Actor                string `json:"actor"`
	Scope                string `json:"scope"`
	Mode                 string `json:"mode"`
	Clock                string `json:"clock"`
	ChangedAt            string `json:"changed_at"`
	Deadline             string `json:"deadline,omitempty"`
	Acknowledgement      string `json:"acknowledgement,omitempty"`
	AuthorityDigest      string `json:"authority_digest"`
	SourcePhysicalRoot   string `json:"source_physical_root"`
	SourceTaskSequence   int64  `json:"source_task_sequence"`
	SourceDocumentDigest string `json:"source_document_digest"`
}

var retentionTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func RetentionTime(value string) (time.Time, error) {
	if len(value) < 20 || len(value) > 30 {
		return time.Time{}, Fail(InvalidArgument, "bounded canonical UTC retention timestamp required")
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || t.IsZero() || t.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, Fail(InvalidArgument, "canonical UTC retention timestamp required")
	}
	return t, nil
}

func (p RetentionPolicy) Validate() error {
	changed, err := RetentionTime(p.ChangedAt)
	if err != nil || p.SchemaVersion != 1 || !retentionTaskID.MatchString(p.TaskID) || p.Revision < 1 || p.Revision > 1_000_000_000 || p.Actor != "USER" || p.Scope != "MANAGED_FAMILY_TASK_CONTENT" || p.Clock != "SYSTEM_UTC" || !ValidDigest(p.AuthorityDigest) || !ValidDigest(p.SourcePhysicalRoot) || p.SourceTaskSequence < 1 || !ValidDigest(p.SourceDocumentDigest) {
		return Fail(InvalidArgument, "source-bound explicit retention policy required")
	}
	switch p.Mode {
	case "KEEP":
		if p.Deadline == "" && p.Acknowledgement == "" {
			return nil
		}
	case "EXPIRE":
		deadline, err := RetentionTime(p.Deadline)
		if err == nil && deadline.After(changed) && p.Acknowledgement == RetentionAcknowledgement {
			return nil
		}
	}
	return Fail(PolicyDenied, "expiry requires a future UTC deadline and explicit managed-content deletion consent")
}

type RetentionExpiry struct {
	Policy     RetentionPolicy `json:"policy"`
	ObservedAt string          `json:"observed_at"`
}

func (e RetentionExpiry) Validate() error {
	if e.Policy.Validate() != nil || e.Policy.Mode != "EXPIRE" {
		return Fail(PolicyDenied, "enabled retention policy required")
	}
	observed, err := RetentionTime(e.ObservedAt)
	deadline, _ := RetentionTime(e.Policy.Deadline)
	if err != nil || observed.Before(deadline) {
		return Fail(PolicyDenied, "retention deadline has not elapsed")
	}
	return nil
}
