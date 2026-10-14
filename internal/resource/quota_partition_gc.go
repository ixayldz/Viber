package resource

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// GCPartitionSpec is the immutable command envelope supplied by restore.
// The command root itself is measured only after the stage reaches its final
// path; the request digest and bounds remain those admitted by the original
// GC command so a later GC retry can match this retained receipt exactly.
type GCPartitionSpec struct {
	CommandID     string
	RequestDigest string
	UpperBytes    uint64
	UpperEntries  uint64
}

const (
	maxQuotaPartitionGCMembers = 2048
	maxQuotaGCMemberRecords    = 1 << 16
	quotaPartitionGCMembersDDL = `CREATE TABLE IF NOT EXISTS quota_partition_gc_members (
	allocation_id TEXT NOT NULL CHECK(length(CAST(allocation_id AS BLOB))=32) REFERENCES quota_partitions(allocation_id),
	command_id TEXT NOT NULL CHECK(length(CAST(command_id AS BLOB))=64),
	member_id TEXT NOT NULL CHECK(length(CAST(member_id AS BLOB))=32) REFERENCES quota_allocations(id),
	member_root TEXT NOT NULL CHECK(length(CAST(member_root AS BLOB)) BETWEEN 1 AND 2048),
	root_identity TEXT NOT NULL CHECK(length(CAST(root_identity AS BLOB))=64),
	volume_identity TEXT NOT NULL CHECK(length(CAST(volume_identity AS BLOB))=64),
	receipt_digest TEXT NOT NULL CHECK(length(CAST(receipt_digest AS BLOB))=64),
	PRIMARY KEY(allocation_id,command_id),
	UNIQUE(allocation_id,member_id)
)`
)

// gcCommandID recognizes only a direct maintenance/gc/<digest> component or
// one of its descendants.  The fixed maintenance and gc directories remain
// OWNER_STATIC; each command subtree receives its own typed receipt.
func gcCommandID(name string) string {
	parts := strings.Split(filepath.ToSlash(name), "/")
	if len(parts) < 3 || parts[0] != "maintenance" || parts[1] != "gc" || !c.ValidDigest(parts[2]) {
		return ""
	}
	return parts[2]
}

func ensureQuotaPartitionGCSchema(db *sql.DB) error {
	if db == nil {
		return c.Fail(c.InvalidArgument, "physical quota database required")
	}
	if _, err := db.Exec(quotaPartitionGCMembersDDL); err != nil {
		return err
	}
	return nil
}

func validateQuotaPartitionGCRecords(db *sql.DB, ledgerID string) error {
	if err := ensureQuotaPartitionGCSchema(db); err != nil {
		return err
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM quota_partition_gc_members").Scan(&count); err != nil {
		return err
	}
	if count < 0 || count > maxQuotaGCMemberRecords {
		return c.Fail(c.BudgetLimitReached, "physical quota GC partition catalog exceeds its bound")
	}
	var invalid int
	err := db.QueryRow(`SELECT 1 FROM quota_partition_gc_members AS member
		LEFT JOIN quota_partitions AS partition ON partition.allocation_id=member.allocation_id
		LEFT JOIN quota_allocations AS allocation ON allocation.id=member.member_id
		WHERE partition.allocation_id IS NULL OR allocation.id IS NULL OR
		typeof(member.allocation_id)!='text' OR length(CAST(member.allocation_id AS BLOB))!=32 OR
		typeof(member.command_id)!='text' OR length(CAST(member.command_id AS BLOB))!=64 OR
		typeof(member.member_id)!='text' OR length(CAST(member.member_id AS BLOB))!=32 OR
		typeof(member.member_root)!='text' OR length(CAST(member.member_root AS BLOB)) NOT BETWEEN 1 AND 2048 OR
		typeof(member.root_identity)!='text' OR length(CAST(member.root_identity AS BLOB))!=64 OR
		typeof(member.volume_identity)!='text' OR length(CAST(member.volume_identity AS BLOB))!=64 OR
		typeof(member.receipt_digest)!='text' OR length(CAST(member.receipt_digest AS BLOB))!=64
		LIMIT 1`).Scan(&invalid)
	if err == nil {
		return c.Fail(c.StoreIntegrityError, "physical quota GC partition member exceeds its bounds")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if ledgerID == "" {
		return nil
	}
	rows, err := db.Query(`SELECT allocation_id,command_id,member_id,member_root,root_identity,volume_identity,receipt_digest
		FROM quota_partition_gc_members ORDER BY allocation_id,command_id`)
	if err != nil {
		return err
	}
	type savedGCMember struct{ allocationID, commandID, memberID, memberRoot, rootIdentity, volume, receiptDigest string }
	members := make([]savedGCMember, 0, 16)
	for rows.Next() {
		var member savedGCMember
		if err = rows.Scan(&member.allocationID, &member.commandID, &member.memberID, &member.memberRoot, &member.rootIdentity, &member.volume, &member.receiptDigest); err != nil {
			rows.Close()
			return err
		}
		members = append(members, member)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	// All rows must be closed before nested queries: the ledger intentionally
	// has only one database connection.
	for _, member := range members {
		allocationID, commandID, memberID, memberRoot, rootIdentity, volume, receiptDigest := member.allocationID, member.commandID, member.memberID, member.memberRoot, member.rootIdentity, member.volume, member.receiptDigest
		if !validID(allocationID) || !c.ValidDigest(commandID) || !validID(memberID) || !c.ValidDigest(rootIdentity) || !c.ValidDigest(volume) || !c.ValidDigest(receiptDigest) || !filepath.IsAbs(memberRoot) || filepath.Clean(memberRoot) != memberRoot {
			return c.Fail(c.StoreIntegrityError, "physical quota GC partition member binding is invalid")
		}
		var mode, ownerPath, partitionVolume, state string
		if err = db.QueryRow("SELECT mode,owner_path,volume_identity,state FROM quota_partitions WHERE allocation_id=?", allocationID).Scan(&mode, &ownerPath, &partitionVolume, &state); err != nil {
			return c.Fail(c.StoreIntegrityError, "physical quota GC partition is missing its owner")
		}
		if mode != string(IndependentOwnerPartition) || state != "COMMITTED" || partitionVolume != volume || filepath.Clean(memberRoot) != filepath.Join(ownerPath, "maintenance", "gc", commandID) {
			return c.Fail(c.StoreIntegrityError, "physical quota GC partition member path differs from its owner")
		}
		var writer, family, requestDigest, target, targetVolume, targetIdentity, class, status string
		var targetExisted, upperBytes, upperEntries, retainedBytes, retainedEntries int64
		var receipt []byte
		if err = db.QueryRow(`SELECT writer,family,request_digest,target_root,volume_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries,receipt
			FROM quota_allocations WHERE id=?`, memberID).Scan(&writer, &family, &requestDigest, &target, &targetVolume, &targetIdentity, &targetExisted, &class, &status, &upperBytes, &upperEntries, &retainedBytes, &retainedEntries, &receipt); err != nil {
			return c.Fail(c.StoreIntegrityError, "physical quota GC partition allocation is missing")
		}
		if writer != "agent" || family != "GC_MAINTENANCE" || !c.ValidDigest(requestDigest) || target != memberRoot || targetVolume != volume || targetIdentity != rootIdentity || targetExisted != 1 || class != string(Control) || (status != "RETAINED" && status != "FREE_PENDING" && status != "RELEASED") || upperBytes < 0 || upperEntries < 0 || retainedBytes < 0 || retainedEntries < 0 || len(receipt) == 0 || len(receipt) > maxInventoryBytes {
			return c.Fail(c.StoreIntegrityError, "physical quota GC partition allocation is invalid")
		}
		var inventory Inventory
		if strictJSON(receipt, &inventory) != nil || inventory.RootPath != memberRoot || inventory.RootIdentity != rootIdentity || inventory.VolumeIdentity != volume || inventory.ChargedBytes != uint64(retainedBytes) || inventory.Entries != uint64(retainedEntries) || c.HashBytes(receipt) != receiptDigest {
			return c.Fail(c.StoreIntegrityError, "physical quota GC partition receipt is invalid")
		}
	}
	return nil
}

func settleGCPartitionTx(ctx context.Context, tx *sql.Tx, ledger *Ledger, partition quotaPartition, specs []GCPartitionSpec, inventories map[string]Inventory) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_partition_gc_members").Scan(&count); err != nil {
		return err
	}
	if len(specs) > maxQuotaPartitionGCMembers || len(specs) > maxQuotaGCMemberRecords-count {
		return c.Fail(c.BudgetLimitReached, "GC partition catalog capacity exhausted")
	}
	for _, spec := range specs {
		inventory, ok := inventories[spec.CommandID]
		if !ok {
			return c.Fail(c.StoreIntegrityError, "GC partition inventory missing at settlement")
		}
		id := gcPartitionMemberID(ledger.Authority.ID, partition.AllocationID, spec.CommandID, inventory.RootPath, inventory.RootIdentity)
		if err := insertGCPartitionRetainedTx(ctx, tx, ledger, id, spec, inventory); err != nil {
			return err
		}
		raw, err := jsonMarshalInventory(inventory)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO quota_partition_gc_members(allocation_id,command_id,member_id,member_root,root_identity,volume_identity,receipt_digest) VALUES(?,?,?,?,?,?,?)`, partition.AllocationID, spec.CommandID, id, inventory.RootPath, inventory.RootIdentity, inventory.VolumeIdentity, c.HashBytes(raw)); err != nil {
			return err
		}
	}
	return nil
}

func validateGCPartitionMembers(db *sql.DB, partition quotaPartition) error {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM quota_partition_gc_members WHERE allocation_id=?", partition.AllocationID).Scan(&count); err != nil {
		return err
	}
	if count > maxQuotaPartitionGCMembers {
		return c.Fail(c.BudgetLimitReached, "physical quota owner contains too many GC command components")
	}
	return nil
}

func gcPartitionMemberID(authority, allocationID, commandID, root, identity string) string {
	return c.HashBytes([]byte("physical-quota-partition-gc-v1\x00" + authority + "\x00" + allocationID + "\x00" + commandID + "\x00" + filepath.Clean(root) + "\x00" + identity))[:32]
}

func insertGCPartitionRetainedTx(ctx context.Context, tx *sql.Tx, ledger *Ledger, id string, spec GCPartitionSpec, inventory Inventory) error {
	if !validID(id) || !c.ValidDigest(spec.CommandID) || !c.ValidDigest(spec.RequestDigest) || spec.UpperBytes == 0 || spec.UpperEntries == 0 {
		return c.Fail(c.InvalidArgument, "invalid retained GC partition receipt binding")
	}
	if inventory.ChargedBytes > spec.UpperBytes || inventory.Entries > spec.UpperEntries {
		return c.Fail(c.BudgetLimitReached, "retained GC command exceeds its admitted physical envelope")
	}
	raw, err := jsonMarshalInventory(inventory)
	if err != nil {
		return err
	}
	target, volume, parent, parentIdentity, targetIdentity, existed, _, err := targetVolume(inventory.RootPath)
	if err != nil || filepath.Clean(target) != filepath.Clean(inventory.RootPath) || volume != inventory.VolumeIdentity || targetIdentity != inventory.RootIdentity || existed != 1 {
		return c.Fail(c.StaleAuthority, "retained GC partition root changed")
	}
	if err = admitReceiptMutation(ledger.root, raw); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quota_allocations(id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries,receipt)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "agent", "GC_MAINTENANCE", spec.RequestDigest, target, volume, parent, parentIdentity, targetIdentity, 1, string(Control), "RETAINED", spec.UpperBytes, spec.UpperEntries, inventory.ChargedBytes, inventory.Entries, raw)
	return err
}

// validateGCRootsForStatic filters fixed OWNER_STATIC to the two maintenance
// directories when a typed GC component exists.  Without a component the
// legacy filter intentionally includes only empty maintenance namespaces.
func validateGCRootsForStatic(ctx context.Context, ledger *Ledger, partition quotaPartition) (bool, error) {
	var count int
	if err := ledger.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_partition_gc_members WHERE allocation_id=?", partition.AllocationID).Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}
