package agent

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
	"github.com/ixayldz/Viber/internal/store"
)

type GCPlan struct {
	SchemaVersion int                `json:"schema_version"`
	Store         store.SnapshotInfo `json:"store"`
	RootsDigest   string             `json:"roots_digest"`
	Objects       []artifact.Object  `json:"unreachable_objects"`
	HasMore       bool               `json:"has_more"`
	Digest        string             `json:"plan_digest"`
}
type GCCommand struct {
	CommandID string `json:"command_id"`
	Plan      GCPlan `json:"plan"`
}
type GCObjectReceipt struct {
	Object artifact.Object `json:"object"`
	Status string          `json:"status"`
}
type GCResult struct {
	SchemaVersion int               `json:"schema_version"`
	CommandID     string            `json:"command_id"`
	PlanDigest    string            `json:"plan_digest"`
	Objects       []GCObjectReceipt `json:"objects"`
	BytesRemoved  int64             `json:"bytes_removed"`
}
type gcTracer struct {
	session   *Session
	marked    map[string]bool
	docs      map[store.DocumentRef]bool
	snapshots map[artifact.Ref]bool
}

func (g *gcTracer) blob(task, digest string) error {
	if digest == "" {
		return nil
	}
	name, err := artifact.ScopedBlobPath(task, digest)
	if err != nil {
		return err
	}
	if g.marked[name] {
		return nil
	}
	if len(g.marked) >= 100000 {
		return c.Fail(c.BudgetLimitReached, "GC reference graph exceeds quota")
	}
	if _, err = g.session.Archive.GetBytes(task, digest); err != nil {
		return err
	}
	g.marked[name] = true
	return nil
}
func (g *gcTracer) snapshot(ref artifact.Ref) error {
	if g.snapshots[ref] {
		return nil
	}
	name, err := artifact.ScopedSnapshotPath(ref.TaskID, ref.SnapshotDigest)
	if err != nil {
		return err
	}
	manifest, err := g.session.Archive.ReadManifest(ref)
	if err != nil {
		return err
	}
	g.snapshots[ref] = true
	g.marked[name] = true
	for _, entry := range manifest.Snapshot.Entries {
		if err = g.blob(ref.TaskID, entry.Hash); err != nil {
			return err
		}
	}
	if err = g.blob(ref.TaskID, manifest.IndexBlob); err != nil {
		return err
	}
	for _, digest := range manifest.IgnoreBlobs {
		if err = g.blob(ref.TaskID, digest); err != nil {
			return err
		}
	}
	return nil
}
func (g *gcTracer) document(task, digest string) error {
	ref := store.DocumentRef{TaskID: task, Digest: digest}
	if g.docs[ref] {
		return nil
	}
	if len(g.docs) >= 1024 {
		return c.Fail(c.BudgetLimitReached, "GC document graph exceeds quota")
	}
	if err := g.blob(task, digest); err != nil {
		return err
	}
	raw, err := g.session.Archive.GetBytes(task, digest)
	if err != nil {
		return err
	}
	var doc Document
	if c.DecodeStrict(raw, &doc) != nil || doc.TaskID != task {
		return c.Fail(c.StoreIntegrityError, "GC document binding invalid")
	}
	if err = validateNativeLease(doc); err != nil {
		return err
	}
	if err = g.session.validateNativeCleanup(doc); err != nil {
		return err
	}
	g.docs[ref] = true
	for _, ref := range []artifact.Ref{doc.Baseline, doc.Candidate} {
		if err = g.snapshot(ref); err != nil {
			return err
		}
	}
	for _, input := range doc.Spec.Inputs {
		if err = g.blob(task, input.Digest); err != nil {
			return err
		}
	}
	for _, digest := range []string{doc.FixtureDigest, doc.LastResponseBlob, doc.FinalArtifactDigest, doc.VerificationReport} {
		if err = g.blob(task, digest); err != nil {
			return err
		}
	}
	for _, digest := range doc.NativeCleanupAttempts {
		if err = g.blob(task, digest); err != nil {
			return err
		}
		proof, err := g.session.readNativeRisk(doc, digest)
		if err != nil {
			return err
		}
		if err = g.document(task, proof.PreviousDocument); err != nil {
			return err
		}
	}
	if doc.Context != nil {
		if err = g.blob(task, doc.Context.RequestDigest); err != nil {
			return err
		}
	}
	for _, ref := range doc.Compactions {
		if err = g.blob(task, ref.Digest); err != nil {
			return err
		}
		if err = g.blob(task, ref.HistoryDigest); err != nil {
			return err
		}
		raw, err = g.session.Archive.GetBytes(task, ref.Digest)
		if err != nil {
			return err
		}
		var proof CompactionProof
		if c.DecodeStrict(raw, &proof) != nil {
			return c.Fail(c.StoreIntegrityError, "GC compaction proof invalid")
		}
		if err = g.document(task, proof.OriginalDocument); err != nil {
			return err
		}
	}
	for _, ref := range doc.ProjectMemoryRefs {
		if err = g.blob(task, ref.Digest); err != nil {
			return err
		}
	}
	for _, pin := range doc.Pins {
		if err = g.snapshot(artifact.Ref{SchemaVersion: 1, TaskID: task, SnapshotDigest: pin.Candidate}); err != nil {
			return err
		}
		if err = g.blob(task, pin.SourceDigest); err != nil {
			return err
		}
	}
	for _, ref := range doc.CheckRuns {
		if err = g.blob(task, ref.Digest); err != nil {
			return err
		}
		record, err := g.session.readCheckRun(doc, ref)
		if err != nil {
			return err
		}
		if err = g.snapshot(artifact.Ref{SchemaVersion: 1, TaskID: task, SnapshotDigest: checkCandidate(record)}); err != nil {
			return err
		}
	}
	if doc.Plan != nil {
		if err = g.snapshot(artifact.Ref{SchemaVersion: 1, TaskID: task, SnapshotDigest: doc.Plan.Definition.BaseCandidate}); err != nil {
			return err
		}
		for _, progress := range doc.Plan.Progress {
			if err = g.blob(task, progress.OutputDigest); err != nil {
				return err
			}
			if progress.Candidate != "" {
				if err = g.snapshot(artifact.Ref{SchemaVersion: 1, TaskID: task, SnapshotDigest: progress.Candidate}); err != nil {
					return err
				}
			}
		}
	}
	if doc.AttemptOrigin != nil {
		if err = g.document(doc.AttemptOrigin.ParentTask, doc.AttemptOrigin.ParentDocument); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) gcRoots(ctx context.Context) (map[string]bool, error) {
	if _, err := s.backupClosure(ctx); err != nil {
		return nil, err
	}
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return nil, err
	}
	g := gcTracer{s, map[string]bool{}, map[store.DocumentRef]bool{}, map[artifact.Ref]bool{}}
	for _, state := range states {
		if state.Deletion != nil {
			if state.Deletion.Status != "PURGED" {
				return nil, c.Fail(c.PolicyDenied, "pending deletion pins maintenance")
			}
			continue
		}
		if state.Execution == c.Running || state.Execution == c.Verifying || state.Execution == c.Delivering || state.Execution == c.Scoping || state.Execution == c.Pausing {
			return nil, c.Fail(c.PolicyDenied, "GC requires every task to be at a quiescent boundary")
		}
		_, doc, err := s.Load(ctx, state.TaskID)
		if err != nil {
			return nil, err
		}
		if doc.Pending != nil || doc.UnknownEffect {
			return nil, c.Fail(c.UnknownOutcome, "pending or unknown effects pin the archive")
		}
		if state.Tokens != nil {
			for _, r := range state.Tokens.Reservations {
				if r.Status != "SETTLED" {
					return nil, c.Fail(c.UnknownOutcome, "unsettled token risk pins the archive")
				}
				for _, digest := range []string{r.RequestDigest, r.ResponseDigest} {
					if err = g.blob(state.TaskID, digest); err != nil {
						return nil, err
					}
				}
				if r.UsageSource == "OPERATOR_ASSUMED_UPPER_BOUND" {
					raw, err := s.Archive.GetBytes(state.TaskID, r.ResponseDigest)
					if err != nil {
						return nil, err
					}
					var proof ModelRiskReceipt
					if c.DecodeStrict(raw, &proof) != nil {
						return nil, c.Fail(c.StoreIntegrityError, "GC risk proof invalid")
					}
					if err = g.document(state.TaskID, proof.PreviousDocument); err != nil {
						return nil, err
					}
				}
			}
		}
		if state.Resources != nil {
			for _, r := range state.Resources.Reservations {
				if r.Status != "SETTLED" {
					return nil, c.Fail(c.UnknownOutcome, "unsettled resource risk pins the archive")
				}
				if err = g.blob(state.TaskID, r.ReceiptDigest); err != nil {
					return nil, err
				}
				raw, err := s.Archive.GetBytes(state.TaskID, r.ReceiptDigest)
				if err != nil {
					return nil, err
				}
				var receipt ResourceReceipt
				if c.DecodeStrict(raw, &receipt) != nil {
					return nil, c.Fail(c.StoreIntegrityError, "GC resource proof invalid")
				}
				if err = g.snapshot(artifact.Ref{SchemaVersion: 1, TaskID: state.TaskID, SnapshotDigest: receipt.Candidate}); err != nil {
					return nil, err
				}
			}
		}
		for after := int64(0); ; {
			page, err := s.Journal.History(ctx, state.TaskID, after, 256)
			if err != nil {
				return nil, err
			}
			for _, record := range page.Records {
				if err = g.blob(state.TaskID, record.Payload.InputDigest); err != nil {
					return nil, err
				}
			}
			if !page.HasMore {
				break
			}
			after = page.Next
		}
	}
	// Standalone snapshot-save scopes have no kernel journal roots. They are
	// outside this collector's authority and remain pinned in their entirety.
	objects, err := s.Archive.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	for _, object := range objects {
		if _, known := states[artifact.TaskFromPath(object.Path)]; !known {
			g.marked[object.Path] = true
		}
	}
	refs, err := s.Journal.DocumentReferences(ctx)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if states[ref.TaskID].Deletion != nil {
			continue
		}
		if err = g.document(ref.TaskID, ref.Digest); err != nil {
			return nil, err
		}
	}
	return g.marked, nil
}
func rootsDigest(marked map[string]bool) string {
	paths := make([]string, 0, len(marked))
	for path := range marked {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	digest, _ := c.Digest(paths)
	return digest
}
func planDigest(plan GCPlan) string { plan.Digest = ""; digest, _ := c.Digest(plan); return digest }
func (s *Session) GCPreview(ctx context.Context) (GCPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := GCPlan{SchemaVersion: 1, Objects: []artifact.Object{}}
	if err := s.Journal.Writable(); err != nil {
		return plan, err
	}
	marked, err := s.gcRoots(ctx)
	if err != nil {
		return plan, err
	}
	plan.Store, err = s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return plan, err
	}
	plan.RootsDigest = rootsDigest(marked)
	objects, err := s.Archive.Inventory(ctx)
	if err != nil {
		return plan, err
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Path < objects[j].Path })
	maxObjects := 2048
	if s.PhysicalQuota != nil {
		// The control receipt reserves a root, intent, completion and one
		// temporary entry in addition to one record per collected object.
		// Never offer a batch that cannot fit even an empty control ledger.
		entries := s.PhysicalQuota.Limits.ControlEntries
		if entries <= 4 {
			return plan, c.Fail(c.BudgetLimitReached, "GC control entry budget cannot fit a maintenance batch")
		}
		maxObjects = int(min(uint64(maxObjects), entries-4))
	}
	for _, object := range objects {
		if marked[object.Path] {
			continue
		}
		if len(plan.Objects) == maxObjects {
			plan.HasMore = true
			break
		}
		plan.Objects = append(plan.Objects, object)
	}
	plan.Digest = planDigest(plan)
	return plan, nil
}
func (s *Session) CollectGarbage(ctx context.Context, command GCCommand) (GCResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := GCResult{SchemaVersion: 1, CommandID: command.CommandID, PlanDigest: command.Plan.Digest, Objects: []GCObjectReceipt{}}
	plan := command.Plan
	if command.CommandID == "" || len(command.CommandID) > 128 || plan.SchemaVersion != 1 || !c.ValidDigest(plan.Digest) || plan.Digest != planDigest(plan) || len(plan.Objects) > 2048 || !c.ValidDigest(plan.RootsDigest) {
		return result, c.Fail(c.InvalidArgument, "bounded source-bound GC plan and stable command ID required")
	}
	if err := s.Journal.Writable(); err != nil {
		return result, err
	}
	marked, err := s.gcRoots(ctx)
	if err != nil {
		return result, err
	}
	info, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return result, err
	}
	if info != plan.Store || rootsDigest(marked) != plan.RootsDigest {
		return result, c.Fail(c.StaleAuthority, "GC journal or reference graph changed; obtain a new preview")
	}
	previous := ""
	for _, object := range plan.Objects {
		if !artifact.ImmutablePath(object.Path) || object.Path <= previous || !c.ValidDigest(object.Digest) || object.Size < 0 || object.Size > 64<<20 || marked[object.Path] {
			return result, c.Fail(c.PolicyDenied, "GC plan contains an invalid or reachable object")
		}
		previous = object.Path
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return result, err
	}
	defer root.Close()
	id := c.HashBytes([]byte(command.CommandID))
	base := filepath.Join("maintenance", "gc", id)
	raw, err := c.CanonicalV1(command)
	if err != nil {
		return result, err
	}
	commandRaw := raw
	intentPath := filepath.Join(base, "intent.json")
	previousIntent, intentErr := fileguard.ReadRegular(root, intentPath, 2<<20)
	resuming := intentErr == nil
	if resuming && !bytes.Equal(previousIntent, raw) {
		return result, c.Fail(c.CommandIDConflict, "GC command ID reused with different plan")
	}
	if intentErr != nil && !errors.Is(intentErr, os.ErrNotExist) {
		return result, intentErr
	}
	if intentErr != nil && errors.Is(intentErr, os.ErrNotExist) {
		// A completion or object receipt without its original intent cannot
		// authorize replay or receipt retirement.
		entries, readDirErr := fs.ReadDir(root.FS(), filepath.ToSlash(base))
		if readDirErr == nil && len(entries) != 0 {
			return result, c.Fail(c.StoreIntegrityError, "GC records exist without their durable intent")
		} else if readDirErr != nil && !errors.Is(readDirErr, os.ErrNotExist) {
			return result, readDirErr
		}
	}
	// A completion marker may have reached disk immediately before a crash
	// prevented the WRITING allocation from being settled. Reconcile that exact
	// allocation below instead of assuming the marker implies a retained row.
	completePath := filepath.Join(base, "complete.json")
	previousComplete, completeErr := fileguard.ReadRegular(root, completePath, 2<<20)
	if completeErr != nil && !errors.Is(completeErr, os.ErrNotExist) {
		return result, completeErr
	}
	alreadyComplete := false
	if completeErr == nil {
		var prior GCResult
		if err = validateGCRecord(filepath.ToSlash(completePath), previousComplete); err != nil {
			return result, err
		}
		if c.DecodeStrict(previousComplete, &prior) != nil || prior.CommandID != command.CommandID || prior.PlanDigest != command.Plan.Digest {
			return result, c.Fail(c.CommandIDConflict, "immutable GC record differs")
		}
		if err = validateGCCompletionAgainstCommand(command, prior); err != nil {
			return result, err
		}
		result = prior
		alreadyComplete = true
	}
	if resuming {
		records, recordsErr := readGCCommandRecords(root, base)
		if recordsErr != nil {
			return result, recordsErr
		}
		if err = validateGCMaintenanceRecords(records); err != nil {
			return result, err
		}
	}
	if !alreadyComplete {
		if err = s.Journal.DiskAdmission(int64(len(commandRaw))+2<<20, true); err != nil {
			return result, err
		}
	}
	var allocation resource.Allocation
	owned := false
	retained := false
	if s.PhysicalQuota != nil {
		gcTarget := filepath.Join(s.directory, base)
		upperBytes, upperEntries, estimateErr := gcPhysicalEstimate(commandRaw, len(plan.Objects))
		if estimateErr != nil {
			return result, estimateErr
		}
		match, matchErr := s.PhysicalQuota.ReserveOrMatch(ctx, resource.Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes(commandRaw), TargetRoot: gcTarget, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Control})
		if matchErr != nil {
			return result, matchErr
		}
		if match.Scope == resource.ExactReservationScope {
			allocation = match.Allocation
			owned = true
			switch match.Status {
			case "RESERVED":
				if err = allocation.Begin(ctx); err != nil {
					return result, err
				}
			case "WRITING":
				// Continue under the exact envelope after an interrupted attempt.
			case "RETAINED":
				if !alreadyComplete {
					return result, c.Fail(c.StaleAuthority, "GC output is retained without its completion record")
				}
				retained = true
			case "FREE_PENDING":
				if alreadyComplete {
					// A pending member free may have been interrupted after unlink.
					// Verify every still-retained command file against its saved
					// inventory before returning the completion without another unlink.
					pendingRoot, openErr := os.OpenRoot(gcTarget)
					if openErr != nil {
						return result, openErr
					}
					pendingInventory, measureErr := resource.Measure(pendingRoot, true)
					closeErr := pendingRoot.Close()
					if err = errors.Join(measureErr, closeErr); err != nil {
						return result, err
					}
					if err = allocation.ValidateFreePending(ctx, pendingInventory); err != nil {
						return result, err
					}
					return result, nil
				}
				return result, c.Fail(c.StaleAuthority, "incomplete GC output has an unresolved physical free intent")
			default:
				return result, c.Fail(c.StaleAuthority, "GC physical allocation is not writable")
			}
		} else if match.Scope != resource.AncestorReservationScope {
			return result, c.Fail(c.StoreIntegrityError, "GC physical reservation scope is invalid")
		}
	}
	objects, err := s.Archive.Inventory(ctx)
	if err != nil {
		return result, err
	}
	present := map[string]artifact.Object{}
	for _, object := range objects {
		present[object.Path] = object
	}
	for _, object := range plan.Objects {
		actual, found := present[object.Path]
		if found && actual != object {
			return result, c.Fail(c.StaleBase, "GC object differs from preview")
		}
		if !found && !resuming {
			return result, c.Fail(c.StaleBase, "GC object missing before durable intent")
		}
	}
	if err = publishGCSame(root, intentPath, raw); err != nil {
		return result, err
	}
	if !alreadyComplete {
		for _, object := range plan.Objects {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			name := filepath.Join(base, c.HashBytes([]byte(object.Path))+".json")
			prior, readErr := fileguard.ReadRegular(root, name, 4096)
			var receipt GCObjectReceipt
			if readErr == nil {
				if c.DecodeStrict(prior, &receipt) != nil || receipt.Object != object || (receipt.Status != "UNLINKED_AND_DIRECTORY_SYNCED" && receipt.Status != "ALREADY_ABSENT_AFTER_INTENT") {
					return result, c.Fail(c.StoreIntegrityError, "GC receipt binding invalid")
				}
			} else {
				if !errors.Is(readErr, os.ErrNotExist) {
					return result, readErr
				}
				removed, err := s.Archive.RemoveOrphan(object)
				if err != nil {
					return result, err
				}
				receipt = GCObjectReceipt{Object: object, Status: "ALREADY_ABSENT_AFTER_INTENT"}
				if removed {
					receipt.Status = "UNLINKED_AND_DIRECTORY_SYNCED"
				}
				raw, err = c.CanonicalV1(receipt)
				if err != nil {
					return result, err
				}
				if err = fileguard.Publish(root, name, raw); err != nil {
					return result, err
				}
			}
			result.Objects = append(result.Objects, receipt)
			if receipt.Status == "UNLINKED_AND_DIRECTORY_SYNCED" {
				result.BytesRemoved += object.Size
			}
		}
		raw, err = c.CanonicalV1(result)
		if err != nil {
			return result, err
		}
		if err = publishGCSame(root, filepath.Join(base, "complete.json"), raw); err != nil {
			return result, err
		}
	}
	if s.PhysicalQuota != nil && owned {
		gcRoot, openErr := os.OpenRoot(filepath.Join(s.directory, base))
		if openErr != nil {
			return result, openErr
		}
		defer gcRoot.Close()
		inventory, measureErr := resource.Measure(gcRoot, true)
		if err = measureErr; err != nil {
			return result, err
		}
		if retained {
			if err = allocation.ValidateRetained(ctx, inventory); err != nil {
				return result, err
			}
		} else if err = allocation.Commit(ctx, inventory); err != nil {
			return result, err
		}
		pending, retireErr := s.retireGCObjectReceipts(ctx, gcRoot, command, result)
		if retireErr != nil {
			return result, retireErr
		}
		if pending {
			return result, nil
		}
		inventory, err = resource.Measure(gcRoot, true)
		if err != nil {
			return result, err
		}
		if err = allocation.ValidateRetained(ctx, inventory); err != nil {
			return result, err
		}
	}
	return result, nil
}

// retireGCObjectReceipts removes only the per-object files already represented
// by a matching durable completion. The original command and exact result stay
// retained for retries and backup/restore; quota free intents reconcile each
// unlink before the next receipt is considered.
func (s *Session) retireGCObjectReceipts(ctx context.Context, root *os.Root, command GCCommand, result GCResult) (bool, error) {
	if s.PhysicalQuota == nil {
		return false, nil
	}
	commandRaw, err := c.CanonicalV1(command)
	if err != nil {
		return false, err
	}
	upperBytes, upperEntries, err := gcPhysicalEstimate(commandRaw, len(command.Plan.Objects))
	if err != nil {
		return false, err
	}
	request := resource.Request{Writer: "agent", Family: "GC_MAINTENANCE", RequestDigest: c.HashBytes(commandRaw), TargetRoot: root.Name(), UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Control}
	for _, receipt := range result.Objects {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		name := c.HashBytes([]byte(receipt.Object.Path)) + ".json"
		raw, readErr := fileguard.ReadRegular(root, name, 4096)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return false, readErr
		}
		if err = validateGCRecord(filepath.ToSlash(filepath.Join("maintenance", "gc", c.HashBytes([]byte(command.CommandID)), name)), raw); err != nil {
			return false, err
		}
		var actual GCObjectReceipt
		if c.DecodeStrict(raw, &actual) != nil || actual != receipt {
			return false, c.Fail(c.StoreIntegrityError, "GC object receipt differs from its durable completion")
		}
		if _, err = s.PhysicalQuota.ReleaseFile(ctx, root, name, c.HashBytes(raw), int64(len(raw)), int64(len(raw))); err != nil {
			return false, err
		}
		if _, statErr := root.Lstat(name); statErr == nil {
			return false, c.Fail(c.StaleAuthority, "GC object receipt was not physically released")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return false, statErr
		}
		match, matchErr := s.PhysicalQuota.ReserveOrMatch(ctx, request)
		if matchErr != nil {
			return false, matchErr
		}
		if match.Scope != resource.ExactReservationScope {
			return false, c.Fail(c.StoreIntegrityError, "GC physical allocation scope changed during receipt retirement")
		}
		if match.Status == "FREE_PENDING" {
			return true, nil
		}
		if match.Status != "RETAINED" {
			return false, c.Fail(c.BudgetLimitReached, "GC receipt retirement awaits physical free reconciliation")
		}
	}
	return false, nil
}

// gcPhysicalEstimate bounds the durable control records of one GC command:
// the intent, one receipt per object and the final result each repeat the
// bounded object list, plus per-file allocation slack and directory charges.
func gcPhysicalEstimate(intent []byte, objects int) (uint64, uint64, error) {
	if len(intent) > 2<<20 || objects > 2048 {
		return 0, 0, c.Fail(c.UnsupportedCapability, "GC physical receipt exceeds bounded profile")
	}
	upperBytes := uint64(len(intent))*3 + uint64(objects)*(4096+64) + 3*4096 + (64 << 10)
	if upperBytes > 1<<48 {
		return 0, 0, c.Fail(c.UnsupportedCapability, "GC byte estimate exceeds bounded profile")
	}
	upperEntries := uint64(objects) + 4 // intent, complete, root, one live temp
	return upperBytes, upperEntries, nil
}

func publishGCSame(root *os.Root, name string, raw []byte) error {
	prior, err := fileguard.ReadRegular(root, name, 2<<20)
	if err == nil {
		if !bytes.Equal(prior, raw) {
			return c.Fail(c.CommandIDConflict, "immutable GC record differs")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fileguard.Publish(root, name, raw)
}
