package resource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const privacyMetadataSchema = 1

// metadataCatalogRowAdmission is a conservative estimate for one catalog
// row and its index/WAL footprint. The estimate is used only to admit the
// one-time import transaction before SQLite writes; the measured file charge
// remains the managed quota value recorded in each receipt.
const metadataCatalogRowAdmission int64 = 4 << 10

// MetadataFileRef classifies one immutable file in the external privacy
// authority. Ordinary journal/catalog/checkpoint metadata is PAYLOAD; only
// deletion intents may use CONTROL.
type MetadataFileRef struct {
	Path  string
	Class Class
}

type metadataFileRow struct {
	ID, Path, Temp, Class, Status, Request, Root, RootID, Volume               string
	Digest, Identity                                                           string
	UpperBytes, UpperEntries, Logical, Allocated, Charged, Entries, FreeBefore uint64
}

const metadataStateDDL = `CREATE TABLE quota_metadata_state (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	schema_version INTEGER NOT NULL,
	authority_id TEXT NOT NULL,
	physical_root TEXT NOT NULL,
	ledger_id TEXT NOT NULL,
	limits_digest TEXT NOT NULL,
	volume_identity TEXT NOT NULL,
	self_bytes INTEGER NOT NULL,
	self_entries INTEGER NOT NULL,
	imported INTEGER NOT NULL,
	import_digest TEXT NOT NULL
)`

const metadataFilesDDL = `CREATE TABLE quota_metadata_files (
	path TEXT PRIMARY KEY,
	allocation_id TEXT NOT NULL UNIQUE,
	temp_path TEXT NOT NULL,
	class TEXT NOT NULL,
	status TEXT NOT NULL,
	request_digest TEXT NOT NULL,
	root_path TEXT NOT NULL,
	root_identity TEXT NOT NULL,
	volume_identity TEXT NOT NULL,
	upper_bytes INTEGER NOT NULL,
	upper_entries INTEGER NOT NULL,
	digest TEXT NOT NULL,
	file_identity TEXT NOT NULL,
	logical_bytes INTEGER NOT NULL,
	allocated_bytes INTEGER NOT NULL,
	charged_bytes INTEGER NOT NULL,
	retained_entries INTEGER NOT NULL,
	free_before INTEGER NOT NULL DEFAULT 0
)`

// ensureMetadataSchema is an atomic, versioned migration. Its row binds the
// new catalog to the exact preexisting ledger identity and limits; it never
// replaces a ledger or resets quota accounting.

func ensureMetadataSchema(db *sql.DB, root *os.Root, authority Authority, limits Limits, ledgerID string) error {
	if db == nil || root == nil || !validID(ledgerID) {
		return c.Fail(c.InvalidArgument, "pinned physical quota ledger required for metadata migration")
	}
	volume, err := fileguard.VolumeIdentity(root)
	if err != nil {
		return err
	}
	limitsDigest, err := c.Digest(limits)
	if err != nil {
		return err
	}
	selfBytes, selfEntries := PrivacyBootstrapOverhead()
	if selfBytes > math.MaxInt64 || selfEntries > math.MaxInt64 {
		return c.Fail(c.UnsupportedCapability, "allocator self-overhead exceeds bounded counters")
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stateExists, filesExists int
	if err = tx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='quota_metadata_state'").Scan(&stateExists); err != nil {
		return err
	}
	if err = tx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='quota_metadata_files'").Scan(&filesExists); err != nil {
		return err
	}
	if stateExists == 0 && filesExists == 0 {
		if _, err = tx.Exec(metadataStateDDL); err != nil {
			return err
		}
		if _, err = tx.Exec(metadataFilesDDL); err != nil {
			return err
		}
		if _, err = tx.Exec("CREATE INDEX quota_metadata_status ON quota_metadata_files(status,class)"); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO quota_metadata_state VALUES(1,?,?,?,?,?,?,?,?,0,'')", privacyMetadataSchema, authority.ID, authority.PhysicalRoot, ledgerID, limitsDigest, volume, selfBytes, selfEntries); err != nil {
			return err
		}
	} else if stateExists != 1 || filesExists != 1 {
		return c.Fail(c.StoreIntegrityError, "physical privacy metadata migration is partial")
	} else {
		var version int
		var savedAuthority, savedRoot, savedLedger, savedLimits, savedVolume, importDigest string
		var savedSelfBytes, savedSelfEntries int64
		var imported int
		if err = tx.QueryRow("SELECT schema_version,authority_id,physical_root,ledger_id,limits_digest,volume_identity,self_bytes,self_entries,imported,import_digest FROM quota_metadata_state WHERE singleton=1").Scan(&version, &savedAuthority, &savedRoot, &savedLedger, &savedLimits, &savedVolume, &savedSelfBytes, &savedSelfEntries, &imported, &importDigest); err != nil {
			return err
		}
		if version != privacyMetadataSchema || savedAuthority != authority.ID || savedRoot != authority.PhysicalRoot || savedLedger != ledgerID || savedLimits != limitsDigest || savedVolume != volume || savedSelfBytes != int64(selfBytes) || savedSelfEntries != int64(selfEntries) || (imported != 0 && imported != 1) || imported == 0 && importDigest != "" || imported == 1 && !c.ValidDigest(importDigest) {
			return c.Fail(c.StaleAuthority, "physical privacy metadata migration binding changed")
		}
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM quota_metadata_state").Scan(&count); err != nil || count != 1 {
			return c.Fail(c.StoreIntegrityError, "physical privacy metadata state is ambiguous")
		}
		if imported == 0 {
			if err = tx.QueryRow("SELECT COUNT(*) FROM quota_metadata_files").Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return c.Fail(c.StoreIntegrityError, "unimported privacy metadata catalog contains rows")
			}
		}
	}
	return tx.Commit()
}

func validateMetadataRefs(refs []MetadataFileRef) ([]MetadataFileRef, error) {
	if len(refs) > maxLedgerRecords {
		return nil, c.Fail(c.BudgetLimitReached, "privacy metadata catalog exceeds its bounded entry count")
	}
	copyRefs := append([]MetadataFileRef(nil), refs...)
	for i := range copyRefs {
		copyRefs[i].Path = filepath.ToSlash(copyRefs[i].Path)
	}
	sort.Slice(copyRefs, func(i, j int) bool { return copyRefs[i].Path < copyRefs[j].Path })
	prior := ""
	for _, ref := range copyRefs {
		clean := path.Clean(filepath.ToSlash(ref.Path))
		if ref.Path == "" || len(ref.Path) > 4096 || filepath.IsAbs(ref.Path) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != ref.Path || ref.Path <= prior || (ref.Class != Payload && ref.Class != Control) {
			return nil, c.Fail(c.InvalidArgument, "invalid typed privacy metadata path")
		}
		prior = ref.Path
	}
	return copyRefs, nil
}

func validateMetadataClass(name string, class Class, raw []byte) error {
	if class != Control {
		return nil
	}
	if !strings.HasPrefix(name, "records/") {
		return c.Fail(c.PolicyDenied, "only a privacy deletion intent may use the control reserve")
	}
	var record struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &record) != nil || record.Type != "DELETE_INTENT" {
		return c.Fail(c.PolicyDenied, "only a privacy deletion intent may use the control reserve")
	}
	return nil
}

func metadataImportAdmission(refs []MetadataFileRef) (int64, error) {
	admission := int64(metadataAdmission)
	for _, ref := range refs {
		// Path text is stored in both the table and its index. Include it on
		// top of the fixed row allowance, with checked arithmetic so a hostile
		// caller cannot wrap the bounded disk admission.
		pathBytes := int64(len(ref.Path))
		if pathBytes > math.MaxInt64/2 {
			return 0, c.Fail(c.BudgetLimitReached, "privacy metadata import path admission overflowed")
		}
		rowBytes := metadataCatalogRowAdmission + pathBytes*2
		if rowBytes < metadataCatalogRowAdmission || admission > (64<<20)-rowBytes {
			return 0, c.Fail(c.BudgetLimitReached, "privacy metadata catalog import exceeds its SQLite admission profile")
		}
		admission += rowBytes
	}
	// SQLite's WAL ceiling is a hard part of the allocator envelope. A
	// single transaction must fit below it; otherwise a later checkpoint
	// cannot make the write safe after the fact.
	if admission > maxLedgerWALBytes {
		return 0, c.Fail(c.BudgetLimitReached, "privacy metadata catalog import exceeds its WAL admission profile")
	}
	return admission, nil
}

func admitMetadataImportTx(tx *sql.Tx, root *os.Root, refs []MetadataFileRef) error {
	admission, err := metadataImportAdmission(refs)
	if err != nil {
		return err
	}
	var pages, pageSize int64
	if err = tx.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		return err
	}
	if err = tx.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return err
	}
	if pages < 1 || pageSize != 4096 || pages > maxLedgerPages || pages > (maxLedgerDBBytes-admission)/pageSize {
		return c.Fail(c.BudgetLimitReached, "privacy metadata catalog import exceeds its database page ceiling")
	}
	controlOnly := len(refs) != 0
	for _, ref := range refs {
		if ref.Class != Control {
			controlOnly = false
			break
		}
	}
	return admitMetadataWithClass(root, admission, controlOnly)
}

func metadataRootBinding(l *Ledger, root *os.Root) (string, string, string, error) {
	if l == nil || root == nil {
		return "", "", "", c.Fail(c.InvalidArgument, "privacy metadata root and ledger required")
	}
	rootPath, err := filepath.Abs(root.Name())
	if err != nil {
		return "", "", "", err
	}
	rootPath, err = filepath.EvalSymlinks(rootPath)
	if err != nil || filepath.Clean(rootPath) != filepath.Clean(l.Authority.Directory) {
		return "", "", "", c.Fail(c.StaleAuthority, "privacy metadata root path differs from its shared authority")
	}
	rootID, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return "", "", "", err
	}
	volume, err := fileguard.VolumeIdentity(root)
	if err != nil {
		return "", "", "", err
	}
	if rootID != l.Authority.PhysicalRoot {
		return "", "", "", c.Fail(c.StaleAuthority, "privacy metadata root identity changed")
	}
	return rootPath, rootID, volume, nil
}

// ImportMetadataFiles records the exact existing metadata inventory before
// any new writer is admitted. It is idempotent only for the same sorted paths,
// classes, inode identities, contents, and allocation measurements.
func (l *Ledger) ImportMetadataFiles(ctx context.Context, root *os.Root, refs []MetadataFileRef) (string, error) {
	validated, err := validateMetadataRefs(refs)
	if err != nil {
		return "", err
	}
	rootPath, rootID, volume, err := metadataRootBinding(l, root)
	if err != nil {
		return "", err
	}
	measured := make([]FileReceipt, 0, len(validated))
	for _, ref := range validated {
		receipt, measureErr := measureFile(root, ref.Path)
		if measureErr != nil {
			return "", measureErr
		}
		raw, readErr := fileguard.ReadRegular(root, ref.Path, int64(receipt.LogicalBytes))
		if readErr != nil {
			return "", readErr
		}
		if classErr := validateMetadataClass(ref.Path, ref.Class, raw); classErr != nil {
			return "", classErr
		}
		rechecked, recheckErr := measureFile(root, ref.Path)
		if recheckErr != nil || rechecked != receipt {
			return "", errors.Join(recheckErr, c.Fail(c.StaleAuthority, "privacy metadata file changed during import"))
		}
		measured = append(measured, receipt)
	}
	bindings := make([]any, 0, len(validated))
	for i, ref := range validated {
		bindings = append(bindings, struct {
			Path  string      `json:"path"`
			Class Class       `json:"class"`
			File  FileReceipt `json:"file"`
		}{ref.Path, ref.Class, measured[i]})
	}
	digest, err := c.Digest(struct {
		SchemaVersion  int    `json:"schema_version"`
		RootPath       string `json:"root_path"`
		RootIdentity   string `json:"root_identity"`
		VolumeIdentity string `json:"volume_identity"`
		Files          []any  `json:"files"`
	}{privacyMetadataSchema, rootPath, rootID, volume, bindings})
	if err != nil {
		return "", err
	}
	err = l.withMetadataLock(ctx, false, func(tx *sql.Tx) error {
		var imported int
		var savedDigest string
		if err := tx.QueryRowContext(ctx, "SELECT imported,import_digest FROM quota_metadata_state WHERE singleton=1").Scan(&imported, &savedDigest); err != nil {
			return err
		}
		if imported == 1 {
			if savedDigest != digest {
				return c.Fail(c.StaleAuthority, "privacy metadata import set changed during bootstrap")
			}
			return validateMetadataRowsTx(ctx, tx, rootPath, rootID, volume, validated, measured)
		}
		if imported != 0 || savedDigest != "" {
			return c.Fail(c.StoreIntegrityError, "privacy metadata import marker is invalid")
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_metadata_files").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return c.Fail(c.StoreIntegrityError, "unimported privacy metadata catalog already contains rows")
		}
		if err := admitMetadataImportTx(tx, l.root, validated); err != nil {
			return err
		}
		capacity, err := capacityTx(tx, l.Limits)
		if err != nil {
			return err
		}
		if len(validated) > maxLedgerRecords-capacity.Allocations {
			return c.Fail(c.BudgetLimitReached, "privacy metadata inventory exceeds remaining ledger records")
		}
		payloadBytes, err := add(capacity.PayloadBytes, capacity.PayloadOutstandingBytes)
		if err != nil {
			return err
		}
		payloadEntries, err := add(capacity.PayloadEntries, capacity.PayloadOutstandingEntries)
		if err != nil {
			return err
		}
		controlBytes, err := add(capacity.ControlUsedBytes, capacity.ControlOutstandingBytes)
		if err != nil {
			return err
		}
		controlEntries, err := add(capacity.ControlUsedEntries, capacity.ControlOutstandingEntries)
		if err != nil {
			return err
		}
		allBytes, err := add(capacity.RetainedBytes, capacity.OutstandingBytes)
		if err != nil {
			return err
		}
		allEntries, err := add(capacity.RetainedEntries, capacity.OutstandingItems)
		if err != nil {
			return err
		}
		for i, ref := range validated {
			file := measured[i]
			if file.ChargedBytes > math.MaxInt64 {
				return c.Fail(c.UnsupportedCapability, "privacy metadata receipt exceeds bounded counters")
			}
			allBytes, err = add(allBytes, file.ChargedBytes)
			if err != nil {
				return err
			}
			allEntries, err = add(allEntries, 1)
			if err != nil {
				return err
			}
			if ref.Class == Control {
				controlBytes, err = add(controlBytes, file.ChargedBytes)
				if err == nil {
					controlEntries, err = add(controlEntries, 1)
				}
			} else {
				payloadBytes, err = add(payloadBytes, file.ChargedBytes)
				if err == nil {
					payloadEntries, err = add(payloadEntries, 1)
				}
			}
			if err != nil {
				return err
			}
		}
		if payloadBytes > l.Limits.Bytes-l.Limits.ControlBytes || payloadEntries > l.Limits.Entries-l.Limits.ControlEntries || controlBytes > l.Limits.ControlBytes || controlEntries > l.Limits.ControlEntries || allBytes > l.Limits.Bytes || allEntries > l.Limits.Entries {
			return c.Fail(c.BudgetLimitReached, "privacy metadata inventory exceeds its typed shared quota")
		}
		available, freeErr := fileguard.AvailableBytes(root)
		if freeErr != nil {
			return freeErr
		}
		outstanding, outErr := outstandingForVolumeTx(tx, volume)
		if outErr != nil {
			return outErr
		}
		if available < l.Limits.LowWaterBytes || outstanding > available-l.Limits.LowWaterBytes {
			return c.Fail(c.BudgetLimitReached, "privacy metadata inventory exceeds measured volume space")
		}
		for i, ref := range validated {
			file := measured[i]
			id := metadataAllocationID(l.Authority.ID, ref.Path, file.Digest)
			request := c.HashBytes([]byte("privacy-metadata-import-v1\x00" + l.Authority.ID + "\x00" + ref.Path + "\x00" + string(ref.Class) + "\x00" + file.Digest))
			if _, err = tx.ExecContext(ctx, `INSERT INTO quota_metadata_files(path,allocation_id,temp_path,class,status,request_digest,root_path,root_identity,volume_identity,upper_bytes,upper_entries,digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,retained_entries,free_before)
				VALUES(?,?,?,?,'RETAINED',?,?,?,?,?,1,?,?,?,?,?,1,0)`, ref.Path, id, metadataTempPath(id), string(ref.Class), request, rootPath, rootID, volume, file.ChargedBytes, file.Digest, file.Identity, file.LogicalBytes, file.Allocated, file.ChargedBytes); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, "UPDATE quota_metadata_state SET imported=1,import_digest=? WHERE singleton=1 AND imported=0 AND import_digest=''", digest)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "privacy metadata import marker changed during bootstrap")
		}
		return nil
	})
	return digest, err
}

func validateMetadataRowsTx(ctx context.Context, tx *sql.Tx, rootPath, rootID, volume string, refs []MetadataFileRef, files []FileReceipt) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_metadata_files WHERE status<>'RELEASED'").Scan(&count); err != nil {
		return err
	}
	if count < len(refs) {
		return c.Fail(c.StaleAuthority, "privacy metadata path set exceeds its imported catalog")
	}
	seen := make(map[string]bool, len(refs))
	for i, ref := range refs {
		var class, status, savedRoot, savedRootID, savedVolume, digest, identity string
		var logical, allocated, charged, entries int64
		if err := tx.QueryRowContext(ctx, "SELECT class,status,root_path,root_identity,volume_identity,digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,retained_entries FROM quota_metadata_files WHERE path=?", ref.Path).Scan(&class, &status, &savedRoot, &savedRootID, &savedVolume, &digest, &identity, &logical, &allocated, &charged, &entries); err != nil {
			return err
		}
		file := files[i]
		if class != string(ref.Class) || status != "RETAINED" || savedRoot != rootPath || savedRootID != rootID || savedVolume != volume || digest != file.Digest || identity != file.Identity || logical < 0 || allocated < 0 || charged < 0 || entries != 1 || uint64(logical) != file.LogicalBytes || uint64(allocated) != file.Allocated || uint64(charged) != file.ChargedBytes {
			return c.Fail(c.StaleAuthority, "privacy metadata file differs from its exact retained receipt")
		}
		seen[ref.Path] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT path,status,root_path,root_identity,volume_identity FROM quota_metadata_files WHERE status<>'RELEASED'")
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, status, savedRoot, savedRootID, savedVolume string
		if err = rows.Scan(&name, &status, &savedRoot, &savedRootID, &savedVolume); err != nil {
			rows.Close()
			return err
		}
		if savedRoot != rootPath || savedRootID != rootID || savedVolume != volume {
			rows.Close()
			return c.Fail(c.StaleAuthority, "privacy metadata catalog contains a foreign root binding")
		}
		if seen[name] {
			continue
		}
		if status != "FREE_PENDING" {
			rows.Close()
			return c.Fail(c.StaleAuthority, "privacy metadata catalog contains an unlisted retained or outstanding file")
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	return nil
}

func metadataAllocationID(authorityID, name, digest string) string {
	return c.HashBytes([]byte("privacy-metadata-file-v1\x00" + authorityID + "\x00" + name + "\x00" + digest))[:32]
}

func metadataTempPath(id string) string { return ".publish-" + id }

func (l *Ledger) withMetadataLock(ctx context.Context, requireImported bool, fn func(*sql.Tx) error) error {
	if l == nil || l.root == nil || l.db == nil || fn == nil {
		return c.Fail(c.InvalidArgument, "physical quota metadata ledger required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return os.ErrClosed
	}
	if l.readOnly {
		return c.Fail(c.UnsupportedCapability, "read-only physical quota ledger cannot admit mutations")
	}
	lock, err := l.acquire(ctx)
	if err != nil {
		return err
	}
	if err = admitCheckpointMetadata(l.root); err != nil {
		return errors.Join(err, lock.Close())
	}
	if err = l.verifyPinnedMetadata(); err != nil {
		return errors.Join(err, lock.Close())
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(err, lock.Close())
	}
	defer tx.Rollback()
	var imported int
	if err = tx.QueryRowContext(ctx, "SELECT imported FROM quota_metadata_state WHERE singleton=1").Scan(&imported); err != nil {
		return errors.Join(err, lock.Close())
	}
	if imported != 0 && imported != 1 || requireImported && imported != 1 {
		return errors.Join(c.Fail(c.StaleAuthority, "privacy metadata inventory has not been durably imported"), lock.Close())
	}
	if err = fn(tx); err != nil {
		return errors.Join(err, lock.Close())
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(err, lock.Close())
	}
	return errors.Join(l.verifyPinnedMetadata(), lock.Close())
}

// MetadataCatalogFeatureInUse reports the versioned catalog binding for the
// runtime compatibility gate.
func (l *Ledger) MetadataCatalogFeatureInUse(ctx context.Context) (bool, error) {
	var used bool
	err := l.withReadLock(ctx, func() error {
		var version int
		var imported int
		if err := l.db.QueryRowContext(ctx, "SELECT schema_version,imported FROM quota_metadata_state WHERE singleton=1").Scan(&version, &imported); err != nil {
			return err
		}
		if version != privacyMetadataSchema || imported < 0 || imported > 1 {
			return c.Fail(c.StoreIntegrityError, "physical privacy metadata feature marker is invalid")
		}
		used = imported == 1
		return nil
	})
	return used, err
}

// MetadataImportComplete reports whether the exact pre-ledger inventory has
// been durably committed. It is intentionally separate from feature usage so
// legacy reopen can retry the one-time import without re-importing every later
// owner scope or checkpoint file.
func (l *Ledger) MetadataImportComplete(ctx context.Context) (bool, error) {
	var complete bool
	err := l.withReadLock(ctx, func() error {
		var version, imported int
		if err := l.db.QueryRowContext(ctx, "SELECT schema_version,imported FROM quota_metadata_state WHERE singleton=1").Scan(&version, &imported); err != nil {
			return err
		}
		if version != privacyMetadataSchema || (imported != 0 && imported != 1) {
			return c.Fail(c.StoreIntegrityError, "physical privacy metadata feature marker is invalid")
		}
		complete = imported == 1
		return nil
	})
	return complete, err
}

func (l *Ledger) PublishMetadataFile(ctx context.Context, root *os.Root, name string, raw []byte, class Class) error {
	if len(raw) == 0 || len(raw) > 16<<20 || (class != Payload && class != Control) || class == Control && !strings.HasPrefix(name, "records/") {
		return c.Fail(c.InvalidArgument, "bounded typed privacy metadata publication required")
	}
	refs, err := validateMetadataRefs([]MetadataFileRef{{Path: name, Class: class}})
	if err != nil {
		return err
	}
	name = refs[0].Path
	if err = validateMetadataClass(name, class, raw); err != nil {
		return err
	}
	rootPath, rootID, volume, err := metadataRootBinding(l, root)
	if err != nil {
		return err
	}
	digest := c.HashBytes(raw)
	id := metadataAllocationID(l.Authority.ID, name, digest)
	request := c.HashBytes([]byte("privacy-metadata-publish-v1\x00" + l.Authority.ID + "\x00" + name + "\x00" + string(class) + "\x00" + digest))
	upperBytes := uint64(len(raw)) + metadataAdmission
	if upperBytes > math.MaxInt64 {
		return c.Fail(c.BudgetLimitReached, "privacy metadata reservation exceeds bounded counter")
	}
	if err = l.withMetadataLock(ctx, true, func(tx *sql.Tx) error {
		var old metadataFileRow
		err := scanMetadataRow(tx.QueryRowContext(ctx, `SELECT allocation_id,path,temp_path,class,status,request_digest,root_path,root_identity,volume_identity,upper_bytes,upper_entries,digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,retained_entries,free_before FROM quota_metadata_files WHERE path=?`, name), &old)
		if err == nil {
			if old.ID != id || old.Class != string(class) || old.Digest != digest || old.Root != rootPath || old.RootID != rootID || old.Volume != volume {
				return c.Fail(c.StaleAuthority, "privacy metadata path already belongs to a different immutable lineage")
			}
			if old.Status == "RETAINED" {
				actual, measureErr := measureFile(root, name)
				if measureErr != nil || actual.Digest != digest || actual.Identity != old.Identity || actual.LogicalBytes != old.Logical || actual.Allocated != old.Allocated || actual.ChargedBytes != old.Charged {
					return errors.Join(measureErr, c.Fail(c.StaleAuthority, "retained privacy metadata file changed"))
				}
				if string(raw) != string(mustReadMetadata(root, name, int64(len(raw)))) {
					return c.Fail(c.StaleAuthority, "retained privacy metadata content changed")
				}
				return nil
			}
			if old.Status != "WRITING" || old.Request != request || old.Temp != metadataTempPath(id) || old.UpperBytes != upperBytes || old.UpperEntries != 1 {
				return c.Fail(c.StaleAuthority, "pending privacy metadata reservation differs from the retry")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		} else {
			if _, statErr := root.Lstat(name); statErr == nil {
				return c.Fail(c.StaleAuthority, "unaccounted privacy metadata file cannot be adopted by publication")
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return statErr
			}
			capacity, capErr := capacityTx(tx, l.Limits)
			if capErr != nil {
				return capErr
			}
			if capacity.Allocations >= maxLedgerRecords {
				return c.Fail(c.BudgetLimitReached, "privacy metadata catalog is full")
			}
			bytesLimit, entriesLimit := l.Limits.Bytes-l.Limits.ControlBytes, l.Limits.Entries-l.Limits.ControlEntries
			usedBytes, addErr := add(capacity.PayloadBytes, capacity.PayloadOutstandingBytes)
			if addErr != nil {
				return addErr
			}
			usedEntries, addErr := add(capacity.PayloadEntries, capacity.PayloadOutstandingEntries)
			if addErr != nil {
				return addErr
			}
			if class == Control {
				bytesLimit, entriesLimit = l.Limits.ControlBytes, l.Limits.ControlEntries
				usedBytes, addErr = add(capacity.ControlUsedBytes, capacity.ControlOutstandingBytes)
				if addErr != nil {
					return addErr
				}
				usedEntries, addErr = add(capacity.ControlUsedEntries, capacity.ControlOutstandingEntries)
				if addErr != nil {
					return addErr
				}
			}
			if usedBytes > bytesLimit || upperBytes > bytesLimit-usedBytes || usedEntries >= entriesLimit {
				return c.Fail(c.BudgetLimitReached, "shared privacy metadata quota exhausted")
			}
			allBytes, addErr := add(capacity.RetainedBytes, capacity.OutstandingBytes)
			if addErr != nil {
				return addErr
			}
			allEntries, addErr := add(capacity.RetainedEntries, capacity.OutstandingItems)
			if addErr != nil {
				return addErr
			}
			if allBytes > l.Limits.Bytes || upperBytes > l.Limits.Bytes-allBytes || allEntries >= l.Limits.Entries {
				return c.Fail(c.BudgetLimitReached, "shared physical quota exhausted by privacy metadata")
			}
			available, freeErr := fileguard.AvailableBytes(root)
			if freeErr != nil {
				return freeErr
			}
			outstanding, outErr := outstandingForVolumeTx(tx, volume)
			if outErr != nil {
				return outErr
			}
			if available < l.Limits.LowWaterBytes || outstanding > available-l.Limits.LowWaterBytes || upperBytes > available-l.Limits.LowWaterBytes-outstanding {
				return c.Fail(c.BudgetLimitReached, "privacy metadata volume lacks measured reservation space")
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO quota_metadata_files(path,allocation_id,temp_path,class,status,request_digest,root_path,root_identity,volume_identity,upper_bytes,upper_entries,digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,retained_entries,free_before)
				VALUES(?,?,?,?,'WRITING',?,?,?,?,?,1,?,'',0,0,0,0,0)`, name, id, metadataTempPath(id), string(class), request, rootPath, rootID, volume, upperBytes, digest)
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if err = root.MkdirAll(filepath.Dir(filepath.FromSlash(name)), 0700); err != nil {
		return err
	}
	temp := metadataTempPath(id)
	if _, statErr := root.Lstat(name); statErr == nil {
		actual, measureErr := measureFile(root, name)
		if measureErr != nil || actual.Digest != digest {
			return errors.Join(measureErr, c.Fail(c.StaleAuthority, "published privacy metadata differs from pending reservation"))
		}
		return l.commitMetadataReceipt(ctx, root, name, class, id, request, upperBytes, actual)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if _, statErr := root.Lstat(temp); statErr == nil {
		prior, readErr := fileguard.ReadRegular(root, temp, int64(len(raw)))
		if readErr != nil || string(prior) != string(raw) {
			return errors.Join(readErr, c.Fail(c.StaleAuthority, "privacy metadata temporary differs from its durable reservation"))
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		f, openErr := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			return openErr
		}
		_, writeErr := f.Write(raw)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if err = errors.Join(writeErr, closeErr); err != nil {
			return err
		}
	} else {
		return statErr
	}
	if err = fileguard.PublishExisting(root, temp, name); err != nil {
		return err
	}
	actual, err := measureFile(root, name)
	if err != nil {
		return err
	}
	if actual.Digest != digest || actual.LogicalBytes != uint64(len(raw)) {
		return c.Fail(c.StoreIntegrityError, "published privacy metadata differs from its reservation")
	}
	return l.commitMetadataReceipt(ctx, root, name, class, id, request, upperBytes, actual)
}

func (l *Ledger) commitMetadataReceipt(ctx context.Context, root *os.Root, name string, class Class, id, request string, upper uint64, measured FileReceipt) error {
	rootPath, rootID, volume, err := metadataRootBinding(l, root)
	if err != nil {
		return err
	}
	if measured.Path != name || measured.ChargedBytes > upper || measured.ChargedBytes > math.MaxInt64 || measured.LogicalBytes > math.MaxInt64 || measured.Allocated > math.MaxInt64 {
		return c.Fail(c.BudgetLimitReached, "privacy metadata writer exceeded its physical reservation")
	}
	return l.withMetadataLock(ctx, true, func(tx *sql.Tx) error {
		var row metadataFileRow
		if err := scanMetadataRow(tx.QueryRowContext(ctx, `SELECT allocation_id,path,temp_path,class,status,request_digest,root_path,root_identity,volume_identity,upper_bytes,upper_entries,digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,retained_entries,free_before FROM quota_metadata_files WHERE path=?`, name), &row); err != nil {
			return err
		}
		if row.ID == id && row.Status == "RETAINED" && row.Digest == measured.Digest && row.Identity == measured.Identity && row.Logical == measured.LogicalBytes && row.Allocated == measured.Allocated && row.Charged == measured.ChargedBytes {
			return nil
		}
		if row.ID != id {
			return c.Fail(c.StaleAuthority, "privacy metadata receipt allocation changed before commit")
		}
		if row.Status != "WRITING" || row.ID != id || row.Request != request || row.Class != string(class) || row.Root != rootPath || row.RootID != rootID || row.Volume != volume || row.UpperBytes != upper || row.Digest != measured.Digest || row.Temp != metadataTempPath(id) {
			return c.Fail(c.StaleAuthority, "privacy metadata receipt no longer matches its reservation")
		}
		result, err := tx.ExecContext(ctx, `UPDATE quota_metadata_files SET status='RETAINED',file_identity=?,logical_bytes=?,allocated_bytes=?,charged_bytes=?,retained_entries=1 WHERE path=? AND status='WRITING' AND allocation_id=?`, measured.Identity, measured.LogicalBytes, measured.Allocated, measured.ChargedBytes, name, id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "privacy metadata reservation changed before receipt commit")
		}
		return nil
	})
}

type rowScanner interface{ Scan(...any) error }

func scanMetadataRow(row rowScanner, target *metadataFileRow) error {
	var upperBytes, upperEntries, logical, allocated, charged, entries, before int64
	err := row.Scan(&target.ID, &target.Path, &target.Temp, &target.Class, &target.Status, &target.Request, &target.Root, &target.RootID, &target.Volume, &upperBytes, &upperEntries, &target.Digest, &target.Identity, &logical, &allocated, &charged, &entries, &before)
	if err != nil {
		return err
	}
	if upperBytes < 0 || upperEntries < 0 || logical < 0 || allocated < 0 || charged < 0 || entries < 0 || before < 0 {
		return c.Fail(c.StoreIntegrityError, "negative privacy metadata allocation counter")
	}
	target.UpperBytes, target.UpperEntries = uint64(upperBytes), uint64(upperEntries)
	target.Logical, target.Allocated, target.Charged, target.Entries, target.FreeBefore = uint64(logical), uint64(allocated), uint64(charged), uint64(entries), uint64(before)
	return nil
}

func mustReadMetadata(root *os.Root, name string, limit int64) []byte {
	raw, err := fileguard.ReadRegular(root, name, limit)
	if err != nil {
		return nil
	}
	return raw
}

func (l *Ledger) RemoveMetadataFile(ctx context.Context, root *os.Root, name, digest string, logicalBytes, limit int64) (bool, error) {
	if root == nil || !c.ValidDigest(digest) || logicalBytes < 0 || limit < logicalBytes || limit < 0 {
		return false, c.Fail(c.InvalidArgument, "exact privacy metadata free receipt required")
	}
	refs, err := validateMetadataRefs([]MetadataFileRef{{Path: name, Class: Payload}})
	if err != nil {
		return false, err
	}
	name = refs[0].Path
	var row metadataFileRow
	err = l.withMetadataLock(ctx, true, func(tx *sql.Tx) error {
		rootPath, rootID, volume, err := metadataRootBinding(l, root)
		if err != nil {
			return err
		}
		if err = scanMetadataRow(tx.QueryRowContext(ctx, `SELECT allocation_id,path,temp_path,class,status,request_digest,root_path,root_identity,volume_identity,upper_bytes,upper_entries,digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,retained_entries,free_before FROM quota_metadata_files WHERE path=?`, name), &row); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return c.Fail(c.StaleAuthority, "privacy metadata deletion lacks a retained quota receipt")
			}
			return err
		}
		if row.Root != rootPath || row.RootID != rootID || row.Volume != volume || row.Digest != digest || row.Logical != uint64(logicalBytes) || row.Identity == "" || (row.Status != "RETAINED" && row.Status != "FREE_PENDING") {
			return c.Fail(c.StaleAuthority, "privacy metadata deletion differs from its exact retained receipt")
		}
		if row.Status == "RETAINED" {
			available, freeErr := fileguard.AvailableBytes(root)
			if freeErr != nil {
				return freeErr
			}
			result, updateErr := tx.ExecContext(ctx, "UPDATE quota_metadata_files SET status='FREE_PENDING',free_before=? WHERE path=? AND status='RETAINED' AND file_identity=? AND digest=?", available, name, row.Identity, row.Digest)
			if updateErr != nil {
				return updateErr
			}
			changed, updateErr := result.RowsAffected()
			if updateErr != nil || changed != 1 {
				return c.Fail(c.StaleAuthority, "privacy metadata free intent changed before deletion")
			}
			row.Status, row.FreeBefore = "FREE_PENDING", available
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	var removed bool
	err = l.withMetadataLock(ctx, true, func(tx *sql.Tx) error {
		var current metadataFileRow
		if err := scanMetadataRow(tx.QueryRowContext(ctx, `SELECT allocation_id,path,temp_path,class,status,request_digest,root_path,root_identity,volume_identity,upper_bytes,upper_entries,digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,retained_entries,free_before FROM quota_metadata_files WHERE path=?`, name), &current); err != nil {
			return err
		}
		if current.Status == "RELEASED" {
			return nil
		}
		if current.Status != "FREE_PENDING" || current.ID != row.ID || current.Digest != digest || current.Identity != row.Identity {
			return c.Fail(c.StaleAuthority, "privacy metadata free-intent receipt changed before unlink")
		}
		info, statErr := root.Lstat(name)
		if statErr == nil {
			actual, measureErr := measureFile(root, name)
			if measureErr != nil || actual.Identity != current.Identity || actual.Digest != current.Digest || actual.LogicalBytes != current.Logical || actual.Allocated != current.Allocated || actual.ChargedBytes != current.Charged {
				return errors.Join(measureErr, c.Fail(c.StaleAuthority, "privacy metadata free-intent member changed"))
			}
			deleted, removeErr := fileguard.RemoveBoundIdentity(root, filepath.FromSlash(name), info, digest, logicalBytes, limit)
			if removeErr != nil {
				return removeErr
			}
			removed = deleted
			if err = fileguard.SyncParents(root, filepath.Dir(filepath.FromSlash(name))); err != nil {
				return err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		} else if err = syncExistingParents(root, filepath.FromSlash(name)); err != nil {
			return err
		}
		if _, statErr = root.Lstat(name); !errors.Is(statErr, os.ErrNotExist) {
			if statErr == nil {
				return c.Fail(c.StaleAuthority, "privacy metadata free-intent path still exists")
			}
			return statErr
		}
		after, freeErr := fileguard.AvailableBytes(root)
		if freeErr != nil {
			return freeErr
		}
		margin := uint64(metadataAdmission)
		if current.Allocated > math.MaxUint64-current.FreeBefore || margin > math.MaxUint64-current.FreeBefore-current.Allocated || after < current.FreeBefore+current.Allocated+margin {
			return nil // absence alone does not release the retained charge.
		}
		result, err := tx.ExecContext(ctx, "UPDATE quota_metadata_files SET status='RELEASED' WHERE path=? AND status='FREE_PENDING' AND file_identity=? AND digest=?", name, current.Identity, current.Digest)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "privacy metadata free receipt changed before release")
		}
		return nil
	})
	return removed, err
}

func (l *Ledger) ReconcileMetadataFiles(ctx context.Context, root *os.Root) error {
	if root == nil {
		return c.Fail(c.InvalidArgument, "privacy metadata root required")
	}
	return l.withMetadataLock(ctx, true, func(tx *sql.Tx) error {
		rootPath, rootID, volume, err := metadataRootBinding(l, root)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT allocation_id,path,temp_path,class,status,request_digest,root_path,root_identity,volume_identity,upper_bytes,upper_entries,digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,retained_entries,free_before FROM quota_metadata_files WHERE status IN ('FREE_PENDING','WRITING') ORDER BY path`)
		if err != nil {
			return err
		}
		pending := []metadataFileRow{}
		for rows.Next() {
			var row metadataFileRow
			if err = scanMetadataRow(rows, &row); err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, row)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, row := range pending {
			if row.Root != rootPath || row.RootID != rootID || row.Volume != volume {
				return c.Fail(c.StaleAuthority, "pending privacy metadata receipt belongs to another root")
			}
			if row.Status == "WRITING" {
				// PublishMetadataFile owns retries. A surviving temp remains charged;
				// startup never guesses whether an unreferenced temp may be deleted.
				continue
			}
			if row.Status != "FREE_PENDING" {
				return c.Fail(c.StoreIntegrityError, "unknown pending privacy metadata state")
			}
			if _, err = root.Lstat(row.Path); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err = syncExistingParents(root, filepath.FromSlash(row.Path)); err != nil {
				continue
			}
			after, freeErr := fileguard.AvailableBytes(root)
			if freeErr != nil {
				return freeErr
			}
			margin := uint64(metadataAdmission)
			if row.Allocated <= math.MaxUint64-row.FreeBefore && margin <= math.MaxUint64-row.FreeBefore-row.Allocated && after >= row.FreeBefore+row.Allocated+margin {
				if _, err = tx.ExecContext(ctx, "UPDATE quota_metadata_files SET status='RELEASED' WHERE path=? AND status='FREE_PENDING' AND file_identity=? AND digest=?", row.Path, row.Identity, row.Digest); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (l *Ledger) MetadataCatalogSnapshot(ctx context.Context) ([]MetadataFileRef, string, error) {
	refs := []MetadataFileRef{}
	var digest string
	err := l.withReadLock(ctx, func() error {
		var imported int
		if err := l.db.QueryRowContext(ctx, "SELECT imported,import_digest FROM quota_metadata_state WHERE singleton=1").Scan(&imported, &digest); err != nil {
			return err
		}
		if imported != 1 || !c.ValidDigest(digest) {
			return c.Fail(c.StaleAuthority, "privacy metadata catalog has not completed its initial import")
		}
		rows, err := l.db.QueryContext(ctx, "SELECT path,class FROM quota_metadata_files WHERE status<>'RELEASED' ORDER BY path")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref MetadataFileRef
			var class string
			if err = rows.Scan(&ref.Path, &class); err != nil {
				return err
			}
			ref.Class = Class(class)
			refs = append(refs, ref)
		}
		return rows.Err()
	})
	return refs, digest, err
}

func (l *Ledger) ValidateMetadataFiles(ctx context.Context, root *os.Root, refs []MetadataFileRef) error {
	validated, err := validateMetadataRefs(refs)
	if err != nil {
		return err
	}
	rootPath, rootID, volume, err := metadataRootBinding(l, root)
	if err != nil {
		return err
	}
	files := make([]FileReceipt, 0, len(validated))
	for _, ref := range validated {
		file, measureErr := measureFile(root, ref.Path)
		if measureErr != nil {
			return measureErr
		}
		raw, readErr := fileguard.ReadRegular(root, ref.Path, int64(file.LogicalBytes))
		if readErr != nil {
			return readErr
		}
		if classErr := validateMetadataClass(ref.Path, ref.Class, raw); classErr != nil {
			return classErr
		}
		rechecked, recheckErr := measureFile(root, ref.Path)
		if recheckErr != nil || rechecked != file {
			return errors.Join(recheckErr, c.Fail(c.StaleAuthority, "privacy metadata file changed during validation"))
		}
		files = append(files, file)
	}
	return l.withReadLock(ctx, func() error {
		var imported int
		if err := l.db.QueryRowContext(ctx, "SELECT imported FROM quota_metadata_state WHERE singleton=1").Scan(&imported); err != nil {
			return err
		}
		if imported != 1 {
			return c.Fail(c.StaleAuthority, "privacy metadata catalog import is incomplete")
		}
		tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = validateMetadataRowsTx(ctx, tx, rootPath, rootID, volume, validated, files); err != nil {
			return err
		}
		return tx.Commit()
	})
}
