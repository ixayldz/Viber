package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/resource"
)

func TestOpenIndependentEvaluatorBootstrapsAndReopensBoundOwner(t *testing.T) {
	ctx := context.Background()
	parent, options, _ := deletionFixture(t)
	source, _, err := parent.FreezeEvaluationSource(ctx, options.TaskID)
	if err != nil {
		t.Fatal("freeze evaluator source:", err)
	}
	before, err := parent.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal("read quota before owner bootstrap:", err)
	}
	ownerPath := filepath.Join(t.TempDir(), "owner")
	owner, err := parent.OpenIndependentEvaluator(ctx, ownerPath)
	if err != nil {
		t.Fatal("open standalone quota-bound evaluator:", err)
	}
	ownerOpen := true
	t.Cleanup(func() {
		if ownerOpen {
			_ = owner.Close()
		}
	})
	if owner.PhysicalQuota == nil || owner.PhysicalQuota.Authority != parent.PhysicalQuota.Authority || owner.PhysicalQuota.LedgerID != parent.PhysicalQuota.LedgerID {
		t.Fatal("reopened evaluator did not retain the parent's pinned physical quota")
	}
	if err = owner.PhysicalQuota.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatal("standalone evaluator partition was not committed before return:", err)
	}
	partitioned, err := owner.PhysicalQuota.PartitionFeatureInUse(ctx)
	if err != nil || !partitioned {
		t.Fatal("standalone evaluator partition was not recorded in the shared ledger:", partitioned, err)
	}
	afterOpen, err := parent.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal("read quota after owner bootstrap:", err)
	}
	upperBytes, upperEntries := resource.PartitionStartupEnvelope()
	if afterOpen.OutstandingBytes < before.OutstandingBytes || afterOpen.OutstandingBytes-before.OutstandingBytes >= upperBytes ||
		afterOpen.OutstandingItems < before.OutstandingItems || afterOpen.OutstandingItems-before.OutstandingItems >= upperEntries ||
		afterOpen.RetainedBytes < before.RetainedBytes || afterOpen.RetainedBytes-before.RetainedBytes > upperBytes ||
		afterOpen.RetainedEntries < before.RetainedEntries || afterOpen.RetainedEntries-before.RetainedEntries > upperEntries {
		t.Fatalf("owner bootstrap exceeded its single envelope or left the whole startup reserve outstanding: before=%+v after=%+v", before, afterOpen)
	}

	task := owner.EvaluationTaskID()
	if _, err = owner.CreateIndependentEvaluator(ctx, parent, source, privacyEvaluatorOptions(task)); err != nil {
		t.Fatal("create evaluator task after committed owner bootstrap:", err)
	}
	if _, err = owner.Run(ctx, task); err != nil {
		t.Fatal("run evaluator task after committed owner bootstrap:", err)
	}
	if _, err = owner.Archive.PutBytes(task, []byte("standalone evaluator GC orphan")); err != nil {
		t.Fatal("write evaluator artifact through shared quota:", err)
	}
	plan, err := owner.GCPreview(ctx)
	if err != nil || len(plan.Objects) != 1 {
		t.Fatal("standalone evaluator artifact was not admitted to GC:", plan, err)
	}
	if _, err = owner.CollectGarbage(ctx, GCCommand{CommandID: "standalone-owner-gc", Plan: plan}); err != nil {
		t.Fatal("write evaluator GC receipts through shared quota:", err)
	}
	if err = owner.PhysicalQuota.ValidateOwnerPartition(ctx, ownerPath); err != nil {
		t.Fatal("post-bootstrap task/artifact/GC writes invalidated the exact owner partition:", err)
	}
	parentCapacity, err := parent.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal("read parent ledger after evaluator writes:", err)
	}
	ownerCapacity, err := owner.PhysicalQuota.Capacity(ctx)
	if err != nil || !reflect.DeepEqual(parentCapacity, ownerCapacity) {
		t.Fatal("evaluator task/artifact/GC writes did not use the parent's same ledger:", parentCapacity, ownerCapacity, err)
	}
}

func TestOpenIndependentEvaluatorExhaustedBootstrapDoesNotCreateTarget(t *testing.T) {
	ctx := context.Background()
	parent, _, _ := startFixture(t, []Turn{{Text: "ready", UsageKnown: true}}, true)
	defer parent.Close()
	if parent.PhysicalQuota == nil {
		t.Fatal("fixture parent has no shared physical quota")
	}
	exhaustRoot := t.TempDir()
	chunk := uint64(1 << 19)
	for sequence := 0; chunk > 0; sequence++ {
		target := filepath.Join(exhaustRoot, "entries-"+strconv.Itoa(sequence))
		_, err := parent.PhysicalQuota.Reserve(ctx, resource.Request{
			Writer: "test", Family: "EVALUATOR_BOOTSTRAP_EXHAUST", RequestDigest: c.HashBytes([]byte("entry-fill-" + strconv.Itoa(sequence))),
			TargetRoot: target, UpperEntries: chunk, Class: resource.Payload,
		})
		if err == nil {
			continue
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
			t.Fatalf("entry-fill reservation failed for an unexpected reason: %v", err)
		}
		chunk /= 2
	}
	before, err := parent.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal("read exhausted quota:", err)
	}
	ownerPath := filepath.Join(t.TempDir(), "must-not-be-created")
	if _, err = parent.OpenIndependentEvaluator(ctx, ownerPath); err == nil {
		t.Fatal("standalone evaluator ignored exhausted bootstrap admission")
	} else {
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
			t.Fatalf("exhausted evaluator admission returned an unexpected error: %v", err)
		}
	}
	if _, err = os.Lstat(ownerPath); !os.IsNotExist(err) {
		t.Fatal("failed evaluator admission created its owner root:", err)
	}
	after, err := parent.PhysicalQuota.Capacity(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed evaluator admission changed the retained bootstrap charge:", before, after, err)
	}
}

func TestOpenIndependentEvaluatorRefusesExistingOwnerAndForeignTarget(t *testing.T) {
	ctx := context.Background()
	parent, _, _ := startFixture(t, []Turn{{Text: "ready", UsageKnown: true}}, true)
	defer parent.Close()
	ledger := parent.PhysicalQuota
	beforeMissingLedger, err := ledger.Capacity(ctx)
	if err != nil {
		t.Fatal("read quota before missing-ledger refusal:", err)
	}
	parent.PhysicalQuota = nil
	missingLedgerPath := filepath.Join(t.TempDir(), "no-parent-ledger")
	_, missingLedgerErr := parent.OpenIndependentEvaluator(ctx, missingLedgerPath)
	parent.PhysicalQuota = ledger
	var missingLedgerFailure *c.Error
	if !errors.As(missingLedgerErr, &missingLedgerFailure) || missingLedgerFailure.Code != c.StaleAuthority {
		t.Fatalf("standalone evaluator did not require the parent's shared ledger: %v", missingLedgerErr)
	}
	if _, err = os.Lstat(missingLedgerPath); !os.IsNotExist(err) {
		t.Fatal("missing-ledger refusal created an owner root:", err)
	}
	afterMissingLedger, err := ledger.Capacity(ctx)
	if err != nil || !reflect.DeepEqual(beforeMissingLedger, afterMissingLedger) {
		t.Fatal("missing-ledger refusal changed the shared quota:", beforeMissingLedger, afterMissingLedger, err)
	}

	ownerPath := filepath.Join(t.TempDir(), "owned")
	owner, err := parent.OpenIndependentEvaluator(ctx, ownerPath)
	if err != nil {
		t.Fatal("create first standalone owner:", err)
	}
	if err = owner.Close(); err != nil {
		t.Fatal("close first standalone owner:", err)
	}
	before, err := parent.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parent.OpenIndependentEvaluator(ctx, ownerPath); err == nil {
		t.Fatal("existing evaluator owner was adopted")
	}
	afterOwnerRefusal, err := parent.PhysicalQuota.Capacity(ctx)
	if err != nil || !reflect.DeepEqual(before, afterOwnerRefusal) {
		t.Fatal("existing owner refusal changed its quota charge:", before, afterOwnerRefusal, err)
	}

	foreignPath := filepath.Join(t.TempDir(), "foreign")
	if err = os.Mkdir(foreignPath, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(foreignPath, "foreign.txt")
	if err = os.WriteFile(marker, []byte("preserve foreign target"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = parent.OpenIndependentEvaluator(ctx, foreignPath); err == nil {
		t.Fatal("foreign existing target was adopted")
	}
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "preserve foreign target" {
		t.Fatal("foreign target changed after refusal:", string(content), err)
	}
	afterForeignRefusal, err := parent.PhysicalQuota.Capacity(ctx)
	if err != nil || !reflect.DeepEqual(before, afterForeignRefusal) {
		t.Fatal("foreign target refusal changed the shared quota charge:", before, afterForeignRefusal, err)
	}
}
