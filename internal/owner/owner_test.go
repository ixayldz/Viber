package owner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/model"
)

func ownerFixture(t *testing.T, turns []agent.Turn, allow bool) (*Owner, string, string) {
	t.Helper()
	source := t.TempDir()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := agent.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: turns})
	budget := agent.DefaultBudget()
	budget.MaxSteps = 256
	budget.MaxToolCalls = 1024
	if _, err = session.Create(context.Background(), agent.StartOptions{Root: source, Prompt: []byte("Review captured source without host mutation"), TaskID: "task", Budget: budget, Autonomy: "guided", AllowUnverified: allow, Fixture: raw}); err != nil {
		session.Close()
		t.Fatal(err)
	}
	host, err := Open(context.Background(), directory, session)
	if err != nil {
		session.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	return host, directory, source
}
func rpc(t *testing.T, host *Owner, command, id string, payload any) json.RawMessage {
	t.Helper()
	raw, _ := c.CanonicalV1(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := ipc.Call(ctx, host.Server.Info, ipc.Request{ID: id, Command: command, TaskID: "task", Payload: raw})
	if err != nil {
		t.Fatal(command, err)
	}
	return response.Result
}
func decodeView(t *testing.T, raw []byte) View {
	t.Helper()
	var view View
	if err := c.DecodeStrict(raw, &view); err != nil {
		t.Fatal(err)
	}
	return view
}
func longTurns() []agent.Turn {
	turns := []agent.Turn{}
	for i := 0; i < 128; i++ {
		turns = append(turns, agent.Turn{UsageKnown: true, Calls: []model.Call{{ID: fmt.Sprintf("read-%d", i), Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}})
	}
	return append(turns, agent.Turn{UsageKnown: true, Text: "done"})
}
func waitActive(t *testing.T, host *Owner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		active := host.active != nil
		host.mu.Unlock()
		if active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("invocation never became active")
}
func TestConcurrentStatusPauseCancelAndDurableCommandDedup(t *testing.T) {
	host, _, source := ownerFixture(t, longTurns(), true)
	result := make(chan error, 1)
	go func() { _, err := host.Run(context.Background(), "task", "run-one"); result <- err }()
	waitActive(t, host)
	status := decodeView(t, rpc(t, host, "status", "status-one", nil))
	if status.State.Execution == c.Terminated || status.ReleaseReady {
		t.Fatal(status)
	}
	paused := decodeView(t, rpc(t, host, "pause", "pause-one", nil))
	if paused.State.Execution != c.Paused {
		t.Fatal(paused)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	head, err := host.Session.State(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := decodeView(t, rpc(t, host, "resume", "run-one", nil))
	if duplicate.State.Execution != c.Paused || duplicate.State.TaskSeq >= head.TaskSeq {
		t.Fatal("original invocation receipt not returned", duplicate, head)
	}
	duplicatePause := decodeView(t, rpc(t, host, "pause", "pause-one", nil))
	now, _ := host.Session.State(context.Background(), "task")
	if !reflect.DeepEqual(duplicatePause.State, paused.State) || now.TaskSeq != head.TaskSeq {
		t.Fatal("command repeated effects")
	}
	_, err = ipc.Call(context.Background(), host.Server.Info, ipc.Request{ID: "pause-one", Command: "cancel", TaskID: "task", Payload: json.RawMessage("null")})
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != c.CommandIDConflict {
		t.Fatal("ID reuse not rejected", err)
	}
	cancelled := decodeView(t, rpc(t, host, "cancel", "cancel-one", nil))
	if cancelled.State.Outcome != c.Cancelled || cancelled.State.Quality != c.Unverified {
		t.Fatal(cancelled)
	}
	live, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(live) != "base" {
		t.Fatal("live source changed")
	}
}
func TestPauseResumeSameGenerationAndOldPauseCannotInterruptNewRun(t *testing.T) {
	host, _, _ := ownerFixture(t, longTurns(), true)
	paused := decodeView(t, rpc(t, host, "pause", "old-pause", nil))
	done := make(chan error, 1)
	go func() { _, err := host.Run(context.Background(), "task", "new-run"); done <- err }()
	waitActive(t, host)
	old := decodeView(t, rpc(t, host, "pause", "old-pause", nil))
	if old.State.TaskSeq != paused.State.TaskSeq {
		t.Fatal("duplicate was rebound")
	}
	status := decodeView(t, rpc(t, host, "status", "check-running", nil))
	if status.State.Execution == c.Paused {
		t.Fatal("old command interrupted new invocation")
	}
	rpc(t, host, "cancel", "cancel-current", nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestInterruptedForegroundPausesAndShutdownIsIdempotent(t *testing.T) {
	host, _, _ := ownerFixture(t, longTurns(), true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan View, 1)
	failures := make(chan error, 1)
	go func() { view, err := host.Run(ctx, "task", "interrupted"); failures <- err; done <- view }()
	waitActive(t, host)
	cancel()
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	view := <-done
	if view.State.Execution != c.Paused || view.State.Outcome == c.Cancelled {
		t.Fatal("Ctrl+C was not a pause", view)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestAdmissionWithoutReceiptAndFailedInvocationAreNeverRetried(t *testing.T) {
	host, _, source := ownerFixture(t, []agent.Turn{{UsageKnown: true, Calls: []model.Call{{ID: "read", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}}}, true)
	if _, _, err := host.Session.InvocationStart(context.Background(), "task", "lost"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Run(context.Background(), "task", "lost"); err == nil {
		t.Fatal("incomplete admission blindly retried")
	}
	if err := os.Remove(filepath.Join(source, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	_, first := host.Run(context.Background(), "task", "exhausted")
	if first == nil {
		t.Fatal("exhausted fixture unexpectedly succeeded")
	}
	head, _ := host.Session.State(context.Background(), "task")
	_, again := host.Run(context.Background(), "task", "exhausted")
	if again == nil || first.Error() != again.Error() {
		t.Fatal("failed receipt changed", first, again)
	}
	next, _ := host.Session.State(context.Background(), "task")
	if next.TaskSeq != head.TaskSeq {
		t.Fatal("failed invocation reran")
	}
}
func TestOwnerRejectsArbitraryCommandsAndMalformedPayloads(t *testing.T) {
	host, _, _ := ownerFixture(t, []agent.Turn{{UsageKnown: true, Text: "done"}}, true)
	for command, payload := range map[string]string{"host.shell": "null", "status": `{"argv":["evil"]}`, "events": `{"after":0,"limit":1,"unknown":true}`, "respond": `{"command_id":"different"}`} {
		_, err := ipc.Call(context.Background(), host.Server.Info, ipc.Request{ID: "bad-" + command, Command: command, TaskID: "task", Payload: json.RawMessage(payload)})
		if err == nil {
			t.Fatal("invalid owner command accepted", command)
		}
	}
}

func TestActiveSteeringPublishesRawBarrierAndNeverRerunsOnDuplicate(t *testing.T) {
	host, _, _ := ownerFixture(t, longTurns(), true)
	done := make(chan error, 1)
	go func() { _, err := host.Run(context.Background(), "task", "steered-run"); done <- err }()
	waitActive(t, host)
	input := agent.SteeringInput{CommandID: "instruction", TaskID: "task", Text: "Stop and preserve the user's live edits.\r\n"}
	accepted := decodeView(t, rpc(t, host, "steer", input.CommandID, input))
	if !accepted.State.InputBarrier || accepted.State.Execution != c.Paused {
		t.Fatal("input barrier not held", accepted)
	}
	<-done // A concurrent candidate pointer may fail its optimistic sequence guard.
	duplicate := decodeView(t, rpc(t, host, "steer", input.CommandID, input))
	if !reflect.DeepEqual(accepted, duplicate) {
		t.Fatal("duplicate steering changed its recorded result")
	}
	original, err := host.Session.Archive.GetBytes("task", c.HashBytes([]byte(input.Text)))
	if err != nil || string(original) != input.Text {
		t.Fatal("raw input unavailable", err)
	}
	status := decodeView(t, rpc(t, host, "status", "steered-status", nil))
	if !status.State.InputBarrier || status.ReleaseReady {
		t.Fatal(status)
	}
}
