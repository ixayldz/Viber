package agent

import (
	"context"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/diskguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/store"
)

type Observation struct {
	CaseID        string `json:"case_id,omitempty"`
	Repeat        int    `json:"repeat,omitempty"`
	CheckScope    string `json:"check_scope,omitempty"`
	Path          string `json:"path,omitempty"`
	Query         string `json:"query,omitempty"`
	Candidate     string `json:"candidate,omitempty"`
	HistoryDigest string `json:"history_digest,omitempty"`
	Kind          string `json:"kind"`
	Offset        int64  `json:"offset,omitempty"`
	Limit         int64  `json:"limit,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	Stream        string `json:"stream,omitempty"`
}
type ContextComponent struct {
	Index   int    `json:"index"`
	Role    string `json:"role"`
	Bytes   int    `json:"canonical_bytes"`
	Digest  string `json:"digest"`
	Reason  string `json:"inclusion_reason"`
	Calls   int    `json:"tool_calls"`
	Replies int    `json:"tool_replies"`
}
type ContextExplanation struct {
	SchemaVersion     int                `json:"schema_version"`
	TaskID            string             `json:"task_id"`
	Available         bool               `json:"available"`
	RequestDigest     string             `json:"request_digest,omitempty"`
	Audit             *ContextAudit      `json:"audit,omitempty"`
	ContextLimit      int64              `json:"context_limit"`
	SafetyMargin      int64              `json:"safety_margin"`
	Measurement       string             `json:"measurement"`
	ProtocolMandatory bool               `json:"whole_protocol_mandatory"`
	Components        []ContextComponent `json:"components"`
}

func (s *Session) Observe(ctx context.Context, task string, args Observation) (any, error) {
	if args.Kind != "check-output" && (args.CaseID != "" || args.Repeat != 0 || args.CheckScope != "") {
		return nil, c.Fail(c.InvalidArgument, "observer output parameters require check-output")
	}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return nil, err
	}
	if args.HistoryDigest != "" && args.Kind != "history-page" {
		return nil, c.Fail(c.InvalidArgument, "history digest is only valid for history-page")
	}
	if args.Kind == "source-list" || args.Kind == "source-page" {
		return s.sourceObservation(state, doc, args)
	}
	if args.Path != "" || args.Query != "" || args.Candidate != "" {
		return nil, c.Fail(c.InvalidArgument, "source parameters require source observation")
	}
	switch args.Kind {
	case "runtime-info":
		if args.Offset != 0 || args.Limit != 0 || args.RunID != "" || args.Stream != "" || args.Query != "" || args.Path != "" || args.HistoryDigest != "" {
			return nil, c.Fail(c.InvalidArgument, "runtime-info accepts no page arguments")
		}
		return s.NativeRiskInfo(ctx, task)
	case "continuity-info":
		return struct {
			SchemaVersion int             `json:"schema_version"`
			State         c.TaskState     `json:"state"`
			Profile       string          `json:"profile_digest"`
			Compactions   []CompactionRef `json:"compactions"`
			Pins          []ContextPin    `json:"pins"`
			Runtime       *Runtime        `json:"runtime,omitempty"`
		}{1, state, tokenProfile(doc), doc.Compactions, doc.Pins, doc.Runtime}, nil
	case "history-page":
		if args.RunID != "" || args.Stream != "" {
			return nil, c.Fail(c.InvalidArgument, "history page has unrelated check parameters")
		}
		return s.HistoryPage(doc, args.HistoryDigest, args.Offset, args.Limit)
	case "resources":
		if args.Offset != 0 || args.Limit != 0 || args.RunID != "" || args.Stream != "" {
			return nil, c.Fail(c.InvalidArgument, "resources accepts no page arguments")
		}
		ledger, err := s.Journal.ResourceLedger(ctx)
		if err != nil {
			return nil, err
		}
		disk, err := s.Journal.DiskStatus()
		if err != nil {
			return nil, err
		}
		return struct {
			Ledger store.ResourceLedgerView `json:"ledger"`
			Disk   diskguard.Status         `json:"disk"`
		}{ledger, disk}, nil
	case "verification":
		if args.Offset != 0 || args.Limit != 0 || args.RunID != "" || args.Stream != "" {
			return nil, c.Fail(c.InvalidArgument, "verification accepts no page arguments")
		}
		return s.deriveVerification(doc, state)
	case "checks":
		if args.Offset != 0 || args.Limit != 0 || args.RunID != "" || args.Stream != "" {
			return nil, c.Fail(c.InvalidArgument, "checks accepts no page arguments")
		}
		return s.CheckSummaries(doc, state)
	case "check-output":
		return s.readCheckOutput(doc, checkOutputQuery{RunID: args.RunID, Stream: args.Stream, Offset: args.Offset, Limit: args.Limit, CaseID: args.CaseID, Repeat: args.Repeat, Scope: args.CheckScope}, true)
	case "report":
		if args.Offset != 0 || args.Limit != 0 || args.RunID != "" || args.Stream != "" {
			return nil, c.Fail(c.InvalidArgument, "report accepts no page arguments")
		}
		if doc.FinalArtifactDigest == "" {
			return nil, c.Fail(c.InvalidArgument, "final artifact unavailable")
		}
		return finalArtifact(doc), nil
	case "context-why":
		if args.Offset != 0 || args.Limit != 0 || args.RunID != "" || args.Stream != "" {
			return nil, c.Fail(c.InvalidArgument, "context-why accepts no page arguments")
		}
		_, capacity, _ := contextProfile(doc)
		result := ContextExplanation{SchemaVersion: 1, TaskID: task, Available: doc.Context != nil, Audit: doc.Context, ContextLimit: capacity, SafetyMargin: 4096, Measurement: "CONSERVATIVE_SERIALIZED_BYTE_UPPER_BOUND; component sizes are not additive token counts", ProtocolMandatory: true, Components: []ContextComponent{}}
		if doc.Context == nil {
			return result, nil
		}
		result.RequestDigest = doc.Context.RequestDigest
		raw, err := s.Archive.GetBytes(task, doc.Context.RequestDigest)
		if err != nil {
			return nil, err
		}
		var request model.Request
		if err = c.DecodeStrict(raw, &request); err != nil {
			return nil, err
		}
		instructionRaw, _ := c.CanonicalV1(request.Instructions)
		result.Components = append(result.Components, ContextComponent{Index: -2, Role: "instructions", Bytes: len(instructionRaw), Digest: c.HashBytes(instructionRaw), Reason: "CURRENT_KERNEL_SPEC_POLICY_BUDGET_PLAN_CHECK_CONTRACT"})
		toolRaw, _ := c.CanonicalV1(request.Tools)
		result.Components = append(result.Components, ContextComponent{Index: -1, Role: "tool_registry", Bytes: len(toolRaw), Digest: c.HashBytes(toolRaw), Reason: "STRICT_NATIVE_TOOL_PROTOCOL"})
		for i, message := range request.Messages {
			raw, _ := c.CanonicalV1(message)
			reason := "PAIRED_CONVERSATION_BOUNDARY"
			if message.Role == "user" {
				reason = "RAW_INPUT_OR_EXPLICIT_KERNEL_FEEDBACK"
			}
			if message.Role == "tool" {
				reason = "UNTRUSTED_TOOL_RECEIPT_AND_EXACT_PROTOCOL_PAIRING"
			}
			result.Components = append(result.Components, ContextComponent{Index: i, Role: message.Role, Bytes: len(raw), Digest: c.HashBytes(raw), Reason: reason, Calls: len(message.Calls), Replies: len(message.Replies)})
		}
		return result, nil
	case "context-page":
		if doc.Context == nil || args.RunID != "" || args.Stream != "" {
			return nil, c.Fail(c.InvalidArgument, "compiled context unavailable or unrelated page arguments")
		}
		if args.Offset < 0 || args.Limit < 1 || args.Limit > 65536 {
			return nil, c.Fail(c.InvalidArgument, "context byte page outside quota")
		}
		raw, err := s.Archive.GetBytes(task, doc.Context.RequestDigest)
		if err != nil {
			return nil, err
		}
		if args.Offset > int64(len(raw)) {
			return nil, c.Fail(c.InvalidArgument, "context offset exceeds retained request")
		}
		end := min(args.Offset+args.Limit, int64(len(raw)))
		return struct {
			SchemaVersion int    `json:"schema_version"`
			RequestDigest string `json:"request_digest"`
			Bytes         []byte `json:"exact_bytes"`
			Offset        int64  `json:"offset"`
			Next          int64  `json:"next_offset"`
			Total         int64  `json:"total_bytes"`
			Complete      bool   `json:"complete"`
		}{1, doc.Context.RequestDigest, raw[args.Offset:end], args.Offset, end, int64(len(raw)), end == int64(len(raw))}, nil
	default:
		return nil, c.Fail(c.InvalidArgument, "unknown observation")
	}
}
