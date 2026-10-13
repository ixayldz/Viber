package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

type projectMemoryCopyRequest struct {
	SchemaVersion    int    `json:"schema_version"`
	CommandID        string `json:"command_id"`
	SourceTask       string `json:"source_task"`
	TargetTask       string `json:"target_task"`
	SourceItemID     string `json:"source_item_id"`
	SourceItemDigest string `json:"source_item_digest"`
}

// CopyProjectMemory explicitly copies one active user assertion into another
// task in the same owner store. A durable external PREPARED pin precedes both
// target archive writes and the journal publication; ACTIVE follows the exact
// journal receipt. The copied content remains advisory only.
func (s *Session) CopyProjectMemory(ctx context.Context, sourceTask, targetTask, commandID, itemID, itemDigest string) (ProjectMemoryCopyReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	return s.copyProjectMemoryLocked(ctx, sourceTask, targetTask, commandID, itemID, itemDigest)
}

// ProjectMemoryCopyCapabilityUsed reports whether the current store's
// journal-bound privacy authority has ever admitted a copy, including an
// interrupted PREPARED publication. Callers must already hold the store lock;
// the helper separately takes the privacy-registry lock and validates its
// current checkpoint/journal chain before returning.
func ProjectMemoryCopyCapabilityUsed(ctx context.Context, journal *store.Store) (bool, error) {
	if journal == nil {
		return false, c.Fail(c.InvalidArgument, "locked project store required")
	}
	raw, err := journal.PrivacyAuthority(ctx)
	if err != nil || len(raw) == 0 {
		return false, err
	}
	var authority PrivacyAuthority
	if c.DecodeStrict(raw, &authority) != nil || authority.validate() != nil {
		return false, c.Fail(c.StoreIntegrityError, "project store privacy authority is invalid")
	}
	root, err := openPrivacyRoot(authority)
	if err != nil {
		return false, err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return false, err
	}
	return len(view.MemoryCopies) > 0, nil
}

func (s *Session) copyProjectMemoryLocked(ctx context.Context, sourceTask, targetTask, commandID, itemID, itemDigest string) (ProjectMemoryCopyReceipt, error) {
	var empty ProjectMemoryCopyReceipt
	if sourceTask == targetTask || sourceTask == "" || targetTask == "" || !validProjectMemoryCommandID(commandID) ||
		!projectMemoryIDPattern.MatchString(itemID) || !c.ValidDigest(itemDigest) {
		return empty, c.Fail(c.InvalidArgument, "different source and target tasks, stable command ID and exact memory item required")
	}
	request := projectMemoryCopyRequest{SchemaVersion: 1, CommandID: commandID, SourceTask: sourceTask, TargetTask: targetTask,
		SourceItemID: itemID, SourceItemDigest: itemDigest}
	requestDigest, err := c.Digest(request)
	if err != nil {
		return empty, err
	}
	copyID := "pmcopy-" + requestDigest[:32]
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	if err = s.Journal.Writable(); err != nil {
		return empty, err
	}
	if err = s.ensurePrivacy(ctx); err != nil {
		return empty, err
	}

	// Stable-command retries inspect the external intent before loading the
	// target document: after a crash between journal publication and ACTIVE,
	// loadDocument correctly rejects the not-yet-activated typed reference.
	root, lock, view, err := s.lockPrivacyForProjectMemoryCopy(ctx)
	if err != nil {
		return empty, err
	}
	if existingID := view.MemoryCopyCommands[commandID]; existingID != "" {
		var existing *privacyMemoryCopy
		for i := range view.MemoryCopies {
			if view.MemoryCopies[i].CopyID == existingID {
				existing = &view.MemoryCopies[i]
				break
			}
		}
		if existing == nil || existing.RequestDigest != requestDigest || existing.CopyID != copyID ||
			existing.Origin.TaskID != sourceTask || existing.Target.TaskID != targetTask ||
			existing.SourceItemID != itemID || existing.SourceItemDigest != itemDigest {
			err = c.Fail(c.CommandIDConflict, "project memory copy command ID was reused with different content")
		} else if existing.Status == "ACTIVE" {
			_, replayErr := s.replayTaskLocked(ctx, targetTask)
			if replayErr != nil {
				err = replayErr
			} else {
				return s.closePrivacyCopyAndReturn(root, lock, copyReceipt(*existing, "COPIED"), nil)
			}
		} else if existing.Status == "PREPARED" {
			state, replayErr := s.replayTaskLocked(ctx, targetTask)
			if replayErr != nil {
				err = replayErr
			} else if state.Deletion != nil {
				prior, priorErr := s.Journal.ReplayAt(ctx, targetTask, existing.TargetTaskSequence)
				if priorErr == nil && prior.TaskSeq == existing.TargetTaskSequence && prior.DocumentDigest == existing.TargetDocument &&
					prior.ProjectMemoryCopyCount > 0 {
					return s.closePrivacyCopyAndReturn(root, lock, copyReceipt(*existing, "COPIED"), nil)
				}
				err = c.Fail(c.PolicyDenied, "target deletion prevents recovery of an unpublished project memory copy")
			} else if state.TaskSeq == existing.TargetTaskSequence && state.DocumentDigest == existing.TargetDocument {
				err = s.activatePreparedProjectMemoryCopyLocked(ctx, root, view, *existing, state)
				if err == nil {
					var refreshed privacyView
					refreshed, err = readPrivacy(root)
					if err == nil {
						for _, item := range refreshed.MemoryCopies {
							if item.CopyID == existing.CopyID {
								return s.closePrivacyCopyAndReturn(root, lock, copyReceipt(item, "COPIED"), nil)
							}
						}
						err = c.Fail(c.StoreIntegrityError, "activated project memory copy disappeared")
					}
				}
			} else if state.TaskSeq == existing.Target.TaskSeq && state.DocumentDigest == existing.Target.DocumentDigest {
				// Continue the original PREPARED publication below using the same
				// deterministic source and target cursors.
			} else {
				err = c.Fail(c.StaleBase, "prepared project memory copy no longer matches the exact target cursor")
			}
		}
	}
	if err != nil {
		return s.closePrivacyCopyAndReturn(root, lock, empty, err)
	}
	if err = errors.Join(lock.Close(), root.Close()); err != nil {
		return empty, err
	}

	if err = s.preflightProjectMemoryCopyPolicy(ctx, sourceTask, targetTask); err != nil {
		return empty, err
	}

	// Validate current source and target policy before reading any source memory
	// body. In particular, context.read on the source does not grant a transfer
	// to a target with a different effective provider/egress/privacy policy.
	sourceState, sourceDoc, err := s.Load(ctx, sourceTask)
	if err != nil {
		return empty, err
	}
	targetState, targetDoc, err := s.Load(ctx, targetTask)
	if err != nil {
		return empty, err
	}
	if !projectMemoryMutationQuiescent(sourceState) || !projectMemoryMutationQuiescent(targetState) {
		return empty, c.Fail(c.Conflict, "project memory copy requires quiescent source and target tasks")
	}
	if err = currentOperatorEffect(sourceDoc, "context.read"); err != nil {
		return empty, err
	}
	if err = currentOperatorEffect(targetDoc, "context.read"); err != nil {
		return empty, err
	}
	if err = currentOperatorEffect(targetDoc, "context.edit"); err != nil {
		return empty, err
	}
	origin, err := s.projectMemorySource(ctx, sourceState, sourceDoc)
	if err != nil {
		return empty, err
	}
	target, err := s.projectMemorySource(ctx, targetState, targetDoc)
	if err != nil {
		return empty, err
	}
	if origin.ProjectDigest != target.ProjectDigest || origin.PolicyDigest != target.PolicyDigest || origin.PolicyEpoch != target.PolicyEpoch {
		return empty, c.Fail(c.PolicyDenied, "project memory copy requires the same current project and retained policy epoch")
	}
	originEffective, err := projectMemoryEffectivePolicyDigest(sourceDoc)
	if err != nil {
		return empty, err
	}
	targetEffective, err := projectMemoryEffectivePolicyDigest(targetDoc)
	if err != nil {
		return empty, err
	}
	if originEffective != targetEffective {
		return empty, c.Fail(c.PolicyDenied, "source and target effective privacy/provider policies differ")
	}

	stored, current, err := s.projectMemoryInventory(ctx, origin.ProjectDigest)
	if err != nil {
		return empty, err
	}
	items, err := projectMemoryBuildItems(stored, current, origin)
	if err != nil {
		return empty, err
	}
	var selected *ProjectMemoryItem
	for i := range items {
		if items[i].ID == itemID && items[i].Digest == itemDigest && items[i].Source.TaskID == sourceTask {
			selected = &items[i]
			break
		}
	}
	if selected == nil || selected.Status != "ACTIVE" || selected.EvidenceClass != "USER_ASSERTED" || selected.Origin != nil || selected.Redacted {
		return empty, c.Fail(c.PolicyDenied, "only an exact active user assertion can be copied; copied or stale entries cannot be recopied")
	}
	if len(targetDoc.ProjectMemoryRefs) >= projectMemoryMaxEventsPerTask || targetState.ProjectMemoryCopyCount >= projectMemoryMaxCopiesPerTask ||
		targetState.ProjectMemoryRevision == int64(^uint64(0)>>1) {
		return empty, c.Fail(c.BudgetLimitReached, "target project memory history is full; no records were discarded")
	}
	if _, _, _, found, receiptErr := s.Journal.CommandReceipt(ctx, commandID); receiptErr != nil {
		return empty, receiptErr
	} else if found {
		return empty, c.Fail(c.CommandIDConflict, "project memory copy command ID is already used by another store command")
	}

	previous := ""
	if len(targetDoc.ProjectMemoryRefs) > 0 {
		previous = targetDoc.ProjectMemoryRefs[len(targetDoc.ProjectMemoryRefs)-1].Digest
	}
	copyValue := ProjectMemoryCopy{SchemaVersion: 1, ID: copyID, Origin: origin, Target: target,
		TransferPolicyDigest: originEffective, RequestDigest: requestDigest, ContentDigest: selected.ContentDigest,
		PreviousRef: previous, SourceItemID: selected.ID, SourceItemDigest: selected.Digest, CommandID: commandID,
		Kind: selected.Kind, Key: selected.Key, Text: selected.Text, EvidenceClass: "USER_COPIED"}
	raw, err := c.CanonicalV1(copyValue)
	if err != nil {
		return empty, err
	}
	if len(raw) > projectMemoryEventMaxBytes {
		return empty, c.Fail(c.BudgetLimitReached, "project memory copy exceeds its byte bound")
	}
	ref := ProjectMemoryRef{ID: copyID, Digest: c.HashBytes(raw), Type: "PROJECT_MEMORY_COPY"}
	if err = copyValue.Validate(ref, targetTask); err != nil {
		return empty, err
	}
	targetDoc.ProjectMemoryRefs = append(targetDoc.ProjectMemoryRefs, ref)
	docBytes, err := c.CanonicalV1(targetDoc)
	if err != nil || int64(len(docBytes)) > projectMemoryMaxDocumentBytes {
		if err != nil {
			return empty, err
		}
		return empty, c.Fail(c.BudgetLimitReached, "target task document exceeds its byte bound")
	}
	postDocumentDigest := c.HashBytes(docBytes)
	generation := s.Journal.Generation()
	mutation := &c.ProjectMemoryMutation{SourceDocumentDigest: target.DocumentDigest, EventDigest: ref.Digest,
		CommandID: commandID, EventBytes: int64(len(raw)), SourceTaskSeq: target.TaskSeq, SpecVersion: targetState.SpecVersion,
		PolicyEpoch: targetState.PolicyEpoch, KernelGeneration: generation, Action: "COPY",
		ConflictMetadata: &c.ProjectMemoryConflictMetadata{SchemaVersion: 1, ProjectDigest: target.ProjectDigest,
			BaselineDigest: target.BaselineDigest, CandidateDigest: target.CandidateDigest, PolicyDigest: target.PolicyDigest}}
	if err = mutation.Validate(); err != nil {
		return empty, err
	}
	scope, err := s.currentPrivacyMemoryScope()
	if err != nil {
		return empty, err
	}
	metadata := privacyMemoryCopy{Scope: scope, Origin: origin, Target: target,
		TransferPolicyDigest: originEffective, RequestDigest: requestDigest, CopyDigest: ref.Digest, CopyBytes: int64(len(raw)),
		TargetDocument: postDocumentDigest, SourceItemID: selected.ID, SourceItemDigest: selected.Digest,
		CommandID: commandID, CopyID: copyID, TargetTaskSequence: target.TaskSeq + 1, Status: "PREPARED"}
	if err = metadata.validate(); err != nil {
		return empty, err
	}

	root, lock, view, err = s.lockPrivacyForProjectMemoryCopy(ctx)
	if err != nil {
		return empty, err
	}
	if existingID := view.MemoryCopyCommands[commandID]; existingID != "" {
		var existing *privacyMemoryCopy
		for i := range view.MemoryCopies {
			if view.MemoryCopies[i].CopyID == existingID {
				existing = &view.MemoryCopies[i]
				break
			}
		}
		if existing == nil || existing.CopyID != copyID || existing.RequestDigest != requestDigest || !samePrivacyMemoryCopy(*existing, metadata) {
			return s.closePrivacyCopyAndReturn(root, lock, empty, c.Fail(c.CommandIDConflict, "project memory copy command already has a different prepared binding"))
		}
		if existing.Status == "ACTIVE" {
			return s.closePrivacyCopyAndReturn(root, lock, copyReceipt(*existing, "COPIED"), nil)
		}
		metadata = *existing
	} else {
		if err = onlyPrivacyOwner(root, s.privacyOwner.name); err != nil {
			return s.closePrivacyCopyAndReturn(root, lock, empty, err)
		}
		if _, deleted := view.Deletions[sourceTask]; deleted {
			return s.closePrivacyCopyAndReturn(root, lock, empty, c.Fail(c.PolicyDenied, "source task deletion has already been admitted"))
		}
		if _, deleted := view.Deletions[targetTask]; deleted {
			return s.closePrivacyCopyAndReturn(root, lock, empty, c.Fail(c.PolicyDenied, "target task deletion has already been admitted"))
		}
		if privacyCopyWouldCycle(view, metadata.Scope, sourceTask, targetTask) {
			return s.closePrivacyCopyAndReturn(root, lock, empty, c.Fail(c.PolicyDenied, "project memory copy would create a task lineage cycle"))
		}
		if err = s.verifyProjectMemoryCopyOwnerScope(root, metadata.Scope); err != nil {
			return s.closePrivacyCopyAndReturn(root, lock, empty, err)
		}
		if _, _, _, found, receiptErr := s.Journal.CommandReceipt(ctx, commandID); receiptErr != nil {
			return s.closePrivacyCopyAndReturn(root, lock, empty, receiptErr)
		} else if found {
			return s.closePrivacyCopyAndReturn(root, lock, empty, c.Fail(c.CommandIDConflict, "project memory copy command ID is already used by another store command"))
		}
		originCurrent, originErr := s.replayTaskLocked(ctx, sourceTask)
		targetCurrent, targetErr := s.replayTaskLocked(ctx, targetTask)
		if originErr != nil || targetErr != nil || originCurrent.TaskSeq != origin.TaskSeq || originCurrent.DocumentDigest != origin.DocumentDigest ||
			targetCurrent.TaskSeq != target.TaskSeq || targetCurrent.DocumentDigest != target.DocumentDigest || originCurrent.Deletion != nil || targetCurrent.Deletion != nil {
			return s.closePrivacyCopyAndReturn(root, lock, empty, c.Fail(c.StaleBase, "source or target task changed before copy preparation"))
		}
		if err = appendPrivacy(root, view, privacyRecord{Type: "MEMORY_COPY_PREPARED", MemoryCopy: &metadata}); err != nil {
			return s.closePrivacyCopyAndReturn(root, lock, empty, err)
		}
	}
	if s.projectMemoryCopyFault != nil {
		if err = s.projectMemoryCopyFault("prepared-before-archive"); err != nil {
			return s.closePrivacyCopyAndReturn(root, lock, empty, err)
		}
	}

	// The registry intent remains durable if any following step fails. Stable
	// retries either finish this exact publication or fail closed on drift.
	storedDigest, err := s.Archive.PutBytes(targetTask, raw)
	if err == nil && storedDigest != ref.Digest {
		err = c.Fail(c.StoreIntegrityError, "project memory copy archive digest changed")
	}
	var documentDigest string
	if err == nil {
		documentDigest, err = s.Archive.PutControlBytes(targetTask, docBytes, true)
	}
	if err == nil && documentDigest != metadata.TargetDocument {
		err = c.Fail(c.StoreIntegrityError, "project memory copy target document digest changed")
	}
	if err != nil {
		return s.closePrivacyCopyAndReturn(root, lock, empty, err)
	}
	predicted := targetState
	predicted.DocumentDigest = documentDigest
	predicted.TaskSeq++
	predicted.KernelGeneration = generation
	predicted.ProjectMemoryRevision++
	predicted.ProjectMemoryCopyCount++
	if predicted.ProjectMemoryCopyCount > projectMemoryMaxCopiesPerTask {
		return s.closePrivacyCopyAndReturn(root, lock, empty, c.Fail(c.BudgetLimitReached, "target project memory copy limit reached"))
	}
	if predicted.ProjectMemoryRevision != targetState.ProjectMemoryRevision+1 || predicted.ProjectMemoryCopyCount != targetState.ProjectMemoryCopyCount+1 ||
		len(targetDoc.ProjectMemoryRefs) > projectMemoryMaxEventsPerTask {
		return s.closePrivacyCopyAndReturn(root, lock, empty, c.Fail(c.StoreIntegrityError, "project memory copy projection is inconsistent"))
	}
	budgetBefore := targetState.WorkBudget
	next, err := s.Journal.Execute(ctx, store.Command{ID: commandID, TaskID: targetTask, Actor: "user", ExpectedTaskSeq: targetState.TaskSeq,
		Type: "ProjectMemoryRecorded", Payload: c.EventPayload{DocumentDigest: documentDigest, ProjectMemoryMutation: mutation}})
	if err == nil && (next.DocumentDigest != documentDigest || next.ProjectMemoryRevision != targetState.ProjectMemoryRevision+1 ||
		next.ProjectMemoryCopyCount != targetState.ProjectMemoryCopyCount+1 || next.SpecVersion != targetState.SpecVersion ||
		next.PolicyEpoch != targetState.PolicyEpoch || next.Execution != targetState.Execution || !sameProjectMemoryWorkBudget(budgetBefore, next.WorkBudget)) {
		err = c.Fail(c.StoreIntegrityError, "project memory copy changed task authority or work budget")
	}
	if err == nil && s.projectMemoryCopyFault != nil {
		err = s.projectMemoryCopyFault("target-journal-committed")
	}
	if err == nil {
		view, err = readPrivacy(root)
	}
	if err == nil {
		active := metadata
		active.Status = "ACTIVE"
		err = appendPrivacy(root, view, privacyRecord{Type: "MEMORY_COPY_ACTIVE", MemoryCopy: &active})
	}
	if err == nil {
		view, err = readPrivacy(root)
	}
	closeErr := errors.Join(lock.Close(), root.Close())
	if err != nil || closeErr != nil {
		return empty, errors.Join(err, closeErr)
	}
	verifiedState, verifiedDoc, err := s.Load(ctx, targetTask)
	if err != nil {
		return empty, err
	}
	if verifiedState.DocumentDigest != documentDigest || verifiedState.ProjectMemoryCopyCount != next.ProjectMemoryCopyCount ||
		len(verifiedDoc.ProjectMemoryRefs) != len(targetDoc.ProjectMemoryRefs) || verifiedDoc.ProjectMemoryRefs[len(verifiedDoc.ProjectMemoryRefs)-1] != ref {
		return empty, c.Fail(c.StoreIntegrityError, "project memory copy failed current document and pin validation")
	}
	return copyReceipt(metadata, "COPIED"), nil
}

// preflightProjectMemoryCopyPolicy reads only the exact journal-bound task
// documents, so current effect and transfer-policy gates run before Load
// validates (and therefore opens) any referenced memory-event bodies. Its
// metadata is used only for this preflight; publication still uses fresh full
// Load results and repeats the same gates on those validated documents.
func (s *Session) preflightProjectMemoryCopyPolicy(ctx context.Context, sourceTask, targetTask string) error {
	loadMetadata := func(task string) (c.TaskState, Document, error) {
		var empty Document
		state, err := s.State(ctx, task)
		if err != nil {
			return state, empty, err
		}
		if err = s.contentAvailable(task); err != nil {
			return state, empty, err
		}
		if state.Deletion != nil || state.TaskID != task || state.TaskSeq < 1 || !c.ValidDigest(state.DocumentDigest) {
			return state, empty, c.Fail(c.PolicyDenied, "project memory copy task is unavailable")
		}
		raw, err := s.Archive.GetBytes(task, state.DocumentDigest)
		if err != nil {
			return state, empty, err
		}
		var doc Document
		if err = c.DecodeStrict(raw, &doc); err != nil || doc.SchemaVersion != 1 || doc.TaskID != task || doc.Spec.TaskID != task ||
			doc.Spec.Version != state.SpecVersion || doc.Baseline.TaskID != task || doc.Candidate.TaskID != task ||
			doc.Baseline.SnapshotDigest != state.BaselineDigest || doc.Candidate.SnapshotDigest != state.CandidateDigest {
			return state, empty, c.Fail(c.StoreIntegrityError, "project memory copy task document binding mismatch")
		}
		canonical, err := c.CanonicalV1(doc)
		if err != nil || !bytes.Equal(raw, canonical) {
			return state, empty, c.Fail(c.StoreIntegrityError, "project memory copy task document is not canonical")
		}
		if err = doc.Spec.Validate(); err != nil {
			return state, empty, err
		}
		if err = validateTaskConfig(doc); err != nil {
			return state, empty, err
		}
		return state, doc, nil
	}

	sourceState, sourceDoc, err := loadMetadata(sourceTask)
	if err != nil {
		return err
	}
	targetState, targetDoc, err := loadMetadata(targetTask)
	if err != nil {
		return err
	}
	if !projectMemoryMutationQuiescent(sourceState) || !projectMemoryMutationQuiescent(targetState) {
		return c.Fail(c.Conflict, "project memory copy requires quiescent source and target tasks")
	}
	if err = currentOperatorEffect(sourceDoc, "context.read"); err != nil {
		return err
	}
	if err = currentOperatorEffect(targetDoc, "context.read"); err != nil {
		return err
	}
	if err = currentOperatorEffect(targetDoc, "context.edit"); err != nil {
		return err
	}
	source, err := s.projectMemorySource(ctx, sourceState, sourceDoc)
	if err != nil {
		return err
	}
	target, err := s.projectMemorySource(ctx, targetState, targetDoc)
	if err != nil {
		return err
	}
	if source.ProjectDigest != target.ProjectDigest || source.PolicyDigest != target.PolicyDigest || source.PolicyEpoch != target.PolicyEpoch {
		return c.Fail(c.PolicyDenied, "project memory copy requires the same current project and retained policy epoch")
	}
	sourceEffective, err := projectMemoryEffectivePolicyDigest(sourceDoc)
	if err != nil {
		return err
	}
	targetEffective, err := projectMemoryEffectivePolicyDigest(targetDoc)
	if err != nil {
		return err
	}
	if sourceEffective != targetEffective {
		return c.Fail(c.PolicyDenied, "source and target effective privacy/provider policies differ")
	}
	return nil
}

func projectMemoryMutationQuiescent(state c.TaskState) bool {
	if state.Execution == c.Running || state.Execution == c.Verifying || state.Execution == c.Delivering || state.Execution == c.Scoping ||
		state.Execution == c.Pausing || state.Execution == c.Recovering || state.Deletion != nil || state.InputBarrier || len(state.PendingInputIDs) != 0 ||
		state.WorkBudget != nil && state.WorkBudget.EffectPending || projectMemoryHasUnsettledExposure(state) {
		return false
	}
	return true
}

func (s *Session) lockPrivacyForProjectMemoryCopy(ctx context.Context) (*os.Root, *os.File, privacyView, error) {
	var empty privacyView
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return nil, nil, empty, err
	}
	lock, err := privacyLock(ctx, root)
	if err != nil {
		root.Close()
		return nil, nil, empty, err
	}
	if err = onlyPrivacyOwner(root, s.privacyOwner.name); err != nil {
		return nil, nil, empty, errors.Join(err, lock.Close(), root.Close())
	}
	view, err := readPrivacy(root)
	if err != nil {
		return nil, nil, empty, errors.Join(err, lock.Close(), root.Close())
	}
	return root, lock, view, nil
}

func (s *Session) verifyProjectMemoryCopyOwnerScope(root *os.Root, scope privacyMemoryScope) error {
	expected, err := s.currentPrivacyMemoryScope()
	if err != nil {
		return err
	}
	if scope != expected {
		return c.Fail(c.StaleAuthority, "project memory copy requires the current same-owner store scope")
	}
	scopes, _, err := readPrivacyScopes(root)
	if err != nil {
		return err
	}
	for _, registered := range scopes {
		if privacyMemoryScopeFromOwner(registered) == expected {
			return nil
		}
	}
	return c.Fail(c.StaleAuthority, "project memory copy owner is not currently registered")
}

func (s *Session) currentPrivacyMemoryScope() (privacyMemoryScope, error) {
	directory, err := filepath.EvalSymlinks(s.directory)
	if err == nil {
		directory, err = filepath.Abs(directory)
	}
	if err != nil {
		return privacyMemoryScope{}, err
	}
	return privacyMemoryScope{InstanceID: s.instance.ID, Directory: directory, PhysicalRoot: s.instance.PhysicalRoot}, nil
}

func (s *Session) replayTaskLocked(ctx context.Context, task string) (c.TaskState, error) {
	states, err := s.Journal.Replay(ctx, task)
	if err != nil {
		return c.TaskState{}, err
	}
	state, ok := states[task]
	if !ok || state.TaskID != task {
		return c.TaskState{}, c.Fail(c.StaleBase, "project memory task is unavailable")
	}
	return state, nil
}

func (s *Session) activatePreparedProjectMemoryCopyLocked(ctx context.Context, root *os.Root, view privacyView, prepared privacyMemoryCopy, state c.TaskState) error {
	if state.TaskSeq != prepared.TargetTaskSequence || state.DocumentDigest != prepared.TargetDocument || state.Deletion != nil {
		return c.Fail(c.StaleBase, "prepared memory copy does not match the current target journal cursor")
	}
	raw, err := s.Archive.GetBytes(prepared.Target.TaskID, prepared.TargetDocument)
	if err != nil {
		return err
	}
	var doc Document
	if c.DecodeStrict(raw, &doc) != nil || doc.TaskID != prepared.Target.TaskID || doc.SchemaVersion != 1 ||
		c.HashBytes(raw) != prepared.TargetDocument {
		return c.Fail(c.StoreIntegrityError, "prepared memory copy target document is invalid")
	}
	canonical, err := c.CanonicalV1(doc)
	if err != nil || !bytes.Equal(raw, canonical) {
		return c.Fail(c.StoreIntegrityError, "prepared memory copy target document is not canonical")
	}
	found := false
	for _, ref := range doc.ProjectMemoryRefs {
		if ref.ID == prepared.CopyID && ref.Digest == prepared.CopyDigest && ref.Type == "PROJECT_MEMORY_COPY" {
			found = true
			break
		}
	}
	if !found || state.ProjectMemoryCopyCount < 1 || state.ProjectMemoryRevision < 1 {
		return c.Fail(c.StoreIntegrityError, "prepared memory copy journal and target document disagree")
	}
	prior, err := s.Journal.ReplayAt(ctx, prepared.Target.TaskID, prepared.Target.TaskSeq)
	if err != nil || prior.DocumentDigest != prepared.Target.DocumentDigest || prior.TaskSeq != prepared.Target.TaskSeq ||
		state.ProjectMemoryCopyCount != prior.ProjectMemoryCopyCount+1 || state.ProjectMemoryRevision != prior.ProjectMemoryRevision+1 {
		return c.Fail(c.StoreIntegrityError, "prepared memory copy counter does not follow its exact target cursor")
	}
	active := prepared
	active.Status = "ACTIVE"
	if err = appendPrivacy(root, view, privacyRecord{Type: "MEMORY_COPY_ACTIVE", MemoryCopy: &active}); err != nil {
		return err
	}
	return nil
}

func (s *Session) closePrivacyCopyAndReturn(root *os.Root, lock *os.File, receipt ProjectMemoryCopyReceipt, err error) (ProjectMemoryCopyReceipt, error) {
	return receipt, errors.Join(err, lock.Close(), root.Close())
}

func copyReceipt(copy privacyMemoryCopy, status string) ProjectMemoryCopyReceipt {
	return ProjectMemoryCopyReceipt{SchemaVersion: 1, SourceTask: copy.Origin.TaskID, TargetTask: copy.Target.TaskID,
		SourceItemID: copy.SourceItemID, SourceItemDigest: copy.SourceItemDigest, CopyID: copy.CopyID,
		CopyDigest: copy.CopyDigest, RequestDigest: copy.RequestDigest, Status: status, AdvisoryOnly: true}
}
