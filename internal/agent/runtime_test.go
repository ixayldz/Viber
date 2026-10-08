package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/plan"
)

func localSession(t *testing.T, endpoint string) (*Session, StartOptions, string) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("before\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	opts := StartOptions{Root: source, Prompt: []byte("Change a.txt using candidate tools."), TaskID: "local-one", Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Runtime: &LocalRuntime{SchemaVersion: 1, Provider: "ollama", Endpoint: endpoint, Model: "local-test:1", DeclaredLocal: true, ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 1000}}
	if _, err = s.Create(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	return s, opts, source
}
func TestLocalRuntimeCanonicalLoopBackupAndRestoreNeedNoCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("wrong endpoint or credential leak")
		}
		var request struct {
			Model   string `json:"model"`
			Options struct {
				Context int64 `json:"num_ctx"`
				Output  int64 `json:"num_predict"`
			} `json:"options"`
			Messages []struct {
				Role     string `json:"role"`
				ToolName string `json:"tool_name"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "local-test:1" || request.Options.Context != 32768 || request.Options.Output != 512 {
			t.Error("profile not bound", request)
		}
		message := map[string]any{"role": "assistant", "content": "Work complete; required checks remain UNKNOWN."}
		if n == 1 {
			var args any
			json.Unmarshal(patchCall().Arguments, &args)
			message["content"] = ""
			message["tool_calls"] = []any{map[string]any{"function": map[string]any{"name": "candidate_propose", "arguments": args}}}
		} else {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "tool" || last.ToolName != "candidate_propose" {
				t.Error("lost tool/result continuation")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "local-test:1", "message": message, "done": true, "done_reason": "stop", "prompt_eval_count": 10, "eval_count": 4})
	}))
	defer server.Close()
	s, opts, source := localSession(t, server.URL)
	state, err := s.Run(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || state.Quality != c.Unverified || state.Outcome != c.Finished || doc.FixtureCursor != 0 || doc.Budget.UsedInput != 20 || doc.Context.Profile != localContextProfile {
		t.Fatal("bad local loop", state, doc.Budget, calls.Load())
	}
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil || string(candidate.Contents["a.txt"]) != "after\r\n" {
		t.Fatal("candidate not updated", err)
	}
	raw, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(raw) != "before\r\n" {
		t.Fatal("live source changed")
	}
	parent := t.TempDir()
	backup := filepath.Join(parent, "backup")
	restored := filepath.Join(parent, "restored")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreBackup(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenExisting(context.Background(), restored)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	_, actual, err := recovered.Load(context.Background(), opts.TaskID)
	if err != nil || actual.Runtime.Model != opts.Runtime.Model || actual.Context.RequestDigest != doc.Context.RequestDigest || calls.Load() != 2 {
		t.Fatal("restore executed effect or lost profile", err)
	}
}
func TestLocalUnknownUsageRetainsReservationAndNeverRetriesAcrossRestart(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": "not charge proof"}, "done": true, "done_reason": "stop"})
	}))
	defer server.Close()
	s, opts, _ := localSession(t, server.URL)
	state, err := s.Run(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil || state.Execution != c.Blocked || !doc.UnknownEffect || doc.Budget.ReservedInput == 0 || doc.Budget.ReservedOutput == 0 || doc.Pending == nil {
		t.Fatal("unknown cleared", err, state, doc.Budget)
	}
	directory := s.directory
	s.Close()
	reopened, err := OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed, err := reopened.Run(context.Background(), opts.TaskID)
	_, after, loadErr := reopened.Load(context.Background(), opts.TaskID)
	if err != nil || loadErr != nil || resumed.Execution != c.Blocked || calls.Load() != 1 || after.Budget.ReservedInput != doc.Budget.ReservedInput {
		t.Fatal("unknown inference repeated", err, loadErr)
	}
}
func TestLocalProfileRejectsCloudRemoteMixedAndOverflowBeforeDispatch(t *testing.T) {
	cfg := LocalRuntime{SchemaVersion: 1, Provider: "ollama", Endpoint: "http://127.0.0.1:11434", Model: "local:1", DeclaredLocal: true, ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 1000}
	for _, bad := range []LocalRuntime{
		{SchemaVersion: 1, Provider: "ollama", Endpoint: "http://localhost:11434", Model: "local:1", DeclaredLocal: true, ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 1000},
		{SchemaVersion: 1, Provider: "ollama", Endpoint: "http://127.0.0.1:11434", Model: "LOCAL:CLOUD", DeclaredLocal: true, ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 1000},
	} {
		if bad.Validate() == nil {
			t.Fatal("unsafe local profile admitted")
		}
	}
	cfg.DeclaredLocal = false
	if cfg.Validate() == nil {
		t.Fatal("no locality declaration")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	s, opts, _ := localSession(t, server.URL)
	state, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Runtime.ContextLimit = 8192
	if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = s.Run(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, after, err := s.Load(context.Background(), opts.TaskID)
	if err != nil || state.Execution != c.WaitingResource || calls.Load() != 0 || after.Budget.Steps != 0 || after.Budget.ReservedInput != 0 {
		t.Fatal("overflow dispatched", err, state, calls.Load())
	}
}
func TestLocalSteeringRevisionPreservesRuntimeAndInvalidatesPlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("revision dispatched inference") }))
	defer server.Close()
	s, opts, _ := localSession(t, server.URL)
	initial, doc, err := s.Load(context.Background(), opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	definition := plan.Definition{SchemaVersion: 1, ID: "before-revision", SpecVersion: doc.Spec.Version, PolicyEpoch: initial.PolicyEpoch, BaseCandidate: doc.Candidate.SnapshotDigest, Nodes: []plan.Node{{ID: "inspect", Goal: "inspect", InputContract: "captured source", OutputContract: "report", ReadScope: []string{"a.txt"}, RequirementIDs: []string{"user-goal"}}}}
	doc.Plan, err = plan.New(definition, doc.Spec, nil, doc.Candidate.SnapshotDigest, initial.PolicyEpoch)
	if err != nil {
		t.Fatal(err)
	}
	doc.Plan.Next()
	if _, err = s.record(context.Background(), initial, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err := s.RecordSteering(context.Background(), SteeringInput{CommandID: "local-input", TaskID: opts.TaskID, Text: "Only inspect; do not change source."})
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.Controls(context.Background(), opts.TaskID, "pause")
	if err != nil {
		t.Fatal(err)
	}
	revised, err := s.Revise(context.Background(), ScopeRevision{CommandID: "local-revision", TaskID: opts.TaskID, InputID: "local-input", ExpectedSpecVersion: state.SpecVersion, ExpectedPolicyEpoch: state.PolicyEpoch, ExpectedCandidate: state.CandidateDigest})
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err = s.Load(context.Background(), opts.TaskID)
	if err != nil || doc.Runtime == nil || doc.FixtureDigest != "" || revised.InputBarrier || doc.Plan != nil || doc.Spec.Version != 2 {
		t.Fatal("revision lost runtime", err)
	}
}

func TestLocalRuntimeRejectsUnexpectedResponseModelWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"model": "other-model:1", "message": map[string]any{"role": "assistant", "content": "done"}, "done": true, "done_reason": "stop", "prompt_eval_count": 10, "eval_count": 4})
	}))
	defer server.Close()
	s, opts, _ := localSession(t, server.URL)
	state, err := s.Run(context.Background(), opts.TaskID)
	_, doc, loadErr := s.Load(context.Background(), opts.TaskID)
	if err != nil || loadErr != nil || state.Execution != c.Blocked || !doc.UnknownEffect || calls.Load() != 1 || doc.Budget.ReservedInput == 0 {
		t.Fatal("model lock mismatch bypassed", err, loadErr)
	}
}

func TestLocalInferenceCannotExtendTaskActiveWorkDeadline(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx := context.Background()
	session, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	budget := DefaultBudget()
	budget.MaxActiveMillis = 1000
	profile := &LocalRuntime{SchemaVersion: 1, Provider: "ollama", Endpoint: server.URL, Model: "local-test:1", DeclaredLocal: true, ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 3000}
	if _, err = session.Create(ctx, StartOptions{Runtime: profile, Root: t.TempDir(), Prompt: []byte("Inspect empty source"), TaskID: "deadline", Budget: budget, Autonomy: "guided", AllowUnverified: true}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	state, err := session.Run(ctx, "deadline")
	_, doc, loadErr := session.Load(ctx, "deadline")
	if err != nil || loadErr != nil || state.Execution != c.Blocked || !doc.UnknownEffect || calls.Load() != 1 || doc.Budget.ActiveMillis < 1000 || time.Since(started) >= 2800*time.Millisecond || doc.Budget.ReservedInput == 0 {
		t.Fatal("model timeout extended work deadline or lost usage risk", state, doc.Budget, err, loadErr, calls.Load())
	}
}
