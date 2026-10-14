package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func writePRDTestFixture(t *testing.T, directory string) string {
	t.Helper()
	raw, err := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "bounded offline response", UsageKnown: true}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "fixture.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePRDTestUserConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "user-config.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"preferences":{},"restrictions":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPRDPathValidation(t *testing.T) {
	for _, path := range []string{"PRD.md", "docs/product-spec.md", "features/2026 plan.md"} {
		if err := validatePRDPath(path); err != nil {
			t.Fatalf("valid path %q: %v", path, err)
		}
	}
	for _, path := range []string{"", ".", "../outside.md", "docs/../../outside.md", `C:\secret.md`, `docs\PRD.md`, "/absolute/PRD.md", strings.Repeat("a", 257)} {
		if err := validatePRDPath(path); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
}

func TestRunPRDUsesExactCapturedPathAndPreservesRawTask(t *testing.T) {
	source := t.TempDir()
	store := filepath.Join(t.TempDir(), "task-store")
	fixture := writePRDTestFixture(t, t.TempDir())
	userConfig := writePRDTestUserConfig(t)
	const rawTask = "Implement the workflow exactly as requested.\n"
	const prdBody = "PRD body must only be read through the admitted fs_read tool."
	if err := os.WriteFile(filepath.Join(source, "PRD.md"), []byte(prdBody), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	args := []string{"run", rawTask, "--root", source, "--store", store, "--user-config", userConfig, "--task", "prd-positive", "--offline", "--fixture", fixture, "--allow-unverified", "--prd", "PRD.md", "--json"}
	if exit := Execute(args, &out, &stderr); exit != 2 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, out.String(), stderr.String())
	}
	session, err := agent.OpenExisting(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	state, doc, err := session.Load(context.Background(), "prd-positive")
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.Terminated || !doc.FinalReady || len(doc.Messages) < 2 {
		t.Fatalf("offline dispatcher did not complete: state=%#v final=%t messages=%d", state, doc.FinalReady, len(doc.Messages))
	}
	initial := doc.Messages[0].Text
	if !strings.HasPrefix(initial, rawTask) || !strings.Contains(initial, `Exact captured project-relative PRD path: "PRD.md"`) ||
		!strings.Contains(initial, "fs_read") || !strings.Contains(initial, "plan_propose") || !strings.Contains(initial, "registered") {
		t.Fatalf("PRD workflow instruction or raw task missing: %q", initial)
	}
	if strings.Contains(initial, prdBody) {
		t.Fatal("live PRD body was copied into the initial prompt")
	}
}

func TestRunPRDRejectsUnsafeAndExcludedFilesBeforeDispatch(t *testing.T) {
	source := t.TempDir()
	store := filepath.Join(t.TempDir(), "unsafe-store")
	fixture := writePRDTestFixture(t, t.TempDir())
	userConfig := writePRDTestUserConfig(t)
	var out, stderr bytes.Buffer
	unsafe := []string{"run", "Implement feature", "--root", source, "--store", store, "--user-config", userConfig, "--task", "prd-unsafe", "--offline", "--fixture", fixture, "--prd", "../outside.md"}
	if exit := Execute(unsafe, &out, &stderr); exit == 0 {
		t.Fatal("unsafe --prd path accepted")
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("unsafe path created session store: stat err=%v", err)
	}

	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	store = filepath.Join(t.TempDir(), "excluded-store")
	out.Reset()
	stderr.Reset()
	excluded := []string{"run", "Implement feature", "--root", source, "--store", store, "--user-config", userConfig, "--task", "prd-excluded", "--offline", "--fixture", fixture, "--prd", ".env", "--json"}
	if exit := Execute(excluded, &out, &stderr); exit == 0 {
		t.Fatal("excluded PRD path was dispatched")
	}
	session, err := agent.OpenExisting(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	state, doc, err := session.Load(context.Background(), "prd-excluded")
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.Ready || doc.FinalReady || doc.Context != nil {
		t.Fatalf("excluded PRD caused model dispatch: state=%#v final=%t context=%#v", state, doc.FinalReady, doc.Context)
	}
}
