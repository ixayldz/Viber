package agent

import (
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
	"unicode/utf8"
)

type QueueCommand struct {
	CommandID string `json:"command_id"`
	TaskID    string `json:"task_id"`
	Action    string `json:"action"`
	QueueID   string `json:"queue_id,omitempty"`
	Text      string `json:"text,omitempty"`
}
type QueuedPrompt struct {
	Ref  c.PromptRef `json:"ref"`
	Text string      `json:"text"`
}

func (s *Session) queueFromState(state c.TaskState) ([]QueuedPrompt, error) {
	result := []QueuedPrompt{}
	if len(state.PromptQueue) > 64 {
		return nil, c.Fail(c.StoreIntegrityError, "prompt queue quota exceeded")
	}
	seen := map[string]bool{}
	for _, ref := range state.PromptQueue {
		if ref.ID == "" || seen[ref.ID] || !c.ValidDigest(ref.Digest) || ref.Bytes < 1 || ref.Bytes > 64<<10 {
			return nil, c.Fail(c.StoreIntegrityError, "invalid queued prompt binding")
		}
		raw, err := s.Archive.GetBytes(state.TaskID, ref.Digest)
		if err != nil || int64(len(raw)) != ref.Bytes || !utf8.Valid(raw) {
			return nil, c.Fail(c.StoreIntegrityError, "queued prompt payload unavailable")
		}
		seen[ref.ID] = true
		result = append(result, QueuedPrompt{ref, string(raw)})
	}
	return result, nil
}
func (s *Session) PromptQueue(ctx context.Context, task string) ([]QueuedPrompt, error) {
	state, err := s.State(ctx, task)
	if err != nil {
		return nil, err
	}
	return s.queueFromState(state)
}

// QueueControl uses journal optimistic admission, not the work-loop mutex.
// Queueing cannot alter the running spec, candidate, policy or input barrier.
func (s *Session) QueueControl(ctx context.Context, command QueueCommand) (c.TaskState, error) {
	if command.CommandID == "" || len(command.CommandID) > 128 || command.TaskID == "" ||
		(command.Action != "add" && command.Action != "remove") ||
		command.Action == "add" && (command.QueueID != "" || len(command.Text) < 1 || len(command.Text) > 64<<10 || !utf8.ValidString(command.Text)) ||
		command.Action == "remove" && (command.QueueID == "" || command.Text != "") {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "bounded queue command required")
	}
	digest, _ := c.Digest(command)
	kind := "PromptQueued"
	if command.Action == "remove" {
		kind = "PromptQueueRemoved"
	}
	prior, event, payload, found, err := s.Journal.CommandReceipt(ctx, command.CommandID)
	if err != nil {
		return prior, err
	}
	if found {
		if event.TaskID != command.TaskID || event.Actor != "user" || event.Type != kind || payload.Reason != digest {
			return prior, c.Fail(c.CommandIDConflict, "queue command ID conflict")
		}
		return prior, nil
	}
	extra := c.EventPayload{InputID: command.QueueID, Reason: digest}
	if command.Action == "add" {
		blob, err := s.Archive.PutBytes(command.TaskID, []byte(command.Text))
		if err != nil {
			return prior, err
		}
		extra.InputID = command.CommandID
		extra.InputDigest = blob
		extra.InputBytes = int64(len(command.Text))
	}
	for i := 0; i < 32; i++ {
		state, err := s.State(ctx, command.TaskID)
		if err != nil {
			return state, err
		}
		if state.KernelGeneration != s.Journal.Generation() {
			state, err = s.recover(ctx, state)
			if err != nil {
				return state, err
			}
		}
		result, err := s.Journal.Execute(ctx, store.Command{ID: command.CommandID, TaskID: command.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: kind, Payload: extra})
		if err == nil {
			return result, nil
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.Conflict && typed.Code != c.StaleBase {
			return result, err
		}
	}
	return c.TaskState{}, c.Fail(c.Conflict, "queue admission could not reach a journal boundary")
}

func queueIndependentState(state c.TaskState) string {
	state.PromptQueue = nil
	state.TaskSeq = 0
	state.StoreSeq = 0
	digest, _ := c.Digest(state)
	return digest
}

// A concurrent enqueue changes only cursor/queue metadata. This narrow retry
// cannot cross steering barriers, resource settlements or candidate/spec changes.
func (s *Session) executeAroundQueue(ctx context.Context, state c.TaskState, command store.Command) (c.TaskState, error) {
	binding := queueIndependentState(state)
	for i := 0; i < 32; i++ {
		result, err := s.Journal.Execute(ctx, command)
		if err == nil {
			return result, nil
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StaleBase {
			return result, err
		}
		current, readErr := s.State(ctx, state.TaskID)
		if readErr != nil {
			return current, readErr
		}
		if queueIndependentState(current) != binding {
			return result, err
		}
		command.ExpectedTaskSeq = current.TaskSeq
	}
	return state, c.Fail(c.Conflict, "queue traffic exceeded bounded publication retries")
}
