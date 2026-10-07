package store

import (
	"context"
	"os"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type JournalRecord struct {
	Event   c.Event        `json:"event"`
	Payload c.EventPayload `json:"payload"`
}
type HistoryPage struct {
	SchemaVersion int             `json:"schema_version"`
	TaskID        string          `json:"task_id"`
	After         int64           `json:"after_task_seq"`
	Next          int64           `json:"next_task_seq"`
	Head          int64           `json:"head_task_seq"`
	HasMore       bool            `json:"has_more"`
	Records       []JournalRecord `json:"records"`
}

// ReplayAt validates the complete retained journal, then returns exactly the
// requested task cursor. Zero means current; a gap or future cursor is an error.
func (s *Store) ReplayAt(ctx context.Context, task string, sequence int64) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return c.TaskState{}, os.ErrClosed
	}
	if task == "" || sequence < 0 {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "task and nonnegative replay cursor required")
	}
	states, err := s.replayToLocked(ctx, task, sequence)
	return states[task], err
}
func (s *Store) History(ctx context.Context, task string, after int64, limit int) (HistoryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := HistoryPage{SchemaVersion: 1, TaskID: task, After: after, Next: after, Records: []JournalRecord{}}
	if s.db == nil {
		return page, os.ErrClosed
	}
	if task == "" || after < 0 || limit < 1 || limit > 256 {
		return page, c.Fail(c.InvalidArgument, "invalid history page cursor or limit")
	}
	states, err := s.replayLocked(ctx, task)
	if err != nil {
		return page, err
	}
	page.Head = states[task].TaskSeq
	if after > page.Head {
		return page, c.Fail(c.StaleBase, "history cursor is beyond task head")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT e.envelope,p.body FROM events e JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest WHERE e.task_id=? AND e.task_seq>? ORDER BY e.task_seq LIMIT ?", task, after, limit)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, body []byte
		var record JournalRecord
		if err = rows.Scan(&raw, &body); err != nil {
			return page, err
		}
		if err = c.DecodeStrict(raw, &record.Event); err != nil {
			return page, err
		}
		if err = c.DecodeStrict(body, &record.Payload); err != nil {
			return page, err
		}
		page.Records = append(page.Records, record)
		page.Next = record.Event.TaskSeq
	}
	page.HasMore = page.Next < page.Head
	return page, rows.Err()
}
