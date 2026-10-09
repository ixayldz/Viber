package owner

import (
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestDetachedInvocationOutlivesClientAndSupportsCursorReconnect(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "local-test:1", "message": map[string]any{"role": "assistant", "content": "done"}, "done": true, "done_reason": "stop", "prompt_eval_count": 1, "eval_count": 1})
	}))
	defer server.Close()
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	source, directory := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := agent.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	runtime := &agent.Runtime{SchemaVersion: 1, Provider: "ollama", Endpoint: server.URL, Model: "local-test:1", DeclaredLocal: true, ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 10000}
	if _, err = session.Create(context.Background(), agent.StartOptions{Root: source, TaskID: "task", Prompt: []byte("Review source"), Budget: agent.DefaultBudget(), Autonomy: "guided", Runtime: runtime}); err != nil {
		t.Fatal(err)
	}
	host, err := Open(context.Background(), directory, session)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	client, cancel := context.WithCancel(context.Background())
	admission, err := host.StartDetached(client, "task", "detached-one")
	if err != nil || !admission.Detached || admission.InvocationID != "detached-one" {
		t.Fatal(admission, err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model was not dispatched")
	}
	cancel()
	if _, err = session.QueueControl(context.Background(), agent.QueueCommand{CommandID: "while-inference", TaskID: "task", Action: "add", Text: "future work"}); err != nil {
		t.Fatal("active inference blocked independent queue", err)
	}
	duplicate, err := host.StartDetached(context.Background(), "task", "detached-one")
	if err != nil || !reflect.DeepEqual(admission, duplicate) {
		t.Fatal("active detached dedup changed", err)
	}
	if _, err = host.StopSupervisor(context.Background()); err == nil {
		t.Fatal("active owner was stopped")
	}
	page, err := host.AttachPage(context.Background(), "task", Page{After: 0, Limit: 2})
	if err != nil || !page.Active || len(page.History.Records) != 2 || page.History.Next != 2 {
		t.Fatal(page, err)
	}
	next, err := host.AttachPage(context.Background(), "task", Page{After: page.History.Next, Limit: 256})
	if err != nil || len(next.History.Records) == 0 || next.History.Records[0].Event.TaskSeq != 3 {
		t.Fatal("reconnect gap", next, err)
	}
	host.mu.Lock()
	active := host.active
	host.mu.Unlock()
	if active == nil {
		t.Fatal("client cancellation stopped detached task")
	}
	close(gate)
	select {
	case <-active.done:
	case <-time.After(10 * time.Second):
		t.Fatal("detached task failed to settle")
	}
	if active.err != nil {
		t.Fatal(active.err)
	}
	state, doc, err := session.Load(context.Background(), "task")
	if err != nil || state.Execution != c.WaitingUser || doc.UnknownEffect || len(state.Tokens.Reservations) != 1 || state.Tokens.Reservations[0].Status != "SETTLED" {
		t.Fatal(state, doc.Blocker, err)
	}
	if _, err = host.StartDetached(context.Background(), "task", "detached-one"); err != nil {
		t.Fatal("completed dedup lost", err)
	}
	final, err := host.AttachPage(context.Background(), "task", Page{After: next.History.Next, Limit: 256})
	if err != nil || final.Active || final.State.TaskSeq != state.TaskSeq {
		t.Fatal(final, err)
	}
	if _, err = host.AttachPage(context.Background(), "task", Page{After: state.TaskSeq + 1, Limit: 1}); err == nil {
		t.Fatal("future cursor accepted")
	}
	raw, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(raw) != "source" {
		t.Fatal("detached inference changed source")
	}
	if _, err = host.StopSupervisor(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-host.Done():
	case <-time.After(time.Second):
		t.Fatal("quiescent owner did not stop")
	}
}
