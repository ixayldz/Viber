package evaluation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

func TestPartitionedEvaluatorReopensForTaskAndGCWrites(t *testing.T) {
	ctx := context.Background()
	original, _, _ := producer(t, nil)
	if _, err := original.Run(ctx, "producer"); err != nil {
		t.Fatal(err)
	}
	source, _, err := original.FreezeEvaluationSource(ctx, "producer")
	if err != nil {
		t.Fatal(err)
	}
	recipe := nativeRecipe("golang@sha256:"+strings.Repeat("a", 64), "partition-check", []byte("private"), []byte("private"))

	bundle := filepath.Join(t.TempDir(), "evaluation")
	ownerPath := filepath.Join(bundle, "owner")
	allocation, err := original.PhysicalQuota.Reserve(ctx, resource.Request{
		Writer: "evaluation", Family: "EVALUATION_REPORT", RequestDigest: c.HashBytes([]byte("partitioned-evaluation-integration")),
		TargetRoot: bundle, UpperBytes: 512 << 20, UpperEntries: 16384, Class: resource.Payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.PreparePartition(ctx, resource.EvaluationReportPartition); err != nil {
		t.Fatal(err)
	}
	if err = allocation.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(root); err == nil {
		err = allocation.BindPartitionRoot(ctx)
	}
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	owner, err := original.OpenIndependentEvaluatorPartition(ctx, ownerPath, allocation)
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	fixture, err := evaluatorFixture(recipe)
	if err != nil {
		owner.Close()
		root.Close()
		t.Fatal(err)
	}
	plan, err := c.CanonicalV1(recipe.Plan)
	if err != nil {
		owner.Close()
		root.Close()
		t.Fatal(err)
	}
	boundPrompt, err := evaluatorSourcePrompt(source)
	if err != nil {
		owner.Close()
		root.Close()
		t.Fatal(err)
	}
	options := agent.StartOptions{TaskID: owner.EvaluationTaskID(), TaskKind: "ANALYSIS", Prompt: boundPrompt, Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture, CheckPlan: plan, CheckRuntime: &recipe.Runtime}
	if _, err = owner.CreateIndependentEvaluator(ctx, original, source, options); err == nil {
		err = original.RegisterEvaluationReport(ctx, owner, root, source)
	}
	if err == nil {
		_, err = publishNativeFile(root, "report.json", struct {
			SchemaVersion int    `json:"schema_version"`
			Kind          string `json:"kind"`
		}{1, "partition-test"})
	}
	if err != nil {
		owner.Close()
		root.Close()
		t.Fatal(err)
	}
	if err = fileguard.SyncParents(root, "."); err == nil {
		err = root.Close()
	}
	if err != nil {
		owner.Close()
		t.Fatal(err)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err = allocation.CommitPartition(ctx); err != nil {
		t.Fatal(err)
	}
	committed, err := original.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := agent.OpenExisting(ctx, ownerPath)
	if err != nil {
		t.Fatalf("open committed evaluator owner: %v", err)
	}
	closeReopened := func() error {
		if reopened == nil {
			return nil
		}
		closeErr := reopened.Close()
		reopened = nil
		return closeErr
	}
	t.Cleanup(func() { _ = closeReopened() })
	capacityAfterOpen, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil || capacityAfterOpen.RetainedBytes > committed.RetainedBytes+(1<<20) || capacityAfterOpen.RetainedEntries > committed.RetainedEntries+4 {
		closeReopened()
		t.Fatalf("owner reopen duplicated partition receipts beyond SQLite startup growth: before=%+v after=%+v err=%v", committed, capacityAfterOpen, err)
	}
	if err = closeReopened(); err != nil {
		t.Fatal(err)
	}
	reopened, err = agent.OpenExisting(ctx, ownerPath)
	if err != nil {
		t.Fatalf("repeat open of committed evaluator owner: %v", err)
	}
	repeatedOpenCapacity, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil || repeatedOpenCapacity.RetainedBytes > capacityAfterOpen.RetainedBytes+(1<<20) || repeatedOpenCapacity.RetainedEntries > capacityAfterOpen.RetainedEntries+4 {
		closeReopened()
		t.Fatalf("repeated owner open duplicated typed receipts: first=%+v second=%+v err=%v", capacityAfterOpen, repeatedOpenCapacity, err)
	}

	taskRoot := t.TempDir()
	if err = os.WriteFile(filepath.Join(taskRoot, "app.sh"), []byte("echo later\n"), 0600); err != nil {
		closeReopened()
		t.Fatal(err)
	}
	laterFixture, err := c.CanonicalV1(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "later task", UsageKnown: true}}})
	if err == nil {
		_, err = reopened.Create(ctx, agent.StartOptions{TaskID: "partition-followup", Root: taskRoot, Prompt: []byte("Inspect the follow-up source."), Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: laterFixture})
	}
	if err != nil {
		closeReopened()
		t.Fatalf("new task could not reserve owner artifacts after partition: %v", err)
	}
	if err = closeReopened(); err != nil {
		t.Fatal(err)
	}
	reopened, err = agent.OpenExisting(ctx, ownerPath)
	if err != nil {
		t.Fatalf("reopen owner with a new task: %v", err)
	}
	postTaskCapacity, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil {
		closeReopened()
		t.Fatal(err)
	}
	planGC, err := reopened.GCPreview(ctx)
	if err == nil {
		_, err = reopened.CollectGarbage(ctx, agent.GCCommand{CommandID: "partition-followup-gc", Plan: planGC})
	}
	if err != nil {
		closeReopened()
		t.Fatalf("GC could not reserve its charged maintenance namespace: %v", err)
	}
	if err = closeReopened(); err != nil {
		t.Fatal(err)
	}
	reopened, err = agent.OpenExisting(ctx, ownerPath)
	if err != nil {
		t.Fatalf("reopen owner after task and GC output: %v", err)
	}
	finalCapacity, err := reopened.PhysicalQuota.Capacity(ctx)
	if err != nil || finalCapacity.RetainedBytes <= committed.RetainedBytes || finalCapacity.RetainedBytes < postTaskCapacity.RetainedBytes {
		t.Fatalf("later task/GC receipts were not retained exactly: initial=%+v task=%+v final=%+v err=%v", committed, postTaskCapacity, finalCapacity, err)
	}
	if err = reopened.PhysicalQuota.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("owner partition rejected its exact later receipts: %v", err)
	}
	roguePath := filepath.Join(ownerPath, "unreserved-owner-file")
	if err = os.WriteFile(roguePath, []byte("not quota admitted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = reopened.PhysicalQuota.ValidateOwnerPartition(ctx, ownerPath); err == nil {
		t.Fatal("owner partition accepted a file outside exact retained or outstanding receipts")
	}
	if err = os.Remove(roguePath); err != nil {
		t.Fatal(err)
	}
	if err = reopened.PhysicalQuota.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatalf("owner partition remained invalid after removing the foreign test file: %v", err)
	}
	if err = closeReopened(); err != nil {
		t.Fatal(err)
	}
}
