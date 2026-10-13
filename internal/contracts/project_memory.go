package contracts

import (
	"strings"
	"unicode/utf8"
)

// ProjectMemoryMutation is the metadata-only journal witness for an immutable
// user-authored project-memory event. It carries no assertion text or other
// event content.
type ProjectMemoryMutation struct {
	SourceDocumentDigest string                         `json:"source_document_digest"`
	EventDigest          string                         `json:"event_digest"`
	CommandID            string                         `json:"command_id"`
	ConflictMetadata     *ProjectMemoryConflictMetadata `json:"conflict_metadata,omitempty"`
	EventBytes           int64                          `json:"event_bytes"`
	SourceTaskSeq        int64                          `json:"source_task_seq"`
	SpecVersion          int64                          `json:"spec_version"`
	PolicyEpoch          int64                          `json:"policy_epoch"`
	KernelGeneration     int64                          `json:"kernel_generation"`
	Action               string                         `json:"action"`
}

// ProjectMemoryConflictMetadata is a bounded, text-free witness used to
// re-evaluate assertion conflicts without loading another task's archive
// document or assertion body. It is optional only for replay compatibility
// with journals written before conflict indexing existed.
type ProjectMemoryConflictMetadata struct {
	ProjectDigest   string `json:"project_digest"`
	BaselineDigest  string `json:"baseline_digest"`
	CandidateDigest string `json:"candidate_digest"`
	PolicyDigest    string `json:"policy_digest"`
	AssertionID     string `json:"assertion_id,omitempty"`
	ContentDigest   string `json:"content_digest,omitempty"`
	TargetID        string `json:"target_id,omitempty"`
	TargetDigest    string `json:"target_digest,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Key             string `json:"key,omitempty"`
	SchemaVersion   int    `json:"schema_version"`
}

func (w ProjectMemoryConflictMetadata) Validate(action string) error {
	if w.SchemaVersion != 1 || !ValidDigest(w.ProjectDigest) || !ValidDigest(w.BaselineDigest) ||
		!ValidDigest(w.CandidateDigest) || !ValidDigest(w.PolicyDigest) {
		return Fail(InvalidArgument, "invalid project memory conflict metadata")
	}
	switch action {
	case "ASSERT", "CORRECT":
		if !validProjectMemoryID(w.AssertionID) || !validProjectMemoryKind(w.Kind) || !validProjectMemoryKey(w.Key) ||
			!ValidDigest(w.ContentDigest) {
			return Fail(InvalidArgument, "invalid project memory assertion conflict metadata")
		}
		if action == "ASSERT" && (w.TargetID != "" || w.TargetDigest != "") {
			return Fail(InvalidArgument, "assertion conflict metadata cannot carry a correction target")
		}
		if action == "CORRECT" && (!validProjectMemoryID(w.TargetID) || !ValidDigest(w.TargetDigest)) {
			return Fail(InvalidArgument, "invalid project memory correction conflict metadata")
		}
	case "RETIRE":
		if !validProjectMemoryID(w.AssertionID) || !validProjectMemoryID(w.TargetID) || !ValidDigest(w.TargetDigest) ||
			w.ContentDigest != "" || w.Kind != "" || w.Key != "" {
			return Fail(InvalidArgument, "invalid project memory retirement conflict metadata")
		}
	case "COPY":
		if w.AssertionID != "" || w.ContentDigest != "" || w.TargetID != "" || w.TargetDigest != "" || w.Kind != "" || w.Key != "" {
			return Fail(InvalidArgument, "copy conflict metadata cannot assert memory content")
		}
	default:
		return Fail(InvalidArgument, "invalid project memory conflict metadata action")
	}
	return nil
}

func validProjectMemoryID(value string) bool {
	if len(value) != len("pm-")+32 || value[:len("pm-")] != "pm-" {
		return false
	}
	for _, r := range value[len("pm-"):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validProjectMemoryKind(value string) bool {
	return value == "FACT" || value == "DECISION" || value == "CONVENTION"
}

func validProjectMemoryKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, r := range value {
		if index == 0 {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return false
			}
		} else if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (m ProjectMemoryMutation) Validate() error {
	if !ValidDigest(m.SourceDocumentDigest) || !ValidDigest(m.EventDigest) ||
		m.CommandID == "" || len(m.CommandID) > 128 || !utf8.ValidString(m.CommandID) || strings.ContainsAny(m.CommandID, "\x00\r\n") ||
		m.EventBytes < 1 || m.EventBytes > 16<<10 || m.SourceTaskSeq < 1 || m.SourceTaskSeq > 1<<53 ||
		m.SpecVersion < 1 || m.SpecVersion > 1<<53 || m.PolicyEpoch < 0 || m.PolicyEpoch > 1<<53 ||
		m.KernelGeneration < 1 || m.KernelGeneration > 1<<53 {
		return Fail(InvalidArgument, "invalid project memory mutation binding")
	}
	switch m.Action {
	case "ASSERT", "CORRECT", "RETIRE", "COPY":
		if m.ConflictMetadata != nil {
			return m.ConflictMetadata.Validate(m.Action)
		}
		return nil
	default:
		return Fail(InvalidArgument, "invalid project memory mutation action")
	}
}
