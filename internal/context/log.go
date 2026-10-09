package context

import (
	"bytes"
	stdcontext "context"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const LogVersion = "SOURCE_BOUND_INTENT_LOG_V1"

type LogSpan struct {
	Start int    `json:"byte_start"`
	End   int    `json:"byte_end"`
	Line  int    `json:"line"`
	Bytes []byte `json:"exact_bytes"`
}
type LogSelection struct {
	Version         string    `json:"version"`
	Trust           string    `json:"trust"`
	SourceDigest    string    `json:"source_digest"`
	Intent          string    `json:"intent"`
	SourceBytes     int       `json:"source_bytes"`
	SelectedBytes   int       `json:"selected_bytes"`
	OmittedBytes    int       `json:"omitted_bytes"`
	DuplicateLines  int       `json:"duplicate_lines"`
	ScanTruncated   bool      `json:"scan_truncated"`
	SourceTruncated bool      `json:"source_truncated"`
	Spans           []LogSpan `json:"spans"`
}

func ValidLogIntent(intent string) bool {
	return intent == "BUILD" || intent == "TEST" || intent == "ERROR" || intent == "DIAGNOSTIC"
}

// SelectLog never interprets output as a receipt. It preserves exact retained
// source spans, including invalid UTF-8, and explicitly reports all omissions.
func SelectLog(ctx stdcontext.Context, raw []byte, intent string, budget int, sourceTruncated bool) (LogSelection, error) {
	result := LogSelection{Version: LogVersion, Trust: "UNTRUSTED_LOG_HINT_NOT_VERIFICATION", Intent: intent, SourceBytes: len(raw), SourceTruncated: sourceTruncated, Spans: []LogSpan{}}
	if !ValidLogIntent(intent) || budget < 1 || budget > 16384 || len(raw) > 8<<20 {
		return result, c.Fail(c.InvalidArgument, "bounded log intent and retained source required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.SourceDigest = c.HashBytes(raw)
	type line struct{ start, end, score int }
	lines := []line{}
	for start := 0; start < len(raw); {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(lines) == 65536 {
			result.ScanTruncated = true
			break
		}
		end := len(raw)
		if n := bytes.IndexByte(raw[start:], '\n'); n >= 0 {
			end = start + n + 1
		}
		// Classification is bounded per line; hydration always uses original bytes.
		text := strings.ToLower(string(raw[start:min(end, start+4096)]))
		score := 1
		if strings.Contains(text, "error") || strings.Contains(text, "panic:") || strings.Contains(text, "fatal") || strings.Contains(text, "failed") {
			score = 80
		}
		if strings.Contains(text, "stack") || strings.Contains(text, "traceback") || strings.HasPrefix(text, "\tat ") {
			score = max(score, 60)
		}
		switch intent {
		case "TEST":
			if strings.Contains(text, "--- fail:") || strings.HasPrefix(text, "fail\t") || strings.Contains(text, "assert") {
				score = 100
			}
		case "BUILD":
			if strings.Contains(text, "undefined:") || strings.Contains(text, "cannot ") || strings.Contains(text, "syntax error") || strings.Contains(text, "undefined reference") {
				score = 100
			}
		case "DIAGNOSTIC":
			if strings.Contains(text, "warning") || strings.Contains(text, "diagnostic") {
				score = max(score, 90)
			}
		}
		lines = append(lines, line{start, end, score})
		start = end
	}
	order := make([]int, len(lines))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return lines[order[i]].score > lines[order[j]].score })
	chosen := map[int]int{}
	seen := map[string]bool{}
	for _, id := range order {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if result.SelectedBytes == budget || len(chosen) >= 64 {
			break
		}
		if _, ok := chosen[id]; ok {
			continue
		}
		item := lines[id]
		digest := c.HashBytes(raw[item.start:item.end])
		if seen[digest] {
			result.DuplicateLines++
			continue
		}
		seen[digest] = true
		// Main evidence precedes neighboring context within the byte budget.
		for _, next := range []int{id, id - 1, id + 1} {
			if next < 0 || next >= len(lines) || len(chosen) >= 64 {
				continue
			}
			if _, ok := chosen[next]; ok {
				continue
			}
			n := min(lines[next].end-lines[next].start, budget-result.SelectedBytes)
			if n == 0 {
				break
			}
			chosen[next] = n
			result.SelectedBytes += n
		}
	}
	ids := make([]int, 0, len(chosen))
	for id := range chosen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		item := lines[id]
		end := item.start + chosen[id]
		if n := len(result.Spans); n > 0 && result.Spans[n-1].End == item.start {
			last := &result.Spans[n-1]
			last.End = end
			last.Bytes = append(last.Bytes, raw[item.start:end]...)
		} else {
			result.Spans = append(result.Spans, LogSpan{item.start, end, id + 1, bytes.Clone(raw[item.start:end])})
		}
	}
	result.OmittedBytes = len(raw) - result.SelectedBytes
	return result, nil
}
