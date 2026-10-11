package resource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const (
	mutableStartupHeadroom uint64 = uint64(maxLedgerWALBytes) + (1 << 20)
	mutableEntryHeadroom   uint64 = 4
	mutableBaseFamily             = "MUTABLE_SQLITE"
)

const mutableReplacementDDL = "CREATE TABLE quota_mutable_replacements (standby_id TEXT PRIMARY KEY REFERENCES quota_allocations(id),base_id TEXT NOT NULL UNIQUE REFERENCES quota_allocations(id))"

// MutableSQLite is a bounded owner-database receipt. Its standby reservation
// remains charged across close/reopen and covers SQLite's database/WAL/SHM
// transition. It deliberately grants no deletion authority.
type MutableSQLite struct {
	ledger   *Ledger
	rootPath string
	baseID   string
	standby  string
}

// MutableSQLiteUpper estimates the additional SQLite/WAL space for a single
// bounded mutation. The retained database is already charged; this adds the
// startup/WAL envelope plus a conservative page/index allowance for the new
// transaction bytes.
func MutableSQLiteUpper(transactionBytes uint64) (uint64, error) {
	if transactionBytes > 1<<46 {
		return 0, c.Fail(c.BudgetLimitReached, "mutable SQLite transaction exceeds bounded profile")
	}
	if transactionBytes > (math.MaxUint64-(1<<20))/4 {
		return 0, c.Fail(c.BudgetLimitReached, "mutable SQLite transaction upper overflow")
	}
	upper := transactionBytes*4 + (1 << 20)
	if upper < mutableStartupHeadroom {
		upper = mutableStartupHeadroom
	}
	if upper > 1<<48 {
		return 0, c.Fail(c.BudgetLimitReached, "mutable SQLite write upper exceeds bounded profile")
	}
	return upper, nil
}

// MeasureMutableSQLite inventories the fixed mutable SQLite namespace owned
// by a store. The receipt digest commits to inode and physical size metrics,
// not database contents; it must never be used as a content or deletion proof.
func MeasureMutableSQLite(root *os.Root) (Inventory, error) {
	if root == nil {
		return Inventory{}, c.Fail(c.InvalidArgument, "rooted SQLite owner required")
	}
	rootPath, err := filepath.Abs(root.Name())
	if err != nil {
		return Inventory{}, err
	}
	rootPath, err = filepath.EvalSymlinks(rootPath)
	if err != nil {
		return Inventory{}, err
	}
	rootIdentity, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return Inventory{}, err
	}
	volume, err := fileguard.VolumeIdentity(root)
	if err != nil {
		return Inventory{}, err
	}
	result := Inventory{RootPath: rootPath, RootIdentity: rootIdentity, VolumeIdentity: volume, Entries: 1, ChargedBytes: directoryCharge, Files: []FileReceipt{}}
	for _, name := range []string{"owner.lock", "state.sqlite", "state.sqlite-shm", "state.sqlite-wal"} {
		info, statErr := root.Lstat(name)
		if errors.Is(statErr, os.ErrNotExist) {
			if name == "owner.lock" || name == "state.sqlite" {
				return Inventory{}, c.Fail(c.StaleAuthority, "mutable SQLite owner file is missing")
			}
			continue
		}
		if statErr != nil {
			return Inventory{}, statErr
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || uint64(info.Size()) > maxLogicalFileSize {
			return Inventory{}, c.Fail(c.UnsupportedCapability, "mutable SQLite owner entry is not a bounded regular file")
		}
		if name == "state.sqlite-wal" && info.Size() > maxLedgerWALBytes {
			return Inventory{}, c.Fail(c.BudgetLimitReached, "mutable SQLite WAL exceeds its bounded profile")
		}
		if name == "state.sqlite-shm" && info.Size() > maxLedgerSHMBytes {
			return Inventory{}, c.Fail(c.BudgetLimitReached, "mutable SQLite shared memory exceeds its bounded profile")
		}
		file, openErr := root.Open(name)
		if openErr != nil {
			return Inventory{}, openErr
		}
		identity, identityErr := fileguard.FileIdentity(file)
		allocated, allocatedErr := fileguard.AllocatedBytes(file)
		stat, fileStatErr := file.Stat()
		linkErr := fileguard.SingleLink(file)
		closeErr := file.Close()
		if err = errors.Join(identityErr, allocatedErr, fileStatErr, linkErr, closeErr); err != nil {
			return Inventory{}, err
		}
		if !stat.Mode().IsRegular() || stat.Size() != info.Size() || stat.Size() < 0 || allocated < 0 || !os.SameFile(info, stat) || !c.ValidDigest(identity) || uint64(allocated) > maxLogicalFileSize {
			return Inventory{}, c.Fail(c.StaleAuthority, "mutable SQLite owner file changed during measurement")
		}
		logical := uint64(stat.Size())
		allocatedBytes := uint64(allocated)
		measured := allocatedBytes
		if logical > measured {
			measured = logical
		}
		charge := uint64(directoryCharge)
		if measured > 0 {
			if measured > maxLogicalFileSize-lockedFileSlack {
				return Inventory{}, c.Fail(c.BudgetLimitReached, "mutable SQLite file charge exceeds bounded profile")
			}
			charge = measured + lockedFileSlack
		}
		digest := c.HashBytes([]byte(fmt.Sprintf("mutable-sqlite-metric-v1\x00%s\x00%d\x00%d", identity, logical, allocatedBytes)))
		result.Files = append(result.Files, FileReceipt{Path: name, Digest: digest, Identity: identity, LogicalBytes: logical, Allocated: allocatedBytes, ChargedBytes: charge})
		result.Entries++
		result.LogicalBytes, err = add(result.LogicalBytes, logical)
		if err == nil {
			result.AllocatedBytes, err = add(result.AllocatedBytes, allocatedBytes)
		}
		if err == nil {
			result.ChargedBytes, err = add(result.ChargedBytes, charge)
		}
		if err != nil {
			return Inventory{}, err
		}
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return validateInventory(result)
}

// AttachMutableSQLite imports the current database once, then keeps a durable
// bounded standby reservation for startup recovery and the next SQLite write.
// A parent WRITING allocation already owns the whole tree, so nested stores
// use that receipt instead of being charged a second time.
func (l *Ledger) AttachMutableSQLite(ctx context.Context, target string) (*MutableSQLite, error) {
	if l == nil || !filepath.IsAbs(target) {
		return nil, c.Fail(c.InvalidArgument, "absolute mutable SQLite owner required")
	}
	target = filepath.Clean(target)
	root, err := os.OpenRoot(target)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	measured, err := MeasureMutableSQLite(root)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(measured.RootPath) != target {
		return nil, c.Fail(c.StaleAuthority, "mutable SQLite owner path changed")
	}
	baseID := c.HashBytes([]byte("physical-mutable-sqlite-v1\x00" + l.Authority.ID + "\x00" + target + "\x00" + measured.RootIdentity))[:32]
	standbyID := c.HashBytes([]byte("physical-mutable-sqlite-standby-v1\x00" + baseID))[:32]
	if enclosing, covered, coverErr := l.EnclosingReservation(ctx, target); coverErr != nil {
		return nil, coverErr
	} else if covered && enclosing.ID != standbyID {
		// Compound writers measure their enclosing output tree. A matching
		// mutable standby is this owner's crash record and must be reconciled.
		if !enclosing.Covers(target) {
			return nil, c.Fail(c.StoreIntegrityError, "mutable SQLite standby does not cover its owner root")
		}
		return nil, nil
	}
	mutable := &MutableSQLite{ledger: l, rootPath: target, baseID: baseID, standby: standbyID}
	if err = mutable.attachOrRefresh(ctx, measured); err != nil {
		return nil, err
	}
	return mutable, nil
}

// Prepare atomically enlarges and consumes the standby before a SQLite writer
// dispatches. The old retained receipt and new WAL/output upper coexist in the
// ledger until Refresh records the current database state.
func (m *MutableSQLite) Prepare(ctx context.Context, requestDigest string, upperBytes uint64) error {
	if m == nil || m.ledger == nil || !c.ValidDigest(requestDigest) {
		return c.Fail(c.InvalidArgument, "mutable SQLite quota binding required")
	}
	if upperBytes < mutableStartupHeadroom {
		upperBytes = mutableStartupHeadroom
	}
	if upperBytes > 1<<48 {
		return c.Fail(c.BudgetLimitReached, "mutable SQLite write upper exceeds bounded profile")
	}
	if err := m.Refresh(ctx); err != nil {
		return err
	}
	root, err := os.OpenRoot(m.rootPath)
	if err != nil {
		return err
	}
	measured, measureErr := MeasureMutableSQLite(root)
	available, spaceErr := fileguard.AvailableBytes(root)
	closeErr := root.Close()
	if err = errors.Join(measureErr, spaceErr, closeErr); err != nil {
		return err
	}
	return m.ledger.withLock(ctx, func() error {
		tx, err := m.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var status, target, volume, class string
		var oldBytes, oldEntries, oldUpper, oldUpperEntries int64
		var oldRaw []byte
		if err = tx.QueryRowContext(ctx, `SELECT base.status,base.target_root,base.volume_identity,base.class,base.retained_bytes,base.retained_entries,base.receipt,standby.upper_bytes,standby.upper_entries
			FROM quota_allocations AS base JOIN quota_mutable_replacements AS relation ON relation.base_id=base.id
			JOIN quota_allocations AS standby ON standby.id=relation.standby_id WHERE base.id=? AND standby.id=?`, m.baseID, m.standby).Scan(&status, &target, &volume, &class, &oldBytes, &oldEntries, &oldRaw, &oldUpper, &oldUpperEntries); err != nil {
			return err
		}
		var standbyStatus string
		if err = tx.QueryRowContext(ctx, "SELECT status FROM quota_allocations WHERE id=?", m.standby).Scan(&standbyStatus); err != nil {
			return err
		}
		if status != "RETAINED" || standbyStatus != "STANDBY" || filepath.Clean(target) != m.rootPath || class != string(Payload) || oldBytes < 0 || oldEntries < 0 || oldUpper < 0 || oldUpperEntries < 0 {
			return c.Fail(c.StaleAuthority, "mutable SQLite standby is not ready")
		}
		if err = mutableReceiptStillCurrent(oldRaw, measured); err != nil {
			return err
		}
		if measured.ChargedBytes > uint64(oldBytes) || measured.Entries > uint64(oldEntries) {
			return c.Fail(c.StaleAuthority, "mutable SQLite changed outside its admitted writer")
		}
		if err = mutableCapacityForReplacement(ctx, tx, m.ledger.Limits, volume, available, uint64(oldUpper), uint64(oldUpperEntries), upperBytes, mutableEntryHeadroom); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE quota_allocations SET status='WRITING',request_digest=?,upper_bytes=?,upper_entries=? WHERE id=? AND status='STANDBY'", requestDigest, upperBytes, mutableEntryHeadroom, m.standby)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// Refresh finalizes any previous write only when the same owner root and main
// database inode remain bound and the measured transition fits the durable
// standby upper. Shrinks retain their high-water charge; no space is invented.
func (m *MutableSQLite) Refresh(ctx context.Context) error {
	if m == nil || m.ledger == nil {
		return nil
	}
	root, err := os.OpenRoot(m.rootPath)
	if err != nil {
		return err
	}
	measured, measureErr := MeasureMutableSQLite(root)
	closeErr := root.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		return err
	}
	return m.attachOrRefresh(ctx, measured)
}

func (m *MutableSQLite) attachOrRefresh(ctx context.Context, measured Inventory) error {
	if filepath.Clean(measured.RootPath) != m.rootPath {
		return c.Fail(c.StaleAuthority, "mutable SQLite owner root changed")
	}
	root, volume, parent, parentIdentity, targetIdentity, existed, available, err := targetVolume(m.rootPath)
	if err != nil || existed != 1 || filepath.Clean(root) != m.rootPath || targetIdentity != measured.RootIdentity || volume != measured.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "mutable SQLite owner identity changed")
	}
	return m.ledger.withLock(ctx, func() error {
		tx, err := m.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var status string
		var oldBytes, oldEntries int64
		var oldRaw []byte
		err = tx.QueryRowContext(ctx, "SELECT status,retained_bytes,retained_entries,receipt FROM quota_allocations WHERE id=?", m.baseID).Scan(&status, &oldBytes, &oldEntries, &oldRaw)
		if errors.Is(err, sql.ErrNoRows) {
			if err = m.insertMutableBaseline(ctx, tx, measured, root, volume, parent, parentIdentity, targetIdentity, available); err != nil {
				return err
			}
			return tx.Commit()
		}
		if err != nil {
			return err
		}
		if status != "RETAINED" || oldBytes < 0 || oldEntries < 0 {
			return c.Fail(c.StoreIntegrityError, "mutable SQLite base receipt has an invalid lifecycle")
		}
		var standbyStatus, standbyTarget, standbyVolume, standbyClass string
		var standbyUpper, standbyEntries int64
		if err = tx.QueryRowContext(ctx, `SELECT allocation.status,allocation.target_root,allocation.volume_identity,allocation.class,allocation.upper_bytes,allocation.upper_entries
			FROM quota_mutable_replacements AS relation JOIN quota_allocations AS allocation ON allocation.id=relation.standby_id WHERE relation.base_id=? AND allocation.id=?`, m.baseID, m.standby).Scan(&standbyStatus, &standbyTarget, &standbyVolume, &standbyClass, &standbyUpper, &standbyEntries); err != nil {
			return c.Fail(c.StoreIntegrityError, "mutable SQLite standby reservation is missing")
		}
		if (standbyStatus != "STANDBY" && standbyStatus != "WRITING") || filepath.Clean(standbyTarget) != m.rootPath || standbyVolume != volume || standbyClass != string(Payload) || standbyUpper < 0 || standbyEntries < 0 {
			return c.Fail(c.StoreIntegrityError, "mutable SQLite standby reservation is invalid")
		}
		if err = mutableReceiptStillCurrent(oldRaw, measured); err != nil {
			return err
		}
		maxBytes, err := add(uint64(oldBytes), uint64(standbyUpper))
		if err != nil {
			return err
		}
		maxEntries, err := add(uint64(oldEntries), uint64(standbyEntries))
		if err != nil {
			return err
		}
		if measured.ChargedBytes > maxBytes || measured.Entries > maxEntries {
			return c.Fail(c.BudgetLimitReached, "mutable SQLite growth exceeds its durable standby upper")
		}
		if err = finishMutableReceipt(ctx, tx, m.ledger.root, m.baseID, m.standby, oldBytes, oldEntries, oldRaw, measured, mutableStartupHeadroom, mutableEntryHeadroom); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (m *MutableSQLite) insertMutableBaseline(ctx context.Context, tx *sql.Tx, measured Inventory, target, volume, parent, parentIdentity, targetIdentity string, available uint64) error {
	if !filepath.IsAbs(target) || !filepath.IsAbs(parent) || !c.ValidDigest(parentIdentity) || !c.ValidDigest(targetIdentity) || !c.ValidDigest(volume) {
		return c.Fail(c.StoreIntegrityError, "mutable SQLite target binding is invalid")
	}
	raw, err := jsonMarshalInventory(measured)
	if err != nil {
		return err
	}
	if err = admitReceiptMutation(m.ledger.root, raw); err != nil {
		return err
	}
	capacity, err := capacityTx(tx, m.ledger.Limits)
	if err != nil {
		return err
	}
	usedBytes, err := add(capacity.PayloadBytes, capacity.PayloadOutstandingBytes)
	if err != nil {
		return err
	}
	usedEntries, err := add(capacity.PayloadEntries, capacity.PayloadOutstandingEntries)
	if err != nil {
		return err
	}
	bytesLimit := m.ledger.Limits.Bytes - m.ledger.Limits.ControlBytes
	entriesLimit := m.ledger.Limits.Entries - m.ledger.Limits.ControlEntries
	if usedBytes > bytesLimit || measured.ChargedBytes > bytesLimit-usedBytes || usedEntries > entriesLimit || measured.Entries > entriesLimit-usedEntries {
		return c.Fail(c.BudgetLimitReached, "mutable SQLite baseline exceeds shared physical quota")
	}
	allBytes, err := add(capacity.RetainedBytes, capacity.OutstandingBytes)
	if err != nil {
		return err
	}
	allEntries, err := add(capacity.RetainedEntries, capacity.OutstandingItems)
	if err != nil {
		return err
	}
	if allBytes > m.ledger.Limits.Bytes || measured.ChargedBytes+mutableStartupHeadroom > m.ledger.Limits.Bytes-allBytes || allEntries > m.ledger.Limits.Entries || measured.Entries+mutableEntryHeadroom > m.ledger.Limits.Entries-allEntries {
		return c.Fail(c.BudgetLimitReached, "mutable SQLite startup headroom exceeds shared physical quota")
	}
	minimum := m.ledger.Limits.LowWaterBytes
	outstanding, err := outstandingForVolumeTx(tx, volume)
	if err != nil {
		return err
	}
	if available < minimum || outstanding > available-minimum || mutableStartupHeadroom > available-minimum-outstanding {
		return c.Fail(c.BudgetLimitReached, "owner volume lacks measured free space for SQLite startup headroom")
	}
	if capacity.Allocations+2 > maxLedgerRecords {
		return c.Fail(c.BudgetLimitReached, "shared physical quota reservation catalog is full")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quota_allocations(id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries,receipt)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.baseID, "store", mutableBaseFamily, c.HashBytes([]byte("mutable-sqlite-base:"+m.baseID)), target, volume, parent, parentIdentity, targetIdentity, 1, string(Payload), "RETAINED", measured.ChargedBytes, measured.Entries, measured.ChargedBytes, measured.Entries, raw)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quota_allocations(id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.standby, "store", mutableBaseFamily, c.HashBytes([]byte("mutable-sqlite-standby:"+m.baseID)), target, volume, parent, parentIdentity, targetIdentity, 1, string(Payload), "STANDBY", mutableStartupHeadroom, mutableEntryHeadroom, 0, 0)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO quota_mutable_replacements(standby_id,base_id) VALUES(?,?)", m.standby, m.baseID)
	return err
}

func mutableCapacityForReplacement(ctx context.Context, tx *sql.Tx, limits Limits, volume string, available, oldUpper, oldUpperEntries, newUpper, newUpperEntries uint64) error {
	capacity, err := capacityTx(tx, limits)
	if err != nil {
		return err
	}
	if capacity.PayloadOutstandingBytes < oldUpper || capacity.PayloadOutstandingEntries < oldUpperEntries || capacity.OutstandingBytes < oldUpper || capacity.OutstandingItems < oldUpperEntries {
		return c.Fail(c.StoreIntegrityError, "mutable SQLite standby counters are inconsistent")
	}
	usedBytes, err := add(capacity.PayloadBytes, capacity.PayloadOutstandingBytes)
	if err != nil {
		return err
	}
	usedEntries, err := add(capacity.PayloadEntries, capacity.PayloadOutstandingEntries)
	if err != nil {
		return err
	}
	usedBytes -= oldUpper
	usedEntries -= oldUpperEntries
	byteLimit := limits.Bytes - limits.ControlBytes
	entryLimit := limits.Entries - limits.ControlEntries
	if usedBytes > byteLimit || newUpper > byteLimit-usedBytes || usedEntries > entryLimit || newUpperEntries > entryLimit-usedEntries {
		return c.Fail(c.BudgetLimitReached, "shared physical quota exhausted before mutable SQLite write")
	}
	allBytes, err := add(capacity.RetainedBytes, capacity.OutstandingBytes)
	if err != nil {
		return err
	}
	allEntries, err := add(capacity.RetainedEntries, capacity.OutstandingItems)
	if err != nil {
		return err
	}
	allBytes -= oldUpper
	allEntries -= oldUpperEntries
	if allBytes > limits.Bytes || newUpper > limits.Bytes-allBytes || allEntries > limits.Entries || newUpperEntries > limits.Entries-allEntries {
		return c.Fail(c.BudgetLimitReached, "shared physical quota exhausted; SQLite standby cannot consume control reserve")
	}
	outstanding, err := outstandingForVolumeTx(tx, volume)
	if err != nil {
		return err
	}
	if outstanding < oldUpper {
		return c.Fail(c.StoreIntegrityError, "mutable SQLite volume reservation is inconsistent")
	}
	minimum := limits.LowWaterBytes
	newOutstanding := outstanding - oldUpper
	if available < minimum || newOutstanding > available-minimum || newUpper > available-minimum-newOutstanding {
		return c.Fail(c.BudgetLimitReached, "owner volume lacks measured free space for mutable SQLite write")
	}
	return nil
}

func mutableReceiptStillCurrent(oldRaw []byte, measured Inventory) error {
	var old Inventory
	if strictJSON(oldRaw, &old) != nil {
		return c.Fail(c.StoreIntegrityError, "mutable SQLite retained receipt is invalid")
	}
	canonical, err := jsonMarshalInventory(old)
	if err != nil || string(canonical) != string(oldRaw) || old.RootPath != measured.RootPath || old.RootIdentity != measured.RootIdentity || old.VolumeIdentity != measured.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "mutable SQLite retained root binding changed")
	}
	oldDB, newDB := mutableMainReceipt(old), mutableMainReceipt(measured)
	if oldDB.Identity == "" || newDB.Identity == "" || oldDB.Identity != newDB.Identity {
		return c.Fail(c.StaleAuthority, "mutable SQLite database inode changed")
	}
	for _, name := range []string{"owner.lock"} {
		left, right := mutableReceipt(old, name), mutableReceipt(measured, name)
		if left.Identity == "" || right.Identity == "" || left.Identity != right.Identity {
			return c.Fail(c.StaleAuthority, "mutable SQLite owner lock inode changed")
		}
	}
	return nil
}

func mutableMainReceipt(inventory Inventory) FileReceipt {
	return mutableReceipt(inventory, "state.sqlite")
}

func mutableReceipt(inventory Inventory, name string) FileReceipt {
	for _, file := range inventory.Files {
		if file.Path == name {
			return file
		}
	}
	return FileReceipt{}
}

func finishMutableReceipt(ctx context.Context, tx *sql.Tx, root *os.Root, baseID, standbyID string, oldBytes, oldEntries int64, oldRaw []byte, measured Inventory, upperBytes, upperEntries uint64) error {
	var old Inventory
	if strictJSON(oldRaw, &old) != nil || oldBytes < 0 || oldEntries < 0 {
		return c.Fail(c.StoreIntegrityError, "mutable SQLite receipt counters are invalid")
	}
	if err := mutableReceiptStillCurrent(oldRaw, measured); err != nil {
		return err
	}
	if measured.ChargedBytes > math.MaxInt64 || measured.Entries > math.MaxInt64 || upperBytes > math.MaxInt64 || upperEntries > math.MaxInt64 {
		return c.Fail(c.BudgetLimitReached, "mutable SQLite receipt exceeds bounded counters")
	}
	if uint64(oldBytes) > measured.ChargedBytes {
		measured.ChargedBytes = uint64(oldBytes)
	}
	if old.AllocatedBytes > measured.AllocatedBytes {
		measured.AllocatedBytes = old.AllocatedBytes
	}
	if old.LogicalBytes > measured.LogicalBytes {
		measured.LogicalBytes = old.LogicalBytes
	}
	if uint64(oldEntries) > measured.Entries {
		measured.Entries = uint64(oldEntries)
	}
	raw, err := jsonMarshalInventory(measured)
	if err != nil {
		return err
	}
	if err = admitReceiptMutation(root, raw); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE quota_allocations SET request_digest=?,upper_bytes=?,upper_entries=?,retained_bytes=?,retained_entries=?,receipt=? WHERE id=? AND status='RETAINED'", c.HashBytes([]byte("mutable-sqlite-base:"+baseID)), measured.ChargedBytes, measured.Entries, measured.ChargedBytes, measured.Entries, raw, baseID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "mutable SQLite base receipt changed during refresh")
	}
	result, err = tx.ExecContext(ctx, "UPDATE quota_allocations SET status='STANDBY',request_digest=?,upper_bytes=?,upper_entries=?,retained_bytes=0,retained_entries=0,receipt=NULL WHERE id=? AND status IN ('STANDBY','WRITING')", c.HashBytes([]byte("mutable-sqlite-standby:"+baseID)), upperBytes, upperEntries, standbyID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "mutable SQLite standby changed during refresh")
	}
	return nil
}

func jsonMarshalInventory(inventory Inventory) ([]byte, error) {
	return json.Marshal(inventory)
}

func ensureMutableReplacementSchema(db *sql.DB) error {
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='quota_mutable_replacements'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		if _, err := db.Exec(mutableReplacementDDL); err != nil {
			return err
		}
	} else if exists != 1 {
		return c.Fail(c.StoreIntegrityError, "mutable SQLite replacement table is ambiguous")
	}
	rows, err := db.Query("PRAGMA table_info(quota_mutable_replacements)")
	if err != nil {
		return err
	}
	want := []string{"standby_id", "base_id"}
	columns := make([]string, 0, len(want))
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, name)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(columns) != len(want) || columns[0] != want[0] || columns[1] != want[1] {
		return c.Fail(c.StoreIntegrityError, "mutable SQLite replacement schema is invalid")
	}
	var invalid int
	if err = db.QueryRow(`SELECT COUNT(*) FROM quota_mutable_replacements AS relation
		LEFT JOIN quota_allocations AS standby ON standby.id=relation.standby_id
		LEFT JOIN quota_allocations AS base ON base.id=relation.base_id
		WHERE standby.id IS NULL OR base.id IS NULL OR standby.status NOT IN ('STANDBY','WRITING') OR base.status<>'RETAINED'
		OR standby.class<>'PAYLOAD' OR base.class<>'PAYLOAD' OR standby.target_root<>base.target_root
		OR standby.volume_identity<>base.volume_identity`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return c.Fail(c.StoreIntegrityError, "mutable SQLite replacement binding is invalid")
	}
	return nil
}
