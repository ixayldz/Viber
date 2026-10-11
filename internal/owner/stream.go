package owner

import (
	"context"
	"sync"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const (
	maxProviderDeltas      = 128
	maxProviderDeltaBytes  = 64 << 10
	maxProviderDeltaRecord = 4096
)

type CurrentStateRef struct {
	TaskID         string `json:"task_id"`
	TaskSeq        int64  `json:"task_seq"`
	DocumentDigest string `json:"document_digest"`
}

type ProviderDelta struct {
	Sequence       int64  `json:"delta_seq"`
	DurableTaskSeq int64  `json:"durable_task_seq"`
	Text           string `json:"text"`
}

type StreamPageQuery struct {
	After      int64 `json:"after_delta_seq"`
	Limit      int   `json:"limit"`
	WaitMillis int   `json:"wait_ms,omitempty"`
}

type StreamPage struct {
	SchemaVersion    int             `json:"schema_version"`
	TaskID           string          `json:"task_id"`
	InvocationID     string          `json:"invocation_id,omitempty"`
	Replay           string          `json:"ephemeral_stream_replay"`
	After            int64           `json:"after_delta_seq"`
	Next             int64           `json:"next_delta_seq"`
	EarliestDeltaSeq int64           `json:"earliest_available_delta_seq"`
	CurrentDeltaSeq  int64           `json:"current_delta_seq"`
	HasMore          bool            `json:"has_more"`
	ResyncRequired   bool            `json:"resync_required"`
	CurrentState     CurrentStateRef `json:"current_durable_state"`
	Deltas           []ProviderDelta `json:"deltas"`
}

type providerStream struct {
	mu         sync.Mutex
	task       string
	invocation string
	deltas     []ProviderDelta
	bytes      int
	current    int64
	durableSeq int64
	changed    chan struct{}
	complete   bool
}

func newProviderStream(task, invocation string, durableSeq int64) *providerStream {
	return &providerStream{task: task, invocation: invocation, durableSeq: durableSeq, deltas: []ProviderDelta{}, changed: make(chan struct{})}
}

// Publish is bounded and never waits for a reader. Slow attach clients lose
// old ephemeral records and receive an explicit cursor gap on their next page.
func (s *providerStream) Publish(text string) {
	for len(text) > 0 {
		end := len(text)
		if end > maxProviderDeltaRecord {
			end = maxProviderDeltaRecord
			for end > 0 && text[end] >= 0x80 && text[end] < 0xC0 {
				end--
			}
		}
		if end <= 0 {
			return
		}
		piece := text[:end]
		text = text[end:]
		s.mu.Lock()
		s.current++
		delta := ProviderDelta{Sequence: s.current, DurableTaskSeq: s.durableSeq, Text: piece}
		s.deltas = append(s.deltas, delta)
		s.bytes += len(piece)
		for len(s.deltas) > maxProviderDeltas || s.bytes > maxProviderDeltaBytes {
			s.bytes -= len(s.deltas[0].Text)
			s.deltas = s.deltas[1:]
		}
		close(s.changed)
		s.changed = make(chan struct{})
		s.mu.Unlock()
	}
}

func (s *providerStream) Complete() {
	s.mu.Lock()
	s.complete = true
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *providerStream) page(ctx context.Context, query StreamPageQuery, state CurrentStateRef) (StreamPage, error) {
	page := StreamPage{SchemaVersion: 1, TaskID: s.task, InvocationID: s.invocation, Replay: "AVAILABLE", After: query.After, Next: query.After, CurrentState: state, Deltas: []ProviderDelta{}}
	if query.After < 0 || query.Limit < 1 || query.Limit > maxProviderDeltas || query.WaitMillis < 0 || query.WaitMillis > 1000 {
		return page, c.Fail(c.InvalidArgument, "bounded provider stream cursor required")
	}
	deadline := time.NewTimer(time.Duration(query.WaitMillis) * time.Millisecond)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		current := s.current
		earliest := current + 1
		if len(s.deltas) > 0 {
			earliest = s.deltas[0].Sequence
		}
		page.CurrentDeltaSeq = current
		page.EarliestDeltaSeq = earliest
		page.HasMore = query.After < current
		if query.After > current {
			s.mu.Unlock()
			return page, c.Fail(c.StaleBase, "provider stream cursor is beyond its current head")
		}
		if query.After < earliest-1 {
			page.ResyncRequired = true
			s.mu.Unlock()
			return page, nil
		}
		if page.HasMore {
			for _, delta := range s.deltas {
				if delta.Sequence <= query.After {
					continue
				}
				if len(page.Deltas) == query.Limit {
					break
				}
				page.Deltas = append(page.Deltas, delta)
				page.Next = delta.Sequence
			}
			page.HasMore = page.Next < current
			s.mu.Unlock()
			return page, nil
		}
		if query.WaitMillis == 0 || s.complete {
			s.mu.Unlock()
			return page, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return page, ctx.Err()
		case <-deadline.C:
			return page, nil
		case <-changed:
		}
	}
}

func (o *Owner) StreamPage(ctx context.Context, task string, query StreamPageQuery) (StreamPage, error) {
	if task == "" || query.After < 0 || query.Limit < 1 || query.Limit > maxProviderDeltas || query.WaitMillis < 0 || query.WaitMillis > 1000 {
		return StreamPage{}, c.Fail(c.InvalidArgument, "task and bounded provider stream cursor required")
	}
	state, err := o.Session.State(ctx, task)
	if err != nil {
		return StreamPage{}, err
	}
	ref := CurrentStateRef{TaskID: state.TaskID, TaskSeq: state.TaskSeq, DocumentDigest: state.DocumentDigest}
	o.mu.Lock()
	stream := o.streams[task]
	o.mu.Unlock()
	if stream == nil {
		return StreamPage{SchemaVersion: 1, TaskID: task, Replay: "NOT_RETAINED", After: query.After, Next: query.After, EarliestDeltaSeq: 0, CurrentDeltaSeq: 0, CurrentState: ref, Deltas: []ProviderDelta{}}, nil
	}
	return stream.page(ctx, query, ref)
}
