package store

import (
	"context"
	"database/sql"
	"errors"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// CommandReceipt returns a verified durable command receipt without replaying
// its action. Application code validates the type and immutable input digest.
func (s *Store) CommandReceipt(ctx context.Context, id string) (c.TaskState, c.Event, c.EventPayload, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state c.TaskState
	var event c.Event
	var payload c.EventPayload
	if s.db == nil {
		return state, event, payload, false, c.Fail(c.StoreIntegrityError, "store closed")
	}
	if err := s.verifyProjectionsLocked(ctx); err != nil {
		return state, event, payload, false, err
	}
	var result, envelope, body []byte
	err := s.db.QueryRowContext(ctx, "SELECT c.result,e.envelope,p.body FROM commands c JOIN events e ON e.event_id=c.command_id JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest WHERE c.command_id=?", id).Scan(&result, &envelope, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return state, event, payload, false, nil
	}
	if err != nil {
		return state, event, payload, false, err
	}
	for _, item := range []struct {
		raw    []byte
		target any
	}{{result, &state}, {envelope, &event}, {body, &payload}} {
		if err = c.DecodeStrict(item.raw, item.target); err != nil {
			return state, event, payload, false, err
		}
	}
	replayed, err := s.replayToLocked(ctx, event.TaskID, event.TaskSeq)
	if err != nil {
		return state, event, payload, false, err
	}
	actual, _ := c.Digest(state)
	expected, _ := c.Digest(replayed[event.TaskID])
	if actual != expected {
		return state, event, payload, false, c.Fail(c.StoreIntegrityError, "command receipt differs from historical journal")
	}
	return state, event, payload, true, nil
}
