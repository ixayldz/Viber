package resource

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const (
	quotaPartitionSchema     = 1
	maxQuotaPartitions       = 4096
	maxQuotaPartitionMembers = 5
	maxQuotaPartitionSchema  = 8192
	partitionStartupBytes    = uint64(128 << 20)
	partitionStartupEntries  = uint64(16384)
)

type PartitionMode string

const (
	EvaluationReportPartition PartitionMode = "EVALUATION_REPORT"
	IndependentOwnerPartition PartitionMode = "INDEPENDENT_OWNER"
)

// PartitionStartupEnvelope returns the fixed byte and entry allowance for
// evaluator owner metadata in addition to source-derived report contents.
func PartitionStartupEnvelope() (uint64, uint64) {
	return partitionStartupBytes, partitionStartupEntries
}

const quotaPartitionsDDL = `CREATE TABLE IF NOT EXISTS quota_partitions (
	allocation_id TEXT NOT NULL PRIMARY KEY CHECK(length(CAST(allocation_id AS BLOB))=32) REFERENCES quota_allocations(id),
	schema_version INTEGER NOT NULL CHECK(schema_version=1),
	operation_digest TEXT NOT NULL CHECK(length(CAST(operation_digest AS BLOB))=64),
	mode TEXT NOT NULL CHECK(mode IN ('EVALUATION_REPORT','INDEPENDENT_OWNER')),
	root_path TEXT NOT NULL CHECK(length(CAST(root_path AS BLOB)) BETWEEN 1 AND 2048),
	owner_path TEXT NOT NULL UNIQUE CHECK(length(CAST(owner_path AS BLOB)) BETWEEN 1 AND 2048),
	root_identity TEXT NOT NULL CHECK(length(CAST(root_identity AS BLOB)) IN (0,64)),
	owner_identity TEXT NOT NULL CHECK(length(CAST(owner_identity AS BLOB)) IN (0,64)),
	volume_identity TEXT NOT NULL CHECK(length(CAST(volume_identity AS BLOB)) IN (0,64)),
	state TEXT NOT NULL CHECK(state IN ('PREPARED','ROOT_BOUND','OWNER_BOUND','COMMITTED')),
	CHECK((state='PREPARED' AND root_identity='' AND owner_identity='' AND volume_identity='') OR
	 (state='ROOT_BOUND' AND length(CAST(root_identity AS BLOB))=64 AND owner_identity='' AND length(CAST(volume_identity AS BLOB))=64) OR
	 (state IN ('OWNER_BOUND','COMMITTED') AND length(CAST(root_identity AS BLOB))=64 AND length(CAST(owner_identity AS BLOB))=64 AND length(CAST(volume_identity AS BLOB))=64))
)`

const quotaPartitionMembersDDL = `CREATE TABLE IF NOT EXISTS quota_partition_members (
	allocation_id TEXT NOT NULL CHECK(length(CAST(allocation_id AS BLOB))=32) REFERENCES quota_partitions(allocation_id),
	kind TEXT NOT NULL CHECK(kind IN ('REPORT_REMAINDER','OWNER_STATIC','MUTABLE_SQLITE','CONTROL_RESERVE','ARCHIVE')),
	member_id TEXT NOT NULL CHECK(length(CAST(member_id AS BLOB))=32) REFERENCES quota_allocations(id),
	member_root TEXT NOT NULL CHECK(length(CAST(member_root AS BLOB)) BETWEEN 1 AND 2048),
	root_identity TEXT NOT NULL CHECK(length(CAST(root_identity AS BLOB))=64),
	volume_identity TEXT NOT NULL CHECK(length(CAST(volume_identity AS BLOB))=64),
	receipt_digest TEXT NOT NULL CHECK(length(CAST(receipt_digest AS BLOB))=64),
	PRIMARY KEY(allocation_id,kind),
	UNIQUE(allocation_id,member_id)
)`

type quotaPartition struct {
	AllocationID    string
	SchemaVersion   int
	OperationDigest string
	Mode            PartitionMode
	RootPath        string
	OwnerPath       string
	RootIdentity    string
	OwnerIdentity   string
	VolumeIdentity  string
	State           string
}

type quotaPartitionMember struct {
	AllocationID  string
	Kind          string
	MemberID      string
	MemberRoot    string
	RootIdentity  string
	Volume        string
	ReceiptDigest string
}

type partitionBinding struct {
	SchemaVersion  int           `json:"schema_version"`
	LedgerID       string        `json:"ledger_id"`
	AllocationID   string        `json:"allocation_id"`
	RequestDigest  string        `json:"request_digest"`
	Mode           PartitionMode `json:"mode"`
	RootPath       string        `json:"root_path"`
	OwnerPath      string        `json:"owner_path"`
	ParentPath     string        `json:"parent_path"`
	ParentIdentity string        `json:"parent_identity"`
	VolumeIdentity string        `json:"volume_identity"`
}

// PreparePartition durably binds an allocation to the only supported owner
// partition layouts. It must run before Begin and before either root exists.
func (a Allocation) PreparePartition(ctx context.Context, mode PartitionMode) error {
	if a.ledger == nil || !validID(a.ID) || (mode != EvaluationReportPartition && mode != IndependentOwnerPartition) {
		return c.Fail(c.InvalidArgument, "bounded physical quota partition required")
	}
	rootPath, err := canonicalHandoffPath(a.targetRoot)
	if err != nil {
		return err
	}
	ownerPath := rootPath
	if mode == EvaluationReportPartition {
		ownerPath = filepath.Join(rootPath, "owner")
	}
	if len(ownerPath) > maxHandoffPath {
		return c.Fail(c.UnsupportedCapability, "physical quota partition path exceeds its bound")
	}
	if _, err = os.Lstat(rootPath); err == nil {
		return c.Fail(c.StaleAuthority, "physical quota partition root must not exist before preparation")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, volume, parentPath, parentIdentity, _, existed, _, err := targetVolume(rootPath)
	if err != nil {
		return err
	}
	if existed != 0 {
		return c.Fail(c.Conflict, "physical quota partition root already exists")
	}
	return a.ledger.withLock(ctx, func() error {
		if err := ensureQuotaPartitionSchema(a.ledger.db); err != nil {
			return err
		}
		if err := validateQuotaPartitionRecords(a.ledger.db, a.ledger.LedgerID); err != nil {
			return err
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if allocation.Status != "RESERVED" || filepath.Clean(allocation.TargetRoot) != rootPath || allocation.Volume != volume ||
			filepath.Clean(allocation.ParentPath) != filepath.Clean(parentPath) || allocation.ParentIdentity != parentIdentity || allocation.TargetExisted != 0 {
			return c.Fail(c.StaleAuthority, "physical quota partition differs from its reserved root witness")
		}
		if _, found, err := loadHandoffIntent(a.ledger.db, a.ID); err != nil {
			return err
		} else if found {
			return c.Fail(c.Conflict, "physical quota allocation already has a path handoff")
		}
		binding := partitionBinding{SchemaVersion: quotaPartitionSchema, LedgerID: a.ledger.LedgerID, AllocationID: a.ID,
			RequestDigest: allocation.RequestDigest, Mode: mode, RootPath: rootPath, OwnerPath: ownerPath,
			ParentPath: filepath.Clean(parentPath), ParentIdentity: parentIdentity, VolumeIdentity: volume}
		operationDigest, err := c.Digest(binding)
		if err != nil {
			return err
		}
		prior, found, err := loadQuotaPartition(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if found {
			if prior.OperationDigest != operationDigest || prior.Mode != mode || prior.RootPath != rootPath || prior.OwnerPath != ownerPath {
				return c.Fail(c.CommandIDConflict, "physical quota allocation was rebound to another partition")
			}
			return nil
		}
		var count int
		if err = a.ledger.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_partitions").Scan(&count); err != nil {
			return err
		}
		if count >= maxQuotaPartitions {
			return c.Fail(c.BudgetLimitReached, "physical quota partition catalog is full")
		}
		if err = admitMetadata(a.ledger.root, metadataAdmission); err != nil {
			return err
		}
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, `INSERT INTO quota_partitions(allocation_id,schema_version,operation_digest,mode,root_path,owner_path,root_identity,owner_identity,volume_identity,state)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, a.ID, quotaPartitionSchema, operationDigest, string(mode), rootPath, ownerPath, "", "", "", "PREPARED")
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// BindPartitionRoot records a newly created empty output root before the first
// output write. For a standalone evaluator that root is also the owner root.
func (a Allocation) BindPartitionRoot(ctx context.Context) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	return a.ledger.withLock(ctx, func() error {
		if err := ensureQuotaPartitionSchema(a.ledger.db); err != nil {
			return err
		}
		partition, found, err := loadQuotaPartition(a.ledger.db, a.ID)
		if err != nil || !found {
			if err == nil {
				err = c.Fail(c.StaleAuthority, "prepared physical quota partition required")
			}
			return err
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if filepath.Clean(a.targetRoot) != partition.RootPath || allocation.Status != "WRITING" ||
			(partition.State == "PREPARED" && !c.ValidDigest(allocation.Volume)) ||
			(partition.State != "PREPARED" && allocation.Volume != partition.VolumeIdentity) ||
			filepath.Clean(allocation.ParentPath) == "" {
			return c.Fail(c.StaleAuthority, "physical quota partition root is not writing under its admitted witness")
		}
		if partition.State == "ROOT_BOUND" || partition.State == "OWNER_BOUND" || partition.State == "COMMITTED" {
			if err = verifyPartitionDirectory(partition.RootPath, partition.RootIdentity, partition.VolumeIdentity); err != nil {
				return err
			}
			if partition.Mode == IndependentOwnerPartition && partition.State == "OWNER_BOUND" {
				return ensurePartitionOwnerMaintenanceDirs(partition.OwnerPath)
			}
			return nil
		}
		if partition.State != "PREPARED" {
			return c.Fail(c.StoreIntegrityError, "physical quota partition state is invalid")
		}
		if err = verifyPartitionParent(partition, allocation); err != nil {
			return err
		}
		root, err := os.OpenRoot(partition.RootPath)
		if err != nil {
			return err
		}
		identity, identityErr := fileguard.DirectoryIdentity(root)
		volume, volumeErr := fileguard.VolumeIdentity(root)
		empty, emptyErr := emptyDirectory(root)
		closeErr := root.Close()
		if err = errors.Join(identityErr, volumeErr, emptyErr, closeErr); err != nil {
			return err
		}
		if !empty || !c.ValidDigest(identity) || volume != allocation.Volume {
			return c.Fail(c.StaleAuthority, "physical quota partition root is not the admitted empty directory")
		}
		state := "ROOT_BOUND"
		ownerIdentity := ""
		if partition.Mode == IndependentOwnerPartition {
			state = "OWNER_BOUND"
			ownerIdentity = identity
		}
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `UPDATE quota_partitions SET root_identity=?,owner_identity=?,volume_identity=?,state=?
			WHERE allocation_id=? AND state='PREPARED' AND root_identity='' AND owner_identity='' AND volume_identity=''`, identity, ownerIdentity, volume, state, a.ID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "physical quota partition changed before root binding")
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if partition.Mode == IndependentOwnerPartition {
			return ensurePartitionOwnerMaintenanceDirs(partition.OwnerPath)
		}
		return nil
	})
}

// BindPartitionOwner binds the empty evaluator child before the owner store is
// opened and starts writing into it.
func (a Allocation) BindPartitionOwner(ctx context.Context, ownerPath string) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	owner, err := canonicalHandoffPath(ownerPath)
	if err != nil {
		return err
	}
	return a.ledger.withLock(ctx, func() error {
		if err := ensureQuotaPartitionSchema(a.ledger.db); err != nil {
			return err
		}
		partition, found, err := loadQuotaPartition(a.ledger.db, a.ID)
		if err != nil || !found {
			if err == nil {
				err = c.Fail(c.StaleAuthority, "prepared physical quota partition required")
			}
			return err
		}
		if partition.Mode != EvaluationReportPartition || partition.OwnerPath != owner || partition.State == "PREPARED" {
			return c.Fail(c.StaleAuthority, "physical quota evaluator owner differs from its prepared partition")
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if allocation.Status != "WRITING" || filepath.Clean(a.targetRoot) != partition.RootPath {
			return c.Fail(c.StaleAuthority, "physical quota report partition is not writing")
		}
		if partition.State == "OWNER_BOUND" || partition.State == "COMMITTED" {
			if err = verifyPartitionDirectory(owner, partition.OwnerIdentity, partition.VolumeIdentity); err != nil {
				return err
			}
			if partition.State == "OWNER_BOUND" {
				return ensurePartitionOwnerMaintenanceDirs(owner)
			}
			return nil
		}
		if partition.State != "ROOT_BOUND" {
			return c.Fail(c.StoreIntegrityError, "physical quota report partition root is not bound")
		}
		if err = verifyPartitionDirectory(partition.RootPath, partition.RootIdentity, partition.VolumeIdentity); err != nil {
			return err
		}
		root, err := os.OpenRoot(owner)
		if err != nil {
			return err
		}
		identity, identityErr := fileguard.DirectoryIdentity(root)
		volume, volumeErr := fileguard.VolumeIdentity(root)
		empty, emptyErr := emptyDirectory(root)
		closeErr := root.Close()
		if err = errors.Join(identityErr, volumeErr, emptyErr, closeErr); err != nil {
			return err
		}
		if !empty || !c.ValidDigest(identity) || volume != partition.VolumeIdentity {
			return c.Fail(c.StaleAuthority, "physical quota evaluator owner is not an empty bound child")
		}
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `UPDATE quota_partitions SET owner_identity=?,state='OWNER_BOUND'
			WHERE allocation_id=? AND state='ROOT_BOUND' AND owner_identity=''`, identity, a.ID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "physical quota owner binding changed before commit")
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ensurePartitionOwnerMaintenanceDirs(owner)
	})
}

// CommitPartition atomically replaces the broad bootstrap reservation with
// disjoint report/static-owner receipts and the owner's mutable SQLite,
// control-reserve and archive receipts. The owner must be closed first.
func (a Allocation) CommitPartition(ctx context.Context) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	return a.ledger.withLock(ctx, func() error {
		if err := ensureQuotaPartitionSchema(a.ledger.db); err != nil {
			return err
		}
		if err := validateQuotaPartitionRecords(a.ledger.db, a.ledger.LedgerID); err != nil {
			return err
		}
		partition, found, err := loadQuotaPartition(a.ledger.db, a.ID)
		if err != nil || !found {
			if err == nil {
				err = c.Fail(c.StaleAuthority, "prepared physical quota partition required")
			}
			return err
		}
		if partition.State == "COMMITTED" {
			return validateCommittedPartition(ctx, a.ledger, partition)
		}
		if partition.State != "OWNER_BOUND" {
			return c.Fail(c.StaleAuthority, "bound owner partition required before settlement")
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if allocation.Status != "WRITING" || filepath.Clean(a.targetRoot) != partition.RootPath {
			return c.Fail(c.StaleAuthority, "physical quota partition allocation is not writing")
		}
		if err = verifyPartitionDirectory(partition.RootPath, partition.RootIdentity, partition.VolumeIdentity); err != nil {
			return err
		}
		if err = verifyPartitionDirectory(partition.OwnerPath, partition.OwnerIdentity, partition.VolumeIdentity); err != nil {
			return err
		}
		members, err := measurePartitionMembers(partition)
		if err != nil {
			return err
		}
		var measuredBytes, measuredEntries uint64
		for _, inventory := range members {
			measuredBytes, err = add(measuredBytes, inventory.ChargedBytes)
			if err == nil {
				measuredEntries, err = add(measuredEntries, inventory.Entries)
			}
			if err != nil {
				return err
			}
		}
		if allocation.UpperBytes < 0 || allocation.UpperEntries < 0 || measuredBytes > uint64(allocation.UpperBytes) || measuredEntries > uint64(allocation.UpperEntries) {
			return c.Fail(c.BudgetLimitReached, "physical quota partition output exceeds its admitted bootstrap envelope")
		}
		if err = admitPartitionReceipts(a.ledger.root, members); err != nil {
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
		currentPartition, found, err := loadQuotaPartitionTx(ctx, tx, a.ID)
		if err != nil || !found {
			if err == nil {
				err = c.Fail(c.StaleAuthority, "physical quota partition disappeared before settlement")
			}
			return err
		}
		if current.Status != "WRITING" || currentPartition.OperationDigest != partition.OperationDigest || currentPartition.State != "OWNER_BOUND" ||
			currentPartition.RootIdentity != partition.RootIdentity || currentPartition.OwnerIdentity != partition.OwnerIdentity {
			return c.Fail(c.StaleAuthority, "physical quota partition changed before settlement")
		}
		if err = settlePartitionTx(ctx, tx, a.ledger, partition, members); err != nil {
			return err
		}
		if err = checkPartitionCapacity(ctx, tx, a.ledger.Limits); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE quota_partitions SET state='COMMITTED' WHERE allocation_id=? AND state='OWNER_BOUND' AND operation_digest=?`, a.ID, partition.OperationDigest)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "physical quota partition changed during settlement")
		}
		return tx.Commit()
	})
}

// ValidateOwnerPartition prevents an unfinished or substituted evaluator tree
// from being opened as a normal owner. Unpartitioned legacy owners are left to
// the ordinary retained-import path.
func (l *Ledger) ValidateOwnerPartition(ctx context.Context, ownerPath string) error {
	if l == nil {
		return c.Fail(c.InvalidArgument, "shared physical quota ledger required")
	}
	owner, err := canonicalHandoffPath(ownerPath)
	if err != nil {
		return err
	}
	return l.withReadLock(ctx, func() error {
		if err := validateQuotaPartitionRecords(l.db, l.LedgerID); err != nil {
			return err
		}
		partition, found, err := loadQuotaPartitionByOwner(l.db, owner)
		if err != nil || !found {
			return err
		}
		if partition.State != "COMMITTED" {
			return c.Fail(c.StaleAuthority, "independent evaluator owner partition is unresolved and remains charged")
		}
		if err = verifyPartitionDirectory(partition.RootPath, partition.RootIdentity, partition.VolumeIdentity); err != nil {
			return err
		}
		if err = verifyPartitionDirectory(partition.OwnerPath, partition.OwnerIdentity, partition.VolumeIdentity); err != nil {
			return err
		}
		return validateCommittedPartition(ctx, l, partition)
	})
}

func ensureQuotaPartitionSchema(db *sql.DB) error {
	if db == nil {
		return c.Fail(c.InvalidArgument, "physical quota database required")
	}
	for _, ddl := range []string{quotaPartitionsDDL, quotaPartitionMembersDDL} {
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	for _, item := range []struct {
		name, ddl   string
		columns     []struct{ name, kind string }
		foreignKeys []struct{ table, from, to string }
	}{
		{name: "quota_partitions", ddl: quotaPartitionsDDL, columns: []struct{ name, kind string }{
			{"allocation_id", "TEXT"}, {"schema_version", "INTEGER"}, {"operation_digest", "TEXT"}, {"mode", "TEXT"}, {"root_path", "TEXT"}, {"owner_path", "TEXT"}, {"root_identity", "TEXT"}, {"owner_identity", "TEXT"}, {"volume_identity", "TEXT"}, {"state", "TEXT"},
		}, foreignKeys: []struct{ table, from, to string }{{"quota_allocations", "allocation_id", "id"}}},
		{name: "quota_partition_members", ddl: quotaPartitionMembersDDL, columns: []struct{ name, kind string }{
			{"allocation_id", "TEXT"}, {"kind", "TEXT"}, {"member_id", "TEXT"}, {"member_root", "TEXT"}, {"root_identity", "TEXT"}, {"volume_identity", "TEXT"}, {"receipt_digest", "TEXT"},
		}, foreignKeys: []struct{ table, from, to string }{{"quota_partitions", "allocation_id", "allocation_id"}, {"quota_allocations", "member_id", "id"}}},
	} {
		var schemaBytes int
		if err := db.QueryRow("SELECT length(CAST(sql AS BLOB)) FROM sqlite_master WHERE type='table' AND name=?", item.name).Scan(&schemaBytes); err != nil {
			return err
		}
		if schemaBytes <= 0 || schemaBytes > maxQuotaPartitionSchema {
			return c.Fail(c.StoreIntegrityError, "physical quota partition schema exceeds its bound")
		}
		var schemaSQL string
		if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", item.name).Scan(&schemaSQL); err != nil {
			return err
		}
		stored := strings.ToUpper(strings.Join(strings.Fields(schemaSQL), " "))
		expected := strings.ToUpper(strings.Join(strings.Fields(item.ddl), " "))
		expected = strings.Replace(expected, "CREATE TABLE IF NOT EXISTS "+strings.ToUpper(item.name), "CREATE TABLE "+strings.ToUpper(item.name), 1)
		if stored != expected {
			return c.Fail(c.StoreIntegrityError, "physical quota partition schema is invalid")
		}
		rows, err := db.Query("PRAGMA table_info(" + item.name + ")")
		if err != nil {
			return err
		}
		columns := make([]struct{ name, kind string }, 0, len(item.columns))
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
		if len(columns) != len(item.columns) {
			return c.Fail(c.StoreIntegrityError, "physical quota partition columns are invalid")
		}
		for i := range columns {
			if columns[i] != item.columns[i] {
				return c.Fail(c.StoreIntegrityError, "physical quota partition columns are invalid")
			}
		}
		foreignKeys, err := db.Query("PRAGMA foreign_key_list(" + item.name + ")")
		if err != nil {
			return err
		}
		keys := []struct{ table, from, to string }{}
		for foreignKeys.Next() {
			var id, sequence int
			var table, from, to, onUpdate, onDelete, match string
			if err = foreignKeys.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				foreignKeys.Close()
				return err
			}
			keys = append(keys, struct{ table, from, to string }{table, from, to})
		}
		if err = errors.Join(foreignKeys.Err(), foreignKeys.Close()); err != nil {
			return err
		}
		lessFK := func(values []struct{ table, from, to string }, i, j int) bool {
			if values[i].table != values[j].table {
				return values[i].table < values[j].table
			}
			if values[i].from != values[j].from {
				return values[i].from < values[j].from
			}
			return values[i].to < values[j].to
		}
		expectedKeys := append([]struct{ table, from, to string }(nil), item.foreignKeys...)
		sort.Slice(keys, func(i, j int) bool { return lessFK(keys, i, j) })
		sort.Slice(expectedKeys, func(i, j int) bool { return lessFK(expectedKeys, i, j) })
		if len(keys) != len(item.foreignKeys) {
			return c.Fail(c.StoreIntegrityError, "physical quota partition foreign keys are invalid")
		}
		for i := range keys {
			if keys[i] != expectedKeys[i] {
				return c.Fail(c.StoreIntegrityError, "physical quota partition foreign keys are invalid")
			}
		}
		var triggers int
		if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name=?", item.name).Scan(&triggers); err != nil {
			return err
		}
		if triggers != 0 {
			return c.Fail(c.StoreIntegrityError, "physical quota partition triggers are not permitted")
		}
	}
	return validateQuotaPartitionRecords(db, "")
}

func validateQuotaPartitionRecords(db *sql.DB, ledgerID string) error {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM quota_partitions").Scan(&count); err != nil {
		return err
	}
	if count < 0 || count > maxQuotaPartitions {
		return c.Fail(c.BudgetLimitReached, "physical quota partition catalog exceeds its bound")
	}
	var invalid int
	err := db.QueryRow(`SELECT 1 FROM quota_partitions WHERE
		typeof(allocation_id)!='text' OR length(CAST(allocation_id AS BLOB))!=32 OR
		typeof(schema_version)!='integer' OR schema_version!=1 OR
		typeof(operation_digest)!='text' OR length(CAST(operation_digest AS BLOB))!=64 OR
		typeof(mode)!='text' OR mode NOT IN ('EVALUATION_REPORT','INDEPENDENT_OWNER') OR
		typeof(root_path)!='text' OR length(CAST(root_path AS BLOB)) NOT BETWEEN 1 AND 2048 OR
		typeof(owner_path)!='text' OR length(CAST(owner_path AS BLOB)) NOT BETWEEN 1 AND 2048 OR
		typeof(root_identity)!='text' OR length(CAST(root_identity AS BLOB)) NOT IN (0,64) OR
		typeof(owner_identity)!='text' OR length(CAST(owner_identity AS BLOB)) NOT IN (0,64) OR
		typeof(volume_identity)!='text' OR length(CAST(volume_identity AS BLOB)) NOT IN (0,64) OR
		typeof(state)!='text' OR state NOT IN ('PREPARED','ROOT_BOUND','OWNER_BOUND','COMMITTED') OR
		(state='PREPARED' AND (root_identity!='' OR owner_identity!='' OR volume_identity!='')) OR
		(state='ROOT_BOUND' AND (length(CAST(root_identity AS BLOB))!=64 OR owner_identity!='' OR length(CAST(volume_identity AS BLOB))!=64)) OR
		(state IN ('OWNER_BOUND','COMMITTED') AND (length(CAST(root_identity AS BLOB))!=64 OR length(CAST(owner_identity AS BLOB))!=64 OR length(CAST(volume_identity AS BLOB))!=64))
		LIMIT 1`).Scan(&invalid)
	if err == nil {
		return c.Fail(c.StoreIntegrityError, "physical quota partition record exceeds its bounds")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var memberCount int
	if err = db.QueryRow("SELECT COUNT(*) FROM quota_partition_members").Scan(&memberCount); err != nil {
		return err
	}
	if memberCount < 0 || memberCount > maxQuotaPartitions*maxQuotaPartitionMembers {
		return c.Fail(c.BudgetLimitReached, "physical quota partition members exceed their bound")
	}
	var oversized int
	err = db.QueryRow(`SELECT 1 FROM quota_partition_members AS member
		LEFT JOIN quota_partitions AS partition ON partition.allocation_id=member.allocation_id
		LEFT JOIN quota_allocations AS allocation ON allocation.id=member.member_id
		WHERE partition.allocation_id IS NULL OR allocation.id IS NULL OR
		typeof(allocation.receipt)!='blob' OR length(allocation.receipt)<1 OR length(allocation.receipt)>?
		LIMIT 1`, maxInventoryBytes).Scan(&oversized)
	if err == nil {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member receipt exceeds its bound")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = db.QueryRow(`SELECT 1 FROM quota_partition_members WHERE
		typeof(allocation_id)!='text' OR length(CAST(allocation_id AS BLOB))!=32 OR
		typeof(kind)!='text' OR kind NOT IN ('REPORT_REMAINDER','OWNER_STATIC','MUTABLE_SQLITE','CONTROL_RESERVE','ARCHIVE') OR
		typeof(member_id)!='text' OR length(CAST(member_id AS BLOB))!=32 OR
		typeof(member_root)!='text' OR length(CAST(member_root AS BLOB)) NOT BETWEEN 1 AND 2048 OR
		typeof(root_identity)!='text' OR length(CAST(root_identity AS BLOB))!=64 OR
		typeof(volume_identity)!='text' OR length(CAST(volume_identity AS BLOB))!=64 OR
		typeof(receipt_digest)!='text' OR length(CAST(receipt_digest AS BLOB))!=64
		LIMIT 1`).Scan(&invalid)
	if err == nil {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member exceeds its bounds")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := db.Query(`SELECT allocation_id,schema_version,operation_digest,mode,root_path,owner_path,root_identity,owner_identity,volume_identity,state
		FROM quota_partitions ORDER BY allocation_id LIMIT ?`, maxQuotaPartitions+1)
	if err != nil {
		return err
	}
	parts := make([]quotaPartition, 0, 16)
	for rows.Next() {
		var item quotaPartition
		var schema int64
		if err = rows.Scan(&item.AllocationID, &schema, &item.OperationDigest, &item.Mode, &item.RootPath, &item.OwnerPath, &item.RootIdentity, &item.OwnerIdentity, &item.VolumeIdentity, &item.State); err != nil {
			rows.Close()
			return err
		}
		if schema < 0 || schema > math.MaxInt32 {
			rows.Close()
			return c.Fail(c.StoreIntegrityError, "physical quota partition schema version is invalid")
		}
		item.SchemaVersion = int(schema)
		parts = append(parts, item)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(parts) > maxQuotaPartitions {
		return c.Fail(c.BudgetLimitReached, "physical quota partition catalog exceeds its bound")
	}
	for _, item := range parts {
		if err = validateOneQuotaPartition(db, item, ledgerID); err != nil {
			return err
		}
	}
	return nil
}

func validateOneQuotaPartition(db *sql.DB, item quotaPartition, ledgerID string) error {
	allocation, err := loadHandoffAllocation(db, item.AllocationID)
	if err != nil {
		return c.Fail(c.StoreIntegrityError, "physical quota partition allocation is missing")
	}
	if item.SchemaVersion != quotaPartitionSchema || !validID(item.AllocationID) || !c.ValidDigest(item.OperationDigest) ||
		(item.Mode != EvaluationReportPartition && item.Mode != IndependentOwnerPartition) || !filepath.IsAbs(item.RootPath) || !filepath.IsAbs(item.OwnerPath) ||
		filepath.Clean(item.RootPath) != item.RootPath || filepath.Clean(item.OwnerPath) != item.OwnerPath || filepath.Clean(allocation.TargetRoot) != item.RootPath ||
		(item.Mode == EvaluationReportPartition && item.OwnerPath != filepath.Join(item.RootPath, "owner")) ||
		(item.Mode == IndependentOwnerPartition && item.OwnerPath != item.RootPath) {
		return c.Fail(c.StoreIntegrityError, "physical quota partition binding is invalid")
	}
	if ledgerID != "" {
		binding := partitionBinding{SchemaVersion: quotaPartitionSchema, LedgerID: ledgerID, AllocationID: item.AllocationID,
			RequestDigest: allocation.RequestDigest, Mode: item.Mode, RootPath: item.RootPath, OwnerPath: item.OwnerPath,
			ParentPath: filepath.Clean(allocation.ParentPath), ParentIdentity: allocation.ParentIdentity, VolumeIdentity: allocation.Volume}
		operation, digestErr := c.Digest(binding)
		if digestErr != nil || operation != item.OperationDigest {
			return c.Fail(c.StoreIntegrityError, "physical quota partition operation digest is invalid")
		}
	}
	if item.State == "COMMITTED" {
		if allocation.Status != "RETAINED" {
			return c.Fail(c.StoreIntegrityError, "committed physical quota partition has no retained parent")
		}
		return validatePartitionMembers(db, item)
	}
	if allocation.Status != "RESERVED" && allocation.Status != "WRITING" {
		return c.Fail(c.StoreIntegrityError, "unfinished physical quota partition lost its outstanding allocation")
	}
	if item.State == "PREPARED" && allocation.Status != "RESERVED" && allocation.Status != "WRITING" ||
		item.State != "PREPARED" && allocation.Status != "WRITING" {
		return c.Fail(c.StoreIntegrityError, "physical quota partition state differs from its allocation")
	}
	var members int
	if err = db.QueryRow("SELECT COUNT(*) FROM quota_partition_members WHERE allocation_id=?", item.AllocationID).Scan(&members); err != nil {
		return err
	}
	if members != 0 {
		return c.Fail(c.StoreIntegrityError, "unfinished physical quota partition already has member receipts")
	}
	return nil
}

func validatePartitionMembers(db *sql.DB, partition quotaPartition) error {
	rows, err := db.Query(`SELECT allocation_id,kind,member_id,member_root,root_identity,volume_identity,receipt_digest
		FROM quota_partition_members WHERE allocation_id=? ORDER BY kind`, partition.AllocationID)
	if err != nil {
		return err
	}
	members := make([]quotaPartitionMember, 0, maxQuotaPartitionMembers)
	for rows.Next() {
		var member quotaPartitionMember
		if err = rows.Scan(&member.AllocationID, &member.Kind, &member.MemberID, &member.MemberRoot, &member.RootIdentity, &member.Volume, &member.ReceiptDigest); err != nil {
			rows.Close()
			return err
		}
		members = append(members, member)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	expected := 4
	if partition.Mode == EvaluationReportPartition {
		expected = 5
	}
	if len(members) != expected {
		return c.Fail(c.StoreIntegrityError, "committed physical quota partition member set is incomplete")
	}
	seen := map[string]bool{}
	for _, member := range members {
		if member.AllocationID != partition.AllocationID || seen[member.Kind] {
			return c.Fail(c.StoreIntegrityError, "duplicate physical quota partition member")
		}
		seen[member.Kind] = true
		if err = validatePartitionMemberRow(db, partition, member); err != nil {
			return err
		}
	}
	if !seen["OWNER_STATIC"] || !seen["MUTABLE_SQLITE"] || !seen["CONTROL_RESERVE"] || !seen["ARCHIVE"] || (partition.Mode == EvaluationReportPartition) != seen["REPORT_REMAINDER"] {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member kinds are incomplete")
	}
	return nil
}

func validatePartitionMemberRow(db *sql.DB, partition quotaPartition, member quotaPartitionMember) error {
	if !validID(member.MemberID) || !c.ValidDigest(member.RootIdentity) || !c.ValidDigest(member.Volume) || !c.ValidDigest(member.ReceiptDigest) || !filepath.IsAbs(member.MemberRoot) || filepath.Clean(member.MemberRoot) != member.MemberRoot || member.Volume != partition.VolumeIdentity {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member binding is invalid")
	}
	expectedRoot, expectedIdentity := partition.OwnerPath, partition.OwnerIdentity
	switch member.Kind {
	case "REPORT_REMAINDER":
		if partition.Mode != EvaluationReportPartition {
			return c.Fail(c.StoreIntegrityError, "unexpected report-remainder partition member")
		}
		expectedRoot, expectedIdentity = partition.RootPath, partition.RootIdentity
	case "OWNER_STATIC", "MUTABLE_SQLITE", "CONTROL_RESERVE":
	case "ARCHIVE":
		expectedRoot = filepath.Join(partition.OwnerPath, "artifacts")
		expectedIdentity = member.RootIdentity
	default:
		return c.Fail(c.StoreIntegrityError, "unknown physical quota partition member kind")
	}
	if member.MemberRoot != expectedRoot || member.RootIdentity != expectedIdentity {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member root differs from its typed binding")
	}
	if member.Kind == "ARCHIVE" {
		if err := verifyPartitionDirectory(member.MemberRoot, member.RootIdentity, member.Volume); err != nil {
			return err
		}
	}
	var authorityID string
	if err := db.QueryRow("SELECT authority_id FROM quota_meta WHERE singleton=1").Scan(&authorityID); err != nil || !validID(authorityID) {
		return c.Fail(c.StoreIntegrityError, "physical quota partition authority is missing")
	}
	var status, target, volume string
	var targetIdentity string
	var targetExisted int
	var retainedBytes, retainedEntries int64
	var receipt []byte
	if err := db.QueryRow("SELECT status,target_root,volume_identity,target_identity,target_existed,retained_bytes,retained_entries,receipt FROM quota_allocations WHERE id=?", member.MemberID).Scan(
		&status, &target, &volume, &targetIdentity, &targetExisted, &retainedBytes, &retainedEntries, &receipt); err != nil {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member allocation is missing")
	}
	if status != "RETAINED" && status != "FREE_PENDING" && status != "RELEASED" || filepath.Clean(target) != member.MemberRoot || volume != member.Volume || targetIdentity != member.RootIdentity || targetExisted != 1 || retainedBytes < 0 || retainedEntries < 0 {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member allocation differs from its binding")
	}
	if len(receipt) == 0 || len(receipt) > maxInventoryBytes {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member allocation lacks a bounded receipt")
	}
	var inventory Inventory
	if strictJSON(receipt, &inventory) != nil {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member receipt is invalid")
	}
	if _, err := validateInventory(inventory); err != nil || inventory.RootPath != member.MemberRoot || inventory.RootIdentity != member.RootIdentity ||
		inventory.VolumeIdentity != member.Volume || inventory.Entries != uint64(retainedEntries) || inventory.ChargedBytes != uint64(retainedBytes) {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member receipt counters or root are invalid")
	}
	if err := validateInventoryBound(inventory, member.Kind, partition); err != nil {
		return err
	}
	if c.HashBytes(receipt) != member.ReceiptDigest && member.Kind != "MUTABLE_SQLITE" && member.Kind != "CONTROL_RESERVE" {
		return c.Fail(c.StoreIntegrityError, "physical quota partition member receipt changed")
	}
	if member.MemberID == partition.AllocationID {
		parentIdentity := partition.RootIdentity
		if partition.Mode == IndependentOwnerPartition {
			parentIdentity = partition.OwnerIdentity
		}
		if member.Kind != "REPORT_REMAINDER" && member.Kind != "OWNER_STATIC" || targetIdentity != parentIdentity {
			return c.Fail(c.StoreIntegrityError, "physical quota partition parent row is bound to the wrong member")
		}
	} else if member.Kind == "REPORT_REMAINDER" || member.Kind == "OWNER_STATIC" {
		want := partitionMemberAllocationID(authorityID, partition.AllocationID, member.Kind, member.MemberRoot, member.RootIdentity)
		if member.MemberID != want {
			return c.Fail(c.StoreIntegrityError, "physical quota retained partition member identity is invalid")
		}
	} else {
		var want string
		switch member.Kind {
		case "MUTABLE_SQLITE":
			want, _ = mutableSQLiteIDs(authorityID, member.MemberRoot, member.RootIdentity)
		case "CONTROL_RESERVE":
			want, _ = controlReserveIDs(authorityID, member.MemberRoot, member.RootIdentity)
		case "ARCHIVE":
			want = retainedSeedID(authorityID, member.MemberRoot, member.RootIdentity)
		}
		if member.MemberID != want {
			return c.Fail(c.StoreIntegrityError, "physical quota typed partition member identity is invalid")
		}
	}
	if member.Kind == "MUTABLE_SQLITE" {
		_, standby := mutableSQLiteIDs(authorityID, member.MemberRoot, member.RootIdentity)
		var linked string
		if err := db.QueryRow("SELECT standby_id FROM quota_mutable_replacements WHERE base_id=?", member.MemberID).Scan(&linked); err != nil || linked != standby {
			return c.Fail(c.StoreIntegrityError, "partitioned mutable SQLite member has no standby")
		}
	}
	if member.Kind == "CONTROL_RESERVE" {
		_, standby := controlReserveIDs(authorityID, member.MemberRoot, member.RootIdentity)
		var linked string
		if err := db.QueryRow("SELECT standby_id FROM quota_control_reserves WHERE base_id=?", member.MemberID).Scan(&linked); err != nil || linked != standby {
			return c.Fail(c.StoreIntegrityError, "partitioned control reserve member has no standby")
		}
	}
	return nil
}

func loadQuotaPartition(db *sql.DB, id string) (quotaPartition, bool, error) {
	var item quotaPartition
	var schema int64
	err := db.QueryRow(`SELECT allocation_id,schema_version,operation_digest,mode,root_path,owner_path,root_identity,owner_identity,volume_identity,state
		FROM quota_partitions WHERE allocation_id=?`, id).Scan(&item.AllocationID, &schema, &item.OperationDigest, &item.Mode, &item.RootPath, &item.OwnerPath, &item.RootIdentity, &item.OwnerIdentity, &item.VolumeIdentity, &item.State)
	if errors.Is(err, sql.ErrNoRows) {
		return quotaPartition{}, false, nil
	}
	if err != nil {
		return item, false, err
	}
	if schema < 0 || schema > math.MaxInt32 {
		return item, false, c.Fail(c.StoreIntegrityError, "physical quota partition schema version is invalid")
	}
	item.SchemaVersion = int(schema)
	return item, true, nil
}

func loadQuotaPartitionTx(ctx context.Context, tx *sql.Tx, id string) (quotaPartition, bool, error) {
	var item quotaPartition
	var schema int64
	err := tx.QueryRowContext(ctx, `SELECT allocation_id,schema_version,operation_digest,mode,root_path,owner_path,root_identity,owner_identity,volume_identity,state
		FROM quota_partitions WHERE allocation_id=?`, id).Scan(&item.AllocationID, &schema, &item.OperationDigest, &item.Mode, &item.RootPath, &item.OwnerPath, &item.RootIdentity, &item.OwnerIdentity, &item.VolumeIdentity, &item.State)
	if errors.Is(err, sql.ErrNoRows) {
		return quotaPartition{}, false, nil
	}
	if err != nil {
		return item, false, err
	}
	if schema < 0 || schema > math.MaxInt32 {
		return item, false, c.Fail(c.StoreIntegrityError, "physical quota partition schema version is invalid")
	}
	item.SchemaVersion = int(schema)
	return item, true, nil
}

func loadQuotaPartitionByOwner(db *sql.DB, owner string) (quotaPartition, bool, error) {
	var id string
	err := db.QueryRow("SELECT allocation_id FROM quota_partitions WHERE owner_path=?", owner).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return quotaPartition{}, false, nil
	}
	if err != nil {
		return quotaPartition{}, false, err
	}
	return loadQuotaPartition(db, id)
}

func verifyPartitionParent(partition quotaPartition, allocation handoffAllocation) error {
	if filepath.Clean(allocation.ParentPath) == "" || filepath.Clean(allocation.ParentPath) != filepath.Dir(partition.RootPath) || allocation.ParentIdentity != allocation.TargetIdentity {
		return c.Fail(c.StaleAuthority, "physical quota partition parent witness is invalid")
	}
	parent, err := os.OpenRoot(allocation.ParentPath)
	if err != nil {
		return err
	}
	identity, identityErr := fileguard.DirectoryIdentity(parent)
	volume, volumeErr := fileguard.VolumeIdentity(parent)
	closeErr := parent.Close()
	if err = errors.Join(identityErr, volumeErr, closeErr); err != nil {
		return err
	}
	if identity != allocation.ParentIdentity || volume != allocation.Volume {
		return c.Fail(c.StaleAuthority, "physical quota partition parent identity or volume changed")
	}
	return nil
}

func verifyPartitionDirectory(path, expectedIdentity, expectedVolume string) error {
	if !c.ValidDigest(expectedIdentity) || !c.ValidDigest(expectedVolume) {
		return c.Fail(c.StaleAuthority, "physical quota partition directory is unbound")
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
	if identity != expectedIdentity || volume != expectedVolume {
		return c.Fail(c.StaleAuthority, "physical quota partition directory identity or volume changed")
	}
	return nil
}

// ensurePartitionOwnerMaintenanceDirs establishes the fixed GC namespace while
// the bootstrap allocation still owns the whole tree. Later GC receipts can
// then reserve exact absent children without leaving ancestor directories
// outside any quota receipt.
func ensurePartitionOwnerMaintenanceDirs(ownerPath string) (retErr error) {
	root, err := os.OpenRoot(ownerPath)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	for _, name := range []string{"maintenance", "maintenance/gc"} {
		info, statErr := root.Lstat(filepath.FromSlash(name))
		if errors.Is(statErr, os.ErrNotExist) {
			if err = root.Mkdir(filepath.FromSlash(name), 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, statErr = root.Lstat(filepath.FromSlash(name))
		}
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() {
			return c.Fail(c.StaleAuthority, "physical quota maintenance namespace changed")
		}
		directory, openErr := root.OpenRoot(filepath.FromSlash(name))
		if openErr != nil {
			return openErr
		}
		privateErr := fileguard.Private(directory)
		closeErr := directory.Close()
		if err = errors.Join(privateErr, closeErr); err != nil {
			return err
		}
	}
	return fileguard.SyncParents(root, filepath.FromSlash("maintenance/gc"))
}

func partitionClass(partition quotaPartition, name string) string {
	name = filepath.ToSlash(name)
	if partition.Mode == EvaluationReportPartition {
		if name == "." || !strings.HasPrefix(name, "owner/") && name != "owner" {
			return "REPORT_REMAINDER"
		}
		name = strings.TrimPrefix(name, "owner/")
		if name == "owner" || name == "." {
			return "MUTABLE_SQLITE"
		}
	} else if name == "." {
		return "MUTABLE_SQLITE"
	}
	if name == "artifacts" || strings.HasPrefix(name, "artifacts/") {
		return "ARCHIVE"
	}
	switch name {
	case "owner.lock", "state.sqlite", "state.sqlite-shm", "state.sqlite-wal":
		return "MUTABLE_SQLITE"
	case "control.reserve":
		return "CONTROL_RESERVE"
	case "native-engine.json", "runtime-instance.json", "maintenance", "maintenance/gc":
		return "OWNER_STATIC"
	default:
		return ""
	}
}

type partitionTreeItem struct {
	name string
	dir  bool
	kind string
}

func measurePartitionMembers(partition quotaPartition) (map[string]Inventory, error) {
	root, err := os.OpenRoot(partition.RootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rootIdentity, err := fileguard.DirectoryIdentity(root)
	if err != nil || rootIdentity != partition.RootIdentity {
		return nil, c.Fail(c.StaleAuthority, "physical quota partition root changed")
	}
	items := make([]partitionTreeItem, 0, 64)
	counts := map[string]uint64{}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		kind := partitionClass(partition, name)
		if kind == "" {
			return c.Fail(c.UnsupportedCapability, "physical quota owner contains an unpartitioned entry")
		}
		isDir := name == "." || entry.IsDir()
		if name == "owner" && partition.Mode == EvaluationReportPartition {
			isDir = true
		}
		items = append(items, partitionTreeItem{name: filepath.ToSlash(name), dir: isDir, kind: kind})
		counts[kind]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if counts["MUTABLE_SQLITE"] == 0 || counts["CONTROL_RESERVE"] != 1 || (counts["OWNER_STATIC"] != 3 && counts["OWNER_STATIC"] != 4) || counts["ARCHIVE"] == 0 ||
		(partition.Mode == EvaluationReportPartition && counts["REPORT_REMAINDER"] == 0) {
		return nil, c.Fail(c.StoreIntegrityError, "physical quota partition is missing an owned component")
	}
	if counts["MUTABLE_SQLITE"] > maxQuotaPartitionMembers*100000 || len(items) > maxInventoryItems {
		return nil, c.Fail(c.BudgetLimitReached, "physical quota partition exceeds its inventory bound")
	}
	ownerRoot, err := os.OpenRoot(partition.OwnerPath)
	if err != nil {
		return nil, err
	}
	defer ownerRoot.Close()
	ownerIdentity, err := fileguard.DirectoryIdentity(ownerRoot)
	if err != nil || ownerIdentity != partition.OwnerIdentity {
		return nil, c.Fail(c.StaleAuthority, "physical quota partition owner identity changed")
	}
	actualNames := map[string][]string{}
	for _, item := range items {
		name := item.name
		if partition.Mode == EvaluationReportPartition {
			if name == "owner" {
				name = "."
			} else if strings.HasPrefix(name, "owner/") {
				name = strings.TrimPrefix(name, "owner/")
			}
		}
		if item.kind == "MUTABLE_SQLITE" && name != "." || item.kind != "MUTABLE_SQLITE" {
			actualNames[item.kind] = append(actualNames[item.kind], name)
		}
	}
	result := make(map[string]Inventory, 5)
	if partition.Mode == EvaluationReportPartition {
		result["REPORT_REMAINDER"], err = measurePartitionSubset(root, func(name string) bool { return partitionClass(partition, name) == "REPORT_REMAINDER" })
		if err != nil {
			return nil, err
		}
	}
	result["MUTABLE_SQLITE"], err = MeasureMutableSQLite(ownerRoot)
	if err != nil {
		return nil, err
	}
	result["CONTROL_RESERVE"], err = MeasureControlReserve(ownerRoot)
	if err != nil {
		return nil, err
	}
	result["OWNER_STATIC"], err = measurePartitionSubset(ownerRoot, func(name string) bool {
		return name == "native-engine.json" || name == "runtime-instance.json" || name == "maintenance" || name == "maintenance/gc"
	})
	if err != nil {
		return nil, err
	}
	archiveRoot, err := os.OpenRoot(filepath.Join(partition.OwnerPath, "artifacts"))
	if err != nil {
		return nil, err
	}
	result["ARCHIVE"], err = Measure(archiveRoot, true)
	closeErr := archiveRoot.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	for _, kind := range []string{"REPORT_REMAINDER", "MUTABLE_SQLITE", "CONTROL_RESERVE", "OWNER_STATIC", "ARCHIVE"} {
		inventory, ok := result[kind]
		if !ok {
			continue
		}
		actual := actualNames[kind]
		sort.Strings(actual)
		measuredNames := make([]string, 0, len(inventory.Files))
		for _, file := range inventory.Files {
			if kind == "REPORT_REMAINDER" || kind == "ARCHIVE" {
				measuredNames = append(measuredNames, filepath.ToSlash(file.Path))
			} else {
				measuredNames = append(measuredNames, filepath.ToSlash(file.Path))
			}
		}
		// Statistic-only lock/database members intentionally omit their file
		// receipts from generic inventories, but their entry counts still bind
		// them. The typed member measurements carry those exact inodes.
		expectedEntries := uint64(len(actual))
		if kind == "MUTABLE_SQLITE" {
			expectedEntries++ // MeasureMutableSQLite owns the owner root directory.
		}
		if inventory.Entries != expectedEntries {
			return nil, c.Fail(c.StoreIntegrityError, "physical quota partition member inventory is incomplete")
		}
		if kind == "OWNER_STATIC" {
			validNames := len(actual) == 3 && len(measuredNames) == 1 &&
				actual[0] == "maintenance" && actual[1] == "maintenance/gc" && actual[2] == "runtime-instance.json" &&
				measuredNames[0] == "runtime-instance.json"
			if len(actual) == 4 && len(measuredNames) == 2 {
				validNames = actual[0] == "maintenance" && actual[1] == "maintenance/gc" &&
					actual[2] == "native-engine.json" && actual[3] == "runtime-instance.json" &&
					measuredNames[0] == "native-engine.json" && measuredNames[1] == "runtime-instance.json"
			}
			if !validNames {
				return nil, c.Fail(c.StoreIntegrityError, "physical quota owner static member changed")
			}
		}
	}
	var totalEntries uint64
	for _, inventory := range result {
		totalEntries, err = add(totalEntries, inventory.Entries)
		if err != nil {
			return nil, err
		}
	}
	full, err := Measure(root, false)
	if err != nil {
		return nil, err
	}
	if totalEntries != full.Entries {
		return nil, c.Fail(c.StoreIntegrityError, "physical quota partition does not cover every file and directory exactly once")
	}
	return result, nil
}

func measurePartitionSubset(root *os.Root, include func(string) bool) (Inventory, error) {
	if root == nil || include == nil {
		return Inventory{}, c.Fail(c.InvalidArgument, "partitioned physical root required")
	}
	rootPath, err := filepath.Abs(root.Name())
	if err != nil {
		return Inventory{}, err
	}
	rootPath, err = filepath.EvalSymlinks(rootPath)
	if err != nil {
		return Inventory{}, err
	}
	identity, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return Inventory{}, err
	}
	volume, err := fileguard.VolumeIdentity(root)
	if err != nil {
		return Inventory{}, err
	}
	result := Inventory{RootPath: rootPath, RootIdentity: identity, VolumeIdentity: volume, Entries: 0, Files: []FileReceipt{}}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || !include(name) {
			return walkErr
		}
		if name == "." {
			result.Entries++
			result.ChargedBytes += directoryCharge
			return nil
		}
		if entry.IsDir() {
			result.Entries++
			result.ChargedBytes += directoryCharge
			return nil
		}
		if !entry.Type().IsRegular() {
			return c.Fail(c.UnsupportedCapability, "nonregular entry in physical quota partition")
		}
		if statOnlyFile(name) {
			info, statErr := root.Lstat(filepath.FromSlash(name))
			if statErr != nil || !info.Mode().IsRegular() || info.Size() < 0 || uint64(info.Size()) > maxLogicalFileSize {
				return c.Fail(c.StoreIntegrityError, "active owner metadata changed during partition measurement")
			}
			charge := uint64(info.Size())
			if charge == 0 {
				charge = directoryCharge
			} else if charge <= maxLogicalFileSize-lockedFileSlack {
				charge += lockedFileSlack
			}
			result.Entries++
			result.LogicalBytes, err = add(result.LogicalBytes, uint64(info.Size()))
			if err == nil {
				result.ChargedBytes, err = add(result.ChargedBytes, charge)
			}
			return err
		}
		file, measureErr := measureFile(root, filepath.ToSlash(name))
		if measureErr != nil {
			return measureErr
		}
		result.Entries++
		result.AllocatedBytes, err = add(result.AllocatedBytes, file.Allocated)
		if err == nil {
			result.LogicalBytes, err = add(result.LogicalBytes, file.LogicalBytes)
		}
		if err == nil {
			result.ChargedBytes, err = add(result.ChargedBytes, file.ChargedBytes)
		}
		result.Files = append(result.Files, file)
		return err
	})
	if err != nil {
		return Inventory{}, err
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return validateInventory(result)
}

func settlePartitionTx(ctx context.Context, tx *sql.Tx, ledger *Ledger, partition quotaPartition, members map[string]Inventory) error {
	parentKind := "REPORT_REMAINDER"
	if partition.Mode == IndependentOwnerPartition {
		parentKind = "OWNER_STATIC"
	}
	parent, ok := members[parentKind]
	if !ok {
		return c.Fail(c.StoreIntegrityError, "physical quota partition parent receipt is missing")
	}
	if err := validateInventoryBound(parent, parentKind, partition); err != nil {
		return err
	}
	parentRaw, err := jsonMarshalInventory(parent)
	if err != nil {
		return err
	}
	parentIdentity := partition.RootIdentity
	if partition.Mode == IndependentOwnerPartition {
		parentIdentity = partition.OwnerIdentity
	}
	result, err := tx.ExecContext(ctx, `UPDATE quota_allocations SET status='RETAINED',retained_bytes=?,retained_entries=?,receipt=?,target_identity=?,target_existed=1
		WHERE id=? AND status='WRITING' AND target_root=? AND volume_identity=?`, parent.ChargedBytes, parent.Entries, parentRaw, parentIdentity,
		partition.AllocationID, partition.RootPath, partition.VolumeIdentity)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "physical quota parent changed during atomic partition settlement")
	}

	kinds := []string{"REPORT_REMAINDER", "OWNER_STATIC", "MUTABLE_SQLITE", "CONTROL_RESERVE", "ARCHIVE"}
	for _, kind := range kinds {
		inventory, present := members[kind]
		if !present {
			if kind == "REPORT_REMAINDER" && partition.Mode == IndependentOwnerPartition {
				continue
			}
			return c.Fail(c.StoreIntegrityError, "physical quota partition member measurement is missing")
		}
		if err = validateInventoryBound(inventory, kind, partition); err != nil {
			return err
		}
		id := partitionMemberAllocationID(ledger.Authority.ID, partition.AllocationID, kind, inventory.RootPath, inventory.RootIdentity)
		writer, family := "evaluation", "EVALUATOR_"+kind
		if kind == parentKind {
			id = partition.AllocationID
		} else if kind == "MUTABLE_SQLITE" {
			baseID, standbyID := mutableSQLiteIDs(ledger.Authority.ID, inventory.RootPath, inventory.RootIdentity)
			_, volume, parentPath, ownerParentIdentity, targetIdentity, existed, available, volumeErr := targetVolume(inventory.RootPath)
			if volumeErr != nil {
				return volumeErr
			}
			if existed != 1 || targetIdentity != inventory.RootIdentity || volume != inventory.VolumeIdentity {
				return c.Fail(c.StaleAuthority, "partitioned SQLite root changed before settlement")
			}
			member := &MutableSQLite{ledger: ledger, rootPath: inventory.RootPath, baseID: baseID, standby: standbyID}
			if err = member.insertMutableBaseline(ctx, tx, inventory, inventory.RootPath, volume, parentPath, ownerParentIdentity, targetIdentity, available); err != nil {
				return err
			}
			id = baseID
		} else if kind == "CONTROL_RESERVE" {
			baseID, standbyID := controlReserveIDs(ledger.Authority.ID, inventory.RootPath, inventory.RootIdentity)
			_, volume, parentPath, ownerParentIdentity, targetIdentity, existed, available, volumeErr := targetVolume(inventory.RootPath)
			if volumeErr != nil {
				return volumeErr
			}
			if existed != 1 || targetIdentity != inventory.RootIdentity || volume != inventory.VolumeIdentity {
				return c.Fail(c.StaleAuthority, "partitioned control reserve root changed before settlement")
			}
			member := &ControlReserve{ledger: ledger, root: inventory.RootPath, baseID: baseID, standby: standbyID}
			if err = member.insertBaseline(ctx, tx, inventory, inventory.RootPath, volume, parentPath, ownerParentIdentity, targetIdentity, available); err != nil {
				return err
			}
			id = baseID
		} else if kind == "ARCHIVE" {
			id = retainedSeedID(ledger.Authority.ID, inventory.RootPath, inventory.RootIdentity)
			writer, family = "artifact", "ARCHIVE_BASE"
		} else if kind == "OWNER_STATIC" {
			writer, family = "evaluation", "EVALUATOR_OWNER_STATIC"
		}
		if kind != parentKind && kind != "MUTABLE_SQLITE" && kind != "CONTROL_RESERVE" {
			if err = insertPartitionRetainedTx(ctx, tx, ledger, id, writer, family, inventory); err != nil {
				return err
			}
		}
		raw, err := jsonMarshalInventory(inventory)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO quota_partition_members(allocation_id,kind,member_id,member_root,root_identity,volume_identity,receipt_digest)
			VALUES(?,?,?,?,?,?,?)`, partition.AllocationID, kind, id, inventory.RootPath, inventory.RootIdentity, inventory.VolumeIdentity, c.HashBytes(raw)); err != nil {
			return err
		}
	}
	return nil
}

func validateInventoryBound(inventory Inventory, kind string, partition quotaPartition) error {
	if _, err := validateInventory(inventory); err != nil {
		return err
	}
	if inventory.VolumeIdentity != partition.VolumeIdentity || (kind == "REPORT_REMAINDER" && inventory.RootPath != partition.RootPath) ||
		(kind != "REPORT_REMAINDER" && inventory.RootPath != partition.OwnerPath && !(kind == "ARCHIVE" && inventory.RootPath == filepath.Join(partition.OwnerPath, "artifacts"))) ||
		inventory.RootIdentity == "" {
		return c.Fail(c.StaleAuthority, "physical quota partition member root is outside its bound path")
	}
	if kind == "OWNER_STATIC" {
		baseline := len(inventory.Files) == 1 && inventory.Files[0].Path == "runtime-instance.json" && inventory.Entries == 3
		withNativeEngine := len(inventory.Files) == 2 && inventory.Files[0].Path == "native-engine.json" &&
			inventory.Files[1].Path == "runtime-instance.json" && inventory.Entries == 4
		if !baseline && !withNativeEngine {
			return c.Fail(c.StoreIntegrityError, "physical quota owner static receipt is not the fixed runtime and maintenance namespace")
		}
		if withNativeEngine && inventory.Files[0].LogicalBytes > 1024 {
			return c.Fail(c.UnsupportedCapability, "native engine receipt exceeds its bounded reader size")
		}
	}
	return nil
}

func partitionMemberAllocationID(authority, allocationID, kind, root, identity string) string {
	return c.HashBytes([]byte("physical-quota-partition-member-v1\x00" + authority + "\x00" + allocationID + "\x00" + kind + "\x00" + filepath.Clean(root) + "\x00" + identity))[:32]
}

// checkPartitionAdmissionTx prevents a fresh generic reservation from
// overlapping bytes already covered by an owner partition. New absent paths
// inside the mutable owner tree remain admissible so task, GC, and support
// writers can carry their own bounded receipts after the initial settlement.
func checkPartitionAdmissionTx(ctx context.Context, tx *sql.Tx, request Request, target string, targetExisted int) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_partitions").Scan(&count); err != nil {
		return err
	}
	if count < 0 || count > maxQuotaPartitions {
		return c.Fail(c.BudgetLimitReached, "physical quota partition catalog exceeds its bound")
	}
	rows, err := tx.QueryContext(ctx, "SELECT allocation_id,mode,root_path,owner_path,state FROM quota_partitions ORDER BY allocation_id LIMIT ?", maxQuotaPartitions+1)
	if err != nil {
		return err
	}
	type binding struct {
		allocationID       string
		mode               PartitionMode
		root, owner, state string
	}
	items := make([]binding, 0, count)
	for rows.Next() {
		var item binding
		if err = rows.Scan(&item.allocationID, &item.mode, &item.root, &item.owner, &item.state); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(items) > maxQuotaPartitions {
		return c.Fail(c.BudgetLimitReached, "physical quota partition catalog exceeds its bound")
	}
	for _, item := range items {
		if (item.mode != EvaluationReportPartition && item.mode != IndependentOwnerPartition) ||
			!filepath.IsAbs(item.root) || !filepath.IsAbs(item.owner) ||
			filepath.Clean(item.root) != item.root || filepath.Clean(item.owner) != item.owner ||
			(item.state != "PREPARED" && item.state != "ROOT_BOUND" && item.state != "OWNER_BOUND" && item.state != "COMMITTED") {
			return c.Fail(c.StoreIntegrityError, "physical quota partition admission binding is corrupt")
		}
		if !partitionPathsOverlap(target, item.root) {
			continue
		}
		if item.state != "COMMITTED" {
			return c.Fail(c.StaleAuthority, "unresolved physical quota partition retains exclusive write ownership")
		}
		if !partitionPathWithin(item.owner, target) {
			return c.Fail(c.StaleAuthority, "physical quota request overlaps a retained partition subset")
		}
		rel, relOK := partitionRelativePath(item.owner, target)
		if !relOK || rel == "." {
			return c.Fail(c.StaleAuthority, "physical quota request overlaps the partitioned owner root")
		}
		rel = filepath.ToSlash(rel)
		archiveRootWrite := false
		for _, protected := range []string{"runtime-instance.json", "owner.lock", "state.sqlite", "state.sqlite-wal", "state.sqlite-shm", "control.reserve", "artifacts"} {
			if partitionNameAtOrBelow(rel, protected) {
				if protected == "artifacts" && strings.EqualFold(rel, protected) {
					if request.Writer != "artifact" || request.Family != "IMMUTABLE" ||
						(request.Class != Payload && request.Class != Control) || targetExisted != 1 {
						return c.Fail(c.StaleAuthority, "archive root writes require the bound immutable artifact writer")
					}
					if err = verifyPartitionArchiveAdmission(ctx, tx, item.allocationID, target); err != nil {
						return err
					}
					archiveRootWrite = true
					continue
				}
				if protected == "artifacts" && !strings.EqualFold(rel, protected) {
					if request.Writer != "artifact" || request.Family != "IMMUTABLE" || request.Class != Payload || targetExisted != 0 {
						return c.Fail(c.StaleAuthority, "archive partition bytes require a new exact immutable artifact reservation")
					}
					break
				}
				return c.Fail(c.StaleAuthority, "physical quota request overlaps a typed owner partition member")
			}
		}
		if !partitionPathWithin(item.owner, target) {
			return c.Fail(c.StaleAuthority, "physical quota request overlaps a retained partition subset")
		}
		rel, relOK = partitionRelativePath(item.owner, target)
		if !relOK || rel == "." {
			return c.Fail(c.StaleAuthority, "physical quota request overlaps the partitioned owner root")
		}
		if targetExisted != 0 && !archiveRootWrite {
			return c.Fail(c.StaleAuthority, "new owner reservation must bind an absent disjoint output path")
		}
		if archiveRootWrite {
			continue
		}
		parentInfo, parentErr := os.Stat(filepath.Dir(target))
		if parentErr != nil || !parentInfo.IsDir() {
			return c.Fail(c.StaleAuthority, "new owner reservation requires an already charged parent directory")
		}
	}
	return nil
}

func verifyPartitionArchiveAdmission(ctx context.Context, tx *sql.Tx, allocationID, target string) error {
	var memberRoot, memberIdentity, memberVolume string
	if err := tx.QueryRowContext(ctx, "SELECT member_root,root_identity,volume_identity FROM quota_partition_members WHERE allocation_id=? AND kind='ARCHIVE'", allocationID).Scan(&memberRoot, &memberIdentity, &memberVolume); err != nil {
		return c.Fail(c.StaleAuthority, "bound archive partition member is missing")
	}
	if filepath.Clean(memberRoot) != filepath.Clean(target) || !c.ValidDigest(memberIdentity) || !c.ValidDigest(memberVolume) {
		return c.Fail(c.StaleAuthority, "archive writer target differs from its partition member")
	}
	actualRoot, actualVolume, _, _, actualIdentity, existed, _, err := targetVolume(target)
	if err != nil || filepath.Clean(actualRoot) != filepath.Clean(target) || existed != 1 || actualIdentity != memberIdentity || actualVolume != memberVolume {
		return c.Fail(c.StaleAuthority, "archive partition directory identity changed")
	}
	return nil
}

func partitionNameAtOrBelow(name, base string) bool {
	name, base = filepath.ToSlash(name), filepath.ToSlash(base)
	return strings.EqualFold(name, base) || strings.HasPrefix(name, base+"/")
}

func partitionPathWithin(parent, child string) bool {
	parent, child = filepath.Clean(parent), filepath.Clean(child)
	if runtime.GOOS == "windows" {
		if strings.EqualFold(parent, child) {
			return true
		}
		return strings.HasPrefix(strings.ToLower(child), strings.ToLower(parent+string(filepath.Separator)))
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	if rel == "." || strings.EqualFold(rel, ".") {
		return true
	}
	first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	return !strings.EqualFold(first, "..")
}

func partitionRelativePath(parent, child string) (string, bool) {
	parent, child = filepath.Clean(parent), filepath.Clean(child)
	if !partitionPathWithin(parent, child) {
		return "", false
	}
	if strings.EqualFold(parent, child) {
		return ".", true
	}
	if runtime.GOOS == "windows" {
		prefix := parent + string(filepath.Separator)
		if len(child) < len(prefix) || !strings.EqualFold(child[:len(prefix)], prefix) {
			return "", false
		}
		return child[len(prefix):], true
	}
	rel, err := filepath.Rel(parent, child)
	return rel, err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func partitionPathsOverlap(left, right string) bool {
	return partitionPathWithin(left, right) || partitionPathWithin(right, left)
}

func mutableSQLiteIDs(authority, rootPath, identity string) (string, string) {
	base := c.HashBytes([]byte("physical-mutable-sqlite-v1\x00" + authority + "\x00" + filepath.Clean(rootPath) + "\x00" + identity))[:32]
	standby := c.HashBytes([]byte("physical-mutable-sqlite-standby-v1\x00" + base))[:32]
	return base, standby
}

func controlReserveIDs(authority, rootPath, identity string) (string, string) {
	base := c.HashBytes([]byte("physical-control-reserve-v1\x00" + authority + "\x00" + filepath.Clean(rootPath) + "\x00" + identity))[:32]
	standby := c.HashBytes([]byte("physical-control-reserve-standby-v1\x00" + base))[:32]
	return base, standby
}

func insertPartitionRetainedTx(ctx context.Context, tx *sql.Tx, ledger *Ledger, id, writer, family string, inventory Inventory) error {
	if !validID(id) || writer == "" || family == "" {
		return c.Fail(c.InvalidArgument, "partition retained receipt binding required")
	}
	raw, err := jsonMarshalInventory(inventory)
	if err != nil {
		return err
	}
	target, volume, parent, parentIdentity, targetIdentity, existed, _, err := targetVolume(inventory.RootPath)
	if err != nil || filepath.Clean(target) != filepath.Clean(inventory.RootPath) || volume != inventory.VolumeIdentity || targetIdentity != inventory.RootIdentity || existed != 1 {
		return c.Fail(c.StaleAuthority, "partition retained member root changed")
	}
	if err = admitReceiptMutation(ledger.root, raw); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quota_allocations(id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries,receipt)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, writer, family, c.HashBytes(raw), target, volume, parent, parentIdentity, targetIdentity, 1, string(Payload), "RETAINED", inventory.ChargedBytes, inventory.Entries, inventory.ChargedBytes, inventory.Entries, raw)
	return err
}

func admitPartitionReceipts(root *os.Root, members map[string]Inventory) error {
	for _, inventory := range members {
		raw, err := jsonMarshalInventory(inventory)
		if err != nil {
			return err
		}
		if err = admitReceiptMutation(root, raw); err != nil {
			return err
		}
	}
	return nil
}

func checkPartitionCapacity(ctx context.Context, tx *sql.Tx, limits Limits) error {
	capacity, err := capacityTx(tx, limits)
	if err != nil {
		return err
	}
	if capacity.RetainedBytes > limits.Bytes || capacity.OutstandingBytes > limits.Bytes-capacity.RetainedBytes ||
		capacity.RetainedEntries > limits.Entries || capacity.OutstandingItems > limits.Entries-capacity.RetainedEntries {
		return c.Fail(c.BudgetLimitReached, "partitioned physical quota settlement exceeds shared capacity")
	}
	payloadBytes, err := add(capacity.PayloadBytes, capacity.PayloadOutstandingBytes)
	if err != nil {
		return err
	}
	payloadEntries, err := add(capacity.PayloadEntries, capacity.PayloadOutstandingEntries)
	if err != nil {
		return err
	}
	if payloadBytes > limits.Bytes-limits.ControlBytes || payloadEntries > limits.Entries-limits.ControlEntries {
		return c.Fail(c.BudgetLimitReached, "partitioned physical quota settlement would consume protected control capacity")
	}
	return nil
}

func validateCommittedPartition(ctx context.Context, ledger *Ledger, partition quotaPartition) error {
	if err := verifyPartitionDirectory(partition.RootPath, partition.RootIdentity, partition.VolumeIdentity); err != nil {
		return err
	}
	if err := verifyPartitionDirectory(partition.OwnerPath, partition.OwnerIdentity, partition.VolumeIdentity); err != nil {
		return err
	}
	if err := validatePartitionMembers(ledger.db, partition); err != nil {
		return err
	}
	root, err := os.OpenRoot(partition.RootPath)
	if err != nil {
		return err
	}
	full, measureErr := Measure(root, true)
	var remainder Inventory
	if measureErr == nil && partition.Mode == EvaluationReportPartition {
		remainder, measureErr = measurePartitionSubset(root, func(name string) bool { return partitionClass(partition, name) == "REPORT_REMAINDER" })
	}
	closeErr := root.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		return err
	}
	if partition.Mode == EvaluationReportPartition {
		if err = validateExactPartitionInventory(ctx, ledger, partition, "REPORT_REMAINDER", remainder); err != nil {
			return err
		}
	}
	owner, err := os.OpenRoot(partition.OwnerPath)
	if err != nil {
		return err
	}
	static, staticErr := measurePartitionSubset(owner, func(name string) bool {
		return name == "native-engine.json" || name == "runtime-instance.json" || name == "maintenance" || name == "maintenance/gc"
	})
	ownerCloseErr := owner.Close()
	if err = errors.Join(staticErr, ownerCloseErr); err != nil {
		return err
	}
	if err = validateExactPartitionInventory(ctx, ledger, partition, "OWNER_STATIC", static); err != nil {
		return err
	}
	tx, err := ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateRetainedReopen(ctx, tx, full); err != nil {
		return err
	}
	return tx.Commit()
}

func validateExactPartitionInventory(ctx context.Context, ledger *Ledger, partition quotaPartition, kind string, inventory Inventory) error {
	raw, err := jsonMarshalInventory(inventory)
	if err != nil {
		return err
	}
	var id, digest string
	if err = ledger.db.QueryRowContext(ctx, "SELECT member_id,receipt_digest FROM quota_partition_members WHERE allocation_id=? AND kind=?", partition.AllocationID, kind).Scan(&id, &digest); err != nil {
		return err
	}
	var stored []byte
	if err = ledger.db.QueryRowContext(ctx, "SELECT receipt FROM quota_allocations WHERE id=?", id).Scan(&stored); err != nil {
		return err
	}
	if digest != c.HashBytes(raw) || string(stored) != string(raw) {
		return c.Fail(c.StaleAuthority, "immutable physical quota partition member changed")
	}
	return nil
}
