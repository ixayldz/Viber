package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/store"
)

type DeliveryApplyOptions struct {
	ExclusiveNTFS bool
}

type deliveryOperationFile struct {
	Path         string `json:"path"`
	BeforeDigest string `json:"before_digest"`
	AfterDigest  string `json:"after_digest"`
	Mode         uint32 `json:"mode"`
}

type deliveryOperationPlan struct {
	SchemaVersion         int                       `json:"schema_version"`
	OperationID           string                    `json:"operation_id"`
	ParentOperationID     string                    `json:"parent_operation_id,omitempty"`
	Phase                 string                    `json:"phase"`
	TaskID                string                    `json:"task_id"`
	TargetRoot            string                    `json:"target_root"`
	SourceDocument        string                    `json:"source_document_digest"`
	SourceTaskSeq         int64                     `json:"source_task_seq"`
	SpecVersion           int64                     `json:"spec_version"`
	PolicyEpoch           int64                     `json:"policy_epoch"`
	Generation            int64                     `json:"kernel_generation"`
	CandidateDigest       string                    `json:"candidate_digest"`
	TargetDigest          string                    `json:"target_digest"`
	ResultDigest          string                    `json:"result_digest"`
	PreviewDigest         string                    `json:"preview_digest"`
	PreviewManifestDigest string                    `json:"preview_manifest_digest"`
	Files                 []deliveryOperationFile   `json:"files"`
	Seal                  delivery.NativeTargetSeal `json:"exclusive_target_seal"`
}

type deliveryFileReceipt struct {
	SchemaVersion     int    `json:"schema_version"`
	OperationID       string `json:"operation_id"`
	ParentOperationID string `json:"parent_operation_id,omitempty"`
	Phase             string `json:"phase"`
	PlanDigest        string `json:"plan_manifest_digest"`
	Ordinal           int    `json:"ordinal"`
	Path              string `json:"path"`
	BeforeDigest      string `json:"before_digest"`
	AfterDigest       string `json:"after_digest"`
	FileIdentity      string `json:"file_identity"`
	ObservedAfter     string `json:"observed_after_digest"`
	Observation       string `json:"observation"`
}

type deliveryAggregateReceipt struct {
	SchemaVersion     int                   `json:"schema_version"`
	OperationID       string                `json:"operation_id"`
	ParentOperationID string                `json:"parent_operation_id,omitempty"`
	Phase             string                `json:"phase"`
	PlanDigest        string                `json:"plan_manifest_digest"`
	Files             []deliveryFileReceipt `json:"exact_touched_files"`
	ReceiptDigests    []string              `json:"per_file_receipt_manifest_digests"`
	TouchedScope      string                `json:"touched_scope"`
	WorkspaceQuality  string                `json:"whole_workspace_quality"`
	VerificationClaim string                `json:"verification_claim"`
}

func (s *Session) readRegisteredDeliveryPreviewFiles(ctx context.Context, directory, task, manifestDigest string, names []string) (DeliveryPreviewManifest, map[string][]byte, error) {
	var manifest DeliveryPreviewManifest
	contents := map[string][]byte{}
	path, err := fileguard.ResolveProspective(directory)
	if err != nil {
		return manifest, nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return manifest, nil, err
	}
	defer root.Close()
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return manifest, nil, err
	}
	registry, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return manifest, nil, err
	}
	lock, err := privacyLock(ctx, registry)
	if err != nil {
		registry.Close()
		return manifest, nil, err
	}
	view, err := readPrivacy(registry)
	closeErr := errors.Join(lock.Close(), registry.Close())
	if err != nil || closeErr != nil {
		return manifest, nil, errors.Join(err, closeErr)
	}
	var registered *ManagedCopy
	for i := range view.Copies {
		copy := &view.Copies[i]
		if copy.Directory == path && copy.Kind == "DELIVERY_PREVIEW" && copy.PhysicalRoot == physical && sameManagedTasks(copy.Tasks, []string{task}) {
			registered = copy
			break
		}
	}
	if registered == nil {
		return manifest, nil, c.Fail(c.PolicyDenied, "fresh preview package must be registered with its task owner")
	}
	if err = checkManagedCopy(ctx, *registered, false); err != nil {
		return manifest, nil, err
	}
	manifestRaw, err := fileguard.ReadRegular(root, "manifest.json", 8<<20)
	if err != nil {
		return manifest, nil, err
	}
	if err = c.DecodeStrict(manifestRaw, &manifest); err != nil {
		return manifest, nil, err
	}
	expectedDigest := manifest.Digest
	manifest.Digest = ""
	actualDigest, digestErr := c.Digest(manifest)
	manifest.Digest = expectedDigest
	if digestErr != nil || expectedDigest != actualDigest || expectedDigest != manifestDigest || manifest.SchemaVersion != 1 || manifest.TaskID != task || manifest.Preview.Status != "READY" || manifest.Changeset == nil {
		return manifest, nil, c.Fail(c.StaleBase, "registered preview manifest is stale or incomplete")
	}
	if len(registered.Files) != len(manifest.Files)+1 {
		return manifest, nil, c.Fail(c.StoreIntegrityError, "registered preview inventory differs from its manifest")
	}
	expected := map[string]BackupFile{"manifest.json": {Path: "manifest.json", Digest: c.HashBytes(manifestRaw), Size: int64(len(manifestRaw))}}
	for _, file := range manifest.Files {
		if file.Path == "manifest.json" || file.Path == "" || !safeCopyPath("DELIVERY_PREVIEW", file.Path) || !c.ValidDigest(file.Digest) || file.Size < 0 {
			return manifest, nil, c.Fail(c.StoreIntegrityError, "invalid preview file inventory")
		}
		expected[file.Path] = file
	}
	for _, file := range registered.Files {
		want, ok := expected[file.Path]
		if !ok || want != file {
			return manifest, nil, c.Fail(c.StoreIntegrityError, "registered preview inventory is not the exact sealed package")
		}
	}
	for _, name := range names {
		file, ok := expected[name]
		if !ok || name == "manifest.json" {
			return manifest, nil, c.Fail(c.StoreIntegrityError, "delivery plan requested a file outside the preview manifest")
		}
		contents[name], err = fileguard.ReadRegular(root, name, backupMaxBytes)
		if err != nil {
			return manifest, nil, err
		}
		if int64(len(contents[name])) != file.Size || c.HashBytes(contents[name]) != file.Digest {
			return manifest, nil, c.Fail(c.StaleBase, "registered preview bytes changed after publication")
		}
	}
	return manifest, contents, nil
}

func buildDeliveryOperationPlan(state c.TaskState, preview DeliveryPreviewManifest, root string, contents map[string][]byte, operationID string) (deliveryOperationPlan, map[string][]byte, error) {
	plan := deliveryOperationPlan{SchemaVersion: 1, OperationID: operationID, Phase: c.DeliveryApplyPhase, TaskID: state.TaskID, TargetRoot: root, SourceDocument: state.DocumentDigest, SourceTaskSeq: state.TaskSeq, SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch, Generation: state.KernelGeneration, CandidateDigest: state.CandidateDigest, TargetDigest: preview.Preview.Target, ResultDigest: preview.Preview.Result, PreviewDigest: preview.Preview.Digest, PreviewManifestDigest: preview.Digest, Files: []deliveryOperationFile{}}
	if preview.Changeset == nil || len(preview.Changeset.Changes) == 0 {
		return plan, nil, c.Fail(c.UnsupportedCapability, "delivery candidate has no existing file changes for the native apply profile")
	}
	intents := make([]delivery.NativeFileIntent, 0, len(preview.Changeset.Changes))
	packageFiles := map[string][]byte{}
	for _, change := range preview.Changeset.Changes {
		if change.Kind != "MODIFIED" || change.BeforeDigest == "" || change.AfterDigest == "" || change.BeforeMode == 0 || change.BeforeMode != change.AfterMode {
			return plan, nil, c.Fail(c.UnsupportedCapability, "native apply supports only same-mode modifications to existing regular files")
		}
		before, beforeOK := contents["current/"+change.Path]
		after, afterOK := contents["merged/"+change.Path]
		if !beforeOK || !afterOK || c.HashBytes(before) != change.BeforeDigest || c.HashBytes(after) != change.AfterDigest {
			return plan, nil, c.Fail(c.StoreIntegrityError, "review package bytes do not match the exact per-file changeset")
		}
		plan.Files = append(plan.Files, deliveryOperationFile{Path: change.Path, BeforeDigest: change.BeforeDigest, AfterDigest: change.AfterDigest, Mode: change.BeforeMode})
		intents = append(intents, delivery.NativeFileIntent{Path: change.Path, Before: before, After: after, Mode: change.BeforeMode})
		packageFiles["before/"+change.Path] = append([]byte{}, before...)
		packageFiles["after/"+change.Path] = append([]byte{}, after...)
	}
	sort.Slice(plan.Files, func(i, j int) bool { return strings.ToLower(plan.Files[i].Path) < strings.ToLower(plan.Files[j].Path) })
	sort.Slice(intents, func(i, j int) bool { return strings.ToLower(intents[i].Path) < strings.ToLower(intents[j].Path) })
	return buildDeliverySeal(plan, intents, packageFiles)
}

func buildDeliverySeal(plan deliveryOperationPlan, intents []delivery.NativeFileIntent, packageFiles map[string][]byte) (deliveryOperationPlan, map[string][]byte, error) {
	seal, err := delivery.InspectNativeTarget(plan.TargetRoot, intents)
	if err != nil {
		return plan, nil, err
	}
	plan.Seal = seal
	return plan, packageFiles, nil
}

func (p deliveryOperationPlan) validate(artifact deliveryArtifact) error {
	if p.SchemaVersion != 1 || artifact.Manifest.Kind != "PLAN" || !validDeliveryOperationID(p.OperationID) || (p.Phase != c.DeliveryApplyPhase && p.Phase != c.DeliveryRestorePhase) || p.TaskID != artifact.Manifest.TaskID || p.OperationID != artifact.Manifest.OperationID || !filepath.IsAbs(p.TargetRoot) || filepath.Clean(p.TargetRoot) != p.TargetRoot || !c.ValidDigest(p.SourceDocument) || p.SourceTaskSeq < 1 || p.SpecVersion < 1 || p.PolicyEpoch < 1 || p.Generation < 1 || !c.ValidDigest(p.CandidateDigest) || !c.ValidDigest(p.TargetDigest) || !c.ValidDigest(p.ResultDigest) || !c.ValidDigest(p.PreviewDigest) || !c.ValidDigest(p.PreviewManifestDigest) || len(p.Files) == 0 || len(p.Files) != len(p.Seal.Files) || len(p.Files) > 256 || len(artifact.Files) != 2*len(p.Files) || p.Seal.Root != p.TargetRoot || (p.Phase == c.DeliveryApplyPhase && p.ParentOperationID != "") || (p.Phase == c.DeliveryRestorePhase && !validDeliveryOperationID(p.ParentOperationID)) {
		return c.Fail(c.StoreIntegrityError, "invalid managed delivery operation plan")
	}
	raw, err := c.CanonicalV1(p)
	if err != nil || string(raw) != string(artifact.Report) {
		return c.Fail(c.StoreIntegrityError, "delivery operation report is not canonical")
	}
	for i, file := range p.Files {
		seal := p.Seal.Files[i]
		before, beforeOK := artifact.Files["before/"+file.Path]
		after, afterOK := artifact.Files["after/"+file.Path]
		if file.Path == "" || file.Mode == 0 || !c.ValidDigest(file.BeforeDigest) || !c.ValidDigest(file.AfterDigest) || file.BeforeDigest == file.AfterDigest || !beforeOK || !afterOK || c.HashBytes(before) != file.BeforeDigest || c.HashBytes(after) != file.AfterDigest || seal.Path != file.Path || seal.BeforeDigest != file.BeforeDigest || seal.AfterDigest != file.AfterDigest || seal.Mode != file.Mode || seal.Size != int64(len(before)) || seal.AfterSize != int64(len(after)) {
			return c.Fail(c.StoreIntegrityError, "delivery plan file bytes differ from the sealed native target")
		}
		if i > 0 && strings.ToLower(p.Files[i-1].Path) >= strings.ToLower(file.Path) {
			return c.Fail(c.StoreIntegrityError, "delivery plan files are not uniquely ordered")
		}
	}
	for name := range artifact.Files {
		if !strings.HasPrefix(name, "before/") && !strings.HasPrefix(name, "after/") {
			return c.Fail(c.StoreIntegrityError, "delivery plan package contains bytes outside its before/after images")
		}
	}
	return nil
}

func (s *Session) readDeliveryPlan(ctx context.Context, task, operationID string) (deliveryArtifact, deliveryOperationPlan, error) {
	artifacts, err := s.readDeliveryArtifacts(ctx, task, operationID, "PLAN")
	if err != nil {
		return deliveryArtifact{}, deliveryOperationPlan{}, err
	}
	if len(artifacts) != 1 {
		return deliveryArtifact{}, deliveryOperationPlan{}, c.Fail(c.StoreIntegrityError, "delivery operation must have one exact registered plan")
	}
	var plan deliveryOperationPlan
	if err = c.DecodeStrict(artifacts[0].Report, &plan); err != nil {
		return deliveryArtifact{}, plan, err
	}
	if err = plan.validate(artifacts[0]); err != nil {
		return deliveryArtifact{}, plan, err
	}
	return artifacts[0], plan, nil
}

func (s *Session) recordDeliveryEffect(ctx context.Context, state c.TaskState, effect c.DeliveryEffect) (c.TaskState, error) {
	reason, err := c.Digest(effect)
	if err != nil {
		return state, err
	}
	return s.Journal.Execute(ctx, store.Command{ID: newID("delivery-"), TaskID: state.TaskID, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "DeliveryEffectRecorded", Payload: c.EventPayload{DocumentDigest: state.DocumentDigest, SnapshotDigest: effect.CandidateDigest, Reason: reason, DeliveryEffect: &effect}})
}

func (s *Session) recordReconciledDeliveryEffect(ctx context.Context, state c.TaskState, effect c.DeliveryEffect) (c.TaskState, error) {
	reason, err := c.Digest(effect)
	if err != nil {
		return state, err
	}
	return s.Journal.Execute(ctx, store.Command{ID: newID("delivery-reconcile-"), TaskID: state.TaskID, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "DeliveryEffectReconciled", Payload: c.EventPayload{DocumentDigest: state.DocumentDigest, SnapshotDigest: effect.CandidateDigest, Reason: reason, DeliveryEffect: &effect}})
}

func (s *Session) checkDeliveryWriteAuthority(ctx context.Context, task string, effect c.DeliveryEffect, intentSequence int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return err
	}
	if state.Execution != c.Terminated || state.InputBarrier || len(state.PendingInputIDs) != 0 || state.TaskSeq != intentSequence || state.DocumentDigest != effect.SourceDocument || state.SpecVersion != effect.SpecVersion || state.PolicyEpoch != effect.PolicyEpoch || state.KernelGeneration != effect.Generation || state.KernelGeneration != s.Journal.Generation() || state.CandidateDigest != effect.CandidateDigest || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(state) != nil || !tokensQuiescent(state) {
		return c.Fail(c.StaleAuthority, "delivery task cursor, policy epoch or kernel generation changed")
	}
	found := false
	for _, prior := range state.DeliveryEffects {
		if prior.OperationID == effect.OperationID && prior.Status == c.DeliveryUnknown && prior.Phase == effect.Phase {
			found = true
			break
		}
	}
	if !found {
		return c.Fail(c.StaleAuthority, "durable delivery intent is no longer the active cursor")
	}
	return currentOperatorEffect(doc, deliveryLiveEffect(effect.Phase))
}

func (s *Session) writeDeliveryPlan(ctx context.Context, output string, state c.TaskState, doc Document, plan deliveryOperationPlan, packageFiles map[string][]byte, parentOperationID string, lease delivery.NativeTargetLease) (DeliveryApplyResult, error) {
	result := DeliveryApplyResult{SchemaVersion: 1, TaskID: state.TaskID, TaskSeq: state.TaskSeq, Status: "PREPARING", Backend: plan.Seal.Profile, OperationID: plan.OperationID, TargetDigest: plan.TargetDigest, TouchedScope: "EXACT_TOUCHED_FILES", WorkspaceQuality: "UNKNOWN", Decisions: []delivery.MergeDecision{}, Changeset: nil}
	if err := currentOperatorEffect(doc, deliveryLiveEffect(plan.Phase)); err != nil {
		return result, err
	}
	planRaw, err := c.CanonicalV1(plan)
	if err != nil {
		return result, err
	}
	planDirectory, err := deliveryArtifactDirectory(output, plan.OperationID, "plan")
	if err != nil {
		return result, err
	}
	planArtifact, err := s.publishDeliveryArtifact(ctx, state.TaskID, planDirectory, "PLAN", plan.OperationID, planRaw, packageFiles, plan.TargetRoot)
	if err != nil {
		return result, err
	}
	result.PlanDirectory = planArtifact.Directory
	result.PlanDigest = planArtifact.Manifest.Digest
	effect := c.DeliveryEffect{OperationID: plan.OperationID, ParentOperationID: parentOperationID, Phase: plan.Phase, Status: c.DeliveryUnknown, PlanDigest: planArtifact.Manifest.Digest, SourceDocument: plan.SourceDocument, SourceTaskSeq: plan.SourceTaskSeq, SpecVersion: plan.SpecVersion, PolicyEpoch: plan.PolicyEpoch, Generation: plan.Generation, CandidateDigest: plan.CandidateDigest, TargetDigest: plan.TargetDigest, ResultDigest: plan.ResultDigest}
	if _, err = s.recordDeliveryEffect(ctx, state, effect); err != nil {
		return result, err
	}
	result.Status = "UNKNOWN_EFFECT"
	intentSequence := state.TaskSeq + 1
	fileReceipts := make([]deliveryFileReceipt, 0, len(plan.Files))
	receiptDigests := make([]string, 0, len(plan.Files))
	for i, file := range plan.Files {
		if err = s.checkDeliveryWriteAuthority(ctx, state.TaskID, effect, intentSequence); err != nil {
			return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN before the next file: "+err.Error())
		}
		after := packageFiles["after/"+file.Path]
		current, readErr := lease.ReadFile(file.Path, 64<<20)
		if readErr != nil || c.HashBytes(current) != file.BeforeDigest {
			return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because a touched file no longer matches its sealed before-image: "+errors.Join(readErr, c.Fail(c.StaleBase, "before-image mismatch")).Error())
		}
		result.TargetWritten = true // WriteFile can fail after a partial write; the result must then remain ambiguous.
		if err = lease.WriteFile(ctx, file.Path, file.BeforeDigest, after); err != nil {
			return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because a sealed write did not complete: "+err.Error())
		}
		readback, readErr := lease.ReadFile(file.Path, 64<<20)
		if readErr != nil || c.HashBytes(readback) != file.AfterDigest {
			return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because a synced after-image could not be verified: "+errors.Join(readErr, c.Fail(c.StoreIntegrityError, "after-image mismatch")).Error())
		}
		receipt := deliveryFileReceipt{SchemaVersion: 1, OperationID: plan.OperationID, ParentOperationID: parentOperationID, Phase: plan.Phase, PlanDigest: planArtifact.Manifest.Digest, Ordinal: i + 1, Path: file.Path, BeforeDigest: file.BeforeDigest, AfterDigest: file.AfterDigest, FileIdentity: plan.Seal.Files[i].Identity, ObservedAfter: file.AfterDigest, Observation: "SYNCED_READBACK"}
		receiptRaw, encodeErr := c.CanonicalV1(receipt)
		if encodeErr != nil {
			return result, encodeErr
		}
		receiptDirectory, dirErr := deliveryArtifactDirectory(output, plan.OperationID, fmt.Sprintf("file-%03d", i+1))
		if dirErr != nil {
			return result, dirErr
		}
		fileArtifact, publishErr := s.publishDeliveryArtifact(ctx, state.TaskID, receiptDirectory, "FILE_RECEIPT", plan.OperationID, receiptRaw, nil, plan.TargetRoot)
		if publishErr != nil {
			err = publishErr
			return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because the per-file receipt could not be registered: "+err.Error())
		}
		fileReceipts = append(fileReceipts, receipt)
		receiptDigests = append(receiptDigests, fileArtifact.Manifest.Digest)
	}
	for _, file := range plan.Files {
		if err = s.checkDeliveryWriteAuthority(ctx, state.TaskID, effect, intentSequence); err != nil {
			return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN before final reconciliation: "+err.Error())
		}
		readback, readErr := lease.ReadFile(file.Path, 64<<20)
		if readErr != nil || c.HashBytes(readback) != file.AfterDigest {
			return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because an applied file no longer matches its receipt: "+errors.Join(readErr, c.Fail(c.StaleBase, "postimage mismatch")).Error())
		}
	}
	aggregate := deliveryAggregateReceipt{SchemaVersion: 1, OperationID: plan.OperationID, ParentOperationID: parentOperationID, Phase: plan.Phase, PlanDigest: planArtifact.Manifest.Digest, Files: fileReceipts, ReceiptDigests: receiptDigests, TouchedScope: "EXACT_TOUCHED_FILES", WorkspaceQuality: "UNKNOWN", VerificationClaim: "CANDIDATE_VERIFICATION_NOT_TRANSFERRED_TO_LIVE_WORKSPACE"}
	aggregateRaw, err := c.CanonicalV1(aggregate)
	if err != nil {
		return result, err
	}
	receiptDirectory, err := deliveryArtifactDirectory(output, plan.OperationID, "receipt")
	if err != nil {
		return result, err
	}
	aggregateArtifact, err := s.publishDeliveryArtifact(ctx, state.TaskID, receiptDirectory, "AGGREGATE_RECEIPT", plan.OperationID, aggregateRaw, nil, plan.TargetRoot)
	if err != nil {
		return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because the aggregate receipt could not be registered: "+err.Error())
	}
	if plan.Phase == c.DeliveryApplyPhase {
		effect.Status = c.DeliveryApplied
	} else {
		effect.Status = c.DeliveryRestored
	}
	effect.ReceiptDigest = aggregateArtifact.Manifest.Digest
	currentState, err := s.State(ctx, state.TaskID)
	if err != nil {
		return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because the task state could not be reread: "+err.Error())
	}
	if currentState.TaskSeq != intentSequence || currentState.KernelGeneration != s.Journal.Generation() {
		return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because the completion cursor changed after the last file receipt")
	}
	if _, err = s.recordDeliveryEffect(ctx, currentState, effect); err != nil {
		return keepDeliveryUnknown(result, "durable intent retained as UNKNOWN because the aggregate receipt could not be committed: "+err.Error())
	}
	if plan.Phase == c.DeliveryApplyPhase {
		result.Status = "APPLIED_EXACT_TOUCHED_FILES"
	} else {
		result.Status = "RESTORED_EXACT_TOUCHED_FILES"
	}
	result.ReceiptDigest = aggregateArtifact.Manifest.Digest
	result.Reason = "only the listed existing files were written and receipted under an exclusive NTFS handle lease; whole-workspace quality remains UNKNOWN"
	return result, nil
}

func deliveryLiveEffect(phase string) string {
	if phase == c.DeliveryRestorePhase {
		return "workspace.restore"
	}
	return "workspace.apply"
}

func keepDeliveryUnknown(result DeliveryApplyResult, reason string) (DeliveryApplyResult, error) {
	result.Status = "UNKNOWN_EFFECT"
	result.Reason = reason
	return result, nil
}

func findDeliveryEffect(state c.TaskState, operationID string) (c.DeliveryEffect, bool) {
	for _, effect := range state.DeliveryEffects {
		if effect.OperationID == operationID {
			return effect, true
		}
	}
	return c.DeliveryEffect{}, false
}

func (s *Session) loadDeliveryAggregate(ctx context.Context, task string, effect c.DeliveryEffect) (deliveryAggregateReceipt, deliveryArtifact, error) {
	var result deliveryAggregateReceipt
	artifacts, err := s.readDeliveryArtifacts(ctx, task, effect.OperationID, "AGGREGATE_RECEIPT")
	if err != nil {
		return result, deliveryArtifact{}, err
	}
	if len(artifacts) != 1 || artifacts[0].Manifest.Kind != "AGGREGATE_RECEIPT" || artifacts[0].Manifest.Digest != effect.ReceiptDigest {
		return result, deliveryArtifact{}, c.Fail(c.StoreIntegrityError, "delivery operation does not have one exact registered aggregate receipt")
	}
	if err = c.DecodeStrict(artifacts[0].Report, &result); err != nil {
		return result, deliveryArtifact{}, err
	}
	if result.SchemaVersion != 1 || result.OperationID != effect.OperationID || result.ParentOperationID != effect.ParentOperationID || result.Phase != effect.Phase || result.PlanDigest != effect.PlanDigest || len(result.Files) == 0 || len(result.ReceiptDigests) != len(result.Files) || result.TouchedScope != "EXACT_TOUCHED_FILES" || result.WorkspaceQuality != "UNKNOWN" || result.VerificationClaim != "CANDIDATE_VERIFICATION_NOT_TRANSFERRED_TO_LIVE_WORKSPACE" {
		return result, deliveryArtifact{}, c.Fail(c.StoreIntegrityError, "delivery aggregate receipt binding mismatch")
	}
	seenPaths := map[string]bool{}
	seenDigests := map[string]bool{}
	for i, receipt := range result.Files {
		if receipt.SchemaVersion != 1 || receipt.OperationID != effect.OperationID || receipt.ParentOperationID != effect.ParentOperationID || receipt.Phase != effect.Phase || receipt.PlanDigest != effect.PlanDigest || receipt.Ordinal != i+1 || !policy.SafePath(receipt.Path) || !c.ValidDigest(receipt.BeforeDigest) || !c.ValidDigest(receipt.AfterDigest) || receipt.BeforeDigest == receipt.AfterDigest || !c.ValidDigest(receipt.FileIdentity) || receipt.ObservedAfter != receipt.AfterDigest || receipt.Observation != "SYNCED_READBACK" || seenPaths[receipt.Path] || !c.ValidDigest(result.ReceiptDigests[i]) || seenDigests[result.ReceiptDigests[i]] {
			return result, deliveryArtifact{}, c.Fail(c.StoreIntegrityError, "delivery aggregate per-file receipt is invalid")
		}
		if i > 0 && strings.ToLower(result.Files[i-1].Path) >= strings.ToLower(receipt.Path) {
			return result, deliveryArtifact{}, c.Fail(c.StoreIntegrityError, "delivery aggregate files are not uniquely ordered")
		}
		seenPaths[receipt.Path], seenDigests[result.ReceiptDigests[i]] = true, true
	}
	return result, artifacts[0], nil
}

func (s *Session) loadDeliveryFileReceipts(ctx context.Context, task string, effect c.DeliveryEffect) (map[string]deliveryFileReceipt, error) {
	result := map[string]deliveryFileReceipt{}
	aggregate, _, err := s.loadDeliveryAggregate(ctx, task, effect)
	if err != nil {
		return nil, err
	}
	packages, err := s.readDeliveryArtifacts(ctx, task, effect.OperationID, "FILE_RECEIPT")
	if err != nil {
		return nil, err
	}
	byDigest := map[string]deliveryArtifact{}
	for _, item := range packages {
		byDigest[item.Manifest.Digest] = item
	}
	for i, summary := range aggregate.Files {
		item, found := byDigest[aggregate.ReceiptDigests[i]]
		if !found || item.Manifest.Kind != "FILE_RECEIPT" {
			return nil, c.Fail(c.StoreIntegrityError, "per-file receipt package digest is missing")
		}
		var receipt deliveryFileReceipt
		if c.DecodeStrict(item.Report, &receipt) != nil || receipt != summary {
			return nil, c.Fail(c.StoreIntegrityError, "per-file receipt does not match the aggregate receipt")
		}
		if _, duplicate := result[summary.Path]; duplicate {
			return nil, c.Fail(c.StoreIntegrityError, "duplicate path in per-file receipt")
		}
		result[summary.Path] = receipt
	}
	if len(result) != len(packages) || len(result) != len(aggregate.Files) {
		return nil, c.Fail(c.StoreIntegrityError, "unreferenced or missing per-file delivery receipt")
	}
	return result, nil
}

func exactOperationIDs(files []deliveryOperationFile) []string {
	result := make([]string, len(files))
	for i, file := range files {
		result[i] = file.Path
	}
	return result
}

func deliveryPlanArtifactFiles(plan deliveryOperationPlan, before, after map[string][]byte) map[string][]byte {
	result := map[string][]byte{}
	for _, file := range plan.Files {
		result["before/"+file.Path] = append([]byte{}, before[file.Path]...)
		result["after/"+file.Path] = append([]byte{}, after[file.Path]...)
	}
	return result
}

func planBytesByName(plan deliveryOperationPlan, artifact deliveryArtifact) (map[string][]byte, map[string][]byte) {
	before, after := map[string][]byte{}, map[string][]byte{}
	for _, file := range plan.Files {
		before[file.Path] = artifact.Files["before/"+file.Path]
		after[file.Path] = artifact.Files["after/"+file.Path]
	}
	return before, after
}

func deliveryArtifactDigestMatches(artifact deliveryArtifact, digest string) bool {
	return c.ValidDigest(digest) && artifact.Manifest.Digest == digest
}

func encodeDeliveryPlan(plan deliveryOperationPlan) ([]byte, error) { return c.CanonicalV1(plan) }

func stableDeliveryFiles(files []deliveryOperationFile) bool {
	for i, file := range files {
		if file.Path == "" || (i > 0 && strings.ToLower(files[i-1].Path) >= strings.ToLower(file.Path)) {
			return false
		}
	}
	return true
}
