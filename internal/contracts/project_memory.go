package contracts

import (
	"strings"
	"unicode/utf8"
)

// ProjectMemoryMutation is the metadata-only journal witness for an immutable
// user-authored project-memory event. It carries no assertion text or other
// event content.
type ProjectMemoryMutation struct {
	SourceDocumentDigest string `json:"source_document_digest"`
	EventDigest          string `json:"event_digest"`
	CommandID            string `json:"command_id"`
	EventBytes           int64  `json:"event_bytes"`
	SourceTaskSeq        int64  `json:"source_task_seq"`
	SpecVersion          int64  `json:"spec_version"`
	PolicyEpoch          int64  `json:"policy_epoch"`
	KernelGeneration     int64  `json:"kernel_generation"`
	Action               string `json:"action"`
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
		return nil
	default:
		return Fail(InvalidArgument, "invalid project memory mutation action")
	}
}
