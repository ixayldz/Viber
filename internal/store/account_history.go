package store

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
)

type AccountRecord struct {
	Event c.Event     `json:"event"`
	State c.TaskState `json:"state"`
}
type AccountHistoryPage struct {
	SchemaVersion int             `json:"schema_version"`
	TaskID        string          `json:"task_id"`
	After         int64           `json:"after_task_seq"`
	Next          int64           `json:"next_task_seq"`
	Head          int64           `json:"head_task_seq"`
	HasMore       bool            `json:"has_more"`
	Records       []AccountRecord `json:"records"`
}

// AccountHistory collects a bounded set of historical account boundaries in one
// complete validated global replay. It does not trust current projections,
// skip unseen corruption, or invoke a caller under the owner mutex.
func (s *Store) AccountHistory(ctx context.Context, task string, after int64, limit int) (AccountHistoryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := AccountHistoryPage{SchemaVersion: 1, TaskID: task, After: after, Next: after, Records: []AccountRecord{}}
	if s.db == nil {
		return page, os.ErrClosed
	}
	if task == "" || after < 0 || limit < 1 || limit > 64 {
		return page, c.Fail(c.InvalidArgument, "bounded account history cursor required")
	}
	states, err := s.replayObserveLocked(ctx, task, 0, func(event c.Event, payload c.EventPayload, state c.TaskState) {
		if event.TaskID != task || event.TaskSeq <= after || (payload.Tokens == nil && payload.Resources == nil && !((event.Type == "InputRecorded" || event.Type == "PromptQueued") && payload.InputDigest != "")) {
			return
		}
		if len(page.Records) == limit {
			page.HasMore = true
			return
		}
		page.Records = append(page.Records, AccountRecord{event, state})
		page.Next = event.TaskSeq
	})
	if err != nil {
		return page, err
	}
	page.Head = states[task].TaskSeq
	if after > page.Head {
		return page, c.Fail(c.StaleBase, "account cursor exceeds head")
	}
	if !page.HasMore {
		page.Next = page.Head
	}
	return page, nil
}
