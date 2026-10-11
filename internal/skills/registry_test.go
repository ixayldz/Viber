package skills

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func testTrustPolicy(t *testing.T) (TrustPolicy, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	protocol := EvaluationProtocol{
		Digest:          c.HashBytes([]byte("protocol-v1")),
		DatasetDigest:   c.HashBytes([]byte("dataset-v1")),
		HoldoutDigest:   c.HashBytes([]byte("holdout-v1")),
		EvaluatorDigest: c.HashBytes([]byte("evaluator-build-v1")),
		MinCases:        1,
		MaxCases:        4,
		AllowedScopes:   []string{"TASK", "PROJECT"},
	}
	policy := TrustPolicy{
		SchemaVersion: 1,
		Revision:      1,
		Evaluators:    []PinnedEvaluator{{ID: c.HashBytes(public), PublicKey: public}},
		Protocols:     []EvaluationProtocol{protocol},
		Capabilities:  []string{"fs.read", "plan.update"},
		Scopes:        []string{"TASK", "PROJECT"},
	}
	if err = policy.Validate(); err != nil {
		t.Fatal(err)
	}
	return policy, private
}

func testSource() Source {
	return Source{TaskID: "task-1", TaskSeq: 4, Document: c.HashBytes([]byte("document")), Snapshot: c.HashBytes([]byte("snapshot")), Candidate: c.HashBytes([]byte("candidate")), ProjectDigest: c.HashBytes([]byte("project"))}
}

func testDefinition(source Source, version uint64) Definition {
	return Definition{
		SchemaVersion: SchemaVersion,
		ID:            "review.procedure",
		Version:       version,
		Scope:         Scope{Kind: "TASK", Key: source.TaskID},
		Trigger:       "When reviewing this task's candidate",
		Steps:         []Step{{Action: "inspect", Instruction: "Read the changed source files and compare them with the requirements.", Paths: []string{"src/main.go"}}},
		Verification:  []string{"Report the observed test result and its source."},
		Capabilities:  []string{"fs.read", "plan.update"},
		License:       "MIT",
		Source:        source,
	}
}

func signedReceipt(t *testing.T, trust TrustPolicy, private ed25519.PrivateKey, definition Definition, definitionDigest string, holdout string) []byte {
	t.Helper()
	protocol := trust.Protocols[0]
	receipt := EvaluationReceipt{
		SchemaVersion:    SchemaVersion,
		EvaluatorKeyID:   trust.Evaluators[0].ID,
		EvaluatorDigest:  protocol.EvaluatorDigest,
		ProtocolDigest:   protocol.Digest,
		DatasetDigest:    protocol.DatasetDigest,
		HoldoutDigest:    holdout,
		DefinitionDigest: definitionDigest,
		SkillID:          definition.ID,
		SkillVersion:     definition.Version,
		Source:           definition.Source,
		Scope:            definition.Scope,
		Cases: []CaseEvidence{{
			CaseID:         "case-1",
			InputDigest:    c.HashBytes([]byte("input")),
			ExpectedDigest: c.HashBytes([]byte("expected")),
			ObservedDigest: c.HashBytes([]byte("expected")),
			ResultDigest:   c.HashBytes([]byte("result")),
			Verdict:        "PASS",
		}},
		CreatedAtUTC: "2026-10-11T00:00:00Z",
	}
	message, err := EvaluationSigningBytes(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Signature = ed25519.Sign(private, message)
	raw, err := c.CanonicalV1(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func proposeAndEvaluate(t *testing.T, registry *Registry, trust TrustPolicy, private ed25519.PrivateKey, source Source, version uint64) Definition {
	t.Helper()
	definition := testDefinition(source, version)
	status, err := registry.Propose(context.Background(), definition)
	if err != nil || status.Lifecycle != "CANDIDATE" {
		t.Fatalf("propose: status=%+v err=%v", status, err)
	}
	receipt := signedReceipt(t, trust, private, definition, status.DefinitionDigest, trust.Protocols[0].HoldoutDigest)
	status, err = registry.ImportEvaluation(context.Background(), definition.ID, version, receipt)
	if err != nil || status.Lifecycle != "EVALUATED" {
		t.Fatalf("evaluate: status=%+v err=%v", status, err)
	}
	return definition
}

func TestRegistryRequiresPinnedSourceBoundEvaluationAndExplicitActivation(t *testing.T) {
	trust, private := testTrustPolicy(t)
	directory := filepath.Join(t.TempDir(), "skills")
	registry, err := Open(directory, trust)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	source := testSource()
	definition := testDefinition(source, 1)
	candidate, err := registry.Propose(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	scope := definition.Scope
	authority := c.HashBytes([]byte("current-authority"))
	if _, err = registry.Activate(context.Background(), definition.ID, 1, scope, authority); err == nil {
		t.Fatal("candidate without an external evaluation activated")
	}
	bad := signedReceipt(t, trust, private, definition, candidate.DefinitionDigest, c.HashBytes([]byte("wrong-holdout")))
	if _, err = registry.ImportEvaluation(context.Background(), definition.ID, 1, bad); err == nil {
		t.Fatal("receipt for an unpinned holdout was accepted")
	}
	good := signedReceipt(t, trust, private, definition, candidate.DefinitionDigest, trust.Protocols[0].HoldoutDigest)
	evaluated, err := registry.ImportEvaluation(context.Background(), definition.ID, 1, good)
	if err != nil || evaluated.Lifecycle != "EVALUATED" {
		t.Fatalf("pinned evaluator result rejected: status=%+v err=%v", evaluated, err)
	}
	active, err := registry.Activate(context.Background(), definition.ID, 1, scope, authority)
	if err != nil || active.Lifecycle != "ACTIVE" {
		t.Fatalf("explicit activation failed: status=%+v err=%v", active, err)
	}
	loaded, err := registry.LoadActive(context.Background(), []Scope{scope}, authority, []string{"fs.read"})
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0].CurrentCapabilities, []string{"fs.read"}) {
		t.Fatalf("active skill did not stay within current capability intersection: skills=%+v err=%v", loaded, err)
	}
	if err = registry.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err = Open(directory, trust)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := registry.Status(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].Lifecycle != "ACTIVE" {
		t.Fatalf("activation did not survive registry reopen: statuses=%+v err=%v", statuses, err)
	}
	if _, err = registry.Revoke(context.Background(), definition.ID, 1, authority); err != nil {
		t.Fatal(err)
	}
	loaded, err = registry.LoadActive(context.Background(), []Scope{scope}, authority, []string{"fs.read"})
	if err != nil || len(loaded) != 0 {
		t.Fatalf("revoked skill remained active: skills=%+v err=%v", loaded, err)
	}
	if err = registry.Close(); err != nil {
		t.Fatal(err)
	}

	// A direct edit to the append-only lifecycle log must be detected on replay.
	eventPath := filepath.Join(directory, "events", "00000000000000000001.json")
	raw, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err = os.WriteFile(eventPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err = Open(directory, trust)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, err = registry.Status(context.Background()); err == nil {
		t.Fatal("tampered lifecycle history was accepted")
	}
}

func TestRegistryVersionRollbackUsesImmutableEvaluations(t *testing.T) {
	trust, private := testTrustPolicy(t)
	registry, err := Open(filepath.Join(t.TempDir(), "skills"), trust)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	ctx := context.Background()
	source := testSource()
	authority := c.HashBytes([]byte("authority"))
	definition1 := proposeAndEvaluate(t, registry, trust, private, source, 1)
	if _, err = registry.Activate(ctx, definition1.ID, 1, definition1.Scope, authority); err != nil {
		t.Fatal(err)
	}
	definition2 := proposeAndEvaluate(t, registry, trust, private, source, 2)
	if _, err = registry.Activate(ctx, definition2.ID, 2, definition2.Scope, authority); err != nil {
		t.Fatal(err)
	}
	status, err := registry.Rollback(ctx, definition1.ID, 1, definition1.Scope, authority)
	if err != nil || status.Lifecycle != "ACTIVE" {
		t.Fatalf("rollback to an earlier evaluated version failed: status=%+v err=%v", status, err)
	}
	statuses, err := registry.Status(ctx)
	if err != nil || len(statuses) != 2 || statuses[0].Lifecycle != "ACTIVE" || statuses[1].Lifecycle != "EVALUATED" {
		t.Fatalf("rollback lifecycle state is invalid: statuses=%+v err=%v", statuses, err)
	}
}

func TestCrossScopeActivationRequiresSeparateReviewedEvent(t *testing.T) {
	trust, private := testTrustPolicy(t)
	trust.Scopes = append(trust.Scopes, "USER")
	trust.Protocols[0].AllowedScopes = append(trust.Protocols[0].AllowedScopes, "USER")
	if err := trust.Validate(); err != nil {
		t.Fatal(err)
	}
	registry, err := Open(filepath.Join(t.TempDir(), "skills"), trust)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	source := testSource()
	definition := testDefinition(source, 1)
	definition.Scope = Scope{Kind: "USER", Key: "local-user"}
	status, err := registry.Propose(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.ImportEvaluation(context.Background(), definition.ID, definition.Version, signedReceipt(t, trust, private, definition, status.DefinitionDigest, trust.Protocols[0].HoldoutDigest)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authority := c.HashBytes([]byte("authority"))
	if _, err = registry.Activate(ctx, definition.ID, definition.Version, definition.Scope, authority); err == nil {
		t.Fatal("cross-scope activation without a review was accepted")
	}
	if _, err = registry.ActivateReviewed(ctx, definition.ID, definition.Version, definition.Scope, authority, true); err != nil {
		t.Fatalf("explicitly reviewed cross-scope activation failed: %v", err)
	}
	if err = registry.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err = Open(filepath.Join(filepath.Dir(registry.String()), "skills"), trust)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	statuses, err := registry.Status(ctx)
	if err != nil || len(statuses) != 1 || statuses[0].Lifecycle != "ACTIVE" {
		t.Fatalf("reviewed cross-scope event did not replay: statuses=%+v err=%v", statuses, err)
	}
}
