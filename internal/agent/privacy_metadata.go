package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

// openPrivacyMetadataLedger discovers the already-anchored authority ledger
// without creating one. A missing pair is the pre-bootstrap state; a partial
// pair is corruption and must fail closed. Callers hold the privacy registry
// lock before invoking mutation helpers, so the lock order remains registry
// then quota.
func openPrivacyMetadataLedger(ctx context.Context, root *os.Root) (*resource.Ledger, error) {
	if root == nil {
		return nil, c.Fail(c.InvalidArgument, "privacy metadata root required")
	}
	anchorInfo, anchorErr := root.Lstat("physical-quota.anchor")
	sqliteInfo, sqliteErr := root.Lstat("physical-quota.sqlite")
	anchorExists := anchorErr == nil
	sqliteExists := sqliteErr == nil
	if anchorErr != nil && !errors.Is(anchorErr, os.ErrNotExist) || sqliteErr != nil && !errors.Is(sqliteErr, os.ErrNotExist) {
		return nil, errors.Join(anchorErr, sqliteErr)
	}
	_ = anchorInfo
	_ = sqliteInfo
	if !anchorExists && !sqliteExists {
		return nil, nil
	}
	if !anchorExists || !sqliteExists {
		return nil, c.Fail(c.StaleAuthority, "privacy metadata ledger anchor and database are incomplete")
	}
	genesis, err := fileguard.ReadRegular(root, "authority.json", 4096)
	if err != nil {
		return nil, err
	}
	var authority PrivacyAuthority
	if c.DecodeStrict(genesis, &authority) != nil || authority.validate() != nil {
		return nil, c.Fail(c.StaleAuthority, "privacy metadata authority genesis is invalid")
	}
	rawAnchor, err := fileguard.ReadRegular(root, "physical-quota.anchor", 4096)
	if err != nil {
		return nil, err
	}
	var anchor privacyQuotaAnchor
	if c.DecodeStrict(rawAnchor, &anchor) != nil || anchor.SchemaVersion != 1 || anchor.AuthorityID != authority.ID || anchor.PhysicalRoot != authority.PhysicalRoot || !validPrivacyLedgerID(anchor.LedgerID) || !c.ValidDigest(anchor.DatabaseIdentity) {
		return nil, c.Fail(c.StaleAuthority, "privacy metadata ledger anchor is invalid")
	}
	ledger, err := resource.OpenPinnedExisting(resource.Authority{ID: authority.ID, Directory: authority.Directory, PhysicalRoot: authority.PhysicalRoot}, anchor.LedgerID)
	if err != nil {
		return nil, err
	}
	if err = ledger.ReconcileMetadataFiles(ctx, root); err != nil {
		return nil, errors.Join(err, ledger.Close())
	}
	return ledger, nil
}

func validPrivacyLedgerID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// existingPrivacyQuotaLedgerID validates an already-published shared ledger
// without creating metadata schema or selecting a replacement. A derived
// owner may arrive without its own bootstrap/pin; an anchored authority must
// be adopted by its exact ledger ID, while a missing or partial pair remains
// fail-closed for legacy recovery.
func existingPrivacyQuotaLedgerID(ctx context.Context, authority PrivacyAuthority) (string, bool, error) {
	root, err := openPrivacyRoot(authority)
	if err != nil {
		return "", false, err
	}
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return "", false, errors.Join(err, root.Close())
	}
	defer func() {
		_ = lock.Close()
		_ = root.Close()
	}()
	_, anchorErr := root.Lstat("physical-quota.anchor")
	_, sqliteErr := root.Lstat("physical-quota.sqlite")
	anchorExists := anchorErr == nil
	sqliteExists := sqliteErr == nil
	if anchorErr != nil && !errors.Is(anchorErr, os.ErrNotExist) {
		return "", false, anchorErr
	}
	if sqliteErr != nil && !errors.Is(sqliteErr, os.ErrNotExist) {
		return "", false, sqliteErr
	}
	if !anchorExists && !sqliteExists {
		return "", false, nil
	}
	if !anchorExists || !sqliteExists {
		return "", false, c.Fail(c.StaleAuthority, "privacy metadata ledger anchor and database are incomplete")
	}
	rawAnchor, err := fileguard.ReadRegular(root, "physical-quota.anchor", 4096)
	if err != nil {
		return "", false, err
	}
	var anchor privacyQuotaAnchor
	if err = c.DecodeStrict(rawAnchor, &anchor); err != nil || !validPrivacyLedgerID(anchor.LedgerID) {
		return "", false, c.Fail(c.StaleAuthority, "privacy metadata ledger anchor is invalid")
	}
	ledger, err := resource.OpenPinnedExistingReadOnly(resource.Authority{ID: authority.ID, Directory: authority.Directory, PhysicalRoot: authority.PhysicalRoot}, anchor.LedgerID)
	if err != nil {
		return "", false, err
	}
	if closeErr := ledger.Close(); closeErr != nil {
		return "", false, closeErr
	}
	return anchor.LedgerID, true, nil
}

// publishPrivacyMetadata routes immutable external metadata through the
// shared catalog when the authority ledger is bound. Direct publication is
// retained only for old in-memory/unit fixtures that intentionally predate the
// physical ledger; a real authority root with a partial ledger fails closed.
func publishPrivacyMetadata(ctx context.Context, root *os.Root, name string, raw []byte, class resource.Class) error {
	ledger, err := openPrivacyMetadataLedger(ctx, root)
	if err != nil {
		return err
	}
	if ledger == nil {
		return fileguard.Publish(root, name, raw)
	}
	defer ledger.Close()
	return ledger.PublishMetadataFile(ctx, root, name, raw, class)
}

func removePrivacyMetadata(ctx context.Context, root *os.Root, name, digest string, logicalBytes int64, limit int64, identity os.FileInfo) (bool, error) {
	ledger, err := openPrivacyMetadataLedger(ctx, root)
	if err != nil {
		return false, err
	}
	if ledger == nil {
		return fileguard.RemoveBoundIdentity(root, name, identity, digest, logicalBytes, limit)
	}
	defer ledger.Close()
	return ledger.RemoveMetadataFile(ctx, root, name, digest, logicalBytes, limit)
}

func importPrivacyMetadata(ctx context.Context, root *os.Root, refs []resource.MetadataFileRef) (string, error) {
	ledger, err := openPrivacyMetadataLedger(ctx, root)
	if err != nil {
		return "", err
	}
	if ledger == nil {
		return "", c.Fail(c.StaleAuthority, "privacy metadata ledger is not bound")
	}
	defer ledger.Close()
	return ledger.ImportMetadataFiles(ctx, root, refs)
}

// preparePrivacyMetadataBootstrap commits the owner-store phase before the
// first allocator DDL. The ledger itself is then created/reopened by exact ID,
// so a crash cannot make the next open silently choose a replacement ledger.
func (s *Session) preparePrivacyMetadataBootstrap(ctx context.Context) error {
	if s == nil || s.privacy == nil || s.Journal == nil {
		return nil
	}
	bootstrap, err := s.Journal.PrivacyBootstrap(ctx)
	if err != nil {
		return err
	}
	pin, err := s.Journal.PhysicalQuotaBinding(ctx)
	if err != nil {
		return err
	}
	if bootstrap == nil {
		// A derived owner with an already-anchored shared ledger adopts that
		// exact ledger; it must not treat the authority as an empty legacy store
		// merely because this owner has no local pin yet. A legacy authority
		// without that exact pair requires an explicit quota migration and is
		// never reset by opening this owner.
		if pin == nil {
			if _, existing, existingErr := existingPrivacyQuotaLedgerID(ctx, *s.privacy); existingErr != nil {
				return existingErr
			} else if existing {
				return nil
			}
			return c.Fail(c.UnsupportedCapability, "legacy privacy authority lacks an exact physical quota ledger; explicit quota migration required")
		}
		return nil
	}
	if err = s.validatePrivacyBootstrapOwner(*bootstrap); err != nil {
		return err
	}
	if pin != nil {
		if pin.AuthorityID != s.privacy.ID || pin.Directory != s.privacy.Directory || pin.PhysicalRoot != s.privacy.PhysicalRoot || !validPrivacyLedgerID(pin.LedgerID) {
			return c.Fail(c.StaleAuthority, "owner physical-quota pin differs from bootstrap authority")
		}
		if bootstrap.State != "AUTHORITY_BOUND" && bootstrap.LedgerID != "" && pin.LedgerID != bootstrap.LedgerID {
			return c.Fail(c.StaleAuthority, "owner physical-quota pin differs from bootstrap ledger")
		}
	}
	if (bootstrap.State == "BOUND" || bootstrap.State == "RELEASED") && pin == nil {
		return c.Fail(c.StaleAuthority, "completed privacy bootstrap lacks its owner physical-quota pin")
	}
	switch bootstrap.State {
	case "AUTHORITY_BOUND":
		bootstrap.State = "IMPORTING"
		if pin != nil {
			bootstrap.LedgerID = pin.LedgerID
		} else {
			bootstrap.LedgerID = newID("")
		}
		if err = s.Journal.AdvancePrivacyBootstrap(ctx, "AUTHORITY_BOUND", *bootstrap); err != nil {
			return err
		}
	case "IMPORTING":
		if !validPrivacyLedgerID(bootstrap.LedgerID) {
			return c.Fail(c.StaleAuthority, "privacy bootstrap import lacks its exact ledger ID")
		}
	case "BOUND", "RELEASED":
		return nil
	default:
		return c.Fail(c.StaleAuthority, "privacy bootstrap is not ready for quota import")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return err
	}
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return errors.Join(err, root.Close())
	}
	ledger, err := resource.OpenPinnedBootstrap(resource.Authority{ID: s.privacy.ID, Directory: s.privacy.Directory, PhysicalRoot: s.privacy.PhysicalRoot}, resource.DefaultLimits(), bootstrap.LedgerID)
	var ledgerErr error
	if ledger != nil {
		ledgerErr = ledger.Close()
	}
	closeErr := errors.Join(ledgerErr, lock.Close(), root.Close())
	return errors.Join(err, closeErr)
}

// finishPrivacyMetadataBootstrap imports all pre-ledger immutable metadata
// after the owner's physical-quota pin is durable, then records the exact
// import digest before releasing the owner bootstrap charge.
func (s *Session) finishPrivacyMetadataBootstrap(ctx context.Context) error {
	if s == nil || s.privacy == nil || s.PhysicalQuota == nil || s.Journal == nil {
		return nil
	}
	bootstrap, err := s.Journal.PrivacyBootstrap(ctx)
	if err != nil {
		return err
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if bootstrap == nil {
		complete, completeErr := s.PhysicalQuota.MetadataImportComplete(ctx)
		if completeErr != nil {
			return completeErr
		}
		if complete {
			return nil
		}
		refs, collectErr := collectPrivacyMetadataRefs(root)
		if collectErr != nil {
			return collectErr
		}
		_, importErr := s.PhysicalQuota.ImportMetadataFiles(ctx, root, refs)
		return importErr
	}
	if bootstrap.State == "BOUND" || bootstrap.State == "RELEASED" {
		complete, completeErr := s.PhysicalQuota.MetadataImportComplete(ctx)
		if completeErr != nil {
			return completeErr
		}
		if !complete || !c.ValidDigest(bootstrap.InventoryDigest) {
			return c.Fail(c.StaleAuthority, "completed privacy bootstrap lacks its imported metadata receipt")
		}
		_, importedDigest, digestErr := s.PhysicalQuota.MetadataCatalogSnapshot(ctx)
		if digestErr != nil {
			return digestErr
		}
		if importedDigest != bootstrap.InventoryDigest {
			return c.Fail(c.StaleAuthority, "completed privacy bootstrap metadata receipt differs from its ledger")
		}
		return nil
	}
	if bootstrap.State != "IMPORTING" || !validPrivacyLedgerID(bootstrap.LedgerID) {
		return c.Fail(c.StaleAuthority, "privacy bootstrap import phase is invalid")
	}
	refs, err := collectPrivacyMetadataRefs(root)
	if err != nil {
		return err
	}
	digest, err := s.PhysicalQuota.ImportMetadataFiles(ctx, root, refs)
	if err != nil {
		return err
	}
	bootstrap.InventoryDigest = digest
	bootstrap.State = "BOUND"
	if err = s.Journal.AdvancePrivacyBootstrap(ctx, "IMPORTING", *bootstrap); err != nil {
		return err
	}
	bootstrap.State = "RELEASED"
	return s.Journal.AdvancePrivacyBootstrap(ctx, "BOUND", *bootstrap)
}

// collectPrivacyMetadataRefs describes immutable files that existed before
// the shared ledger was first opened. Mutable checkpoint selectors are
// protocol witnesses and are validated/recovered separately; immutable data,
// anchor, retirement, journal and owner scope files receive exact catalog
// rows here.
func collectPrivacyMetadataRefs(root *os.Root) ([]resource.MetadataFileRef, error) {
	if root == nil {
		return nil, c.Fail(c.InvalidArgument, "privacy metadata root required")
	}
	genesis, err := fileguard.ReadRegular(root, "authority.json", 4096)
	if err != nil {
		return nil, err
	}
	var authority PrivacyAuthority
	if c.DecodeStrict(genesis, &authority) != nil || authority.validate() != nil {
		return nil, c.Fail(c.StoreIntegrityError, "privacy metadata authority genesis is invalid")
	}
	authorityDigest, err := c.Digest(authority)
	if err != nil {
		return nil, err
	}
	refs := make([]resource.MetadataFileRef, 0, 256)
	records := make([]privacyRecord, 0, privacyRecordLimit)
	recordSequences := make(map[int64]bool, privacyRecordLimit)
	checkpointData := make(map[string]privacyCheckpoint)
	checkpointAnchors := make(map[string]privacyCheckpointAnchor)
	checkpointReceipts := make(map[string]privacyCheckpointRetirement)
	add := func(name string, class resource.Class) error {
		refs = append(refs, resource.MetadataFileRef{Path: filepath.ToSlash(name), Class: class})
		return nil
	}
	// Journal records are immutable and deletion intents use the protected
	// control class. Every other record remains in ordinary payload capacity.
	if err := collectRootFiles(root, "records", func(name string, raw []byte) error {
		sequence, parseErr := strconv.ParseInt(strings.TrimSuffix(name, ".json"), 10, 64)
		if parseErr != nil || sequence < 1 || name != fmt.Sprintf("%010d.json", sequence) {
			return c.Fail(c.StoreIntegrityError, "foreign privacy journal metadata member")
		}
		var record privacyRecord
		if c.DecodeStrict(raw, &record) != nil || record.SchemaVersion != 1 || record.Sequence != sequence {
			return c.Fail(c.StoreIntegrityError, "invalid pre-ledger privacy journal record")
		}
		canonical, canonicalErr := c.CanonicalV1(record)
		if canonicalErr != nil || string(canonical) != string(raw) {
			return c.Fail(c.StoreIntegrityError, "pre-ledger privacy journal record is noncanonical")
		}
		records = append(records, record)
		recordSequences[sequence] = true
		class := resource.Payload
		if record.Type == "DELETE_INTENT" {
			class = resource.Control
		}
		return add(filepath.Join("records", name), class)
	}); err != nil {
		return nil, err
	}
	if err := collectRootFiles(root, "owners", func(name string, raw []byte) error {
		// Per-owner leases are mutable lock witnesses and are accounted by the
		// owner attach protocol, not by the immutable JSON catalog.
		if validPrivacyLeaseFilename(name) {
			return nil
		}
		if !strings.HasSuffix(name, ".json") || !c.ValidDigest(strings.TrimSuffix(name, ".json")) {
			return c.Fail(c.StoreIntegrityError, "foreign pre-ledger owner metadata member")
		}
		var scope privacyScope
		if c.DecodeStrict(raw, &scope) != nil || !validPrivacyScope(scope) || scopeName(scope) != name {
			return c.Fail(c.StoreIntegrityError, "invalid pre-ledger owner scope")
		}
		canonical, canonicalErr := c.CanonicalV1(scope)
		if canonicalErr != nil || string(canonical) != string(raw) {
			return c.Fail(c.StoreIntegrityError, "pre-ledger owner scope is noncanonical")
		}
		return add(filepath.Join("owners", name), resource.Payload)
	}); err != nil {
		return nil, err
	}
	for _, directory := range []string{"data", "anchors", "retired"} {
		dir := filepath.Join(privacyCheckpointDirectory, directory)
		if err := collectRootFiles(root, dir, func(name string, raw []byte) error {
			base := strings.TrimSuffix(name, ".json")
			if !strings.HasSuffix(name, ".json") || !c.ValidDigest(base) || len(raw) == 0 {
				return c.Fail(c.StoreIntegrityError, "invalid pre-ledger checkpoint object")
			}
			switch directory {
			case "data":
				var checkpoint privacyCheckpoint
				if c.DecodeStrict(raw, &checkpoint) != nil || validatePrivacyCheckpoint(checkpoint, authority) != nil {
					return c.Fail(c.StoreIntegrityError, "invalid pre-ledger checkpoint projection")
				}
				canonical, canonicalErr := c.CanonicalV1(checkpoint)
				if canonicalErr != nil || string(canonical) != string(raw) || c.HashBytes(raw) != base {
					return c.Fail(c.StoreIntegrityError, "pre-ledger checkpoint projection identity mismatch")
				}
				checkpointData[base] = checkpoint
			case "anchors":
				var anchor privacyCheckpointAnchor
				if c.DecodeStrict(raw, &anchor) != nil || validatePrivacyCheckpointAnchor(anchor) != nil || anchor.AuthorityDigest != authorityDigest {
					return c.Fail(c.StoreIntegrityError, "invalid pre-ledger checkpoint anchor")
				}
				canonical, canonicalErr := c.CanonicalV1(anchor)
				if canonicalErr != nil || string(canonical) != string(raw) || c.HashBytes(raw) != base {
					return c.Fail(c.StoreIntegrityError, "pre-ledger checkpoint anchor identity mismatch")
				}
				checkpointAnchors[base] = anchor
			case "retired":
				var receipt privacyCheckpointRetirement
				if c.DecodeStrict(raw, &receipt) != nil || receipt.SchemaVersion != privacyCheckpointSchema || receipt.AuthorityDigest != authorityDigest || receipt.AnchorDigest != base || receipt.SourceDigest != checkpointSourceDigest(receipt.SourceRecords) || len(receipt.SourceRecords) > privacyRecordLimit {
					return c.Fail(c.StoreIntegrityError, "invalid pre-ledger checkpoint retirement receipt")
				}
				canonical, canonicalErr := c.CanonicalV1(receipt)
				if canonicalErr != nil || string(canonical) != string(raw) {
					return c.Fail(c.StoreIntegrityError, "pre-ledger checkpoint retirement receipt is noncanonical")
				}
				checkpointReceipts[base] = receipt
			}
			return add(filepath.Join(dir, name), resource.Payload)
		}); err != nil {
			return nil, err
		}
	}
	if len(records) != 0 {
		if _, err = readPrivacyCheckpointRecoveryView(root); err != nil {
			return nil, err
		}
	}
	if len(checkpointData) != 0 || len(checkpointAnchors) != 0 || len(checkpointReceipts) != 0 {
		current, currentDigest, currentErr := currentPrivacyCheckpointAnchor(root)
		if currentErr != nil {
			return nil, currentErr
		}
		if current == nil {
			return nil, c.Fail(c.StoreIntegrityError, "unanchored pre-ledger checkpoint metadata")
		}
		if _, _, currentErr = readPrivacyCheckpointBaseMode(root, true); currentErr != nil {
			return nil, currentErr
		}
		history, byCheckpoint, historyErr := privacyCheckpointAnchorIndex(root, *current, currentDigest)
		if historyErr != nil {
			return nil, historyErr
		}
		if historyErr = validateCompletePrivacyAnchorInventory(root, history); historyErr != nil {
			return nil, historyErr
		}
		if len(checkpointAnchors) != len(history) || len(checkpointData) != len(byCheckpoint) || len(history) != len(byCheckpoint) {
			return nil, c.Fail(c.StaleAuthority, "pre-ledger checkpoint inventory is incomplete")
		}
		for digest, anchor := range history {
			if imported, ok := checkpointAnchors[digest]; !ok || imported != anchor {
				return nil, c.Fail(c.StaleAuthority, "pre-ledger checkpoint anchor inventory is not bound")
			}
			checkpoint, ok := checkpointData[anchor.CheckpointDigest]
			if !ok || checkpoint.Generation != anchor.Generation || checkpoint.Sequence != anchor.Sequence || checkpoint.Tail != anchor.Tail || checkpoint.Watermark != anchor.Watermark || checkpoint.ScopeDigest != anchor.ScopeDigest || len(checkpoint.Scopes) != anchor.ScopeCount {
				return nil, c.Fail(c.StaleAuthority, "pre-ledger checkpoint projection is not bound")
			}
		}
		for digest := range checkpointData {
			if _, ok := byCheckpoint[digest]; !ok {
				return nil, c.Fail(c.StaleAuthority, "pre-ledger checkpoint projection is not in anchor lineage")
			}
		}
		for digest, receipt := range checkpointReceipts {
			anchor, ok := history[digest]
			if !ok {
				return nil, c.Fail(c.StaleAuthority, "pre-ledger checkpoint receipt is not in anchor lineage")
			}
			checkpoint := checkpointData[anchor.CheckpointDigest]
			if err = validateHistoricalPrivacyCheckpointReceipt(receipt, anchor, digest, history); err != nil {
				return nil, err
			}
			if digest == currentDigest {
				if err = validatePrivacyCheckpointRetirement(receipt, anchor, digest, checkpoint); err != nil {
					return nil, err
				}
			}
		}
		currentCheckpoint := checkpointData[current.CheckpointDigest]
		if len(currentCheckpoint.SourceRecords) != 0 {
			_, currentReceipt := checkpointReceipts[currentDigest]
			if !currentReceipt {
				for _, source := range currentCheckpoint.SourceRecords {
					if !recordSequences[source.Sequence] {
						return nil, c.Fail(c.StoreIntegrityError, "retired pre-ledger checkpoint source lacks its receipt")
					}
				}
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })
	return refs, nil
}

func collectRootFiles(root *os.Root, directory string, fn func(string, []byte) error) error {
	dir, err := root.Open(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, readErr := dir.Readdir(100001)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(readErr, closeErr)
	}
	if err = closeErr; err != nil {
		return err
	}
	if len(entries) > 100000 {
		return c.Fail(c.BudgetLimitReached, "pre-ledger privacy metadata catalog exceeds its bound")
	}
	for _, entry := range entries {
		if !entry.Mode().IsRegular() {
			return c.Fail(c.StoreIntegrityError, "pre-ledger privacy metadata member is not regular")
		}
		name := entry.Name()
		// Owner leases are mutable lock witnesses. Their handles may be held by
		// another live owner and must be validated by the owner attach protocol,
		// not hydrated as immutable metadata during bootstrap inventory.
		if directory == "owners" && validPrivacyLeaseFilename(name) {
			continue
		}
		raw, readErr := fileguard.ReadRegular(root, filepath.Join(directory, name), 16<<20)
		if readErr != nil {
			return readErr
		}
		if err = fn(name, raw); err != nil {
			return err
		}
	}
	return nil
}
