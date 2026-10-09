package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func deletionState() (c.TaskState, c.Event, c.EventPayload) {
	digest := c.HashBytes([]byte("document"))
	state := c.TaskState{SchemaVersion: 1, TaskID: "task", Execution: c.Terminated, TaskSeq: 7, StoreSeq: 7, KernelGeneration: 1, DocumentDigest: digest, Quality: c.Verified, Outcome: c.Finished, Fulfillment: c.Satisfied}
	event := c.Event{SchemaVersion: 1, ID: "delete", TaskID: "task", TaskSeq: 8, StoreSeq: 8, KernelGeneration: 2, Actor: "user", Type: "TaskContentDeleted"}
	tombstone := c.TaskDeletion{SchemaVersion: 1, PlanDigest: c.HashBytes([]byte("plan")), ObjectsDigest: c.HashBytes([]byte("objects")), OriginalDocument: digest, Scope: "TASK_CONTENT", Status: "PENDING", Watermark: 1}
	return state, event, c.EventPayload{Deletion: &tombstone, Reason: tombstone.PlanDigest}
}
func TestTaskDeletionPreservesHistoricalLedgerButRevokesContentSuccess(t *testing.T) {
	state, event, payload := deletionState()
	next, err := Reduce(&state, event, payload)
	if err != nil || next.Deletion == nil || next.Deletion.Status != "PENDING" || next.Quality != state.Quality || next.Outcome != state.Outcome || next.DocumentDigest != state.DocumentDigest {
		t.Fatal("tombstone lost audit", err)
	}
	if StrictSuccess(next) || InvocationExit(next, false) == 0 {
		t.Fatal("deleted content offered as current success")
	}
	event.TaskSeq++
	event.StoreSeq++
	event.Type = "TaskDeletionPurged"
	event.Actor = "kernel"
	confirmed := *payload.Deletion
	confirmed.Status = "PURGED"
	payload.Deletion = &confirmed
	completed, err := Reduce(&next, event, payload)
	if err != nil || completed.Deletion.Status != "PURGED" {
		t.Fatal("purge confirmation", err)
	}
	event.TaskSeq++
	event.StoreSeq++
	event.Type = "SessionRecorded"
	payload = c.EventPayload{DocumentDigest: state.DocumentDigest}
	if _, err = Reduce(&completed, event, payload); err == nil {
		t.Fatal("deleted scope revived")
	}
	if state.Deletion != nil {
		t.Fatal("reducer mutated previous audit")
	}
}
func TestTaskDeletionCannotWaiveRiskOrManufactureQuality(t *testing.T) {
	tests := []struct {
		name   string
		change func(*c.TaskState, *c.Event, *c.EventPayload)
	}{
		{"running", func(s *c.TaskState, e *c.Event, p *c.EventPayload) { s.Execution = c.Running }},
		{"model", func(s *c.TaskState, e *c.Event, p *c.EventPayload) { e.Actor = "model" }},
		{"old generation", func(s *c.TaskState, e *c.Event, p *c.EventPayload) { e.KernelGeneration = 0 }},
		{"wrong document", func(s *c.TaskState, e *c.Event, p *c.EventPayload) { p.Deletion.OriginalDocument = c.HashBytes(nil) }},
		{"forged purge", func(s *c.TaskState, e *c.Event, p *c.EventPayload) { p.Deletion.Status = "PURGED" }},
		{"quality", func(s *c.TaskState, e *c.Event, p *c.EventPayload) { p.Quality = c.Verified }},
		{"unknown charge", func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			s.Tokens = &c.TokenAccount{Reservations: []c.TokenReservation{{Status: "UNKNOWN"}}}
		}},
		{"unknown native", func(s *c.TaskState, e *c.Event, p *c.EventPayload) {
			s.Resources = &c.ResourceAccount{Reservations: []c.ResourceReservation{{Status: "UNKNOWN"}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, event, payload := deletionState()
			test.change(&state, &event, &payload)
			if _, err := Reduce(&state, event, payload); err == nil {
				t.Fatal("unsafe deletion admitted")
			}
		})
	}
}
