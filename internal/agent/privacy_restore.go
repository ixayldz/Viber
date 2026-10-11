package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

type privacyRestoreBindingV1 struct {
	SchemaVersion        int    `json:"schema_version"`
	AuthorityDigest      string `json:"authority_digest"`
	Source               string `json:"source"`
	SourcePhysicalRoot   string `json:"source_physical_root"`
	BackupDigest         string `json:"backup_digest"`
	SourceWatermark      int64  `json:"source_watermark"`
	Stage                string `json:"stage"`
	Target               string `json:"target"`
	InstanceID           string `json:"instance_id"`
	PhysicalRoot         string `json:"physical_root"`
	AnchorDigest         string `json:"anchor_digest,omitempty"`
	PreviousAnchorDigest string `json:"previous_anchor_digest,omitempty"`
	RestoreBinding       string `json:"previous_restore_binding,omitempty"`
	CurrentWatermark     int64  `json:"current_watermark"`
}

func validPrivacyInstanceID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			if r < 'a' || r > 'f' {
				return false
			}
		}
	}
	return true
}

func privacyRestoreBindingDigest(authority PrivacyAuthority, copy ManagedCopy) (string, error) {
	authorityDigest, err := c.Digest(authority)
	if err != nil {
		return "", err
	}
	value := privacyRestoreBindingV1{
		SchemaVersion: 1, AuthorityDigest: authorityDigest,
		Source: copy.RestoreSource, SourcePhysicalRoot: copy.RestoreSourcePhysicalRoot,
		BackupDigest: copy.RestoreBackupDigest, SourceWatermark: copy.RestoreWatermark,
		Stage: copy.RestoreStageDirectory, Target: copy.RestoreTarget,
		InstanceID: copy.RestoreInstanceID, PhysicalRoot: copy.PhysicalRoot,
		AnchorDigest: copy.RestoreAnchor, PreviousAnchorDigest: copy.RestorePreviousAnchor,
		RestoreBinding: copy.RestorePreviousBinding, CurrentWatermark: copy.RestoreCurrentWatermark,
	}
	raw, err := c.CanonicalV1(value)
	if err != nil {
		return "", err
	}
	return c.HashBytes(raw), nil
}

func canonicalExistingDirectory(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func findManagedCopy(view privacyView, directory string) (ManagedCopy, bool) {
	for _, copy := range view.Copies {
		if filepath.Clean(copy.Directory) == filepath.Clean(directory) {
			return copy, true
		}
	}
	return ManagedCopy{}, false
}

func privacyRestoreSource(ctx context.Context, root *os.Root, authority PrivacyAuthority, view privacyView, copy ManagedCopy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if copy.RestoreTarget == "" || copy.RestoreCurrentWatermark != view.Watermark || copy.RestoreWatermark != view.Watermark {
		return c.Fail(c.PolicyDenied, "restore source no longer matches the current deletion watermark")
	}
	canonical, err := canonicalExistingDirectory(copy.RestoreSource)
	if err != nil || canonical != copy.RestoreSource {
		return c.Fail(c.StaleAuthority, "registered restore source path changed")
	}
	sourceRoot, err := os.OpenRoot(copy.RestoreSource)
	if err != nil {
		return err
	}
	physical, err := fileguard.DirectoryIdentity(sourceRoot)
	if err != nil || physical != copy.RestoreSourcePhysicalRoot {
		sourceRoot.Close()
		return c.Fail(c.StaleAuthority, "registered restore source root changed")
	}
	raw, err := fileguard.ReadRegular(sourceRoot, "backup.json", 8<<20)
	closeErr := sourceRoot.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if c.HashBytes(raw) != copy.RestoreBackupDigest {
		return c.Fail(c.StaleAuthority, "registered restore backup manifest changed")
	}
	var manifest BackupManifest
	if c.DecodeStrict(raw, &manifest) != nil || validateBackupManifest(manifest) != nil || manifest.SchemaVersion != 2 || manifest.PrivacyAuthority == nil {
		return c.Fail(c.StoreIntegrityError, "restore backup manifest is invalid")
	}
	expectedAuthority, _ := c.CanonicalV1(authority)
	actualAuthority, _ := c.CanonicalV1(*manifest.PrivacyAuthority)
	if string(actualAuthority) != string(expectedAuthority) || manifest.DeletionWatermark != copy.RestoreWatermark {
		return c.Fail(c.StaleAuthority, "restore backup no longer belongs to the current family watermark")
	}
	registered := false
	for _, candidate := range view.Copies {
		if candidate.Kind != "BACKUP" || candidate.Directory != copy.RestoreSource || candidate.PhysicalRoot != copy.RestoreSourcePhysicalRoot || !sameManagedTasks(candidate.Tasks, copy.Tasks) {
			continue
		}
		for _, file := range candidate.Files {
			if file.Path == "backup.json" && file.Digest == copy.RestoreBackupDigest && file.Size == int64(len(raw)) {
				registered = true
				break
			}
		}
	}
	if !registered {
		return c.Fail(c.PolicyDenied, "restore source is not the exact current registered backup")
	}
	for _, task := range copy.Tasks {
		if _, deleted := view.Deletions[task]; deleted {
			return c.Fail(c.PolicyDenied, "deleted task content cannot be rebound from a restore stage")
		}
	}
	if root == nil {
		return c.Fail(c.StoreIntegrityError, "privacy restore authority root unavailable")
	}
	return nil
}

func readRestoreSourcePin(ctx context.Context, directory string, authority PrivacyAuthority) (*store.PrivacyCheckpointPin, error) {
	raw, pin, err := store.ReadPrivacyCheckpointOwnerAt(ctx, directory)
	if err != nil {
		return nil, err
	}
	expected, _ := c.CanonicalV1(authority)
	if string(raw) != string(expected) {
		return nil, c.Fail(c.StaleAuthority, "restored owner store authority differs from its registered source")
	}
	return pin, nil
}

func readRestoreAnchor(root *os.Root, digest string) (privacyCheckpointAnchor, error) {
	var anchor privacyCheckpointAnchor
	raw, err := fileguard.ReadRegular(root, privacyCheckpointAnchorHistoryPath(digest), 4096)
	if err != nil {
		return anchor, err
	}
	if c.HashBytes(raw) != digest || c.DecodeStrict(raw, &anchor) != nil || validatePrivacyCheckpointAnchor(anchor) != nil {
		return anchor, c.Fail(c.StoreIntegrityError, "restore checkpoint anchor history is invalid")
	}
	canonical, err := c.CanonicalV1(anchor)
	if err != nil || string(canonical) != string(raw) {
		return anchor, c.Fail(c.StoreIntegrityError, "restore checkpoint anchor is noncanonical")
	}
	return anchor, nil
}

func restoreAnchorIsAncestor(root *os.Root, current privacyCheckpointAnchor, currentDigest, wanted string) (privacyCheckpointAnchor, bool, error) {
	anchor, digest := current, currentDigest
	for steps := int64(0); steps < privacyCheckpointAnchorLimit; steps++ {
		if digest == wanted {
			return anchor, true, nil
		}
		if anchor.Generation <= 1 || anchor.PreviousAnchor == "" {
			return privacyCheckpointAnchor{}, false, nil
		}
		previous, err := readRestoreAnchor(root, anchor.PreviousAnchor)
		if err != nil {
			return privacyCheckpointAnchor{}, false, err
		}
		if previous.AuthorityDigest != anchor.AuthorityDigest || previous.Generation+1 != anchor.Generation || previous.Sequence > anchor.Sequence || previous.Watermark > anchor.Watermark {
			return privacyCheckpointAnchor{}, false, c.Fail(c.StoreIntegrityError, "restore checkpoint anchor lineage regressed")
		}
		digest, anchor = anchor.PreviousAnchor, previous
	}
	return privacyCheckpointAnchor{}, false, c.Fail(c.BudgetLimitReached, "restore checkpoint anchor lineage exceeds its bound")
}

func validateSourceCheckpointPin(ctx context.Context, root *os.Root, authority PrivacyAuthority, pin *store.PrivacyCheckpointPin, current privacyCheckpointAnchor, currentDigest string) error {
	if pin == nil {
		return nil
	}
	authorityDigest, _ := c.Digest(authority)
	if pin.AuthorityDigest != authorityDigest || pin.Phase != "ACTIVE" {
		return c.Fail(c.StaleAuthority, "restore source checkpoint pin has another authority")
	}
	anchor, ancestor, err := restoreAnchorIsAncestor(root, current, currentDigest, pin.AnchorDigest)
	if err != nil || !ancestor || anchor.Generation != pin.Generation || anchor.Sequence != pin.Sequence || anchor.Tail != pin.Tail || anchor.Watermark != pin.Watermark {
		return c.Fail(c.StaleAuthority, "restore source checkpoint pin is not on the current anchor lineage")
	}
	checkpointRaw, err := fileguard.ReadRegular(root, privacyCheckpointDataPath(anchor.CheckpointDigest), privacyCheckpointFileLimit)
	if err != nil || c.HashBytes(checkpointRaw) != anchor.CheckpointDigest {
		return c.Fail(c.StoreIntegrityError, "restore source checkpoint projection is missing")
	}
	var checkpoint privacyCheckpoint
	if c.DecodeStrict(checkpointRaw, &checkpoint) != nil || validatePrivacyCheckpoint(checkpoint, authority) != nil {
		return c.Fail(c.StoreIntegrityError, "restore source checkpoint projection is invalid")
	}
	if checkpoint.Generation != anchor.Generation || checkpoint.Sequence != anchor.Sequence || checkpoint.Tail != anchor.Tail ||
		checkpoint.Watermark != anchor.Watermark || checkpoint.ScopeDigest != anchor.ScopeDigest || len(checkpoint.Scopes) != anchor.ScopeCount {
		return c.Fail(c.StoreIntegrityError, "restore source checkpoint projection differs from its anchor")
	}
	if anchor.Generation == 1 {
		if checkpoint.BaseSequence != 0 || checkpoint.BaseTail != "" || anchor.PreviousAnchor != "" {
			return c.Fail(c.StoreIntegrityError, "restore source checkpoint genesis has an invalid base")
		}
	} else {
		previous, previousErr := readRestoreAnchor(root, anchor.PreviousAnchor)
		if previousErr != nil || previous.Generation+1 != anchor.Generation || previous.AuthorityDigest != anchor.AuthorityDigest ||
			previous.Sequence > anchor.Sequence || previous.Watermark > anchor.Watermark ||
			checkpoint.BaseSequence != previous.Sequence || checkpoint.BaseTail != previous.Tail {
			return c.Fail(c.StoreIntegrityError, "restore source checkpoint projection has an invalid prior-anchor base")
		}
	}
	for _, scope := range checkpoint.Scopes {
		// The checkpoint projection is captured before its new anchor is
		// published, so its owner witness necessarily names an earlier anchor.
		// For a restored owner, prove that the recorded binding anchor is in
		// this checkpoint's prior lineage instead of comparing it to the new
		// pin's anchor digest (which would be a self-reference).
		if !validPrivacyScope(scope) || scope.InstanceID != pin.InstanceID || scope.PhysicalRoot != pin.PhysicalRoot || scope.RestoreBinding != pin.RestoreBinding {
			continue
		}
		if scope.CheckpointAnchor != "" {
			if anchor.Generation <= 1 {
				continue
			}
			previous, previousErr := readRestoreAnchor(root, anchor.PreviousAnchor)
			if previousErr != nil {
				return c.Fail(c.StoreIntegrityError, "restore source checkpoint lost its prior owner anchor")
			}
			_, priorBinding, lineageErr := restoreAnchorIsAncestor(root, previous, anchor.PreviousAnchor, scope.CheckpointAnchor)
			if lineageErr != nil {
				return lineageErr
			}
			if !priorBinding {
				continue
			}
		}
		if expected := expectedPrivacyCheckpointPin(anchor, pin.AnchorDigest, authorityDigest, scope, "ACTIVE"); expected == *pin {
			return nil
		}
	}
	_ = ctx
	return c.Fail(c.StaleAuthority, "restore source pin has no matching checkpoint owner witness")
}

func restorePinMatches(copy ManagedCopy, anchor privacyCheckpointAnchor, anchorDigest, authorityDigest, binding string, pin *store.PrivacyCheckpointPin) bool {
	if pin == nil {
		return false
	}
	scope := privacyScope{InstanceID: copy.RestoreInstanceID, Directory: copy.Directory, PhysicalRoot: copy.PhysicalRoot, CheckpointAnchor: anchorDigest, RestoreBinding: binding}
	expected := expectedPrivacyCheckpointPin(anchor, anchorDigest, authorityDigest, scope, "ACTIVE")
	return *pin == expected
}

func updateRestoreCopyLocked(root *os.Root, view privacyView, next ManagedCopy) error {
	if err := validateManagedCopy(next); err != nil {
		return err
	}
	if err := appendPrivacy(root, view, privacyRecord{Type: "MANAGED_COPY", Copy: &next}); err != nil {
		return err
	}
	return nil
}

// privacyCheckpointHasUnfinishedRestore prevents a later checkpoint from
// overtaking a stage whose exact source owner pin has not yet been rebound and
// published. Successful stage->target renames leave an immutable historical
// stage allocation; that entry is complete only when its matching target is
// the exact registered restore owner.
func privacyCheckpointHasUnfinishedRestore(view privacyView, scopes []privacyScope) bool {
	for _, candidate := range view.Copies {
		if candidate.Kind != "RESTORE_STAGE" || restoreCopyIsCurrentOwner(candidate, scopes) {
			continue
		}
		if candidate.Directory != candidate.RestoreStageDirectory || candidate.RestoreTarget == "" {
			return true
		}
		completed := false
		for _, target := range view.Copies {
			if target.Kind == "RESTORE_STAGE" && target.Directory == candidate.RestoreTarget &&
				target.PhysicalRoot == candidate.PhysicalRoot && sameManagedTasks(target.Tasks, candidate.Tasks) &&
				sameRestoreProvenance(target, candidate) && restoreCopyIsCurrentOwner(target, scopes) {
				completed = true
				break
			}
		}
		if !completed {
			return true
		}
	}
	return false
}

func initializePrivacyRestoreStage(ctx context.Context, authority PrivacyAuthority, manifest BackupManifest, manifestRaw []byte, backup, stage, target string, instance RuntimeInstance) error {
	canonicalStage, err := canonicalExistingDirectory(stage)
	if err != nil {
		return err
	}
	canonicalBackup, err := canonicalExistingDirectory(backup)
	if err != nil {
		return err
	}
	canonicalTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return err
	}
	if !fileguard.Disjoint(canonicalStage, authority.Directory) || !fileguard.Disjoint(canonicalStage, canonicalBackup) || !fileguard.Disjoint(canonicalStage, canonicalTarget) {
		return c.Fail(c.PolicyDenied, "restore stage overlaps its authority or source backup")
	}
	sourceRoot, err := os.OpenRoot(canonicalBackup)
	if err != nil {
		return err
	}
	sourcePhysical, err := fileguard.DirectoryIdentity(sourceRoot)
	closeErr := sourceRoot.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	root, err := openPrivacyRoot(authority)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return err
	}
	base, found := findManagedCopy(view, canonicalStage)
	if !found || base.Kind != "RESTORE_STAGE" || base.PhysicalRoot != instance.PhysicalRoot || len(base.Files) != 0 || len(manifestRaw) == 0 || c.HashBytes(manifestRaw) != c.HashBytes(mustCanonical(manifest)) {
		return c.Fail(c.StaleAuthority, "restore stage allocation does not match its runtime and source manifest")
	}
	anchor, anchorDigest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil {
		return err
	}
	watermark := view.Watermark
	if manifest.DeletionWatermark != watermark {
		return c.Fail(c.PolicyDenied, "restore backup no longer matches the current deletion watermark")
	}
	base.RestoreStageDirectory = canonicalStage
	base.RestoreTarget = canonicalTarget
	base.RestoreSource = canonicalBackup
	base.RestoreSourcePhysicalRoot = sourcePhysical
	base.RestoreBackupDigest = c.HashBytes(manifestRaw)
	base.RestoreInstanceID = instance.ID
	base.RestoreAnchor = anchorDigest
	if anchor == nil {
		base.RestoreAnchor = ""
	}
	base.RestoreWatermark = watermark
	base.RestoreCurrentWatermark = watermark
	if err = privacyRestoreSource(ctx, root, authority, view, base); err != nil {
		return err
	}
	pin, err := readRestoreSourcePin(ctx, canonicalStage, authority)
	if err != nil {
		return err
	}
	if anchor == nil {
		if pin != nil {
			return c.Fail(c.StaleAuthority, "restore owner pin exists without a current external anchor")
		}
	} else {
		if pin == nil {
			return c.Fail(c.StaleAuthority, "restore backup lacks the current external checkpoint owner pin")
		}
		if err = validateSourceCheckpointPin(ctx, root, authority, pin, *anchor, anchorDigest); err != nil {
			return err
		}
		base.RestorePinnedAnchor = pin.AnchorDigest
		base.RestorePinnedBinding = pin.RestoreBinding
	}
	base.RestoreBinding, err = privacyRestoreBindingDigest(authority, base)
	if err != nil {
		return err
	}
	return updateRestoreCopyLocked(root, view, base)
}

func rebasePrivacyRestoreCopyLocked(ctx context.Context, root *os.Root, authority PrivacyAuthority, view privacyView, copy ManagedCopy, journal *store.Store, instance RuntimeInstance) (ManagedCopy, privacyScope, error) {
	var scope privacyScope
	if err := privacyRestoreSource(ctx, root, authority, view, copy); err != nil {
		return copy, scope, err
	}
	if copy.RestoreInstanceID != instance.ID || copy.PhysicalRoot != instance.PhysicalRoot || copy.Directory != copy.RestoreStageDirectory && copy.Directory != copy.RestoreTarget {
		return copy, scope, c.Fail(c.StaleAuthority, "restore allocation runtime identity changed")
	}
	canonical, err := canonicalExistingDirectory(copy.Directory)
	if err != nil || canonical != copy.Directory {
		return copy, scope, c.Fail(c.StaleAuthority, "restore allocation directory moved before publication")
	}
	anchor, anchorDigest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil {
		return copy, scope, err
	}
	actualPin, err := journal.PrivacyCheckpointPin(ctx)
	if err != nil {
		return copy, scope, err
	}
	if anchor == nil {
		if copy.RestoreAnchor != "" || actualPin != nil {
			return copy, scope, c.Fail(c.StaleAuthority, "restore checkpoint witness exists without a current anchor")
		}
		return copy, privacyScope{InstanceID: instance.ID, Directory: canonical, PhysicalRoot: instance.PhysicalRoot}, nil
	}
	if copy.RestoreAnchor == "" {
		return copy, scope, c.Fail(c.StaleAuthority, "restore stage began without the current external checkpoint anchor")
	}
	if copy.RestoreAnchor != "" {
		priorAnchor, ancestor, anchorErr := restoreAnchorIsAncestor(root, *anchor, anchorDigest, copy.RestoreAnchor)
		if anchorErr != nil || !ancestor || priorAnchor.Sequence > anchor.Sequence || priorAnchor.Watermark != copy.RestoreWatermark {
			return copy, scope, c.Fail(c.StaleAuthority, "restore checkpoint base is outside the current anchor lineage")
		}
	}
	if actualPin != nil {
		matchedPin := false
		if copy.RestorePinnedAnchor != "" && actualPin.AnchorDigest == copy.RestorePinnedAnchor && actualPin.RestoreBinding == copy.RestorePinnedBinding {
			if actualPin.InstanceID == copy.RestoreInstanceID {
				pinnedAnchor, ancestor, pinErr := restoreAnchorIsAncestor(root, *anchor, anchorDigest, copy.RestorePinnedAnchor)
				if pinErr != nil || !ancestor || !restorePinMatches(copy, pinnedAnchor, copy.RestorePinnedAnchor, mustDigest(authority), copy.RestorePinnedBinding, actualPin) {
					return copy, scope, c.Fail(c.StaleAuthority, "restore owner pin differs from its exact recorded rebase")
				}
			} else if err = validateSourceCheckpointPin(ctx, root, authority, actualPin, *anchor, anchorDigest); err != nil {
				return copy, scope, err
			}
			matchedPin = true
		}
		if !matchedPin && copy.RestorePinnedAnchor == "" {
			for _, binding := range []struct {
				digest  string
				binding string
			}{{copy.RestoreAnchor, copy.RestoreBinding}, {copy.RestorePreviousAnchor, copy.RestorePreviousBinding}} {
				if binding.digest == "" || binding.binding == "" {
					continue
				}
				prior, priorErr := readRestoreAnchor(root, binding.digest)
				if priorErr != nil {
					continue
				}
				if restorePinMatches(copy, prior, binding.digest, mustDigest(authority), binding.binding, actualPin) {
					matchedPin = true
					break
				}
			}
		}
		if !matchedPin {
			return copy, scope, c.Fail(c.StaleAuthority, "restore owner pin is not the exact recorded source or rebase binding")
		}
	} else if copy.RestorePreviousAnchor != "" {
		return copy, scope, c.Fail(c.StaleAuthority, "restore lost a previously bound owner checkpoint pin")
	} else {
		return copy, scope, c.Fail(c.StaleAuthority, "restore owner checkpoint pin is missing")
	}
	if copy.RestoreAnchor != anchorDigest {
		priorAnchor, priorBinding := copy.RestoreAnchor, copy.RestoreBinding
		copy.RestorePreviousAnchor = priorAnchor
		copy.RestorePreviousBinding = priorBinding
		copy.RestoreAnchor = anchorDigest
		copy.RestoreCurrentWatermark = view.Watermark
		copy.RestoreBinding, err = privacyRestoreBindingDigest(authority, copy)
		if err != nil {
			return copy, scope, err
		}
		if err = updateRestoreCopyLocked(root, view, copy); err != nil {
			return copy, scope, err
		}
		view, err = readPrivacy(root)
		if err != nil {
			return copy, scope, err
		}
	}
	desired := expectedPrivacyCheckpointPin(*anchor, anchorDigest, mustDigest(authority), privacyScope{
		InstanceID: instance.ID, Directory: canonical, PhysicalRoot: instance.PhysicalRoot,
		CheckpointAnchor: anchorDigest, RestoreBinding: copy.RestoreBinding,
	}, "ACTIVE")
	if actualPin == nil || *actualPin != desired {
		if err = journal.RebindRestoredPrivacyCheckpointPin(ctx, actualPin, &desired, copy.RestoreBinding); err != nil {
			return copy, scope, err
		}
	}
	if copy.RestorePinnedAnchor != anchorDigest || copy.RestorePinnedBinding != copy.RestoreBinding {
		copy.RestorePinnedAnchor = anchorDigest
		copy.RestorePinnedBinding = copy.RestoreBinding
		view, err = readPrivacy(root)
		if err != nil {
			return copy, scope, err
		}
		if err = updateRestoreCopyLocked(root, view, copy); err != nil {
			return copy, scope, err
		}
	}
	scope = privacyScope{
		InstanceID: instance.ID, Directory: canonical, PhysicalRoot: instance.PhysicalRoot,
		CheckpointAnchor: anchorDigest, RestoreBinding: copy.RestoreBinding,
	}
	return copy, scope, nil
}

func preparePrivacyRestoreTarget(ctx context.Context, authority PrivacyAuthority, stage, target string, instance RuntimeInstance, journal *store.Store) error {
	canonicalStage, err := canonicalExistingDirectory(stage)
	if err != nil {
		return err
	}
	canonicalTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return err
	}
	root, err := openPrivacyRoot(authority)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return err
	}
	stageCopy, found := findManagedCopy(view, canonicalStage)
	if !found || stageCopy.Kind != "RESTORE_STAGE" || stageCopy.RestoreTarget != canonicalTarget {
		return c.Fail(c.StaleAuthority, "restore stage lost its durable target binding")
	}
	stageCopy, _, err = rebasePrivacyRestoreCopyLocked(ctx, root, authority, view, stageCopy, journal, instance)
	if err != nil {
		return err
	}
	view, err = readPrivacy(root)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(canonicalTarget); err == nil {
		return c.Fail(c.Conflict, "restore target appeared before its durable publication intent")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if existing, found := findManagedCopy(view, canonicalTarget); found {
		if existing.Kind != "RESTORE_STAGE" || !sameRestoreProvenance(stageCopy, existing) || existing.PhysicalRoot != stageCopy.PhysicalRoot || !sameManagedTasks(existing.Tasks, stageCopy.Tasks) || len(existing.Files) != 0 {
			return c.Fail(c.StaleAuthority, "restore target intent changed")
		}
		if existing.RestoreBinding != stageCopy.RestoreBinding || existing.RestorePinnedAnchor != stageCopy.RestorePinnedAnchor || existing.RestorePinnedBinding != stageCopy.RestorePinnedBinding {
			bindingChanged := existing.RestoreBinding != stageCopy.RestoreBinding
			if bindingChanged && (stageCopy.RestorePreviousBinding != existing.RestoreBinding || stageCopy.RestorePreviousAnchor != existing.RestoreAnchor) {
				return c.Fail(c.StaleAuthority, "restore target intent lost its prior anchor binding")
			}
			next := existing
			next.RestoreAnchor = stageCopy.RestoreAnchor
			next.RestorePreviousAnchor = stageCopy.RestorePreviousAnchor
			next.RestoreBinding = stageCopy.RestoreBinding
			next.RestorePreviousBinding = stageCopy.RestorePreviousBinding
			next.RestoreCurrentWatermark = stageCopy.RestoreCurrentWatermark
			if err = updateRestoreCopyLocked(root, view, next); err != nil {
				return err
			}
			if next.RestorePinnedAnchor != stageCopy.RestorePinnedAnchor || next.RestorePinnedBinding != stageCopy.RestorePinnedBinding {
				view, err = readPrivacy(root)
				if err != nil {
					return err
				}
				next.RestorePinnedAnchor = stageCopy.RestorePinnedAnchor
				next.RestorePinnedBinding = stageCopy.RestorePinnedBinding
				if err = updateRestoreCopyLocked(root, view, next); err != nil {
					return err
				}
			}
		}
		return nil
	}
	targetCopy := stageCopy
	targetCopy.Directory = canonicalTarget
	targetCopy.Files = []BackupFile{}
	if err = validateManagedCopy(targetCopy); err != nil {
		return err
	}
	return appendPrivacy(root, view, privacyRecord{Type: "MANAGED_COPY", Copy: &targetCopy})
}

func reconcilePrivacyRestoreScopeLocked(ctx context.Context, root *os.Root, authority PrivacyAuthority, view privacyView, copy ManagedCopy, journal *store.Store, instance RuntimeInstance, register bool) error {
	copy, scope, err := rebasePrivacyRestoreCopyLocked(ctx, root, authority, view, copy, journal, instance)
	if err != nil {
		return err
	}
	if register && copy.Directory != copy.RestoreTarget {
		return c.Fail(c.StaleAuthority, "restore stage cannot become a registered owner before final rename")
	}
	if register {
		if err = publishPrivacyScope(root, scope); err != nil {
			return err
		}
	}
	_, err = readPrivacy(root)
	return err
}
