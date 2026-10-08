package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestLocalStreamUsesDurableToolLoopAndRetainedReceipts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var request map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&request) != nil || string(request["stream"]) != "true" || r.Header.Get("Accept") != "application/x-ndjson" || r.Header.Get("Authorization") != "" {
			t.Error("stream profile/credential drift")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		message := map[string]any{"role": "assistant", "content": "Streamed candidate result; verification remains UNKNOWN."}
		if n == 1 {
			var args any
			json.Unmarshal(patchCall().Arguments, &args)
			message["content"] = ""
			message["tool_calls"] = []any{map[string]any{"function": map[string]any{"index": 0, "name": "candidate_propose", "arguments": args}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "local-test:1", "message": message, "done": false})
		w.(http.Flusher).Flush()
		json.NewEncoder(w).Encode(map[string]any{"model": "local-test:1", "message": map[string]any{"role": "assistant", "content": ""}, "done": true, "done_reason": "stop", "prompt_eval_count": 10, "eval_count": 4})
	}))
	defer server.Close()
	s, options, source := localSession(t, server.URL)
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Runtime.Stream = true
	if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err = s.Load(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.Terminated || state.Quality != c.Unverified || doc.Budget.UsedInput != 20 || calls.Load() != 2 {
		t.Fatal("stream loop/usage", state, doc.Budget, calls.Load(), err)
	}
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil || string(candidate.Contents["a.txt"]) != "after\r\n" {
		t.Fatal("candidate", err)
	}
	live, err := os.ReadFile(filepath.Join(source, "a.txt"))
	if err != nil || string(live) != "before\r\n" {
		t.Fatal("source changed", err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	target := filepath.Join(t.TempDir(), "restored")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreBackup(context.Background(), backup, target); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	_, actual, err := restored.Load(context.Background(), options.TaskID)
	if err != nil || !actual.Runtime.Stream || actual.Context.RequestDigest != doc.Context.RequestDigest || calls.Load() != 2 {
		t.Fatal("stream restore dispatch/profile", err)
	}
	actual.Runtime.Stream = false
	if err = restored.validateContext(actual); err == nil {
		t.Fatal("context allowed JSON/stream profile switch")
	}
}

func TestDisconnectedLocalStreamKeepsCandidateAndUnknownReservationAcrossRestore(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var args any
		json.Unmarshal(patchCall().Arguments, &args)
		json.NewEncoder(w).Encode(map[string]any{"model": "local-test:1", "message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "candidate_propose", "arguments": args}}}}, "done": false})
		// A full tool block without the done marker is not dispatch authority.
	}))
	defer server.Close()
	s, options, _ := localSession(t, server.URL)
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Runtime.Stream = true
	if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err = s.Load(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.Blocked || !doc.UnknownEffect || doc.Candidate != doc.Baseline || doc.Budget.ReservedInput == 0 || doc.Budget.ReservedOutput == 0 {
		t.Fatal("partial dispatch or released unknown", state, doc.Budget, err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	target := filepath.Join(t.TempDir(), "restored")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreBackup(context.Background(), backup, target); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	state, err = restored.Run(context.Background(), options.TaskID)
	_, actual, loadErr := restored.Load(context.Background(), options.TaskID)
	if err != nil || loadErr != nil || state.Execution != c.Blocked || calls.Load() != 1 || actual.Budget.ReservedInput != doc.Budget.ReservedInput || actual.Candidate != actual.Baseline {
		t.Fatal("partial request retry after restore", err, loadErr, calls.Load())
	}
}
