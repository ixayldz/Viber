package resource

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const (
	quotaHandoffSchema = 1
	maxHandoffRecords  = 4096
	maxHandoffPath     = 2048
	maxHandoffSchema   = 4096
)

// HandoffState is the durable lifecycle of a single stage-to-target move.
type HandoffState string

const (
	HandoffPrepared   HandoffState = "PREPARED"
	HandoffStageBound HandoffState = "STAGE_BOUND"
	HandoffCommitted  HandoffState = "COMMITTED"
)

const quotaHandoffsDDL = `CREATE TABLE IF NOT EXISTS quota_handoffs (
	allocation_id TEXT NOT NULL PRIMARY KEY CHECK(length(CAST(allocation_id AS BLOB))=32) REFERENCES quota_allocations(id),
	schema_version INTEGER NOT NULL CHECK(schema_version=1),
	operation_digest TEXT NOT NULL CHECK(length(CAST(operation_digest AS BLOB))=64),
	stage_root TEXT NOT NULL CHECK(length(CAST(stage_root AS BLOB)) BETWEEN 1 AND 2048),
	final_root TEXT NOT NULL CHECK(length(CAST(final_root AS BLOB)) BETWEEN 1 AND 2048),
	parent_path TEXT NOT NULL CHECK(length(CAST(parent_path AS BLOB)) BETWEEN 1 AND 2048),
	parent_identity TEXT NOT NULL CHECK(length(CAST(parent_identity AS BLOB))=64),
	volume_identity TEXT NOT NULL CHECK(length(CAST(volume_identity AS BLOB))=64),
	stage_identity TEXT NOT NULL CHECK(length(CAST(stage_identity AS BLOB)) IN (0,64)),
	state TEXT NOT NULL CHECK(state IN ('PREPARED','STAGE_BOUND','COMMITTED')),
	CHECK((state='PREPARED' AND stage_identity='') OR (state IN ('STAGE_BOUND','COMMITTED') AND length(CAST(stage_identity AS BLOB))=64))
)`

type handoffIntent struct {
	AllocationID    string
	SchemaVersion   int
	OperationDigest string
	StageRoot       string
	FinalRoot       string
	ParentPath      string
	ParentIdentity  string
	VolumeIdentity  string
	StageIdentity   string
	State           HandoffState
}

type handoffAllocation struct {
	Status         string
	RequestDigest  string
	TargetRoot     string
	Volume         string
	ParentPath     string
	ParentIdentity string
	TargetIdentity string
	TargetExisted  int
	UpperBytes     int64
	UpperEntries   int64
	RetainedBytes  int64
	RetainedItems  int64
	Receipt        []byte
}

type handoffOperationBinding struct {
	SchemaVersion  int    `json:"schema_version"`
	LedgerID       string `json:"ledger_id"`
	AllocationID   string `json:"allocation_id"`
	RequestDigest  string `json:"request_digest"`
	StageRoot      string `json:"stage_root"`
	FinalRoot      string `json:"final_root"`
	ParentPath     string `json:"parent_path"`
	ParentIdentity string `json:"parent_identity"`
	VolumeIdentity string `json:"volume_identity"`
}

// ensureHandoffSchema creates the bounded intent table for existing ledgers
// and rejects schema drift. OpenPinned calls this under its metadata admission
// before returning a ledger; public handoff operations repeat the check while
// holding the cross-process ledger lock.
func ensureHandoffSchema(db *sql.DB) error {
	if db == nil {
		return c.Fail(c.InvalidArgument, "physical quota database required")
	}
	if _, err := db.Exec(quotaHandoffsDDL); err != nil {
		return err
	}
	var schemaBytes int64
	if err := db.QueryRow("SELECT length(CAST(sql AS BLOB)) FROM sqlite_master WHERE type='table' AND name='quota_handoffs'").Scan(&schemaBytes); err != nil {
		return err
	}
	if schemaBytes <= 0 || schemaBytes > maxHandoffSchema {
		return c.Fail(c.StoreIntegrityError, "physical quota handoff schema is invalid")
	}
	var schemaSQL string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='quota_handoffs'").Scan(&schemaSQL); err != nil {
		return err
	}
	storedDDL := strings.ToUpper(strings.Join(strings.Fields(schemaSQL), " "))
	expectedDDL := strings.ToUpper(strings.Join(strings.Fields(quotaHandoffsDDL), " "))
	expectedDDL = strings.Replace(expectedDDL, "CREATE TABLE IF NOT EXISTS QUOTA_HANDOFFS", "CREATE TABLE QUOTA_HANDOFFS", 1)
	if storedDDL != expectedDDL {
		return c.Fail(c.StoreIntegrityError, "physical quota handoff schema is invalid")
	}
	rows, err := db.Query("PRAGMA table_info(quota_handoffs)")
	if err != nil {
		return err
	}
	want := []struct{ name, kind string }{
		{"allocation_id", "TEXT"}, {"schema_version", "INTEGER"}, {"operation_digest", "TEXT"},
		{"stage_root", "TEXT"}, {"final_root", "TEXT"}, {"parent_path", "TEXT"},
		{"parent_identity", "TEXT"}, {"volume_identity", "TEXT"}, {"stage_identity", "TEXT"}, {"state", "TEXT"},
	}
	columns := make([]struct{ name, kind string }, 0, len(want))
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, struct{ name, kind string }{name, strings.ToUpper(kind)})
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(columns) != len(want) {
		return c.Fail(c.StoreIntegrityError, "physical quota handoff schema is invalid")
	}
	for i := range want {
		if columns[i] != want[i] {
			return c.Fail(c.StoreIntegrityError, "physical quota handoff schema is invalid")
		}
	}
	var triggers int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='quota_handoffs'").Scan(&triggers); err != nil {
		return err
	}
	if triggers != 0 {
		return c.Fail(c.StoreIntegrityError, "physical quota handoff triggers are not permitted")
	}
	foreignKeys, err := db.Query("PRAGMA foreign_key_list(quota_handoffs)")
	if err != nil {
		return err
	}
	type foreignKey struct{ table, from, to string }
	keys := []foreignKey{}
	for foreignKeys.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err = foreignKeys.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			foreignKeys.Close()
			return err
		}
		keys = append(keys, foreignKey{table, from, to})
	}
	if err = errors.Join(foreignKeys.Err(), foreignKeys.Close()); err != nil {
		return err
	}
	if len(keys) != 1 || keys[0] != (foreignKey{"quota_allocations", "allocation_id", "id"}) {
		return c.Fail(c.StoreIntegrityError, "physical quota handoff allocation binding is invalid")
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM quota_handoffs").Scan(&count); err != nil {
		return err
	}
	if count < 0 || count > maxHandoffRecords {
		return c.Fail(c.BudgetLimitReached, "physical quota handoff catalog exceeds its bound")
	}
	return nil
}

// validateHandoffRecordsOnOpen checks that every intent is bounded and bound
// to exactly one existing allocation. Incomplete moves remain WRITING and
// charged; malformed or detached metadata blocks ledger attachment.
func validateHandoffRecordsOnOpen(db *sql.DB, _ *os.Root, _ Authority, ledgerID string) error {
	if !validID(ledgerID) {
		return c.Fail(c.StoreIntegrityError, "physical quota handoff ledger binding is invalid")
	}
	if err := ensureHandoffSchema(db); err != nil {
		return err
	}
	if err := validateHandoffRecordBounds(db); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT allocation_id,schema_version,operation_digest,stage_root,final_root,parent_path,parent_identity,volume_identity,stage_identity,state
		FROM quota_handoffs ORDER BY allocation_id LIMIT ?`, maxHandoffRecords+1)
	if err != nil {
		return err
	}
	intents := make([]handoffIntent, 0, 16)
	for rows.Next() {
		var item handoffIntent
		var schemaVersion int64
		if err = rows.Scan(&item.AllocationID, &schemaVersion, &item.OperationDigest, &item.StageRoot, &item.FinalRoot,
			&item.ParentPath, &item.ParentIdentity, &item.VolumeIdentity, &item.StageIdentity, &item.State); err != nil {
			rows.Close()
			return err
		}
		if schemaVersion < 0 || schemaVersion > int64(^uint(0)>>1) {
			rows.Close()
			return c.Fail(c.StoreIntegrityError, "physical quota handoff schema version is invalid")
		}
		item.SchemaVersion = int(schemaVersion)
		intents = append(intents, item)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(intents) > maxHandoffRecords {
		return c.Fail(c.BudgetLimitReached, "physical quota handoff catalog exceeds its bound")
	}
	for _, item := range intents {
		allocation, err := loadHandoffAllocation(db, item.AllocationID)
		if err != nil {
			return err
		}
		if err = validateHandoffBinding(item, allocation, ledgerID); err != nil {
			return err
		}
		if err = verifyHandoffParent(item); err != nil {
			return err
		}
		if item.State == HandoffStageBound || item.State == HandoffCommitted {
			if err = validateBoundHandoffLocation(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateHandoffRecordBounds(db *sql.DB) error {
	var invalid int
	err := db.QueryRow(`SELECT 1 FROM quota_handoffs WHERE
		typeof(allocation_id)!='text' OR length(CAST(allocation_id AS BLOB))!=32 OR
		typeof(schema_version)!='integer' OR schema_version!=1 OR
		typeof(operation_digest)!='text' OR length(CAST(operation_digest AS BLOB))!=64 OR
		typeof(stage_root)!='text' OR length(CAST(stage_root AS BLOB)) NOT BETWEEN 1 AND 2048 OR
		typeof(final_root)!='text' OR length(CAST(final_root AS BLOB)) NOT BETWEEN 1 AND 2048 OR
		typeof(parent_path)!='text' OR length(CAST(parent_path AS BLOB)) NOT BETWEEN 1 AND 2048 OR
		typeof(parent_identity)!='text' OR length(CAST(parent_identity AS BLOB))!=64 OR
		typeof(volume_identity)!='text' OR length(CAST(volume_identity AS BLOB))!=64 OR
		typeof(stage_identity)!='text' OR length(CAST(stage_identity AS BLOB)) NOT IN (0,64) OR
		typeof(state)!='text' OR length(CAST(state AS BLOB)) NOT BETWEEN 1 AND 16 OR
		(state='PREPARED' AND stage_identity!='') OR
		(state IN ('STAGE_BOUND','COMMITTED') AND length(CAST(stage_identity AS BLOB))!=64) OR
		state NOT IN ('PREPARED','STAGE_BOUND','COMMITTED')
		LIMIT 1`).Scan(&invalid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.Fail(c.StoreIntegrityError, "physical quota handoff record exceeds its bounds")
}

// PrepareHandoff durably binds a reserved stage allocation to a sibling final
// path. Call it after Reserve and before Begin or creating the stage directory.
// Only a same-parent move is supported, so the rename cannot cross volumes.
func (a Allocation) PrepareHandoff(ctx context.Context, finalRoot string) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	stage, err := canonicalHandoffPath(a.targetRoot)
	if err != nil {
		return err
	}
	final, err := canonicalHandoffPath(finalRoot)
	if err != nil {
		return err
	}
	if stage == final || filepath.Dir(stage) != filepath.Dir(final) || filepath.Base(stage) == "." || filepath.Base(final) == "." {
		return c.Fail(c.UnsupportedCapability, "physical quota handoff requires distinct sibling paths")
	}
	if _, err = os.Lstat(stage); err == nil {
		return c.Fail(c.StaleAuthority, "physical quota handoff stage must not exist before preparation")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = os.Lstat(final); err == nil {
		return c.Fail(c.Conflict, "physical quota handoff target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stageParent, stageVolume, parentPath, parentIdentity, _, stageExisted, _, err := targetVolume(stage)
	if err != nil {
		return err
	}
	finalTarget, finalVolume, finalParent, finalParentIdentity, _, finalExisted, _, err := targetVolume(final)
	if err != nil {
		return err
	}
	if stageParent != stage || finalTarget != final || stageExisted != 0 || finalExisted != 0 ||
		filepath.Clean(parentPath) != filepath.Clean(finalParent) || parentIdentity != finalParentIdentity || stageVolume != finalVolume {
		return c.Fail(c.StaleAuthority, "physical quota handoff paths do not share one stable parent and volume")
	}
	return a.ledger.withLock(ctx, func() error {
		if err := ensureHandoffSchema(a.ledger.db); err != nil {
			return err
		}
		if err := validateHandoffRecordsOnOpen(a.ledger.db, a.ledger.root, a.ledger.Authority, a.ledger.LedgerID); err != nil {
			return err
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if filepath.Clean(allocation.TargetRoot) != stage || allocation.Volume != stageVolume ||
			filepath.Clean(allocation.ParentPath) != filepath.Clean(parentPath) || allocation.ParentIdentity != parentIdentity ||
			allocation.TargetExisted != 0 || allocation.TargetIdentity != parentIdentity {
			return c.Fail(c.StaleAuthority, "physical quota handoff differs from its reserved stage witness")
		}
		if allocation.Status != "RESERVED" {
			return c.Fail(c.StaleAuthority, "physical quota handoff must be prepared before writer dispatch")
		}
		intent := handoffIntent{AllocationID: a.ID, SchemaVersion: quotaHandoffSchema, StageRoot: stage, FinalRoot: final,
			ParentPath: filepath.Clean(parentPath), ParentIdentity: parentIdentity, VolumeIdentity: stageVolume, State: HandoffPrepared}
		intent.OperationDigest, err = handoffOperationDigest(a.ledger, allocation, intent)
		if err != nil {
			return err
		}
		prior, found, err := loadHandoffIntent(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if found {
			if prior.OperationDigest != intent.OperationDigest || prior.StageRoot != intent.StageRoot || prior.FinalRoot != intent.FinalRoot ||
				prior.ParentPath != intent.ParentPath || prior.ParentIdentity != intent.ParentIdentity || prior.VolumeIdentity != intent.VolumeIdentity {
				return c.Fail(c.CommandIDConflict, "physical quota handoff allocation was rebound")
			}
			return nil
		}
		var count int
		if err = a.ledger.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_handoffs").Scan(&count); err != nil {
			return err
		}
		if count >= maxHandoffRecords {
			return c.Fail(c.BudgetLimitReached, "physical quota handoff catalog is full")
		}
		if err = admitMetadata(a.ledger.root, metadataAdmission); err != nil {
			return err
		}
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, `INSERT INTO quota_handoffs(allocation_id,schema_version,operation_digest,stage_root,final_root,parent_path,parent_identity,volume_identity,stage_identity,state)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, intent.AllocationID, intent.SchemaVersion, intent.OperationDigest, intent.StageRoot, intent.FinalRoot,
			intent.ParentPath, intent.ParentIdentity, intent.VolumeIdentity, "", string(intent.State))
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// BindHandoffStage records the inode created at the prepared stage path. It
// must run before the first staged write. An unbound nonempty stage is never
// adopted after a crash.
func (a Allocation) BindHandoffStage(ctx context.Context) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	return a.ledger.withLock(ctx, func() error {
		if err := ensureHandoffSchema(a.ledger.db); err != nil {
			return err
		}
		if err := validateHandoffRecordsOnOpen(a.ledger.db, a.ledger.root, a.ledger.Authority, a.ledger.LedgerID); err != nil {
			return err
		}
		intent, found, err := loadHandoffIntent(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if !found || filepath.Clean(a.targetRoot) != intent.StageRoot {
			return c.Fail(c.StaleAuthority, "prepared physical quota handoff required")
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if err = validateHandoffBinding(intent, allocation, a.ledger.LedgerID); err != nil {
			return err
		}
		if intent.State == HandoffStageBound || intent.State == HandoffCommitted {
			return verifyBoundHandoffPresence(intent)
		}
		if intent.State != HandoffPrepared || allocation.Status != "WRITING" {
			return c.Fail(c.StaleAuthority, "physical quota handoff stage cannot be bound before Begin")
		}
		if err = verifyHandoffParent(intent); err != nil {
			return err
		}
		if _, err = os.Lstat(intent.FinalRoot); err == nil {
			return c.Fail(c.Conflict, "physical quota handoff target appeared before stage binding")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		stageRoot, err := os.OpenRoot(intent.StageRoot)
		if err != nil {
			return err
		}
		identity, identityErr := fileguard.DirectoryIdentity(stageRoot)
		volume, volumeErr := fileguard.VolumeIdentity(stageRoot)
		empty, emptyErr := emptyDirectory(stageRoot)
		closeErr := stageRoot.Close()
		if err = errors.Join(identityErr, volumeErr, emptyErr, closeErr); err != nil {
			return err
		}
		if !empty {
			return c.Fail(c.StaleAuthority, "unbound physical quota handoff stage is not empty")
		}
		if volume != intent.VolumeIdentity || !c.ValidDigest(identity) {
			return c.Fail(c.StaleAuthority, "physical quota handoff stage identity or volume changed")
		}
		if err = admitMetadata(a.ledger.root, metadataAdmission); err != nil {
			return err
		}
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, "UPDATE quota_handoffs SET stage_identity=?,state=? WHERE allocation_id=? AND state=? AND stage_identity=''", identity, string(HandoffStageBound), a.ID, string(HandoffPrepared))
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "physical quota handoff preparation changed before stage binding")
		}
		return tx.Commit()
	})
}

// CommitHandoff performs the no-replace sibling rename itself, syncs the
// containing directory, measures the final root, and atomically settles the
// same allocation row at the final path. It returns an Allocation bound to
// that final path. If the process dies after rename, an exact retry adopts
// only the final directory whose identity was durably bound by BindHandoffStage.
func (a Allocation) CommitHandoff(ctx context.Context) (Allocation, error) {
	if a.ledger == nil || !validID(a.ID) {
		return Allocation{}, c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	var settled Allocation
	err := a.ledger.withLock(ctx, func() error {
		if err := ensureHandoffSchema(a.ledger.db); err != nil {
			return err
		}
		if err := validateHandoffRecordsOnOpen(a.ledger.db, a.ledger.root, a.ledger.Authority, a.ledger.LedgerID); err != nil {
			return err
		}
		intent, found, err := loadHandoffIntent(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if !found || filepath.Clean(a.targetRoot) != intent.StageRoot {
			return c.Fail(c.StaleAuthority, "prepared physical quota handoff required")
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if err = validateHandoffBinding(intent, allocation, a.ledger.LedgerID); err != nil {
			return err
		}
		if intent.State == HandoffCommitted {
			if allocation.Status != "RETAINED" && allocation.Status != "FREE_PENDING" {
				return c.Fail(c.StoreIntegrityError, "committed physical quota handoff has an invalid allocation lifecycle")
			}
			root, openErr := os.OpenRoot(intent.FinalRoot)
			if openErr != nil {
				return openErr
			}
			inventory, measureErr := Measure(root, true)
			closeErr := root.Close()
			if err = errors.Join(measureErr, closeErr); err != nil {
				return err
			}
			raw, err := jsonMarshalInventory(inventory)
			if err != nil {
				return err
			}
			if !sameAllocationReceipt(allocation, raw, inventory) {
				return c.Fail(c.StaleAuthority, "committed physical quota handoff receipt changed")
			}
			settled = Allocation{ledger: a.ledger, ID: a.ID, targetRoot: intent.FinalRoot}
			return nil
		}
		if intent.State != HandoffStageBound || allocation.Status != "WRITING" {
			return c.Fail(c.StaleAuthority, "bound physical quota handoff stage required")
		}
		if err = verifyHandoffParent(intent); err != nil {
			return err
		}
		if err = moveHandoffRoot(intent); err != nil {
			return err
		}
		root, err := os.OpenRoot(intent.FinalRoot)
		if err != nil {
			return err
		}
		rootIdentity, identityErr := fileguard.DirectoryIdentity(root)
		volume, volumeErr := fileguard.VolumeIdentity(root)
		inventory, measureErr := Measure(root, true)
		closeErr := root.Close()
		if err = errors.Join(identityErr, volumeErr, measureErr, closeErr); err != nil {
			return err
		}
		if rootIdentity != intent.StageIdentity || inventory.RootIdentity != intent.StageIdentity || volume != intent.VolumeIdentity ||
			filepath.Clean(inventory.RootPath) != intent.FinalRoot {
			return c.Fail(c.StaleAuthority, "physical quota handoff final root differs from the bound stage inode")
		}
		raw, err := jsonMarshalInventory(inventory)
		if err != nil {
			return err
		}
		if err = admitReceiptMutation(a.ledger.root, raw); err != nil {
			return err
		}
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		current, err := loadHandoffAllocationTx(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		currentIntent, found, err := loadHandoffIntentTx(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if !found || current.Status != "WRITING" || currentIntent.OperationDigest != intent.OperationDigest ||
			currentIntent.State != HandoffStageBound || currentIntent.StageIdentity != intent.StageIdentity ||
			current.TargetRoot != intent.StageRoot || current.Volume != intent.VolumeIdentity {
			return c.Fail(c.StaleAuthority, "physical quota handoff changed before settlement")
		}
		_, err = tx.ExecContext(ctx, `UPDATE quota_allocations SET target_root=?,parent_path=?,parent_identity=?,target_identity=?,target_existed=1,
			status='RETAINED',retained_bytes=?,retained_entries=?,receipt=? WHERE id=? AND status='WRITING'`, intent.FinalRoot, intent.ParentPath,
			intent.ParentIdentity, intent.StageIdentity, inventory.ChargedBytes, inventory.Entries, raw, a.ID)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE quota_handoffs SET state=? WHERE allocation_id=? AND state=? AND operation_digest=?", string(HandoffCommitted), a.ID, string(HandoffStageBound), intent.OperationDigest)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "physical quota handoff intent changed before settlement")
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if inventory.ChargedBytes > uint64(allocation.UpperBytes) || inventory.Entries > uint64(allocation.UpperEntries) {
			return c.Fail(c.BudgetLimitReached, "writer exceeded its physical quota reservation; moved output remains charged")
		}
		settled = Allocation{ledger: a.ledger, ID: a.ID, targetRoot: intent.FinalRoot}
		return nil
	})
	if err != nil {
		return Allocation{}, err
	}
	return settled, nil
}

// HandoffAllocation reloads the durable stage binding after a process restart.
// The returned allocation remains stage-bound for exact retry; CommitHandoff
// returns the final-path allocation after successful settlement.
func (l *Ledger) HandoffAllocation(ctx context.Context, allocationID string) (Allocation, HandoffState, error) {
	if l == nil || !validID(allocationID) {
		return Allocation{}, "", c.Fail(c.InvalidArgument, "physical quota allocation ID required")
	}
	var allocation Allocation
	var state HandoffState
	err := l.withLock(ctx, func() error {
		if err := ensureHandoffSchema(l.db); err != nil {
			return err
		}
		if err := validateHandoffRecordsOnOpen(l.db, l.root, l.Authority, l.LedgerID); err != nil {
			return err
		}
		intent, found, err := loadHandoffIntent(l.db, allocationID)
		if err != nil {
			return err
		}
		if !found {
			return c.Fail(c.StaleAuthority, "physical quota allocation has no handoff intent")
		}
		allocation = Allocation{ledger: l, ID: allocationID, targetRoot: intent.StageRoot}
		state = intent.State
		return nil
	})
	return allocation, state, err
}

func canonicalHandoffPath(name string) (string, error) {
	if name == "" || !filepath.IsAbs(name) || len(name) > maxHandoffPath {
		return "", c.Fail(c.InvalidArgument, "bounded absolute physical quota handoff path required")
	}
	resolved, err := fileguard.ResolveProspective(name)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if !filepath.IsAbs(resolved) || len(resolved) > maxHandoffPath {
		return "", c.Fail(c.UnsupportedCapability, "physical quota handoff path exceeds its bound")
	}
	return resolved, nil
}

func handoffOperationDigest(ledger *Ledger, allocation handoffAllocation, intent handoffIntent) (string, error) {
	return c.Digest(handoffOperationBinding{SchemaVersion: quotaHandoffSchema, LedgerID: ledger.LedgerID, AllocationID: intent.AllocationID,
		RequestDigest: allocation.RequestDigest, StageRoot: intent.StageRoot, FinalRoot: intent.FinalRoot, ParentPath: intent.ParentPath,
		ParentIdentity: intent.ParentIdentity, VolumeIdentity: intent.VolumeIdentity})
}

func validateHandoffBinding(intent handoffIntent, allocation handoffAllocation, ledgerID string) error {
	if intent.SchemaVersion != quotaHandoffSchema || !validID(intent.AllocationID) || !c.ValidDigest(intent.OperationDigest) ||
		!c.ValidDigest(allocation.RequestDigest) || !filepath.IsAbs(intent.StageRoot) || !filepath.IsAbs(intent.FinalRoot) ||
		!filepath.IsAbs(intent.ParentPath) || len(intent.StageRoot) > maxHandoffPath || len(intent.FinalRoot) > maxHandoffPath ||
		len(intent.ParentPath) > maxHandoffPath || filepath.Clean(intent.StageRoot) != intent.StageRoot || filepath.Clean(intent.FinalRoot) != intent.FinalRoot ||
		filepath.Clean(intent.ParentPath) != intent.ParentPath || filepath.Dir(intent.StageRoot) != intent.ParentPath || filepath.Dir(intent.FinalRoot) != intent.ParentPath ||
		intent.StageRoot == intent.FinalRoot || !c.ValidDigest(intent.ParentIdentity) || !c.ValidDigest(intent.VolumeIdentity) ||
		(intent.State != HandoffPrepared && intent.State != HandoffStageBound && intent.State != HandoffCommitted) {
		return c.Fail(c.StoreIntegrityError, "physical quota handoff intent is invalid")
	}
	if intent.State == HandoffPrepared && intent.StageIdentity != "" ||
		(intent.State == HandoffStageBound || intent.State == HandoffCommitted) && !c.ValidDigest(intent.StageIdentity) {
		return c.Fail(c.StoreIntegrityError, "physical quota handoff stage identity is invalid")
	}
	if ledgerID != "" {
		operation, err := handoffOperationDigestFromID(ledgerID, intent, allocation.RequestDigest)
		if err != nil || operation != intent.OperationDigest {
			return c.Fail(c.StoreIntegrityError, "physical quota handoff operation digest is invalid")
		}
	}
	if intent.State == HandoffCommitted {
		if (allocation.Status != "RETAINED" && allocation.Status != "FREE_PENDING") || filepath.Clean(allocation.TargetRoot) != intent.FinalRoot ||
			allocation.Volume != intent.VolumeIdentity || filepath.Clean(allocation.ParentPath) != intent.ParentPath ||
			allocation.ParentIdentity != intent.ParentIdentity || allocation.TargetIdentity != intent.StageIdentity || allocation.TargetExisted != 1 ||
			allocation.RetainedBytes < 0 || allocation.RetainedItems < 0 || len(allocation.Receipt) == 0 {
			return c.Fail(c.StoreIntegrityError, "committed physical quota handoff differs from its settled allocation")
		}
		var inventory Inventory
		if strictJSON(allocation.Receipt, &inventory) != nil || filepath.Clean(inventory.RootPath) != intent.FinalRoot ||
			inventory.RootIdentity != intent.StageIdentity || inventory.VolumeIdentity != intent.VolumeIdentity ||
			inventory.ChargedBytes != uint64(allocation.RetainedBytes) || inventory.Entries != uint64(allocation.RetainedItems) {
			return c.Fail(c.StoreIntegrityError, "committed physical quota handoff receipt is invalid")
		}
	} else {
		if (allocation.Status != "RESERVED" && allocation.Status != "WRITING") || filepath.Clean(allocation.TargetRoot) != intent.StageRoot ||
			allocation.Volume != intent.VolumeIdentity || filepath.Clean(allocation.ParentPath) != intent.ParentPath ||
			allocation.ParentIdentity != intent.ParentIdentity || allocation.TargetIdentity != intent.ParentIdentity || allocation.TargetExisted != 0 ||
			len(allocation.Receipt) != 0 {
			return c.Fail(c.StoreIntegrityError, "unfinished physical quota handoff differs from its outstanding allocation")
		}
		if intent.State == HandoffStageBound && allocation.Status != "WRITING" {
			return c.Fail(c.StoreIntegrityError, "bound physical quota handoff has not begun")
		}
	}
	return nil
}

func handoffOperationDigestFromID(ledgerID string, intent handoffIntent, requestDigest string) (string, error) {
	return c.Digest(handoffOperationBinding{SchemaVersion: quotaHandoffSchema, LedgerID: ledgerID, AllocationID: intent.AllocationID,
		RequestDigest: requestDigest, StageRoot: intent.StageRoot, FinalRoot: intent.FinalRoot, ParentPath: intent.ParentPath,
		ParentIdentity: intent.ParentIdentity, VolumeIdentity: intent.VolumeIdentity})
}

func verifyHandoffParent(intent handoffIntent) error {
	resolved, err := filepath.EvalSymlinks(intent.ParentPath)
	if err != nil || filepath.Clean(resolved) != intent.ParentPath {
		return c.Fail(c.StaleAuthority, "physical quota handoff parent path changed")
	}
	parent, err := os.OpenRoot(intent.ParentPath)
	if err != nil {
		return err
	}
	identity, identityErr := fileguard.DirectoryIdentity(parent)
	volume, volumeErr := fileguard.VolumeIdentity(parent)
	closeErr := parent.Close()
	if err = errors.Join(identityErr, volumeErr, closeErr); err != nil {
		return err
	}
	if identity != intent.ParentIdentity || volume != intent.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "physical quota handoff parent identity or volume changed")
	}
	return nil
}

func validateBoundHandoffLocation(intent handoffIntent) error {
	stageInfo, stageErr := os.Lstat(intent.StageRoot)
	finalInfo, finalErr := os.Lstat(intent.FinalRoot)
	stageExists := stageErr == nil
	finalExists := finalErr == nil
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) || finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
		return errors.Join(stageErr, finalErr)
	}
	if intent.State == HandoffCommitted {
		if stageExists || !finalExists {
			return c.Fail(c.StaleAuthority, "committed physical quota handoff is not at its final path")
		}
	} else if stageExists == finalExists {
		return c.Fail(c.StaleAuthority, "physical quota handoff has an ambiguous stage/target location")
	}
	path := intent.StageRoot
	info := stageInfo
	if finalExists {
		path, info = intent.FinalRoot, finalInfo
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return c.Fail(c.StaleAuthority, "physical quota handoff root is not a real directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	identity, identityErr := fileguard.DirectoryIdentity(root)
	volume, volumeErr := fileguard.VolumeIdentity(root)
	closeErr := root.Close()
	if err = errors.Join(identityErr, volumeErr, closeErr); err != nil {
		return err
	}
	if identity != intent.StageIdentity || volume != intent.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "physical quota handoff root identity or volume changed")
	}
	return nil
}

func verifyBoundHandoffPresence(intent handoffIntent) error {
	return validateBoundHandoffLocation(intent)
}

func moveHandoffRoot(intent handoffIntent) error {
	if err := validateBoundHandoffLocation(intent); err != nil {
		return err
	}
	stageInfo, stageErr := os.Lstat(intent.StageRoot)
	_, finalErr := os.Lstat(intent.FinalRoot)
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) || finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
		return errors.Join(stageErr, finalErr)
	}
	if stageErr == nil && finalErr == nil {
		return c.Fail(c.StaleAuthority, "physical quota handoff stage and target both exist")
	}
	if stageErr == nil {
		if !stageInfo.IsDir() || stageInfo.Mode()&os.ModeSymlink != 0 {
			return c.Fail(c.StaleAuthority, "physical quota handoff stage is not a directory")
		}
		parent, err := os.OpenRoot(intent.ParentPath)
		if err != nil {
			return err
		}
		err = fileguard.RenameNoReplace(parent, filepath.Base(intent.StageRoot), filepath.Base(intent.FinalRoot))
		closeErr := parent.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return err
		}
	}
	return validateBoundHandoffLocation(handoffIntent{StageRoot: intent.StageRoot, FinalRoot: intent.FinalRoot,
		ParentPath: intent.ParentPath, ParentIdentity: intent.ParentIdentity, VolumeIdentity: intent.VolumeIdentity,
		StageIdentity: intent.StageIdentity, State: HandoffStageBound})
}

func emptyDirectory(root *os.Root) (bool, error) {
	if root == nil {
		return false, c.Fail(c.InvalidArgument, "physical quota handoff stage root required")
	}
	directory, err := root.Open(".")
	if err != nil {
		return false, err
	}
	names, err := directory.Readdirnames(1)
	closeErr := directory.Close()
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err = errors.Join(err, closeErr); err != nil {
		return false, err
	}
	return len(names) == 0, nil
}

func loadHandoffIntent(db *sql.DB, allocationID string) (handoffIntent, bool, error) {
	var intent handoffIntent
	var schemaVersion int64
	err := db.QueryRow("SELECT allocation_id,schema_version,operation_digest,stage_root,final_root,parent_path,parent_identity,volume_identity,stage_identity,state FROM quota_handoffs WHERE allocation_id=?", allocationID).
		Scan(&intent.AllocationID, &schemaVersion, &intent.OperationDigest, &intent.StageRoot, &intent.FinalRoot, &intent.ParentPath,
			&intent.ParentIdentity, &intent.VolumeIdentity, &intent.StageIdentity, &intent.State)
	if errors.Is(err, sql.ErrNoRows) {
		return handoffIntent{}, false, nil
	}
	if err != nil {
		return intent, false, err
	}
	if schemaVersion < 0 || schemaVersion > int64(^uint(0)>>1) {
		return intent, false, c.Fail(c.StoreIntegrityError, "physical quota handoff schema version is invalid")
	}
	intent.SchemaVersion = int(schemaVersion)
	return intent, true, nil
}

func loadHandoffIntentTx(ctx context.Context, tx *sql.Tx, allocationID string) (handoffIntent, bool, error) {
	var intent handoffIntent
	var schemaVersion int64
	err := tx.QueryRowContext(ctx, "SELECT allocation_id,schema_version,operation_digest,stage_root,final_root,parent_path,parent_identity,volume_identity,stage_identity,state FROM quota_handoffs WHERE allocation_id=?", allocationID).
		Scan(&intent.AllocationID, &schemaVersion, &intent.OperationDigest, &intent.StageRoot, &intent.FinalRoot, &intent.ParentPath,
			&intent.ParentIdentity, &intent.VolumeIdentity, &intent.StageIdentity, &intent.State)
	if errors.Is(err, sql.ErrNoRows) {
		return handoffIntent{}, false, nil
	}
	if err != nil {
		return intent, false, err
	}
	if schemaVersion < 0 || schemaVersion > int64(^uint(0)>>1) {
		return intent, false, c.Fail(c.StoreIntegrityError, "physical quota handoff schema version is invalid")
	}
	intent.SchemaVersion = int(schemaVersion)
	return intent, true, nil
}

func loadHandoffAllocation(db *sql.DB, allocationID string) (handoffAllocation, error) {
	var allocation handoffAllocation
	err := db.QueryRow(`SELECT status,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,
		upper_bytes,upper_entries,retained_bytes,retained_entries,receipt FROM quota_allocations WHERE id=?`, allocationID).Scan(
		&allocation.Status, &allocation.RequestDigest, &allocation.TargetRoot, &allocation.Volume, &allocation.ParentPath,
		&allocation.ParentIdentity, &allocation.TargetIdentity, &allocation.TargetExisted, &allocation.UpperBytes,
		&allocation.UpperEntries, &allocation.RetainedBytes, &allocation.RetainedItems, &allocation.Receipt)
	return allocation, err
}

func loadHandoffAllocationTx(ctx context.Context, tx *sql.Tx, allocationID string) (handoffAllocation, error) {
	var allocation handoffAllocation
	err := tx.QueryRowContext(ctx, `SELECT status,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,
		upper_bytes,upper_entries,retained_bytes,retained_entries,receipt FROM quota_allocations WHERE id=?`, allocationID).Scan(
		&allocation.Status, &allocation.RequestDigest, &allocation.TargetRoot, &allocation.Volume, &allocation.ParentPath,
		&allocation.ParentIdentity, &allocation.TargetIdentity, &allocation.TargetExisted, &allocation.UpperBytes,
		&allocation.UpperEntries, &allocation.RetainedBytes, &allocation.RetainedItems, &allocation.Receipt)
	return allocation, err
}

func sameAllocationReceipt(allocation handoffAllocation, raw []byte, inventory Inventory) bool {
	return allocation.RetainedBytes >= 0 && allocation.RetainedItems >= 0 &&
		uint64(allocation.RetainedBytes) == inventory.ChargedBytes && uint64(allocation.RetainedItems) == inventory.Entries &&
		string(allocation.Receipt) == string(raw)
}
