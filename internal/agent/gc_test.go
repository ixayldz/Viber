package agent

import (
	"context"
	"errors"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/resource"
	"github.com/ixayldz/Viber/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestGCCollectsOnlyOrphansAndRetainsHistoricalAndStandaloneScopes(t *testing.T) {
	ctx := context.Background()
	s, opts, source := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	state, err := s.Run(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := s.Archive.Put("standalone", base)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.Archive.PutBytes(opts.TaskID, []byte("unreferenced interrupted write"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := artifact.ScopedBlobPath(opts.TaskID, orphan)
	if len(plan.Objects) != 1 || plan.Objects[0].Path != name {
		t.Fatal("unexpected reachability", plan.Objects)
	}
	command := GCCommand{CommandID: "gc-one", Plan: plan}
	first, err := s.CollectGarbage(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 1 || first.Objects[0].Object.Digest != orphan {
		t.Fatal("GC completion differs from previewed orphan", first)
	}
	second, err := s.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("GC not idempotent", err)
	}
	if first.BytesRemoved != int64(len("unreferenced interrupted write")) {
		t.Fatal(first)
	}
	if _, err = os.Stat(filepath.Join(s.directory, "artifacts", filepath.FromSlash(name))); !os.IsNotExist(err) {
		t.Fatal("orphan retained", err)
	}
	if _, err = s.Archive.Get(standalone); err != nil {
		t.Fatal("standalone scope collected", err)
	}
	actual, _, err := s.Load(ctx, opts.TaskID)
	if err != nil || !reflect.DeepEqual(state, actual) {
		t.Fatal("GC changed task state", err)
	}
	if _, _, err = s.Inspect(ctx, opts.TaskID, 1); err != nil {
		t.Fatal("historical evidence lost", err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(ctx, backup, restored); err != nil {
		t.Fatal("GC invalidated backup closure", err)
	}
	raw, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(raw) != "before\r\n" {
		t.Fatal("GC touched source")
	}
}
func TestGCRejectsReachableObjectsForgedDigestsAndStalePlans(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer s.Close()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.Archive.PutBytes(opts.TaskID, []byte("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	attacked := plan
	attacked.Objects = append([]artifact.Object{}, plan.Objects...)
	attacked.Objects[0].Digest = c.HashBytes([]byte("forged"))
	attacked.Digest = planDigest(attacked)
	if _, err = s.CollectGarbage(ctx, GCCommand{"bad-digest", attacked}); err == nil {
		t.Fatal("forged object accepted")
	}
	raw, err := s.Archive.GetBytes(opts.TaskID, doc.Spec.Inputs[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := artifact.ScopedBlobPath(opts.TaskID, doc.Spec.Inputs[0].Digest)
	attacked = plan
	attacked.Objects = append(append([]artifact.Object{}, plan.Objects...), artifact.Object{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
	sort.Slice(attacked.Objects, func(i, j int) bool { return attacked.Objects[i].Path < attacked.Objects[j].Path })
	attacked.Digest = planDigest(attacked)
	if _, err = s.CollectGarbage(ctx, GCCommand{"reachable", attacked}); err == nil {
		t.Fatal("reachable source accepted")
	}
	if _, err = s.Archive.GetBytes(opts.TaskID, orphan); err != nil {
		t.Fatal("invalid plan partially collected", err)
	}
	if _, err = s.RecordSteering(ctx, SteeringInput{CommandID: "new-input", TaskID: opts.TaskID, Text: "retain this raw input"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CollectGarbage(ctx, GCCommand{"stale", plan}); err == nil {
		t.Fatal("stale reference graph accepted")
	}
}
func TestGCReconcilesMissingFileOnlyAfterBoundIntent(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer s.Close()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive.PutBytes(opts.TaskID, []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command := GCCommand{"interrupted", plan}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	raw, _ := c.CanonicalV1(command)
	base := filepath.Join("maintenance", "gc", c.HashBytes([]byte(command.CommandID)))
	if err = fileguard.Publish(root, filepath.Join(base, "intent.json"), raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Archive.RemoveOrphan(plan.Objects[0]); err != nil {
		t.Fatal(err)
	}
	result, err := s.CollectGarbage(ctx, command)
	if err != nil || len(result.Objects) != 1 || result.Objects[0].Status != "ALREADY_ABSENT_AFTER_INTENT" || result.BytesRemoved != 0 {
		t.Fatal(result, err)
	}
	conflicting := command
	conflicting.Plan.HasMore = !plan.HasMore
	conflicting.Plan.Digest = planDigest(conflicting.Plan)
	if _, err = s.CollectGarbage(ctx, conflicting); err == nil {
		t.Fatal("command ID conflict accepted")
	}
}
func TestGCUnknownEffectsPinWholeArchive(t *testing.T) {
	s, opts, _ := startFixture(t, []Turn{{Text: "lost", UsageKnown: false}}, true)
	defer s.Close()
	if _, err := s.Run(context.Background(), opts.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GCPreview(context.Background()); err == nil {
		t.Fatal("unknown risk collected")
	}
}

func TestGCCompactsCompletedReceiptsAndPreservesExactRetryAfterRestore(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	compacted, err := GCReceiptCompactionUsed(ctx, s.directory)
	if err != nil || compacted {
		t.Fatal("empty GC history reported compacted", compacted, err)
	}
	orphan, err := s.Archive.PutBytes(opts.TaskID, []byte("completed GC receipt compaction"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil || len(plan.Objects) != 1 {
		t.Fatal("unexpected GC preview", plan, err)
	}
	command := GCCommand{CommandID: "gc-compact-receipts", Plan: plan}
	before, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CollectGarbage(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 1 || first.Objects[0].Object.Digest != orphan {
		t.Fatal("GC completion differs from previewed orphan", first)
	}
	gcBase := filepath.Join(s.directory, "maintenance", "gc", c.HashBytes([]byte(command.CommandID)))
	entries, err := os.ReadDir(gcBase)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "complete.json" || entries[1].Name() != "intent.json" {
		t.Fatal("completed GC did not retire only its redundant object receipt", entries)
	}
	compacted, err = GCReceiptCompactionUsed(ctx, s.directory)
	if err != nil || !compacted {
		t.Fatal("durable detector missed compacted GC completion", compacted, err)
	}
	if _, err = os.Stat(filepath.Join(s.directory, "artifacts", filepath.FromSlash(plan.Objects[0].Path))); !os.IsNotExist(err) {
		t.Fatal("orphan remains after completion", err)
	}
	if err = s.visitGCMaintenance(ctx, func(string, []byte) error { return nil }); err != nil {
		t.Fatal("live GC records failed cross-validation", err)
	}
	after, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil || after.RetainedEntries <= before.RetainedEntries {
		t.Fatal("GC control receipt was not retained", before, after, err)
	}
	second, err := s.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("compacted GC retry did not return its exact completion", second, err, first)
	}
	retried, err := s.PhysicalQuota.Capacity(ctx)
	if err != nil || retried != after {
		t.Fatal("exact retry changed physical quota accounting", after, retried, err)
	}

	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(ctx, backup); err != nil {
		t.Fatal("backup rejected compacted GC records", err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(ctx, backup, restored); err != nil {
		t.Fatal("restore rejected compacted GC records", err)
	}
	compacted, err = GCReceiptCompactionUsed(ctx, restored)
	if err != nil || !compacted {
		t.Fatal("backup/restore did not preserve compaction-use evidence", compacted, err)
	}
	restoredSession, err := OpenExisting(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredSession.Close()
	third, err := restoredSession.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(first, third) {
		t.Fatal("restored GC retry did not return its exact completion", third, err, first)
	}
	completePath := filepath.Join(gcBase, "complete.json")
	originalComplete, err := os.ReadFile(completePath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := first
	if tampered.Objects[0].Status == "UNLINKED_AND_DIRECTORY_SYNCED" {
		tampered.Objects[0].Status = "ALREADY_ABSENT_AFTER_INTENT"
		tampered.BytesRemoved = 0
	} else {
		tampered.Objects[0].Status = "UNLINKED_AND_DIRECTORY_SYNCED"
		tampered.BytesRemoved = tampered.Objects[0].Object.Size
	}
	if err = validateGCCompletionAgainstCommand(command, tampered); err != nil {
		t.Fatalf("test completion mutation is not otherwise valid against its durable intent: %v", err)
	}
	tamperedRaw, err := c.CanonicalV1(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(completePath, tamperedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	_, retryErr := s.CollectGarbage(ctx, command)
	if err = os.WriteFile(completePath, originalComplete, 0600); err != nil {
		t.Fatal(err)
	}
	if retryErr == nil {
		t.Fatal("exact retry accepted a validly encoded completion changed after receipt retirement")
	}
}

func TestCollectGarbageCompactionRetryUnderCommittedOwnerPartition(t *testing.T) {
	ctx := context.Background()
	parent, _, _ := startFixture(t, []Turn{{Text: "owner ready", UsageKnown: true}}, true)
	defer parent.Close()

	reportRoot := filepath.Join(t.TempDir(), "partitioned-report")
	ownerPath := filepath.Join(reportRoot, "owner")
	upperBytes, upperEntries := resource.PartitionStartupEnvelope()
	partition, err := parent.PhysicalQuota.Reserve(ctx, resource.Request{
		Writer: "evaluation", Family: "EVALUATION_REPORT", RequestDigest: c.HashBytes([]byte("gc-partitioned-owner")),
		TargetRoot: reportRoot, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload,
	})
	if err != nil {
		t.Fatal("reserve evaluator owner partition:", err)
	}
	if err = partition.PreparePartition(ctx, resource.EvaluationReportPartition); err != nil {
		t.Fatal("prepare evaluator owner partition:", err)
	}
	if err = partition.Begin(ctx); err != nil {
		t.Fatal("begin evaluator owner partition:", err)
	}
	if err = os.Mkdir(reportRoot, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(reportRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(root); err == nil {
		err = partition.BindPartitionRoot(ctx)
	}
	if err = errors.Join(err, root.Close()); err != nil {
		t.Fatal("bind evaluator report root:", err)
	}
	owner, err := parent.OpenIndependentEvaluatorPartition(ctx, ownerPath, partition)
	if err != nil {
		t.Fatal("open partitioned evaluator owner:", err)
	}
	ownerOpen := true
	defer func() {
		if ownerOpen {
			_ = owner.Close()
		}
	}()
	sourceRoot := filepath.Join(t.TempDir(), "source")
	if err = os.Mkdir(sourceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(sourceRoot, "input.txt"), []byte("partitioned GC source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture, err := c.CanonicalV1(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "ready", UsageKnown: true}}})
	if err != nil {
		t.Fatal(err)
	}
	const taskID = "partitioned-gc-task"
	if _, err = owner.Create(ctx, StartOptions{Root: sourceRoot, Prompt: []byte("Inspect the partitioned owner."), TaskID: taskID, Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture}); err != nil {
		t.Fatal("create task in partitioned owner:", err)
	}
	if _, err = owner.Run(ctx, taskID); err != nil {
		t.Fatal("run task in partitioned owner:", err)
	}
	orphan, err := owner.Archive.PutBytes(taskID, []byte("orphan in partitioned owner"))
	if err != nil {
		t.Fatal("create GC orphan:", err)
	}

	if err = owner.Close(); err != nil {
		t.Fatal("close owner before partition settlement:", err)
	}
	ownerOpen = false
	if err = partition.CommitPartition(ctx); err != nil {
		t.Fatal("commit evaluator owner partition:", err)
	}
	owner, err = OpenExisting(ctx, ownerPath)
	if err != nil {
		t.Fatal("reopen committed evaluator owner:", err)
	}
	ownerOpen = true

	plan, err := owner.GCPreview(ctx)
	if err != nil || len(plan.Objects) != 1 || plan.Objects[0].Digest != orphan {
		t.Fatal("unexpected partitioned-owner GC plan:", plan, err)
	}
	command := GCCommand{CommandID: "partitioned-gc-compaction", Plan: plan}
	result, err := owner.CollectGarbage(ctx, command)
	if err != nil || len(result.Objects) != 1 || result.Objects[0].Object.Digest != orphan {
		t.Fatal("partitioned-owner GC failed to compact a real orphan:", result, err)
	}
	gcRoot := filepath.Join(ownerPath, "maintenance", "gc", c.HashBytes([]byte(command.CommandID)))
	entries, err := os.ReadDir(gcRoot)
	if err != nil || len(entries) != 2 || entries[0].Name() != "complete.json" || entries[1].Name() != "intent.json" {
		t.Fatal("partitioned-owner GC did not retire only redundant object receipts:", entries, err)
	}

	commandRaw, err := c.CanonicalV1(command)
	if err != nil {
		t.Fatal(err)
	}
	upperBytes, upperEntries, err = gcPhysicalEstimate(commandRaw, len(plan.Objects))
	if err != nil {
		t.Fatal(err)
	}
	request := resource.Request{
		Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes(commandRaw), TargetRoot: gcRoot,
		UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Control,
	}
	match, err := owner.PhysicalQuota.ReserveOrMatch(ctx, request)
	if err != nil || match.Status != "FREE_PENDING" || match.RequestDigest != request.RequestDigest || match.UpperBytes != request.UpperBytes || match.UpperEntries != request.UpperEntries {
		t.Fatalf("GC receipt unlink did not retain its exact pending-free charge: %+v err=%v", match, err)
	}
	verifyCurrentAllocation := func() (resource.Inventory, error) {
		current, matchErr := owner.PhysicalQuota.ReserveOrMatch(ctx, request)
		if matchErr != nil {
			return resource.Inventory{}, matchErr
		}
		if (current.Status != "FREE_PENDING" && current.Status != "RETAINED") || current.Allocation.ID != match.Allocation.ID || current.RequestDigest != request.RequestDigest || current.UpperBytes != request.UpperBytes || current.UpperEntries != request.UpperEntries {
			return resource.Inventory{}, errors.New("partitioned-owner GC retry changed its exact allocation binding")
		}
		gcDirectory, openErr := os.OpenRoot(gcRoot)
		if openErr != nil {
			return resource.Inventory{}, openErr
		}
		inventory, measureErr := resource.Measure(gcDirectory, true)
		closeErr := gcDirectory.Close()
		if measureErr = errors.Join(measureErr, closeErr); measureErr != nil {
			return resource.Inventory{}, measureErr
		}
		if current.Status == "FREE_PENDING" {
			measureErr = current.Allocation.ValidateFreePending(ctx, inventory)
		} else {
			measureErr = current.Allocation.ValidateRetained(ctx, inventory)
		}
		if measureErr != nil {
			return resource.Inventory{}, measureErr
		}
		return inventory, nil
	}
	currentInventory, err := verifyCurrentAllocation()
	if err != nil {
		t.Fatal("partitioned-owner GC current files differ from their exact retained or pending-free receipt:", err)
	}
	retained, err := owner.PhysicalQuota.Capacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := owner.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(result, retried) {
		t.Fatal("partitioned-owner FREE_PENDING retry changed the exact result:", retried, err, result)
	}
	retryInventory, err := verifyCurrentAllocation()
	if err != nil {
		t.Fatal("partitioned-owner GC retry changed its exact retained or pending-free receipt:", err)
	}
	if !reflect.DeepEqual(currentInventory, retryInventory) {
		t.Fatal("partitioned-owner exact retry changed the current GC inventory:", currentInventory, retryInventory)
	}
	afterRetry, err := owner.PhysicalQuota.Capacity(ctx)
	if err != nil || afterRetry.Allocations != retained.Allocations {
		t.Fatal("partitioned-owner exact retry created a physical allocation:", retained, afterRetry, err)
	}

	completePath := filepath.Join(gcRoot, "complete.json")
	originalComplete, err := os.ReadFile(completePath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := result
	tampered.Objects = append([]GCObjectReceipt(nil), result.Objects...)
	if tampered.Objects[0].Status == "UNLINKED_AND_DIRECTORY_SYNCED" {
		tampered.Objects[0].Status = "ALREADY_ABSENT_AFTER_INTENT"
		tampered.BytesRemoved = 0
	} else {
		tampered.Objects[0].Status = "UNLINKED_AND_DIRECTORY_SYNCED"
		tampered.BytesRemoved = tampered.Objects[0].Object.Size
	}
	if err = validateGCCompletionAgainstCommand(command, tampered); err != nil {
		t.Fatalf("test completion mutation is not otherwise valid against its durable intent: %v", err)
	}
	tamperedRaw, err := c.CanonicalV1(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(completePath, tamperedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	_, retryErr := owner.CollectGarbage(ctx, command)
	if err = os.WriteFile(completePath, originalComplete, 0600); err != nil {
		t.Fatal(err)
	}
	if retryErr == nil {
		t.Fatal("partitioned-owner FREE_PENDING retry accepted a changed but valid completion")
	}
	if err = owner.Close(); err != nil {
		t.Fatal("close owner before persisted partition validation:", err)
	}
	ownerOpen = false
	owner, err = OpenExisting(ctx, ownerPath)
	if err != nil {
		t.Fatal("partition reopen rejected exact pending-free absence:", err)
	}
	ownerOpen = true
}

func TestGCMaintenanceAggregateBound(t *testing.T) {
	name := "maintenance/gc/" + c.HashBytes([]byte("bounded command")) + "/intent.json"
	records := map[string][]byte{name: make([]byte, gcMaintenanceMaxBytes+1)}
	err := validateGCMaintenanceRecords(records)
	var coded *c.Error
	if !errors.As(err, &coded) || coded.Code != c.BudgetLimitReached {
		t.Fatal("oversized GC maintenance aggregate was not rejected at its declared bound", err)
	}
}

func TestBackupManifestBoundsGCMaintenanceBeforeReads(t *testing.T) {
	manifest := BackupManifest{
		SchemaVersion:  1,
		Store:          store.SnapshotInfo{SchemaVersion: 1, ReducerVersion: c.ReducerVersion},
		DeletionPolicy: "UNSUPPORTED_NO_DELETIONS",
		Files: []BackupFile{{
			Path:   "database/state.sqlite",
			Digest: c.HashBytes([]byte("database")),
			Size:   1,
		}},
	}
	for i := byte(1); i <= 17; i++ {
		commandID := c.HashBytes([]byte{i})
		manifest.Files = append(manifest.Files, BackupFile{
			Path:   "maintenance/gc/" + commandID + "/intent.json",
			Digest: c.HashBytes([]byte("intent")),
			Size:   2 << 20,
		})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	err := validateBackupManifest(manifest)
	var coded *c.Error
	if !errors.As(err, &coded) || coded.Code != c.BudgetLimitReached {
		t.Fatal("backup manifest exceeded the GC bound without rejection", err)
	}
}

func TestGCCompletionMustMatchDurableIntent(t *testing.T) {
	digest := c.HashBytes([]byte("orphan"))
	object := artifact.Object{Path: "tasks/gc-bound-completion/blobs/" + digest[:2] + "/" + digest, Digest: digest, Size: 6}
	plan := GCPlan{SchemaVersion: 1, RootsDigest: c.HashBytes([]byte("roots")), Objects: []artifact.Object{object}}
	plan.Digest = planDigest(plan)
	command := GCCommand{CommandID: "gc-bound-completion", Plan: plan}
	intent, err := c.CanonicalV1(command)
	if err != nil {
		t.Fatal(err)
	}
	result := GCResult{SchemaVersion: 1, CommandID: command.CommandID, PlanDigest: plan.Digest, Objects: []GCObjectReceipt{{Object: object, Status: "UNLINKED_AND_DIRECTORY_SYNCED"}}, BytesRemoved: object.Size}
	complete, err := c.CanonicalV1(result)
	if err != nil {
		t.Fatal(err)
	}
	base := "maintenance/gc/" + c.HashBytes([]byte(command.CommandID)) + "/"
	if err = validateGCMaintenanceRecords(map[string][]byte{base + "intent.json": intent, base + "complete.json": complete}); err != nil {
		t.Fatal("completion without redundant per-object receipt should validate", err)
	}
	result.Objects[0].Object.Size++
	complete, err = c.CanonicalV1(result)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateGCMaintenanceRecords(map[string][]byte{base + "intent.json": intent, base + "complete.json": complete}); err == nil {
		t.Fatal("completion differing from its durable intent was accepted")
	}

	// A self-consistent but mismatched completion must also be rejected during
	// restore preflight before a destination is created.
	result.Objects[0].Object = object
	result.Objects[0].Object.Path = "tasks/gc-other-command/blobs/" + digest[:2] + "/" + digest
	complete, err = c.CanonicalV1(result)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err = os.Mkdir(backup, 0700); err != nil {
		t.Fatal(err)
	}
	files := []struct {
		name string
		raw  []byte
	}{
		{name: "database/state.sqlite", raw: []byte("db placeholder")},
		{name: base + "intent.json", raw: intent},
		{name: base + "complete.json", raw: complete},
	}
	manifest := BackupManifest{
		SchemaVersion:  1,
		Store:          store.SnapshotInfo{SchemaVersion: 1, ReducerVersion: c.ReducerVersion},
		DeletionPolicy: "UNSUPPORTED_NO_DELETIONS",
		Files:          []BackupFile{},
	}
	for _, file := range files {
		path := filepath.Join(backup, filepath.FromSlash(file.name))
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, file.raw, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, BackupFile{Path: file.name, Digest: c.HashBytes(file.raw), Size: int64(len(file.raw))})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	manifestRaw, err := c.CanonicalV1(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(backup, "backup.json"), manifestRaw, 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if _, err = restoreBackup(context.Background(), backup, destination, true); err == nil {
		t.Fatal("restore accepted completion mismatched with its intent")
	}
	if _, err = os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("restore created destination before rejecting mismatched completion", err)
	}
}
