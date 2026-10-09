package agent

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/runner"
)

type checkOutputQuery struct {
	RunID  string `json:"run_id"`
	Stream string `json:"stream"`
	Offset int64  `json:"offset"`
	Limit  int64  `json:"limit"`
	CaseID string `json:"case_id,omitempty"`
	Repeat int    `json:"repeat,omitempty"`
	Scope  string `json:"scope,omitempty"`
}

func (s *Session) readCheckOutput(doc Document, args checkOutputQuery, operator bool) (any, error) {
	if args.Offset < 0 || args.Limit < 1 || args.Limit > 16384 || args.Stream != "stdout" && args.Stream != "stderr" || args.Repeat < 0 || args.Repeat > 4 || args.Scope != "" && args.Scope != "candidate" && args.Scope != "baseline" {
		return nil, c.Fail(c.InvalidArgument, "invalid check output byte page")
	}
	for _, ref := range doc.CheckRuns {
		if ref.ID != args.RunID {
			continue
		}
		record, err := s.readCheckRun(doc, ref)
		if err != nil {
			return nil, err
		}
		result := record.Result
		if record.Observer != nil {
			// A subject can echo hidden fixture input to stdout. No output scanner can
			// safely declassify arbitrary encodings; protected observer bytes are local
			// operator access only and never returned by a model tool.
			if !operator {
				return nil, c.Fail(c.PolicyDenied, "protected observer output requires local operator observation")
			}
			run := record.Observer.Current
			if args.Scope == "baseline" {
				run = record.Observer.Baseline
			}
			if run == nil {
				return nil, c.Fail(c.InvalidArgument, "observer phase not executed")
			}
			repeat := args.Repeat
			if repeat == 0 {
				repeat = 1
			}
			found := false
			for _, attempt := range run.Attempts {
				if (args.CaseID == "" || attempt.CaseID == args.CaseID) && attempt.Repeat == repeat {
					result = attempt.Result
					found = true
					break
				}
			}
			if !found {
				return nil, c.Fail(c.InvalidArgument, "observer case/repeat not retained")
			}
		} else if args.CaseID != "" || args.Repeat != 0 || args.Scope != "" {
			return nil, c.Fail(c.InvalidArgument, "case/repeat/scope require independent observer")
		}
		return outputPage(args, result)
	}
	return nil, c.Fail(c.InvalidArgument, "run ID unavailable in this task")
}
func outputPage(args checkOutputQuery, result runner.Result) (any, error) {
	data := result.Stdout
	if args.Stream == "stderr" {
		data = result.Stderr
	}
	if args.Offset > int64(len(data)) {
		return nil, c.Fail(c.InvalidArgument, "output offset outside retained bytes")
	}
	end := min(args.Offset+args.Limit, int64(len(data)))
	return struct {
		RunID    string `json:"run_id"`
		Stream   string `json:"stream"`
		Bytes    []byte `json:"untrusted_exact_bytes"`
		Total    int64  `json:"total_bytes"`
		Next     int64  `json:"next_offset"`
		Complete bool   `json:"complete"`
	}{args.RunID, args.Stream, data[args.Offset:end], int64(len(data)), end, end == int64(len(data))}, nil
}
