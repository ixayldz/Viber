package agent

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

type DeletionPlan struct {
	StagedAttempts    []DeletionAttempt  `json:"staged_attempts,omitempty"`
	OwnerScopesDigest string             `json:"owner_scopes_digest"`
	Stores            []DeletionStore    `json:"restored_stores"`
	SchemaVersion     int                `json:"schema_version"`
	TaskID            string             `json:"task_id"`
	TaskSequence      int64              `json:"task_sequence"`
	DocumentDigest    string             `json:"document_digest"`
	Store             store.SnapshotInfo `json:"store"`
	AuthorityDigest   string             `json:"authority_digest"`
	RegistrySequence  int64              `json:"registry_sequence"`
	RegistryTail      string             `json:"registry_tail"`
	Watermark         int64              `json:"deletion_watermark"`
	Scope             string             `json:"scope"`
	Objects           []artifact.Object  `json:"objects"`
	Copies            []ManagedCopy      `json:"managed_copies"`
	ObjectsDigest     string             `json:"objects_digest"`
	Digest            string             `json:"plan_digest"`
}
type DeletionCommand struct {
	CommandID string       `json:"command_id"`
	Plan      DeletionPlan `json:"plan"`
}
type DeletionResult struct {
	StagedAttempts int    `json:"staged_attempts,omitempty"`
	RestoredStores int    `json:"restored_stores"`
	SchemaVersion  int    `json:"schema_version"`
	TaskID         string `json:"task_id"`
	CommandID      string `json:"command_id"`
	PlanDigest     string `json:"plan_digest"`
	Watermark      int64  `json:"deletion_watermark"`
	Status         string `json:"status"`
	LocalObjects   int    `json:"local_objects"`
	ManagedCopies  int    `json:"managed_copies"`
	ExternalCopies string `json:"external_copies"`
	MediaErasure   string `json:"physical_media_erasure"`
}

func deletionDigest(plan DeletionPlan) string {
	plan.Digest = ""
	digest, _ := c.Digest(plan)
	return digest
}
func deletionObjectsDigest(plan DeletionPlan) string {
	digest, _ := c.Digest(struct {
		Attempts []DeletionAttempt `json:"staged_attempts,omitempty"`
		Stores   []DeletionStore   `json:"stores"`
		Objects  []artifact.Object `json:"objects"`
		Copies   []ManagedCopy     `json:"copies"`
	}{plan.StagedAttempts, plan.Stores, plan.Objects, plan.Copies})
	return digest
}
func validateDeletionCommand(command DeletionCommand) error {
	plan := command.Plan
	if len(plan.StagedAttempts) > 128 {
		return c.Fail(c.InvalidArgument, "staged attempt inventory quota exceeded")
	}
	previousAttempt := ""
	for _, attempt := range plan.StagedAttempts {
		if attempt.Allocation.validate() != nil || attempt.Allocation.ParentTask != plan.TaskID || attemptKey(attempt.Allocation) <= previousAttempt || len(attempt.Objects) > 32768 {
			return c.Fail(c.InvalidArgument, "invalid staged attempt deletion binding")
		}
		previousAttempt = attemptKey(attempt.Allocation)
		previousObject := ""
		total := int64(0)
		for _, object := range attempt.Objects {
			if !artifact.TaskContentPath(attempt.Allocation.ChildTask, object.Path) || object.Path <= previousObject || !c.ValidDigest(object.Digest) || object.Size < 0 || object.Size > 64<<20 || object.Size > backupMaxBytes-total {
				return c.Fail(c.InvalidArgument, "invalid staged attempt object inventory")
			}
			previousObject = object.Path
			total += object.Size
		}
	}
	if !c.ValidDigest(plan.OwnerScopesDigest) || len(plan.Stores) > 128 {
		return c.Fail(c.InvalidArgument, "bounded managed store binding required")
	}
	previousStore := ""
	for _, bound := range plan.Stores {
		if !validPrivacyScope(bound.Scope) || bound.Scope.Directory <= previousStore || bound.TaskSequence < 1 || !c.ValidDigest(bound.DocumentDigest) || len(bound.Objects) > 32768 {
			return c.Fail(c.InvalidArgument, "invalid managed restored store plan")
		}
		previousStore = bound.Scope.Directory
		previousObject := ""
		total := int64(0)
		for _, object := range bound.Objects {
			if !artifact.TaskContentPath(plan.TaskID, object.Path) || object.Path <= previousObject || !c.ValidDigest(object.Digest) || object.Size < 0 || object.Size > 64<<20 || object.Size > backupMaxBytes-total {
				return c.Fail(c.InvalidArgument, "invalid restored store object set")
			}
			previousObject = object.Path
			total += object.Size
		}
	}
	if command.CommandID == "" || len(command.CommandID) > 128 || !utf8.ValidString(command.CommandID) || strings.ContainsAny(command.CommandID, "\x00\r\n") || plan.SchemaVersion != 1 || plan.TaskSequence < 1 || !c.ValidDigest(plan.DocumentDigest) || plan.Digest != deletionDigest(plan) || !c.ValidDigest(plan.Digest) || plan.ObjectsDigest != deletionObjectsDigest(plan) || !c.ValidDigest(plan.AuthorityDigest) || plan.RegistrySequence < 0 || plan.RegistrySequence == 0 && plan.RegistryTail != "" || plan.RegistrySequence > 0 && !c.ValidDigest(plan.RegistryTail) || plan.Watermark < 1 || plan.Scope != "TASK_CONTENT" || len(plan.Objects) > 32768 || len(plan.Copies) > 1024 {
		return c.Fail(c.InvalidArgument, "bounded exact deletion plan and stable command ID required")
	}
	if _, err := artifact.ScopedBlobPath(plan.TaskID, c.HashBytes(nil)); err != nil {
		return err
	}
	previous := ""
	total := int64(0)
	for _, object := range plan.Objects {
		if !artifact.TaskContentPath(plan.TaskID, object.Path) || object.Path <= previous || !c.ValidDigest(object.Digest) || object.Size < 0 || object.Size > 64<<20 || object.Size > backupMaxBytes-total {
			return c.Fail(c.InvalidArgument, "invalid deletion object set")
		}
		previous = object.Path
		total += object.Size
	}
	previous = ""
	for _, copy := range plan.Copies {
		if validateManagedCopy(copy) != nil || copy.Directory <= previous {
			return c.Fail(c.InvalidArgument, "invalid deletion copy set")
		}
		previous = copy.Directory
	}
	return nil
}
func (s *Session) deletionQuiescent(ctx context.Context, task string) (c.TaskState, error) {
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return state, err
	}
	if state.Execution != c.Terminated || doc.Pending != nil || doc.UnknownEffect || doc.Budget.ReservedInput != 0 || doc.Budget.ReservedOutput != 0 {
		return state, c.Fail(c.PolicyDenied, "deletion requires terminal task with no pending or UNKNOWN effect")
	}
	if state.Tokens != nil {
		for _, r := range state.Tokens.Reservations {
			if r.Status != "SETTLED" {
				return state, c.Fail(c.UnknownOutcome, "unsettled model risk pins content")
			}
		}
	}
	if state.Resources != nil {
		for _, r := range state.Resources.Reservations {
			if r.Status != "SETTLED" {
				return state, c.Fail(c.UnknownOutcome, "unsettled native risk pins content")
			}
		}
	}
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return state, err
	}
	for _, other := range states {
		if other.TaskID == task || other.Deletion != nil {
			continue
		}
		_, child, err := s.Load(ctx, other.TaskID)
		if err != nil {
			return state, err
		}
		if child.AttemptOrigin != nil && child.AttemptOrigin.ParentTask == task {
			return state, c.Fail(c.PolicyDenied, "retained derived task pins its parent; delete descendants first")
		}
	}
	return state, nil
}
func (s *Session) deletionPlanLocked(ctx context.Context, task string) (DeletionPlan, error) {
	plan := DeletionPlan{SchemaVersion: 1, TaskID: task, Scope: "TASK_CONTENT", Objects: []artifact.Object{}, Copies: []ManagedCopy{}, Stores: []DeletionStore{}}
	state, err := s.deletionQuiescent(ctx, task)
	if err != nil {
		return plan, err
	}
	if err = s.ensurePrivacy(ctx); err != nil {
		return plan, err
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return plan, err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return plan, err
	}
	defer lock.Close()
	if err = onlyPrivacyOwner(root, s.privacyOwner.name); err != nil {
		return plan, err
	}
	view, err := readPrivacy(root)
	if err != nil {
		return plan, err
	}
	if _, exists := view.Deletions[task]; exists {
		return plan, c.Fail(c.PolicyDenied, "deletion intent already exists; retry its original command")
	}
	plan.StagedAttempts, err = s.stagedAttemptPlans(ctx, task, view)
	if err != nil {
		return plan, err
	}
	plan.Store, err = s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return plan, err
	}
	plan.TaskSequence, plan.DocumentDigest = state.TaskSeq, state.DocumentDigest
	plan.AuthorityDigest, _ = c.Digest(s.privacy)
	plan.RegistrySequence, plan.RegistryTail, plan.Watermark = view.Sequence, view.Tail, view.Watermark+1
	scopes, scopesDigest, err := readPrivacyScopes(root)
	if err != nil {
		return plan, err
	}
	plan.OwnerScopesDigest = scopesDigest
	for _, scope := range scopes {
		if scope.PhysicalRoot == s.instance.PhysicalRoot {
			continue
		}
		bound, found, err := deletionStorePlan(ctx, scope, *s.privacy, task, view.Deletions)
		if err != nil {
			return plan, err
		}
		if found {
			plan.Stores = append(plan.Stores, bound)
		}
	}
	sort.Slice(plan.Stores, func(i, j int) bool { return plan.Stores[i].Scope.Directory < plan.Stores[j].Scope.Directory })
	plan.Objects, err = s.Archive.TaskInventory(ctx, task)
	if err != nil {
		return plan, err
	}
	for _, copy := range view.Copies {
		if sort.SearchStrings(copy.Tasks, task) < len(copy.Tasks) && copy.Tasks[sort.SearchStrings(copy.Tasks, task)] == task {
			bound, err := captureManagedAllocation(ctx, copy)
			if err != nil {
				return plan, err
			}
			plan.Copies = append(plan.Copies, bound)
		}
	}
	sort.Slice(plan.Copies, func(i, j int) bool { return plan.Copies[i].Directory < plan.Copies[j].Directory })
	// Validate exact registered bytes now; mutations after preview remain failures.
	for _, copy := range plan.Copies {
		if err = checkManagedCopy(copy, true); err != nil {
			return plan, err
		}
	}
	plan.ObjectsDigest = deletionObjectsDigest(plan)
	plan.Digest = deletionDigest(plan)
	return plan, validateDeletionCommand(DeletionCommand{"preview", plan})
}
func (s *Session) DeletionPreview(ctx context.Context, task string) (DeletionPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletionPlanLocked(ctx, task)
}
func checkManagedCopy(copy ManagedCopy, permitAbsent bool) error {
	root, err := os.OpenRoot(copy.Directory)
	if errors.Is(err, os.ErrNotExist) && permitAbsent {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	id, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return err
	}
	if id != copy.PhysicalRoot {
		return c.Fail(c.StaleBase, "managed copy root replaced; cannot purge foreign directory")
	}
	inventory := copy
	inventory.Files = []BackupFile{}
	inventory, err = captureManagedAllocation(context.Background(), inventory)
	if err != nil {
		return err
	}
	expected := make(map[string]BackupFile, len(copy.Files))
	for _, f := range copy.Files {
		expected[f.Path] = f
	}
	for _, actual := range inventory.Files {
		f, found := expected[actual.Path]
		if !found || f != actual {
			return c.Fail(c.StaleBase, "new or changed managed-copy content prevents PURGED receipt")
		}
	}
	for _, f := range copy.Files {
		raw, err := fileguard.ReadRegular(root, f.Path, backupMaxBytes)
		if errors.Is(err, os.ErrNotExist) && permitAbsent {
			continue
		}
		if err != nil {
			return err
		}
		if int64(len(raw)) != f.Size || c.HashBytes(raw) != f.Digest {
			return c.Fail(c.StaleBase, "managed copy changed; deletion remains PENDING")
		}
	}
	if !permitAbsent && len(inventory.Files) != len(copy.Files) {
		return c.Fail(c.StaleBase, "managed copy content is missing")
	}
	return nil
}
func purgeManagedCopy(copy ManagedCopy) error {
	if err := checkManagedCopy(copy, true); err != nil {
		return err
	}
	root, err := os.OpenRoot(copy.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	for _, f := range copy.Files {
		if _, err := fileguard.RemoveBound(root, f.Path, f.Digest, f.Size, backupMaxBytes); err != nil {
			return err
		}
	}
	copy.Files = []BackupFile{}
	return checkManagedCopy(copy, true)
}

// DeleteContent publishes a separate authority intent before any byte removal.
// All retries use that exact plan; no new effect or task success is authorized.
func (s *Session) DeleteContent(ctx context.Context, command DeletionCommand) (DeletionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	plan := command.Plan
	result := DeletionResult{SchemaVersion: 1, TaskID: plan.TaskID, CommandID: command.CommandID, PlanDigest: plan.Digest, Watermark: plan.Watermark, Status: "PENDING", LocalObjects: len(plan.Objects), ManagedCopies: len(plan.Copies), RestoredStores: len(plan.Stores), StagedAttempts: len(plan.StagedAttempts), ExternalCopies: "UNMANAGED_OR_PROVIDER_COPIES_NOT_ERASED", MediaErasure: "NO_PHYSICAL_MEDIA_ERASURE_GUARANTEE"}
	if err := validateDeletionCommand(command); err != nil {
		return result, err
	}
	if err := s.Journal.Writable(); err != nil {
		return result, err
	}
	if err := s.ensurePrivacy(ctx); err != nil {
		return result, err
	}
	authorityDigest, _ := c.Digest(s.privacy)
	if authorityDigest != plan.AuthorityDigest {
		return result, c.Fail(c.StaleAuthority, "deletion plan belongs to a different authority")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return result, err
	}
	// Determine retry identity under the registry lock, without recursive reads.
	lock, err := privacyLock(ctx, root)
	if err != nil {
		root.Close()
		return result, err
	}
	view, err := readPrivacy(root)
	if err == nil {
		err = onlyPrivacyOwner(root, s.privacyOwner.name)
	}
	prior, retry := view.Deletions[plan.TaskID]
	if err == nil && retry {
		expected, _ := c.Digest(prior)
		actual, _ := c.Digest(command)
		if expected != actual {
			err = c.Fail(c.CommandIDConflict, "deletion retry must preserve original command and plan")
		}
	}
	err = errors.Join(err, lock.Close(), root.Close())
	if err != nil {
		return result, err
	}
	if !retry {
		ids := []string{command.CommandID, "privacy-purge-" + c.HashBytes([]byte(command.CommandID))}
		for _, id := range ids {
			if _, _, _, found, err := s.Journal.CommandReceipt(ctx, id); err != nil {
				return result, err
			} else if found {
				return result, c.Fail(c.CommandIDConflict, "deletion command namespace already used; no intent was published")
			}
		}
		for _, bound := range plan.Stores {
			journal, archive, err := openDeletionStore(ctx, bound.Scope, *s.privacy)
			if err != nil {
				return result, err
			}
			id := "privacy-scope-" + c.HashBytes([]byte(command.CommandID+":"+bound.Scope.PhysicalRoot))
			for _, internalID := range []string{id, id + "-purged"} {
				if _, _, _, found, lookupErr := journal.CommandReceipt(ctx, internalID); lookupErr != nil || found {
					archive.Close()
					journal.Close()
					if lookupErr != nil {
						return result, lookupErr
					}
					return result, c.Fail(c.CommandIDConflict, "restored deletion namespace already used")
				}
			}
			if err = errors.Join(archive.Close(), journal.Close()); err != nil {
				return result, err
			}
		}
		fresh, err := s.deletionPlanLocked(ctx, plan.TaskID)
		if err != nil {
			return result, err
		}
		if fresh.Digest != plan.Digest {
			return result, c.Fail(c.StaleBase, "deletion preview changed; refresh before authorization")
		}
		root, err = openPrivacyRoot(*s.privacy)
		if err != nil {
			return result, err
		}
		lock, err = privacyLock(ctx, root)
		if err != nil {
			root.Close()
			return result, err
		}
		view, err = readPrivacy(root)
		if err == nil {
			err = onlyPrivacyOwner(root, s.privacyOwner.name)
		}
		if err == nil && (view.Sequence != plan.RegistrySequence || view.Tail != plan.RegistryTail || view.Watermark+1 != plan.Watermark) {
			err = c.Fail(c.StaleBase, "privacy journal changed before deletion")
		}
		if err == nil {
			_, digest, scopeErr := readPrivacyScopes(root)
			err = scopeErr
			if err == nil && digest != plan.OwnerScopesDigest {
				err = c.Fail(c.StaleBase, "managed owner scopes changed before deletion")
			}
		}
		if err == nil {
			err = appendPrivacy(root, view, privacyRecord{Type: "DELETE_INTENT", Command: &command})
		}
		err = errors.Join(err, lock.Close(), root.Close())
		if err != nil {
			return result, err
		}
	}
	if s.retrievalCache != nil {
		s.retrievalCache.InvalidateScope(c.HashBytes([]byte(plan.TaskID)))
	}
	// Hold the admission lock through every physical purge. New family owners and
	// restore operations cannot register while these exact scopes are changing.
	purgeRoot, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return result, err
	}
	defer purgeRoot.Close()
	purgeLock, err := privacyLock(ctx, purgeRoot)
	if err != nil {
		return result, err
	}
	defer purgeLock.Close()
	if err = onlyPrivacyOwner(purgeRoot, s.privacyOwner.name); err != nil {
		return result, err
	}
	states, err := s.Journal.Replay(ctx, plan.TaskID)
	if err != nil {
		return result, err
	}
	state := states[plan.TaskID]
	tombstone := c.TaskDeletion{SchemaVersion: 1, PlanDigest: plan.Digest, OriginalDocument: plan.DocumentDigest, ObjectsDigest: plan.ObjectsDigest, Scope: plan.Scope, Status: "PENDING", Watermark: plan.Watermark}
	if state.Deletion == nil {
		state, err = s.Journal.Execute(ctx, store.Command{ID: command.CommandID, TaskID: plan.TaskID, Actor: "user", ExpectedTaskSeq: plan.TaskSequence, Type: "TaskContentDeleted", Payload: c.EventPayload{Deletion: &tombstone, Reason: plan.Digest}})
		if err != nil {
			return result, err
		}
	}
	expected := tombstone
	if state.Deletion != nil && state.Deletion.Status == "PURGED" {
		expected.Status = "PURGED"
	}
	if state.Deletion == nil || *state.Deletion != expected {
		return result, c.Fail(c.StoreIntegrityError, "deletion journal intent mismatch")
	}
	// Verify remaining paths before side effects. Missing paths are only valid
	// because the external intent and journal tombstone are already durable.
	for _, copy := range plan.Copies {
		if err = checkManagedCopy(copy, true); err != nil {
			return result, err
		}
	}
	for _, object := range plan.Objects {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if _, err = s.Archive.RemoveTaskContent(plan.TaskID, object); err != nil {
			return result, err
		}
	}
	for _, copy := range plan.Copies {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if err = purgeManagedCopy(copy); err != nil {
			return result, err
		}
	}
	for _, bound := range plan.Stores {
		if err = purgeDeletionStore(ctx, bound, *s.privacy, command); err != nil {
			return result, err
		}
	}
	for _, attempt := range plan.StagedAttempts {
		if err = s.purgeStagedAttempt(ctx, attempt); err != nil {
			return result, err
		}
	}
	remaining, err := s.Archive.TaskInventory(ctx, plan.TaskID)
	if err != nil {
		return result, err
	}
	if len(remaining) != 0 {
		return result, c.Fail(c.Conflict, "new or unplanned content prevents PURGED receipt")
	}
	if state.Deletion.Status != "PURGED" {
		tombstone.Status = "PURGED"
		if _, err = s.Journal.Execute(ctx, store.Command{ID: "privacy-purge-" + c.HashBytes([]byte(command.CommandID)), TaskID: plan.TaskID, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "TaskDeletionPurged", Payload: c.EventPayload{Deletion: &tombstone, Reason: plan.Digest}}); err != nil {
			return result, err
		}
	}
	result.Status = "PURGED_MANAGED_LOCAL_CONTENT"
	return result, nil
}
