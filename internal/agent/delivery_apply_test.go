package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func TestDeliveryApplyPreservesOpenWriterAndReviewPackageSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	cacheRoot, err := filepath.Abs(filepath.Join("..", "..", ".cache"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(cacheRoot, 0700); err != nil {
		t.Fatal(err)
	}
	tempRoot, err := os.MkdirTemp(cacheRoot, "delivery-apply-test-")
	if err != nil {
		t.Fatal(err)
	}
	tempRoot, err = filepath.Abs(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cacheRoot, tempRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("test temporary directory escaped the repository cache: %s", tempRoot)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	turns := []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}
	source := filepath.Join(tempRoot, "source")
	storeDirectory := filepath.Join(tempRoot, "store")
	if err = os.Mkdir(source, 0700); err == nil {
		err = os.WriteFile(filepath.Join(source, "a.txt"), []byte("before\r\n"), 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
	session, err := Open(ctx, storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	if err != nil {
		t.Fatal(err)
	}
	options := StartOptions{Root: source, Prompt: []byte("Change a.txt without altering the live workspace."), TaskID: "task-one", Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture}
	if _, err = session.Create(ctx, options); err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	defer func() {
		if session != nil {
			_ = session.Close()
		}
	}()
	state, err := session.Run(ctx, options.TaskID)
	if err != nil || state.Execution != c.Terminated {
		t.Fatal(state, err)
	}
	target := filepath.Join(source, "a.txt")
	writer, err := os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	openedBefore, err := writer.Stat()
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(tempRoot, "apply-review")
	result, err := session.DeliveryApply(ctx, options.TaskID, output)
	if err != nil || result.Status != "UNSUPPORTED_STRONG_EXCLUSIVITY" || result.TargetWritten || result.Backend != "UNSUPPORTED_STRONG_EXCLUSIVITY; CANDIDATE_PATCH_ONLY" || !c.ValidDigest(result.ManifestDigest) || result.PackageDirectory != output {
		t.Fatal("unsafe live apply admission", result, err)
	}
	found := false
	for _, decision := range result.Decisions {
		if decision.Path == "a.txt" && decision.Action == "APPLY_CANDIDATE" {
			found = true
		}
	}
	if !found || result.Changeset == nil {
		t.Fatal("apply preflight omitted per-file target plan", result)
	}
	openedAfter, err := os.Stat(target)
	if err != nil || !os.SameFile(openedBefore, openedAfter) {
		t.Fatal("preflight replaced a file while a writable descriptor was held", err)
	}
	userEdit := []byte("user edit\r\n")
	if err = writer.Truncate(0); err == nil {
		_, err = writer.WriteAt(userEdit, 0)
	}
	if err == nil {
		err = writer.Sync()
	}
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(userEdit) {
		t.Fatal("live apply detached or overwrote the open user writer", string(got), err)
	}

	directory := session.directory
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	session = nil
	reopened, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	session = reopened
	manifest, err := session.readRegisteredDeliveryPreview(ctx, DeliveryReverificationCommand{ParentTask: options.TaskID, PreviewDirectory: output, ManifestDigest: result.ManifestDigest})
	if err != nil || manifest.TaskSeq != state.TaskSeq || manifest.Digest != result.ManifestDigest || manifest.Preview.Status != "READY" {
		t.Fatal("review package could not be reconciled after session restart", manifest, err)
	}
	restored, err := session.DeliveryRestore(ctx, options.TaskID)
	if err != nil || restored.Status != "UNSUPPORTED_NO_APPLIED_RECEIPT" || restored.OwnedReceiptPresent || restored.TargetWritten {
		t.Fatal("restore claimed ownership without an applied receipt", restored, err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != string(userEdit) {
		t.Fatal("restore changed the user's edit", string(got), err)
	}
}

func TestOperatorExportDoesNotAuthorizeLiveDeliveryEffects(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	effects := []string{"export"}
	policy := config.Authority{SchemaVersion: 1, Revision: 1, AllowedEffects: &effects}
	raw, err := c.CanonicalV1(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "operator.json")
	if err = os.WriteFile(policyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	reference, err := config.BindAuthority(policyPath, source)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Resolve(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved.Authority = &reference
	doc := Document{Config: &resolved}
	if err = currentOperatorEffect(doc, "export"); err != nil {
		t.Fatalf("operator export capability was lost: %v", err)
	}
	for _, effect := range []string{"workspace.apply", "workspace.restore"} {
		if err = currentOperatorEffect(doc, effect); err == nil {
			t.Fatalf("export-only operator authority admitted %s", effect)
		}
	}
}

func runExclusiveDeliveryFixture(t *testing.T) (*Session, string, string, DeliveryApplyResult) {
	t.Helper()
	turns := []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}
	session, options, source := startFixture(t, turns, true)
	t.Cleanup(func() { _ = session.Close() })
	userFile := filepath.Join(source, "user-created-after-start.txt")
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.Terminated || state.Quality != c.Unverified {
		t.Fatalf("fixture did not reach an explicitly unverified terminal result: %+v %v", state, err)
	}
	directory := session.directory
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatalf("reopen before apply failed: %v", err)
	}
	if err = os.WriteFile(userFile, []byte("keep this unrelated file\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "review")
	result, err := session.DeliveryApply(context.Background(), options.TaskID, output, DeliveryApplyOptions{ExclusiveNTFS: true})
	if err != nil {
		t.Fatalf("exclusive apply failed: %+v %v", result, err)
	}
	if result.Status != "APPLIED_EXACT_TOUCHED_FILES" || !result.TargetWritten || result.TouchedScope != "EXACT_TOUCHED_FILES" || result.WorkspaceQuality != "UNKNOWN" || !c.ValidDigest(result.ReceiptDigest) || !validDeliveryOperationID(result.OperationID) {
		t.Fatalf("native apply made an invalid claim: %+v", result)
	}
	return session, source, userFile, result
}

func TestExclusiveNTFSApplyAndOwnedRestoreTouchOnlyReceiptedFiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the exclusive native target backend is Windows-only")
	}
	session, source, userFile, applied := runExclusiveDeliveryFixture(t)
	contents, err := os.ReadFile(filepath.Join(source, "a.txt"))
	if err != nil || string(contents) != "after\r\n" {
		t.Fatalf("apply did not publish the receipted postimage: %q %v", contents, err)
	}
	untouched, err := os.ReadFile(userFile)
	if err != nil || string(untouched) != "keep this unrelated file\r\n" {
		t.Fatalf("apply changed an unrelated user file: %q %v", untouched, err)
	}
	state, err := session.State(context.Background(), "task-one")
	if err != nil || state.Quality != c.Unverified || len(state.DeliveryEffects) != 1 || state.DeliveryEffects[0].Status != c.DeliveryApplied {
		t.Fatalf("apply changed historical task quality or omitted its durable receipt: %+v %v", state, err)
	}
	directory := session.directory
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatalf("reopen before owned restore failed: %v", err)
	}
	defer session.Close()
	restored, err := session.DeliveryRestore(context.Background(), "task-one", applied.OperationID)
	if err != nil || restored.Status != "RESTORED_EXACT_TOUCHED_FILES" || !restored.TargetWritten || !restored.OwnedReceiptPresent || restored.TouchedScope != "EXACT_TOUCHED_FILES" || restored.WorkspaceQuality != "UNKNOWN" || !c.ValidDigest(restored.ReceiptDigest) {
		t.Fatalf("owned restore did not return its exact receipt: %+v %v", restored, err)
	}
	contents, err = os.ReadFile(filepath.Join(source, "a.txt"))
	if err != nil || string(contents) != "before\r\n" {
		t.Fatalf("owned restore did not reinstate the exact before-image: %q %v", contents, err)
	}
	untouched, err = os.ReadFile(userFile)
	if err != nil || string(untouched) != "keep this unrelated file\r\n" {
		t.Fatalf("owned restore changed an unrelated user file: %q %v", untouched, err)
	}
}

func TestExclusiveNTFSRestorePreservesPostApplyUserEdit(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the exclusive native target backend is Windows-only")
	}
	session, source, _, applied := runExclusiveDeliveryFixture(t)
	target := filepath.Join(source, "a.txt")
	userEdit := []byte("user edit after apply\r\n")
	if err := os.WriteFile(target, userEdit, 0600); err != nil {
		t.Fatal(err)
	}
	directory := session.directory
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err := OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatalf("reopen before conflict-safe restore failed: %v", err)
	}
	defer session.Close()
	result, err := session.DeliveryRestore(context.Background(), "task-one", applied.OperationID)
	if err != nil || result.Status != "RESTORE_CONFLICT" || result.TargetWritten {
		t.Fatalf("restore admitted a changed postimage: %+v %v", result, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(userEdit) {
		t.Fatalf("restore overwrote the post-apply user edit: %q %v", got, err)
	}
}

func TestExclusiveNTFSReconcileCompletesCrashAfterWriteBeforeReceipt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the exclusive native target backend is Windows-only")
	}
	ctx := context.Background()
	turns := []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}
	session, options, source := startFixture(t, turns, true)
	defer func() { _ = session.Close() }()
	state, err := session.Run(ctx, options.TaskID)
	if err != nil || state.Execution != c.Terminated {
		t.Fatalf("fixture did not reach a terminal candidate: %+v %v", state, err)
	}
	output := filepath.Join(t.TempDir(), "review")
	preview, err := session.DeliveryPreview(ctx, options.TaskID, output)
	if err != nil || preview.Changeset == nil || len(preview.Changeset.Changes) != 1 {
		t.Fatalf("fresh review preview unavailable: %+v %v", preview, err)
	}
	state, doc, err := session.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"current/a.txt", "merged/a.txt"}
	manifest, contents, err := session.readRegisteredDeliveryPreviewFiles(ctx, output, options.TaskID, preview.Digest, names)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := session.Archive.Get(doc.Baseline)
	if err != nil {
		t.Fatal(err)
	}
	operationID := newID("")
	plan, packageFiles, err := buildDeliveryOperationPlan(state, manifest, baseline.Snapshot.Root, contents, operationID)
	if err != nil {
		t.Fatal(err)
	}
	planRaw, err := c.CanonicalV1(plan)
	if err != nil {
		t.Fatal(err)
	}
	planDirectory, err := deliveryArtifactDirectory(output, operationID, "plan")
	if err != nil {
		t.Fatal(err)
	}
	planArtifact, err := session.publishDeliveryArtifact(ctx, options.TaskID, planDirectory, "PLAN", operationID, planRaw, packageFiles, plan.TargetRoot)
	if err != nil {
		t.Fatal(err)
	}
	effect := c.DeliveryEffect{OperationID: operationID, Phase: c.DeliveryApplyPhase, Status: c.DeliveryUnknown, PlanDigest: planArtifact.Manifest.Digest, SourceDocument: plan.SourceDocument, SourceTaskSeq: plan.SourceTaskSeq, SpecVersion: plan.SpecVersion, PolicyEpoch: plan.PolicyEpoch, Generation: plan.Generation, CandidateDigest: plan.CandidateDigest, TargetDigest: plan.TargetDigest, ResultDigest: plan.ResultDigest}
	if _, err = session.recordDeliveryEffect(ctx, state, effect); err != nil {
		t.Fatal(err)
	}
	// Model a process crash after the exclusive write synced but before the
	// per-file receipt could be registered. The reconciler must only certify
	// this exact postimage; it must not perform the write itself.
	if err = os.WriteFile(filepath.Join(source, "a.txt"), packageFiles["after/a.txt"], 0600); err != nil {
		t.Fatal(err)
	}
	directory := session.directory
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenExisting(ctx, directory)
	if err != nil {
		t.Fatalf("reopen before UNKNOWN reconciliation failed: %v", err)
	}
	reconciled, err := session.DeliveryReconcile(ctx, options.TaskID, operationID)
	if err != nil || reconciled.Status != "RECONCILED_APPLIED_EXACT_TOUCHED_FILES" || reconciled.TargetWritten || reconciled.WorkspaceQuality != "UNKNOWN" || !c.ValidDigest(reconciled.ReceiptDigest) {
		t.Fatalf("crash-left exact postimage was not reconciled without a target write: %+v %v", reconciled, err)
	}
	state, err = session.State(ctx, options.TaskID)
	if err != nil || len(state.DeliveryEffects) != 1 || state.DeliveryEffects[0].Status != c.DeliveryApplied || state.Quality != c.Unverified {
		t.Fatalf("reconciliation did not complete only the delivery receipt: %+v %v", state, err)
	}
}
