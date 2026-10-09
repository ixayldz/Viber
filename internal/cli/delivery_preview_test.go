package cli

import (
	"bytes"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIDeliveryPreviewIsPortableUnverifiedAndNeverWritesSource(t *testing.T) {
	source := t.TempDir()
	directory := filepath.Join(t.TempDir(), "session")
	inputs := t.TempDir()
	fixture := filepath.Join(inputs, "fixture.json")
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	args := []string{"run", "Review source", "--offline", "--fixture", fixture, "--root", source, "--store", directory, "--task", "preview", "--allow-unverified", "--json"}
	if exit := Execute(args, &out, &stderr); exit != 2 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	if err := os.WriteFile(filepath.Join(source, "user.txt"), []byte("user"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	output := filepath.Join(t.TempDir(), "preview")
	if exit := Execute([]string{"delivery-preview", "preview", "--store", directory, "--output", output, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	var result agent.DeliveryPreviewManifest
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Preview.Status != "READY" || result.Preview.Verification != "UNVERIFIED" || result.Preview.LiveWorkspaceWritten {
		t.Fatal("CLI preview overstated quality", err, out.String())
	}
	raw, _ = os.ReadFile(filepath.Join(source, "a.txt"))
	if string(raw) != "original" {
		t.Fatal("source changed")
	}
	raw, _ = os.ReadFile(filepath.Join(output, "merged", "user.txt"))
	if string(raw) != "user" {
		t.Fatal("user file missing")
	}
}
