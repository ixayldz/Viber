package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPromptQueueIsDurableAndActivationIsAtomicBarrier(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer s.Close()
	before, _, err := s.Load(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	command := QueueCommand{CommandID: "queued-input", TaskID: opts.TaskID, Action: "add", Text: "Change only a.txt\n@a.txt"}
	queued, err := s.QueueControl(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if queueIndependentState(before) != queueIndependentState(queued) || queued.InputBarrier || len(queued.PromptQueue) != 1 {
		t.Fatal("queue changed authority", queued)
	}
	duplicate, err := s.QueueControl(ctx, command)
	if err != nil || !reflect.DeepEqual(queued, duplicate) {
		t.Fatal("queue dedup", err)
	}
	command.Text = "different"
	if _, err = s.QueueControl(ctx, command); err == nil {
		t.Fatal("queue ID conflict accepted")
	}
	prompt, err := s.PromptQueue(ctx, opts.TaskID)
	if err != nil || len(prompt) != 1 || prompt[0].Text != "Change only a.txt\n@a.txt" {
		t.Fatal(prompt, err)
	}
	activation := SteeringInput{CommandID: "activate-input", TaskID: opts.TaskID, QueueID: "queued-input", Text: prompt[0].Text}
	activation.Text = "forged"
	if _, err = s.RecordSteering(ctx, activation); err == nil {
		t.Fatal("different queue bytes activated")
	}
	current, _ := s.State(ctx, opts.TaskID)
	if len(current.PromptQueue) != 1 || current.InputBarrier {
		t.Fatal("failed activation had effects")
	}
	activation.Text = prompt[0].Text
	activated, err := s.RecordSteering(ctx, activation)
	if err != nil || len(activated.PromptQueue) != 0 || !activated.InputBarrier || len(activated.PendingInputIDs) != 1 {
		t.Fatal(activated, err)
	}
	again, err := s.RecordSteering(ctx, activation)
	if err != nil || !reflect.DeepEqual(activated, again) {
		t.Fatal("activation retried effect", err)
	}
	raw, err := s.pendingInput(ctx, opts.TaskID, activation.CommandID)
	if err != nil || string(raw) != activation.Text {
		t.Fatal(string(raw), err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(ctx, backup, target); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	state, _, err := restored.Load(ctx, opts.TaskID)
	if err != nil || !state.InputBarrier || len(state.PromptQueue) != 0 {
		t.Fatal(state, err)
	}
	raw, err = restored.pendingInput(ctx, opts.TaskID, activation.CommandID)
	if err != nil || string(raw) != activation.Text {
		t.Fatal(string(raw), err)
	}
}
func TestKernelPublicationCanRetryOnlyQueueCursorChanges(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer s.Close()
	state, doc, err := s.Load(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueControl(ctx, QueueCommand{CommandID: "add", TaskID: opts.TaskID, Action: "add", Text: "next"}); err != nil {
		t.Fatal(err)
	}
	next, err := s.record(ctx, state, doc, "SessionRecorded", c.EventPayload{})
	if err != nil || len(next.PromptQueue) != 1 {
		t.Fatal("benign queue cursor killed work", next, err)
	}
	if _, err = s.RecordSteering(ctx, SteeringInput{CommandID: "steer", TaskID: opts.TaskID, Text: "stop"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.record(ctx, next, doc, "SessionRecorded", c.EventPayload{}); err == nil {
		t.Fatal("queue retry crossed steering barrier")
	}
}
func TestSourceObservationIsCandidateBoundAndLimited(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer s.Close()
	state, doc, err := s.Load(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	value, err := s.Observe(ctx, opts.TaskID, Observation{Kind: "source-list", Query: "at", Limit: 32})
	if err != nil {
		t.Fatal(err)
	}
	list := value.(SourceMatches)
	if list.Total != 1 || list.Entries[0].Path != "a.txt" {
		t.Fatal(list)
	}
	if err = os.WriteFile(filepath.Join(opts.Root, "a.txt"), []byte("new live source"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err = s.Observe(ctx, opts.TaskID, Observation{Kind: "source-page", Path: "a.txt", Candidate: doc.Candidate.SnapshotDigest, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	page := value.(SourcePage)
	if page.Next != 2 || page.Complete || page.Entry.Hash != list.Entries[0].Hash {
		t.Fatal(page)
	}
	if _, err = s.Observe(ctx, opts.TaskID, Observation{Kind: "source-page", Path: "../secret", Candidate: doc.Candidate.SnapshotDigest, Limit: 2}); err == nil {
		t.Fatal("path escape")
	}
	if _, err = s.Observe(ctx, opts.TaskID, Observation{Kind: "source-page", Path: "a.txt", Candidate: c.HashBytes([]byte("stale")), Limit: 2}); err == nil {
		t.Fatal("stale candidate")
	}
	// Models cannot enqueue authority through a manufactured event.
	_, err = s.Journal.Execute(ctx, store.Command{ID: "model-queue", TaskID: opts.TaskID, Actor: "model", ExpectedTaskSeq: state.TaskSeq, Type: "PromptQueued", Payload: c.EventPayload{InputID: "model-queue", InputDigest: c.HashBytes([]byte("x")), InputBytes: 1, Reason: c.HashBytes([]byte("x"))}})
	if err == nil {
		t.Fatal("model queue authority")
	}
}
