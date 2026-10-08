package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestCLILocalRuntimeUsesDeclaredProfileAndReportsUnverified(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": "Inspection report; not VERIFIED."}, "done": true, "done_reason": "stop", "prompt_eval_count": 12, "eval_count": 3})
	}))
	defer server.Close()
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "a.txt"), []byte("hello"), 0600)
	directory := t.TempDir()
	var out, errout bytes.Buffer
	exit := Execute([]string{"run", "Inspect source", "--provider", "ollama", "--endpoint", server.URL, "--model", "local-test:1", "--local-model", "--root", source, "--store", directory, "--task", "local-cli", "--allow-unverified", "--json"}, &out, &errout)
	if exit != 2 || calls != 1 {
		t.Fatal(exit, out.String(), errout.String(), calls)
	}
	var result struct {
		Runtime *agent.LocalRuntime `json:"runtime"`
		State   c.TaskState         `json:"state"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Runtime == nil || result.State.Quality != c.Unverified {
		t.Fatal(result, err)
	}
	out.Reset()
	if exit = Execute([]string{"status", "local-cli", "--store", directory, "--json"}, &out, &errout); exit != 0 {
		t.Fatal(exit, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("operator_declared_local")) {
		t.Fatal("status lost runtime")
	}
}
func TestCLIRuntimeRejectsUnsafeConfigBeforeCreatingStore(t *testing.T) {
	for _, args := range [][]string{
		{"--provider", "ollama", "--model", "local:1"},
		{"--provider", "ollama", "--model", "local:CLOUD", "--local-model"},
		{"--provider", "ollama", "--model", "local:1", "--local-model", "--endpoint", "http://192.0.2.1:11434"},
		{"--provider", "ollama", "--model", "local:1", "--local-model", "--offline"},
		{"--provider", "openai", "--model", "remote"},
	} {
		directory := filepath.Join(t.TempDir(), "must-not-exist")
		var out, errout bytes.Buffer
		argv := append([]string{"run", "Inspect", "--store", directory}, args...)
		exit := Execute(argv, &out, &errout)
		if exit != 3 && exit != 4 {
			t.Fatal(exit, out.String())
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("invalid config initialized store")
		}
	}
}
