package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
)

func TestCLIIPCChildOwner(t *testing.T) {
	if os.Getenv("VIBER_TEST_CHILD_OWNER") != "1" {
		return
	}
	directory := os.Args[len(os.Args)-1]
	os.Exit(Execute([]string{"serve", "--store", directory, "--json"}, os.Stdout, os.Stderr))
}
func TestSeparateCLIUsesSameOwnerAndCrashDescriptorReconciles(t *testing.T) {
	source := t.TempDir()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := agent.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{UsageKnown: true, Text: "done"}}})
	_, err = session.Create(context.Background(), agent.StartOptions{Root: source, Prompt: []byte("Review source"), TaskID: "child-task", Budget: agent.DefaultBudget(), Autonomy: "guided", Fixture: fixture})
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process := exec.Command(executable, "-test.run=^TestCLIIPCChildOwner$", "--", directory)
	process.Env = append(os.Environ(), "VIBER_TEST_CHILD_OWNER=1")
	stdout, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var childErrors bytes.Buffer
	process.Stderr = &childErrors
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			process.Process.Kill()
			process.Wait()
		}
	}()
	ready := make(chan error, 1)
	go func() {
		_, readErr := bufio.NewReader(stdout).ReadBytes('\n')
		ready <- readErr
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case err = <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("child owner not ready")
	}
	info, err := ipc.Discover(directory)
	if err != nil || info.PID == os.Getpid() {
		t.Fatal(info, err)
	}
	var out, stderr bytes.Buffer
	for _, command := range []string{"status", "inspect", "events", "diff", "requests"} {
		out.Reset()
		stderr.Reset()
		if exit := Execute([]string{command, "child-task", "--store", directory, "--json"}, &out, &stderr); exit != 0 || !json.Valid(out.Bytes()) {
			t.Fatal(command, exit, out.String(), stderr.String())
		}
	}
	out.Reset()
	if exit := Execute([]string{"resume", "child-task", "--store", directory, "--command-id", "ipc-run", "--json"}, &out, &stderr); exit != 3 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	out.Reset()
	if exit := Execute([]string{"requests", "child-task", "--store", directory, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String())
	}
	var requests []agent.UserRequest
	if err = c.DecodeStrict(out.Bytes(), &requests); err != nil || len(requests) != 1 {
		t.Fatal(err, out.String())
	}
	request := requests[0]
	response := agent.UserResponse{CommandID: "approve-delivery", TaskID: "child-task", RequestID: request.ID, Attempt: request.Attempt, ExpectedSpecVersion: request.SpecVersion, ExpectedPolicyDigest: request.PolicyDigest, ActionDigest: request.ActionDigest}
	response.Response.Decision = "approve"
	raw, _ := json.Marshal(response)
	responseFile := filepath.Join(t.TempDir(), "response.json")
	if err = os.WriteFile(responseFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if exit := Execute([]string{"respond", "child-task", "--store", directory, "--request", request.ID, "--response-file", responseFile, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	out.Reset()
	if exit := Execute([]string{"resume", "child-task", "--store", directory, "--command-id", "ipc-delivery", "--json"}, &out, &stderr); exit != 2 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	before := append([]byte{}, out.Bytes()...)
	out.Reset()
	if exit := Execute([]string{"resume", "child-task", "--store", directory, "--command-id", "ipc-delivery", "--json"}, &out, &stderr); exit != 2 || !bytes.Equal(before, out.Bytes()) {
		t.Fatal("duplicate changed result", exit, out.String())
	}
	process.Process.Kill()
	process.Wait()
	waited = true
	out.Reset()
	if exit := Execute([]string{"status", "child-task", "--store", directory, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal("stale descriptor blocked safe recovery", exit, out.String(), stderr.String(), childErrors.String())
	}
	live, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(live) != "base" {
		t.Fatal("source changed")
	}
}
func TestLostOwnerResponseNeverStartsSecondExecution(t *testing.T) {
	directory := t.TempDir()
	host, err := ipc.OpenServer(context.Background(), directory, 1, func(ctx context.Context, r ipc.Request) (any, error) { <-ctx.Done(); return nil, ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Serve() }()
	defer func() { host.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, routed, err := ownerCall(ctx, directory, "task", "resume", "lost-command", nil)
	var typed *c.Error
	if !routed || !errors.As(err, &typed) || typed.Code != c.UnknownOutcome {
		t.Fatal("dispatch loss eligible for fallback", routed, err)
	}
}

func TestCLISteerRevisePreservesRawInputAndRequiresFreshApproval(t *testing.T) {
	source := t.TempDir()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{UsageKnown: true, Text: "done"}}}
	raw, _ := json.Marshal(fixture)
	fixtureFile := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(fixtureFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if exit := Execute([]string{"run", "Review source", "--offline", "--fixture", fixtureFile, "--root", source, "--store", directory, "--task", "steering-task", "--json"}, &out, &stderr); exit != 3 {
		t.Fatal(exit, out.String())
	}
	out.Reset()
	args := []string{"steer", "steering-task", "Preserve user data.\r\n", "--store", directory, "--command-id", "raw-one", "--json"}
	if exit := Execute(args, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	original := append([]byte{}, out.Bytes()...)
	out.Reset()
	if exit := Execute(args, &out, &stderr); exit != 0 || !bytes.Equal(original, out.Bytes()) {
		t.Fatal("duplicate CLI steering changed outcome", exit, out.String())
	}
	var result struct {
		State c.TaskState `json:"state"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	state := result.State
	if !state.InputBarrier || state.Execution != c.Paused {
		t.Fatal(state)
	}
	revision := agent.ScopeRevision{CommandID: "revision-one", TaskID: "steering-task", InputID: "raw-one", ExpectedSpecVersion: state.SpecVersion, ExpectedPolicyEpoch: state.PolicyEpoch, ExpectedCandidate: state.CandidateDigest, Fixture: raw}
	revisionRaw, _ := json.Marshal(revision)
	file := filepath.Join(t.TempDir(), "revision.json")
	if err := os.WriteFile(file, revisionRaw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if exit := Execute([]string{"revise", "steering-task", "--store", directory, "--revision-file", file, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	if err := c.DecodeStrict(out.Bytes(), &state); err != nil || state.SpecVersion != 2 || state.InputBarrier {
		t.Fatal(err, out.String())
	}
	out.Reset()
	if exit := Execute([]string{"resume", "steering-task", "--store", directory, "--json"}, &out, &stderr); exit != 3 {
		t.Fatal("new scope inherited old delivery approval", exit, out.String())
	}
}
