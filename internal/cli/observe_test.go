package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	"github.com/ixayldz/Viber/internal/auth"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestCLIAnalysisObservationAndExplicitAttemptWorkflow(t *testing.T) {
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "readme.txt"), []byte("hello\r\n"), 0600)
	directory := filepath.Join(t.TempDir(), "store")
	fixture := filepath.Join(t.TempDir(), "fixture.json")
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "Read-only inspection report", UsageKnown: true, InputTokens: 4, OutputTokens: 2}}})
	os.WriteFile(fixture, raw, 0600)
	run := func(want int, args ...string) []byte {
		t.Helper()
		var out, errout bytes.Buffer
		exit := Execute(args, &out, &errout)
		if exit != want {
			t.Fatalf("%v: %d want %d %s %s", args, exit, want, out.String(), errout.String())
		}
		return out.Bytes()
	}
	raw = run(2, "run", "Inspect source", "--offline", "--fixture", fixture, "--task-kind", "ANALYSIS", "--root", source, "--store", directory, "--task", "observe", "--allow-unverified", "--json")
	var view struct {
		State c.TaskState `json:"state"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	raw = run(0, "report", "observe", "--store", directory, "--json")
	var report agent.FinalArtifact
	if err := json.Unmarshal(raw, &report); err != nil || report.Kind != "ANALYSIS" || report.Trust != "MODEL_AUTHORED_UNREVIEWED" {
		t.Fatal("analysis report", string(raw), err)
	}
	raw = run(0, "context-why", "observe", "--store", directory, "--json")
	var why agent.ContextExplanation
	if err := json.Unmarshal(raw, &why); err != nil || !why.Available || len(why.Components) < 3 {
		t.Fatal("context explanation", string(raw), err)
	}
	assembled := []byte{}
	for offset := int64(0); ; {
		raw = run(0, "context-page", "observe", "--store", directory, "--offset", strconv.FormatInt(offset, 10), "--limit", "4096", "--json")
		var page struct {
			Bytes    []byte `json:"exact_bytes"`
			Next     int64  `json:"next_offset"`
			Complete bool   `json:"complete"`
			Digest   string `json:"request_digest"`
		}
		if err := json.Unmarshal(raw, &page); err != nil || page.Digest != why.RequestDigest || page.Next != offset+int64(len(page.Bytes)) {
			t.Fatal("context page", err)
		}
		assembled = append(assembled, page.Bytes...)
		if page.Complete {
			break
		}
		offset = page.Next
	}
	if c.HashBytes(assembled) != why.RequestDigest {
		t.Fatal("lossy context byte pages")
	}
	run(0, "checks", "observe", "--store", directory, "--json")
	for _, args := range [][]string{{"report", "observe", "--store", directory, "--limit", "1"}, {"context-page", "observe", "--store", directory, "--run-id", "unrelated"}, {"check-output", "observe", "--store", directory, "--run-id", "missing"}, {"context-page", "observe", "--store", directory, "--offset", "-1"}} {
		run(4, args...)
	}
	if err := os.WriteFile(filepath.Join(source, "user-edit.txt"), []byte("fresh edit"), 0600); err != nil {
		t.Fatal(err)
	}
	seq := strconv.FormatInt(view.State.TaskSeq, 10)
	run(0, "attempt", "observe", "--store", directory, "--new-task", "observe-again", "--parent-seq", seq, "--fixture", fixture, "--allow-unverified", "--json")
	run(0, "attempt", "observe", "--store", directory, "--new-task", "observe-again", "--parent-seq", seq, "--fixture", fixture, "--allow-unverified", "--json")
	run(2, "resume", "observe-again", "--store", directory, "--json")
	raw = run(0, "report", "observe-again", "--store", directory, "--json")
	if err := json.Unmarshal(raw, &report); err != nil || report.Kind != "ANALYSIS" || report.TaskID != "observe-again" {
		t.Fatal("fresh attempt", err)
	}
	run(4, "attempt", "observe", "--store", directory, "--new-task", "observe-again", "--parent-seq", seq, "--fixture", fixture, "--json")
}
func TestCLIAuthStatusNeverCreatesCredentialsAndRejectsUnsafeProvider(t *testing.T) {
	base := t.TempDir()
	for _, directory := range []string{filepath.Join(base, "missing"), base} {
		var out, errout bytes.Buffer
		exit := Execute([]string{"auth", "status", "--auth-dir", directory, "--json"}, &out, &errout)
		if exit != 0 {
			t.Fatal(exit, out.String(), errout.String())
		}
		var view auth.View
		if err := json.Unmarshal(out.Bytes(), &view); err != nil || len(view.Profiles) != 0 {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(directory, "credentials.bin")); !os.IsNotExist(err) {
			t.Fatal("status created credential file")
		}
	}
	for _, args := range [][]string{{"auth", "login", "--provider", "anthropic"}, {"auth", "select", "--profile", "missing", "--auth-dir", filepath.Join(base, "missing")}, {"run", "Inspect", "--provider", "chatgpt"}, {"run", "Inspect", "--provider", "chatgpt", "--allow-remote", "--auth-dir", filepath.Join(base, "missing")}, {"run", "Inspect", "--offline", "--allow-remote"}} {
		var out, errout bytes.Buffer
		directory := filepath.Join(t.TempDir(), "no-store")
		if args[0] == "run" {
			args = append(args, "--store", directory)
		}
		if exit := Execute(args, &out, &errout); exit != 4 {
			t.Fatal(exit, args, out.String(), errout.String())
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("unsafe auth initialized task store")
		}
	}
}

func TestRemoteCredentialFlagsFailBeforeStoreWithoutLeakingHandleValue(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		handle := "OPENAI_API_KEY"
		if provider == "anthropic" {
			handle = "ANTHROPIC_API_KEY"
		}
		t.Setenv(handle, "fixture-secret-do-not-print")
		for _, extra := range [][]string{{}, {"--endpoint", "https://attacker.test"}, {"--offline"}, {"--context-limit", "1"}, {"--output-limit", "0"}} {
			directory := filepath.Join(t.TempDir(), "no-store")
			args := []string{"run", "Inspect", "--provider", provider, "--allow-remote", "--model", "operator-model", "--store", directory}
			if len(extra) == 0 {
				args = append(args, "--local-model")
			} else {
				args = append(args, extra...)
			}
			var out, errout bytes.Buffer
			if exit := Execute(args, &out, &errout); exit != 4 {
				t.Fatal(exit, out.String(), errout.String())
			}
			if strings.Contains(out.String()+errout.String(), "fixture-secret-do-not-print") {
				t.Fatal("error leaked credential")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("invalid API profile created store")
			}
		}
		t.Setenv(handle, "")
		directory := filepath.Join(t.TempDir(), "no-store")
		var out, errout bytes.Buffer
		if exit := Execute([]string{"run", "Inspect", "--provider", provider, "--allow-remote", "--model", "operator-model", "--store", directory}, &out, &errout); exit != 4 {
			t.Fatal("missing credential accepted", exit)
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("missing credential created store")
		}
	}
}

func TestShippedKeylessExamplesUseAcceptedSchemas(t *testing.T) {
	examples := filepath.Join("..", "..", "examples", "offline")
	for _, name := range []string{"greeting.json", "analysis.json", "check.json"} {
		raw, err := os.ReadFile(filepath.Join(examples, name))

		if err != nil {
			t.Fatal(err)
		}
		if _, err = agent.ParseFixture(raw); err != nil {
			t.Fatal("shipped fixture rejected", name, err)
		}
	}
	var out, errout bytes.Buffer
	if code := Execute([]string{"check-config", "--runtime", filepath.Join(examples, "check-runtime.json"), "--plan", filepath.Join(examples, "check-plan.json"), "--json"}, &out, &errout); code != 0 {
		t.Fatal("shipped runner/check plan mismatch", code, out.String(), errout.String())
	}
	directory := filepath.Join(t.TempDir(), "analysis-store")
	out.Reset()
	errout.Reset()
	if code := Execute([]string{"run", "Explain the captured greeting", "--offline", "--fixture", filepath.Join(examples, "analysis.json"), "--task-kind", "ANALYSIS", "--root", filepath.Join(examples, "source"), "--store", directory, "--task", "shipped-analysis", "--allow-unverified", "--json"}, &out, &errout); code != 2 {
		t.Fatal("shipped analysis flow", code, out.String(), errout.String())
	}
	out.Reset()
	errout.Reset()
	if code := Execute([]string{"report", "shipped-analysis", "--store", directory, "--json"}, &out, &errout); code != 0 {
		t.Fatal("shipped analysis artifact", code, out.String(), errout.String())
	}
	var artifact agent.FinalArtifact
	if err := json.Unmarshal(out.Bytes(), &artifact); err != nil || artifact.Kind != "ANALYSIS" {
		t.Fatal("shipped analysis kind", err, artifact.Kind)
	}
}
