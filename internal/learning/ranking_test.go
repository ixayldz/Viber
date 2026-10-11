package learning

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/retrieval"
	"github.com/ixayldz/Viber/internal/workspace"
)

func rankingFixture(t *testing.T) (Definition, Dataset) {
	t.Helper()
	source := t.TempDir()
	body := ""
	cases := []Case{}
	for i := 0; i < 32; i++ {
		query := fmt.Sprintf("identifier%03d", i)
		body += query + "\n"
		if i < 16 {
			cases = append(cases, Case{ID: fmt.Sprintf("case-%d", i), Query: query, Intent: "IDENTIFIER", RelevantPaths: []string{"source.txt"}})
		}
	}
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	capture, err := workspace.CaptureDirectory(source, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	protocol := c.HashBytes([]byte("frozen-protocol"))
	definition := Definition{SchemaVersion: 1, ID: "lexical-ranker", Version: 1, SourceDigest: capture.Snapshot.Digest, TrainingDigest: c.HashBytes([]byte("separate-training")), ProtocolDigest: protocol, Weights: retrieval.Weights{BM25: 2, Trigram: 1}, License: "MIT"}
	dataset := Dataset{Snapshot: capture.Snapshot, SchemaVersion: 1, Split: "HOLDOUT", SourceDigest: capture.Snapshot.Digest, ProtocolDigest: protocol, Documents: []retrieval.Document{{Path: "source.txt", Digest: c.HashBytes([]byte(body)), Bytes: []byte(body)}}, Cases: cases, LabelProvenanceDigest: c.HashBytes([]byte("explicit-operator-label-review"))}
	return definition, dataset
}

func TestRankingActualHeldoutLifecycleKillSwitchAndTamper(t *testing.T) {
	ctx := context.Background()
	definition, dataset := rankingFixture(t)
	registryPath := filepath.Join(t.TempDir(), "ranking")
	registry, err := Open(registryPath, true)
	if err != nil {
		t.Fatal(err)
	}
	dev := dataset
	dev.Split = "DEV"
	dev.Cases = nil
	for i := 16; i < 32; i++ {
		dev.Cases = append(dev.Cases, Case{ID: fmt.Sprintf("dev-%d", i), Query: fmt.Sprintf("identifier%03d", i), Intent: "IDENTIFIER", RelevantPaths: []string{"source.txt"}})
	}
	devDigest, _ := c.Digest(dev)
	trained, err := registry.Train(ctx, definition.ID, definition.Version, definition.License, dev, devDigest)
	if err != nil {
		t.Fatal(err)
	}
	definition = trained.Definition
	datasetDigest, _ := c.Digest(dataset)
	evaluation, err := registry.Evaluate(ctx, definition, dataset, datasetDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !evaluation.Eligible || evaluation.Cases != 16 || evaluation.BaselineMRRMicros != 1000000 || evaluation.CandidateMRRMicros != 1000000 {
		t.Fatalf("actual evaluation mismatch: %+v", evaluation)
	}
	definitionDigest, _ := c.Digest(definition)
	evaluationDigest, _ := c.Digest(evaluation)
	if _, err = registry.Select(definitionDigest, evaluationDigest, "ACTIVE", Approval); err == nil {
		t.Fatal("activation skipped shadow")
	}
	if _, err = registry.Select(definitionDigest, evaluationDigest, "SHADOW", "approve"); err == nil {
		t.Fatal("missing exact approval admitted")
	}
	if _, err = registry.Select(definitionDigest, evaluationDigest, "SHADOW", Approval); err != nil {
		t.Fatal(err)
	}
	binding, err := registry.Bind()
	if err != nil {
		t.Fatal(err)
	}
	registry.Close()
	decision, err := binding.Current(definition.SourceDigest, "identifier000")
	if err != nil || decision.UseCandidate || decision.Mode != "SHADOW" {
		t.Fatal(decision, err)
	}
	registry, err = Open(registryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Select(definitionDigest, evaluationDigest, "CANARY", Approval); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Select(definitionDigest, evaluationDigest, "ACTIVE", Approval); err != nil {
		t.Fatal(err)
	}
	registry.Close()
	decision, err = binding.Current(definition.SourceDigest, "identifier000")
	if err != nil || !decision.UseCandidate {
		t.Fatal(decision, err)
	}
	stale, err := binding.Current(c.HashBytes([]byte("new-source")), "identifier000")
	if err != nil || stale.UseCandidate || stale.Mode != "OFF" {
		t.Fatal(stale, err)
	}
	registry, err = Open(registryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Evaluate(ctx, definition, dataset, datasetDigest); err == nil {
		t.Fatal("completed assignment rerun admitted")
	}
	if _, err = registry.Disable(); err != nil {
		t.Fatal(err)
	}
	registry.Close()
	killed, err := binding.Current(definition.SourceDigest, "identifier000")
	if err != nil || killed.Mode != "OFF" || killed.UseCandidate || killed.LifecycleDigest == decision.LifecycleDigest {
		t.Fatal(killed, err)
	}
	if err = os.Remove(filepath.Join(registryPath, "event-000002.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(registryPath, false); err == nil {
		t.Fatal("lifecycle gap ignored")
	}
}

func TestRankingDatasetBytesAndReviewedDigestFailBeforeDispatch(t *testing.T) {
	definition, dataset := rankingFixture(t)
	registry, err := Open(filepath.Join(t.TempDir(), "ranking"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	digest, _ := c.Digest(dataset)
	if _, err = registry.Evaluate(context.Background(), definition, dataset, c.HashBytes([]byte("wrong"))); err == nil {
		t.Fatal("unreviewed dataset accepted")
	}
	dataset.Documents[0].Bytes = []byte("foreign bytes")
	digest, _ = c.Digest(dataset)
	if _, err = registry.Evaluate(context.Background(), definition, dataset, digest); err == nil {
		t.Fatal("foreign exact source bytes accepted")
	}
	entries, err := os.ReadDir(registry.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("failed preflight published experiment members: %+v", entries)
	}
}
