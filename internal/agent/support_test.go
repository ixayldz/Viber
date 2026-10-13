package agent

import (
	"bytes"
	"context"
	"encoding/json"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"testing"
)

func TestSupportAllowlistExcludesSourcePromptModelTextIdentifiersAndHashes(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "private-account-canary.txt"), []byte("sk-source-secret-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "Bearer model-secret-canary", UsageKnown: true}}})
	if _, err = s.Create(ctx, StartOptions{Root: source, TaskID: "private-task-canary", Prompt: []byte("sk-prompt-secret-canary"), Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Run(ctx, "private-task-canary"); err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(ctx, "private-task-canary")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "support")
	report, err := s.ExportSupport(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(output, "support.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"sk-source-secret-canary", "private-account-canary.txt", "private-task-canary", "sk-prompt-secret-canary", "model-secret-canary", source, s.directory, doc.Baseline.SnapshotDigest, doc.Spec.Inputs[0].Digest} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatal("support leaked protected data", forbidden)
		}
	}
	if report.RawPayloadsIncluded || report.CredentialMetadataIncluded || report.Telemetry || report.TrainingExport || report.ReleaseReady || len(report.Tasks) != 1 || report.Tasks[0].Quality != c.Unverified {
		t.Fatal(report)
	}
	before, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.ExportSupport(ctx, output)
	if err != nil {
		t.Fatal("exact retained support retry failed", err)
	}
	retryRaw, err := c.CanonicalV1(retry)
	if err != nil || !bytes.Equal(retryRaw, raw) {
		t.Fatal("exact retry changed the original numeric payload", err)
	}
	stored, err := os.ReadFile(filepath.Join(output, "support.json"))
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatal("exact retry overwrote support output", err)
	}
	after, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil || before != after {
		t.Fatal("exact retained retry changed quota accounting", before, after, err)
	}
	foreign := t.TempDir()
	canary := filepath.Join(foreign, "keep.txt")
	if err = os.WriteFile(canary, []byte("keep foreign content"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExportSupport(ctx, foreign); err == nil {
		t.Fatal("support adopted an unrelated existing directory")
	}
	if kept, readErr := os.ReadFile(canary); readErr != nil || string(kept) != "keep foreign content" {
		t.Fatal("refused export changed foreign content", readErr)
	}
	refused, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil || refused != after {
		t.Fatal("foreign output was refused after consuming quota", after, refused, err)
	}
	for _, unsafe := range []string{source, filepath.Join(source, "bundle"), filepath.Join(s.directory, "bundle")} {
		if _, err = s.ExportSupport(ctx, unsafe); err == nil {
			t.Fatal("support output overlaps source/store")
		}
	}
}
func TestSupportOmitsOperatorControlledLocalModelAndEndpoint(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	opts := StartOptions{Root: source, TaskID: "private-task", Prompt: []byte("Inspect"), Budget: DefaultBudget(), Autonomy: "guided", Runtime: &Runtime{SchemaVersion: 1, Provider: "ollama", Endpoint: "http://127.0.0.1:11434", Model: "private-model-secret-canary:1", DeclaredLocal: true, ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 1000}}
	if _, err = s.Create(ctx, opts); err != nil {
		t.Fatal(err)
	}
	report, err := s.Support(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := c.CanonicalV1(report)
	if bytes.Contains(raw, []byte(opts.Runtime.Model)) || bytes.Contains(raw, []byte(opts.Runtime.Endpoint)) || bytes.Contains(raw, []byte(opts.TaskID)) {
		t.Fatal("operator metadata leaked")
	}
	if report.Tasks[0].Provider != "ollama" {
		t.Fatal("provider class lost")
	}
}
