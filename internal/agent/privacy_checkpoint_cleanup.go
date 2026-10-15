package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

const privacyCheckpointBackupScanLimit = 128
const privacyCheckpointBackupScanBytes int64 = 64 << 20

type privacyCheckpointPruneResult struct {
	DataFiles    int
	ReceiptFiles int
	Pending      bool
	Reason       string
}

type privacyCheckpointPruneFile struct {
	Path     string
	Identity os.FileInfo
	Digest   string
	Size     int64
}

// pruneHistoricalPrivacyCheckpointMetadata reclaims immutable cache objects
// only after the current anchor, all owner pins, and every currently restorable
// managed backup have been checked under the privacy registry lock. Anchor
// history and owner catalog entries are deliberately retained.
func pruneHistoricalPrivacyCheckpointMetadata(ctx context.Context, root *os.Root, authority PrivacyAuthority, view privacyView) (privacyCheckpointPruneResult, error) {
	result := privacyCheckpointPruneResult{}
	anchor, anchorDigest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil || anchor == nil {
		return result, err
	}
	if _, err = root.Lstat(privacyCheckpointIntentPath()); err == nil {
		result.Pending = true
		result.Reason = "CHECKPOINT_RECOVERY_PENDING"
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if _, err = root.Lstat(privacyCheckpointNextPath()); err == nil {
		result.Pending = true
		result.Reason = "CHECKPOINT_ANCHOR_PENDING"
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if err = validateCurrentPrivacyPinsReadOnly(ctx, root, authority); err != nil {
		return result, err
	}
	history, byCheckpoint, err := privacyCheckpointAnchorIndex(root, *anchor, anchorDigest)
	if err != nil {
		return result, err
	}
	if err = validateCompletePrivacyAnchorInventory(root, history); err != nil {
		result.Pending = true
		result.Reason = "ANCHOR_HISTORY_INCOMPLETE"
		return result, nil
	}
	keep := map[string]bool{anchor.CheckpointDigest: true}
	backupKeep, safe, reason, err := privacyCheckpointBackupPins(ctx, root, authority, view, *anchor, anchorDigest, history)
	if err != nil {
		return result, err
	}
	if !safe {
		result.Pending = true
		result.Reason = reason
		return result, nil
	}
	for digest := range backupKeep {
		keep[digest] = true
	}

	dataFiles, pending, err := privacyCheckpointDataPrunePlan(root, authority, keep, byCheckpoint)
	if err != nil {
		return result, err
	}
	if pending {
		result.Pending = true
		result.Reason = "DATA_INVENTORY_INCOMPLETE"
		return result, nil
	}
	receiptFiles, pending, err := privacyCheckpointReceiptPrunePlan(root, *anchor, anchorDigest, history)
	if err != nil {
		return result, err
	}
	if pending {
		result.Pending = true
		result.Reason = "RECEIPT_INVENTORY_INCOMPLETE"
		return result, nil
	}

	// Reconfirm the authority boundary immediately before unlink. A changed
	// anchor or owner witness means none of the candidates are safe to retire.
	latest, latestDigest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil || latest == nil || latestDigest != anchorDigest || *latest != *anchor {
		result.Pending = true
		result.Reason = "CURRENT_ANCHOR_CHANGED"
		return result, err
	}
	if err = validateCurrentPrivacyPinsReadOnly(ctx, root, authority); err != nil {
		return result, err
	}
	for _, candidate := range dataFiles {
		removed, removeErr := removePrivacyMetadata(ctx, root, candidate.Path, candidate.Digest, candidate.Size, privacyCheckpointFileLimit, candidate.Identity)
		if removeErr != nil {
			return result, removeErr
		}
		if removed {
			result.DataFiles++
		}
	}
	for _, candidate := range receiptFiles {
		removed, removeErr := removePrivacyMetadata(ctx, root, candidate.Path, candidate.Digest, candidate.Size, privacyCheckpointFileLimit, candidate.Identity)
		if removeErr != nil {
			return result, removeErr
		}
		if removed {
			result.ReceiptFiles++
		}
	}
	return result, nil
}

func privacyCheckpointAnchorIndex(root *os.Root, current privacyCheckpointAnchor, currentDigest string) (map[string]privacyCheckpointAnchor, map[string]privacyCheckpointAnchor, error) {
	history := make(map[string]privacyCheckpointAnchor, current.Generation)
	byCheckpoint := make(map[string]privacyCheckpointAnchor, current.Generation)
	anchor, digest := current, currentDigest
	for depth := int64(0); depth < privacyCheckpointAnchorLimit; depth++ {
		if _, exists := history[digest]; exists {
			return nil, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint anchor cycle")
		}
		history[digest] = anchor
		byCheckpoint[anchor.CheckpointDigest] = anchor
		if anchor.Generation == 1 {
			return history, byCheckpoint, nil
		}
		prior, err := readRestoreAnchor(root, anchor.PreviousAnchor)
		if err != nil {
			return nil, nil, err
		}
		if prior.AuthorityDigest != current.AuthorityDigest || prior.Generation+1 != anchor.Generation || prior.Sequence > anchor.Sequence || prior.Watermark > anchor.Watermark {
			return nil, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint anchor history regressed")
		}
		digest, anchor = anchor.PreviousAnchor, prior
	}
	return nil, nil, c.Fail(c.BudgetLimitReached, "privacy checkpoint anchor history exceeds its bound")
}

func validateCompletePrivacyAnchorInventory(root *os.Root, history map[string]privacyCheckpointAnchor) error {
	directory, err := root.Open(filepath.Join(privacyCheckpointDirectory, "anchors"))
	if err != nil {
		return err
	}
	names, err := directory.Readdirnames(int(privacyCheckpointAnchorLimit + 1))
	err = errors.Join(err, directory.Close())
	if err != nil {
		return err
	}
	if len(names) != len(history) {
		return c.Fail(c.StaleAuthority, "privacy checkpoint anchor inventory contains an unbound historical anchor")
	}
	sort.Strings(names)
	for _, name := range names {
		digest := strings.TrimSuffix(name, ".json")
		if name != digest+".json" || !c.ValidDigest(digest) {
			return c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint anchor member")
		}
		if _, found := history[digest]; !found {
			return c.Fail(c.StaleAuthority, "privacy checkpoint anchor inventory is not the current lineage")
		}
		raw, readErr := fileguard.ReadRegular(root, privacyCheckpointAnchorHistoryPath(digest), 4096)
		if readErr != nil || c.HashBytes(raw) != digest {
			return c.Fail(c.StoreIntegrityError, "privacy checkpoint anchor history object changed")
		}
	}
	return nil
}

func privacyCheckpointBackupPins(ctx context.Context, root *os.Root, authority PrivacyAuthority, view privacyView, current privacyCheckpointAnchor, currentDigest string, history map[string]privacyCheckpointAnchor) (map[string]bool, bool, string, error) {
	keep := map[string]bool{}
	count := 0
	var scannedBytes int64
	for _, copy := range view.Copies {
		if copy.Kind != "BACKUP" {
			continue
		}
		count++
		if count > privacyCheckpointBackupScanLimit {
			return nil, false, "BACKUP_COUNT_LIMIT", nil
		}
		if err := ctx.Err(); err != nil {
			return nil, false, "", err
		}
		copyRoot, openErr := os.OpenRoot(copy.Directory)
		if errors.Is(openErr, os.ErrNotExist) {
			continue // Absent managed copies cannot be restored.
		}
		if openErr != nil {
			return nil, false, "BACKUP_SCAN_UNAVAILABLE", nil
		}
		openReason := "BACKUP_SCAN_UNCERTAIN"
		func() {
			defer copyRoot.Close()
			physical, idErr := fileguard.DirectoryIdentity(copyRoot)
			if idErr != nil {
				openErr = idErr
				openReason = "BACKUP_ROOT_IDENTITY_UNAVAILABLE"
				return
			}
			canonical, canonicalErr := canonicalExistingDirectory(copy.Directory)
			if canonicalErr != nil {
				openErr = canonicalErr
				openReason = "BACKUP_ROOT_PATH_UNAVAILABLE"
				return
			}
			if physical != copy.PhysicalRoot || canonical != copy.Directory {
				return // Replaced/moved copies already fail exact restore authority.
			}
			manifestFile, found := managedCopyFile(copy, "backup.json")
			if !found {
				return
			}
			if manifestFile.Size < 1 || manifestFile.Size > 8<<20 || manifestFile.Size > privacyCheckpointBackupScanBytes-scannedBytes {
				openErr = errPrivacyCheckpointCleanupBound
				openReason = "BACKUP_SCAN_BYTE_LIMIT"
				return
			}
			raw, readErr := readBackupFile(copyRoot, manifestFile)
			if readErr != nil {
				if !isUnavailableOrInvalidManagedBackup(readErr) {
					openErr = readErr
					openReason = "BACKUP_MANIFEST_READ_UNCERTAIN"
				}
				return
			}
			scannedBytes += int64(len(raw))
			var manifest BackupManifest
			if c.DecodeStrict(raw, &manifest) != nil || validateBackupManifest(manifest) != nil {
				return
			}
			canonicalManifest, canonicalErr := c.CanonicalV1(manifest)
			if canonicalErr != nil || string(canonicalManifest) != string(raw) || manifest.SchemaVersion != 2 || manifest.DeletionWatermark != view.Watermark || manifest.PrivacyAuthority == nil {
				return
			}
			expectedAuthority, _ := c.CanonicalV1(authority)
			actualAuthority, _ := c.CanonicalV1(*manifest.PrivacyAuthority)
			if string(expectedAuthority) != string(actualAuthority) {
				return
			}
			databaseFile, found := backupManifestFile(manifest, "database/state.sqlite")
			if !found || databaseFile.Size < 1 || databaseFile.Size > backupFileLimit(databaseFile.Path) || databaseFile.Size > privacyCheckpointBackupScanBytes-scannedBytes {
				openErr = errPrivacyCheckpointCleanupBound
				openReason = "BACKUP_SCAN_BYTE_LIMIT"
				return
			}
			databaseRaw, databaseErr := readBackupFile(copyRoot, databaseFile)
			if databaseErr != nil {
				if !isUnavailableOrInvalidManagedBackup(databaseErr) {
					openErr = databaseErr
					openReason = "BACKUP_DATABASE_READ_UNCERTAIN"
				}
				return
			}
			scannedBytes += int64(len(databaseRaw))
			ownerAuthority, pin, pinErr := store.ReadPrivacyCheckpointOwnerAt(ctx, filepath.Join(copy.Directory, "database"))
			if pinErr != nil {
				openErr = pinErr
				openReason = "BACKUP_OWNER_READ_UNCERTAIN"
				return
			}
			if string(ownerAuthority) != string(expectedAuthority) || pin == nil {
				return
			}
			if err := validateSourceCheckpointPin(ctx, root, authority, pin, current, currentDigest); err != nil {
				openErr = err
				openReason = "BACKUP_PIN_NOT_ON_CURRENT_LINEAGE"
				return
			}
			pinned, found := history[pin.AnchorDigest]
			if !found {
				openErr = c.Fail(c.StaleAuthority, "registered backup checkpoint pin is outside the retained anchor lineage")
				openReason = "BACKUP_PIN_ANCHOR_NOT_RETAINED"
				return
			}
			keep[pinned.CheckpointDigest] = true
		}()
		if errors.Is(openErr, errPrivacyCheckpointCleanupBound) {
			return nil, false, "BACKUP_SCAN_BYTE_LIMIT", nil
		}
		if openErr != nil {
			return nil, false, openReason, nil
		}
	}
	return keep, true, "", nil
}

var errPrivacyCheckpointCleanupBound = errors.New("privacy checkpoint cleanup scan bound reached")

func isUnavailableOrInvalidManagedBackup(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var typed *c.Error
	if errors.As(err, &typed) {
		return typed.Code == c.StoreIntegrityError || typed.Code == c.UnsupportedCapability || typed.Code == c.StaleBase || typed.Code == c.PolicyDenied
	}
	return false
}

func managedCopyFile(copy ManagedCopy, path string) (BackupFile, bool) {
	for _, file := range copy.Files {
		if file.Path == path {
			return file, true
		}
	}
	return BackupFile{}, false
}

func backupManifestFile(manifest BackupManifest, path string) (BackupFile, bool) {
	for _, file := range manifest.Files {
		if file.Path == path {
			return file, true
		}
	}
	return BackupFile{}, false
}

func privacyCheckpointDataPrunePlan(root *os.Root, authority PrivacyAuthority, keep map[string]bool, byCheckpoint map[string]privacyCheckpointAnchor) ([]privacyCheckpointPruneFile, bool, error) {
	files, err := checkpointPruneDirectoryNames(root, filepath.Join(privacyCheckpointDirectory, "data"))
	if err != nil {
		if errors.Is(err, errPrivacyCheckpointCleanupBound) {
			return nil, true, nil
		}
		return nil, false, err
	}
	plan := make([]privacyCheckpointPruneFile, 0, len(files))
	var total int64
	for _, name := range files {
		digest := strings.TrimSuffix(name, ".json")
		if name != digest+".json" || !c.ValidDigest(digest) {
			return nil, false, c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint data member")
		}
		path := privacyCheckpointDataPath(digest)
		info, err := root.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > privacyCheckpointFileLimit-total {
			return nil, false, c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint data object")
		}
		raw, err := fileguard.ReadRegular(root, path, privacyCheckpointFileLimit)
		if err != nil || c.HashBytes(raw) != digest {
			return nil, false, c.Fail(c.StoreIntegrityError, "privacy checkpoint data digest mismatch")
		}
		after, err := root.Lstat(path)
		if err != nil || !os.SameFile(info, after) {
			return nil, false, c.Fail(c.StaleBase, "privacy checkpoint data identity changed during inventory")
		}
		total += int64(len(raw))
		var checkpoint privacyCheckpoint
		if c.DecodeStrict(raw, &checkpoint) != nil || validatePrivacyCheckpoint(checkpoint, authority) != nil {
			return nil, false, c.Fail(c.StoreIntegrityError, "privacy checkpoint data object is invalid")
		}
		canonical, err := c.CanonicalV1(checkpoint)
		if err != nil || string(canonical) != string(raw) {
			return nil, false, c.Fail(c.StoreIntegrityError, "privacy checkpoint data object is noncanonical")
		}
		if anchor, found := byCheckpoint[digest]; found && (checkpoint.Generation != anchor.Generation || checkpoint.Sequence != anchor.Sequence || checkpoint.Tail != anchor.Tail || checkpoint.Watermark != anchor.Watermark || checkpoint.ScopeDigest != anchor.ScopeDigest || len(checkpoint.Scopes) != anchor.ScopeCount) {
			return nil, false, c.Fail(c.StoreIntegrityError, "historical checkpoint projection differs from its anchor")
		}
		if !keep[digest] {
			plan = append(plan, privacyCheckpointPruneFile{Path: path, Identity: info, Digest: digest, Size: int64(len(raw))})
		}
	}
	return plan, false, nil
}

func privacyCheckpointReceiptPrunePlan(root *os.Root, current privacyCheckpointAnchor, currentDigest string, history map[string]privacyCheckpointAnchor) ([]privacyCheckpointPruneFile, bool, error) {
	files, err := checkpointPruneDirectoryNames(root, filepath.Join(privacyCheckpointDirectory, "retired"))
	if err != nil {
		if errors.Is(err, errPrivacyCheckpointCleanupBound) {
			return nil, true, nil
		}
		return nil, false, err
	}
	plan := make([]privacyCheckpointPruneFile, 0, len(files))
	for _, name := range files {
		digest := strings.TrimSuffix(name, ".json")
		if name != digest+".json" || !c.ValidDigest(digest) {
			return nil, false, c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint receipt member")
		}
		anchor, found := history[digest]
		if !found {
			return nil, true, nil
		}
		path := privacyCheckpointRetirementPath(digest)
		info, err := root.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > privacyCheckpointFileLimit {
			return nil, false, c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint receipt object")
		}
		raw, err := fileguard.ReadRegular(root, path, privacyCheckpointFileLimit)
		if err != nil {
			return nil, false, err
		}
		after, err := root.Lstat(path)
		if err != nil || !os.SameFile(info, after) {
			return nil, false, c.Fail(c.StaleBase, "privacy checkpoint receipt identity changed during inventory")
		}
		var receipt privacyCheckpointRetirement
		if c.DecodeStrict(raw, &receipt) != nil || validateHistoricalPrivacyCheckpointReceipt(receipt, anchor, digest, history) != nil {
			return nil, true, nil
		}
		canonical, err := c.CanonicalV1(receipt)
		if err != nil || string(canonical) != string(raw) {
			return nil, true, nil
		}
		if digest != currentDigest {
			plan = append(plan, privacyCheckpointPruneFile{Path: path, Identity: info, Digest: c.HashBytes(raw), Size: int64(len(raw))})
		} else if err = validatePrivacyCheckpointRetirement(receipt, current, currentDigest, privacyCheckpoint{SourceRecords: receipt.SourceRecords, SchemaVersion: privacyCheckpointSchema}); err != nil {
			// The current receipt was checked with its full checkpoint earlier;
			// this branch is intentionally unreachable unless the file changed.
			return nil, false, err
		}
	}
	return plan, false, nil
}

func checkpointPruneDirectoryNames(root *os.Root, path string) ([]string, error) {
	directory, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	names, err := directory.Readdirnames(int(privacyCheckpointAnchorLimit + 1))
	err = errors.Join(err, directory.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(names)) > privacyCheckpointAnchorLimit {
		return nil, errPrivacyCheckpointCleanupBound
	}
	sort.Strings(names)
	return names, nil
}

func validateHistoricalPrivacyCheckpointReceipt(receipt privacyCheckpointRetirement, anchor privacyCheckpointAnchor, digest string, history map[string]privacyCheckpointAnchor) error {
	if receipt.SchemaVersion != privacyCheckpointSchema || receipt.AuthorityDigest != anchor.AuthorityDigest || receipt.AnchorDigest != digest ||
		receipt.PreviousAnchor != anchor.PreviousAnchor || receipt.CheckpointDigest != anchor.CheckpointDigest || receipt.Sequence != anchor.Sequence ||
		receipt.Tail != anchor.Tail || receipt.Watermark != anchor.Watermark || receipt.SourceDigest != checkpointSourceDigest(receipt.SourceRecords) ||
		len(receipt.SourceRecords) > privacyRecordLimit {
		return c.Fail(c.StoreIntegrityError, "historical privacy checkpoint receipt binding mismatch")
	}
	baseSequence := int64(0)
	baseTail := ""
	if anchor.Generation > 1 {
		prior, found := history[anchor.PreviousAnchor]
		if !found || prior.Generation+1 != anchor.Generation || prior.Sequence > anchor.Sequence || prior.Watermark > anchor.Watermark {
			return c.Fail(c.StoreIntegrityError, "historical privacy checkpoint receipt base anchor is invalid")
		}
		baseSequence, baseTail = prior.Sequence, prior.Tail
	}
	if int64(len(receipt.SourceRecords)) != anchor.Sequence-baseSequence {
		return c.Fail(c.StoreIntegrityError, "historical privacy checkpoint receipt source range is incomplete")
	}
	previous := baseSequence
	for _, source := range receipt.SourceRecords {
		if source.Sequence != previous+1 || !c.ValidDigest(source.Digest) || source.Size < 1 || source.Size > 16<<20 {
			return c.Fail(c.StoreIntegrityError, "historical privacy checkpoint receipt source inventory is invalid")
		}
		previous = source.Sequence
	}
	if len(receipt.SourceRecords) == 0 {
		if anchor.Tail != baseTail {
			return c.Fail(c.StoreIntegrityError, "historical privacy checkpoint receipt tail changed")
		}
	} else if receipt.SourceRecords[len(receipt.SourceRecords)-1].Digest != anchor.Tail {
		return c.Fail(c.StoreIntegrityError, "historical privacy checkpoint receipt tail changed")
	}
	return nil
}
