package resource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/diskguard"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const controlReserveFamily = "CONTROL_RESERVE"

const controlReserveAllocationSlack uint64 = 512 << 10

const controlReserveDDL = "CREATE TABLE quota_control_reserves (base_id TEXT PRIMARY KEY REFERENCES quota_allocations(id),standby_id TEXT NOT NULL UNIQUE REFERENCES quota_allocations(id),relative_path TEXT NOT NULL CHECK(relative_path='control.reserve'),file_identity TEXT NOT NULL,root_identity TEXT NOT NULL,volume_identity TEXT NOT NULL)"

// ControlReserve binds the diskguard reserve file to the owner root and keeps
// its measured high-water in PAYLOAD capacity. The linked standby covers only
// growth up to diskguard's fixed ceiling; it is not emergency Control quota.
type ControlReserve struct {
	ledger  *Ledger
	root    string
	baseID  string
	standby string
}

// MeasureControlReserve measures the exact owner-side file namespace without
// reading its random contents. Its metric receipt is an identity/size receipt,
// never a content or deletion proof.
func MeasureControlReserve(root *os.Root) (Inventory, error) {
	if root == nil {
		return Inventory{}, c.Fail(c.InvalidArgument, "rooted control reserve owner required")
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
	info, err := root.Lstat("control.reserve")
	if err != nil {
		return Inventory{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > diskguard.ReserveBytes {
		return Inventory{}, c.Fail(c.UnsupportedCapability, "control reserve is not a bounded regular file")
	}
	file, err := root.Open("control.reserve")
	if err != nil {
		return Inventory{}, err
	}
	identity, identityErr := fileguard.FileIdentity(file)
	allocated, allocatedErr := fileguard.AllocatedBytes(file)
	stat, statErr := file.Stat()
	linkErr := fileguard.SingleLink(file)
	closeErr := file.Close()
	if err = errors.Join(identityErr, allocatedErr, statErr, linkErr, closeErr); err != nil {
		return Inventory{}, err
	}
	if !info.Mode().IsRegular() || !stat.Mode().IsRegular() || info.Size() < 0 || stat.Size() < 0 {
		return Inventory{}, c.Fail(c.UnsupportedCapability, "control reserve is not a regular bounded file")
	}
	if !os.SameFile(info, stat) {
		return Inventory{}, c.Fail(c.StaleAuthority, "control reserve inode changed during physical measurement")
	}
	if stat.Size() != info.Size() {
		return Inventory{}, c.Fail(c.StaleAuthority, "control reserve size changed during physical measurement")
	}
	if allocated < stat.Size() {
		return Inventory{}, c.Fail(c.UnsupportedCapability, "control reserve physical allocation is below its logical size")
	}
	if allocated > diskguard.ReserveBytes+int64(controlReserveAllocationSlack) {
		return Inventory{}, c.Fail(c.BudgetLimitReached, "control reserve exceeds its fixed physical profile")
	}
	if !c.ValidDigest(identity) {
		return Inventory{}, c.Fail(c.StoreIntegrityError, "control reserve inode identity is invalid")
	}
	physical := uint64(allocated)
	if uint64(stat.Size()) > physical {
		physical = uint64(stat.Size())
	}
	charge, err := controlReserveCharge(physical)
	if err != nil {
		return Inventory{}, err
	}
	logical := uint64(stat.Size())
	allocatedBytes := uint64(allocated)
	digest := controlReserveMetricDigest(identity, logical, allocatedBytes)
	fileReceipt := FileReceipt{Path: "control.reserve", Digest: digest, Identity: identity, LogicalBytes: logical, Allocated: allocatedBytes, ChargedBytes: charge}
	return validateInventory(Inventory{
		RootPath:       rootPath,
		RootIdentity:   rootIdentity,
		VolumeIdentity: volume,
		AllocatedBytes: allocatedBytes,
		LogicalBytes:   logical,
		ChargedBytes:   charge,
		Entries:        1,
		Files:          []FileReceipt{fileReceipt},
	})
}

func controlReserveMetricDigest(identity string, logical, allocated uint64) string {
	return c.HashBytes([]byte(fmt.Sprintf("physical-control-reserve-metric-v1\x00%s\x00%d\x00%d", identity, logical, allocated)))
}

func controlReserveCharge(physical uint64) (uint64, error) {
	if physical > uint64(diskguard.ReserveBytes)+controlReserveAllocationSlack {
		return 0, c.Fail(c.BudgetLimitReached, "control reserve exceeds its fixed physical profile")
	}
	if physical == 0 {
		return directoryCharge, nil
	}
	if physical > math.MaxUint64-lockedFileSlack {
		return 0, c.Fail(c.BudgetLimitReached, "control reserve physical charge overflow")
	}
	return physical + lockedFileSlack, nil
}

func controlReserveCeiling() uint64 {
	return uint64(diskguard.ReserveBytes) + controlReserveAllocationSlack + lockedFileSlack
}

func controlReservePhysical(inventory Inventory) uint64 {
	physical := inventory.AllocatedBytes
	if inventory.LogicalBytes > physical {
		physical = inventory.LogicalBytes
	}
	return physical
}

// AttachControlReserve imports the exact existing reserve once and retains a
// bounded standby for future diskguard replenishment. A compound writer that
// already owns the entire owner tree remains the sole receipt for that tree.
func (l *Ledger) AttachControlReserve(ctx context.Context, target string) (*ControlReserve, error) {
	if l == nil || !filepath.IsAbs(target) {
		return nil, c.Fail(c.InvalidArgument, "absolute control reserve owner required")
	}
	target = filepath.Clean(target)
	root, err := os.OpenRoot(target)
	if err != nil {
		return nil, err
	}
	measured, measureErr := MeasureControlReserve(root)
	closeErr := root.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		return nil, err
	}
	if filepath.Clean(measured.RootPath) != target {
		return nil, c.Fail(c.StaleAuthority, "control reserve owner path changed")
	}
	baseID := c.HashBytes([]byte("physical-control-reserve-v1\x00" + l.Authority.ID + "\x00" + target + "\x00" + measured.RootIdentity))[:32]
	standbyID := c.HashBytes([]byte("physical-control-reserve-standby-v1\x00" + baseID))[:32]
	reserve := &ControlReserve{ledger: l, root: target, baseID: baseID, standby: standbyID}
	registered, err := reserve.registered(ctx)
	if err != nil {
		return nil, err
	}
	if _, covered, coverErr := l.EnclosingReservation(ctx, target); coverErr != nil {
		return nil, coverErr
	} else if covered && !registered {
		return nil, nil
	}
	if err = reserve.attachOrRefresh(ctx, measured); err != nil {
		return nil, err
	}
	return reserve, nil
}

func (r *ControlReserve) registered(ctx context.Context) (bool, error) {
	var count int
	err := r.ledger.withReadLock(ctx, func() error {
		return r.ledger.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_control_reserves WHERE base_id=?", r.baseID).Scan(&count)
	})
	if err != nil {
		return false, err
	}
	if count > 1 {
		return false, c.Fail(c.StoreIntegrityError, "ambiguous control reserve identity binding")
	}
	return count == 1, nil
}

// Prepare promotes the growth standby to WRITING before diskguard can refill
// the reserve file. The old high-water remains retained while growth is
// uncertain, including after a process crash.
func (r *ControlReserve) Prepare(ctx context.Context, requestDigest string) error {
	if r == nil || r.ledger == nil || !c.ValidDigest(requestDigest) {
		return c.Fail(c.InvalidArgument, "control reserve quota binding required")
	}
	if err := r.Refresh(ctx); err != nil {
		return err
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return err
	}
	measured, measureErr := MeasureControlReserve(root)
	available, spaceErr := fileguard.AvailableBytes(root)
	closeErr := root.Close()
	if err = errors.Join(measureErr, spaceErr, closeErr); err != nil {
		return err
	}
	return r.ledger.withLock(ctx, func() error {
		tx, err := r.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var baseStatus, standbyStatus, target, volume, class string
		var oldBytes, oldEntries, oldUpper, oldUpperEntries int64
		var oldRaw []byte
		if err = tx.QueryRowContext(ctx, `SELECT base.status,standby.status,base.target_root,base.volume_identity,base.class,base.retained_bytes,base.retained_entries,standby.upper_bytes,standby.upper_entries,base.receipt
			FROM quota_control_reserves AS relation JOIN quota_allocations AS base ON base.id=relation.base_id
			JOIN quota_allocations AS standby ON standby.id=relation.standby_id WHERE base.id=? AND standby.id=? AND relation.relative_path='control.reserve'`, r.baseID, r.standby).Scan(&baseStatus, &standbyStatus, &target, &volume, &class, &oldBytes, &oldEntries, &oldUpper, &oldUpperEntries, &oldRaw); err != nil {
			return c.Fail(c.StoreIntegrityError, "typed control reserve quota relation is missing")
		}
		if baseStatus != "RETAINED" || standbyStatus != "STANDBY" || filepath.Clean(target) != r.root || class != string(Payload) || oldBytes < 0 || uint64(oldBytes) > controlReserveCeiling() || oldEntries != 1 || oldUpper < 0 || oldUpperEntries != 0 {
			return c.Fail(c.StaleAuthority, "control reserve high-water is not ready")
		}
		if err = controlReserveReceiptStillCurrent(oldRaw, measured); err != nil {
			return err
		}
		if controlReservePhysical(measured) > controlReservePhysicalFromReceipt(oldRaw) {
			return c.Fail(c.StaleAuthority, "control reserve grew outside its admitted writer")
		}
		newUpper := controlReserveCeiling() - uint64(oldBytes)
		if err = mutableCapacityForReplacement(ctx, tx, r.ledger.Limits, volume, available, uint64(oldUpper), uint64(oldUpperEntries), newUpper, 0); err != nil {
			return err
		}
		if newUpper > math.MaxInt64 {
			return c.Fail(c.BudgetLimitReached, "control reserve standby exceeds bounded counters")
		}
		_, err = tx.ExecContext(ctx, "UPDATE quota_allocations SET status='WRITING',request_digest=?,upper_bytes=?,upper_entries=0 WHERE id=? AND status='STANDBY'", requestDigest, newUpper, r.standby)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// Refresh reconciles a completed or crashed refill only when the same rooted
// reserve inode remains within its previously admitted high-water envelope.
func (r *ControlReserve) Refresh(ctx context.Context) error {
	if r == nil || r.ledger == nil {
		return nil
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return err
	}
	measured, measureErr := MeasureControlReserve(root)
	closeErr := root.Close()
	if err = errors.Join(measureErr, closeErr); err != nil {
		return err
	}
	return r.attachOrRefresh(ctx, measured)
}

func (r *ControlReserve) attachOrRefresh(ctx context.Context, measured Inventory) error {
	if filepath.Clean(measured.RootPath) != r.root {
		return c.Fail(c.StaleAuthority, "control reserve owner root changed")
	}
	target, volume, parent, parentIdentity, targetIdentity, existed, available, err := targetVolume(r.root)
	if err != nil || existed != 1 || filepath.Clean(target) != r.root || targetIdentity != measured.RootIdentity || volume != measured.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "control reserve owner identity changed")
	}
	return r.ledger.withLock(ctx, func() error {
		tx, err := r.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var status string
		var oldBytes, oldEntries int64
		var oldRaw []byte
		err = tx.QueryRowContext(ctx, "SELECT status,retained_bytes,retained_entries,receipt FROM quota_allocations WHERE id=?", r.baseID).Scan(&status, &oldBytes, &oldEntries, &oldRaw)
		if errors.Is(err, sql.ErrNoRows) {
			if err = r.insertBaseline(ctx, tx, measured, target, volume, parent, parentIdentity, targetIdentity, available); err != nil {
				return err
			}
			return tx.Commit()
		}
		if err != nil {
			return err
		}
		var standbyStatus, standbyTarget, standbyVolume, standbyClass string
		var standbyUpper, standbyEntries int64
		var relationIdentity, relationRoot, relationVolume string
		if err = tx.QueryRowContext(ctx, `SELECT standby.status,standby.target_root,standby.volume_identity,standby.class,standby.upper_bytes,standby.upper_entries,relation.file_identity,relation.root_identity,relation.volume_identity
			FROM quota_control_reserves AS relation JOIN quota_allocations AS standby ON standby.id=relation.standby_id WHERE relation.base_id=? AND standby.id=? AND relation.relative_path='control.reserve'`, r.baseID, r.standby).Scan(&standbyStatus, &standbyTarget, &standbyVolume, &standbyClass, &standbyUpper, &standbyEntries, &relationIdentity, &relationRoot, &relationVolume); err != nil {
			return c.Fail(c.StoreIntegrityError, "control reserve high-water relation is missing")
		}
		if status != "RETAINED" || oldBytes < 0 || oldEntries != 1 || (standbyStatus != "STANDBY" && standbyStatus != "WRITING") || filepath.Clean(standbyTarget) != r.root || standbyVolume != volume || standbyClass != string(Payload) || standbyUpper < 0 || standbyEntries != 0 {
			return c.Fail(c.StoreIntegrityError, "control reserve high-water lifecycle is invalid")
		}
		if relationIdentity != controlReserveReceiptIdentity(oldRaw) || relationRoot != targetIdentity || relationVolume != volume {
			return c.Fail(c.StaleAuthority, "control reserve inode or owner binding changed")
		}
		if err = controlReserveReceiptStillCurrent(oldRaw, measured); err != nil {
			return err
		}
		maxBytes, err := add(uint64(oldBytes), uint64(standbyUpper))
		if err != nil {
			return err
		}
		if measured.ChargedBytes > maxBytes || (standbyStatus == "STANDBY" && measured.ChargedBytes > uint64(oldBytes)) {
			return c.Fail(c.BudgetLimitReached, "control reserve growth exceeds its durable standby")
		}
		if err = finishControlReserveReceipt(ctx, tx, r.ledger.root, r.baseID, r.standby, oldBytes, oldEntries, oldRaw, measured); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (r *ControlReserve) insertBaseline(ctx context.Context, tx *sql.Tx, measured Inventory, target, volume, parent, parentIdentity, targetIdentity string, available uint64) error {
	if !filepath.IsAbs(target) || !filepath.IsAbs(parent) || !c.ValidDigest(parentIdentity) || !c.ValidDigest(targetIdentity) || !c.ValidDigest(volume) {
		return c.Fail(c.StoreIntegrityError, "control reserve target binding is invalid")
	}
	if err := controlReserveReceiptStillCurrent(nil, measured); err != nil {
		return err
	}
	raw, err := jsonMarshalInventory(measured)
	if err != nil {
		return err
	}
	if err = admitReceiptMutation(r.ledger.root, raw); err != nil {
		return err
	}
	standbyBytes := controlReserveCeiling() - measured.ChargedBytes
	capacity, err := capacityTx(tx, r.ledger.Limits)
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
	byteLimit := r.ledger.Limits.Bytes - r.ledger.Limits.ControlBytes
	entryLimit := r.ledger.Limits.Entries - r.ledger.Limits.ControlEntries
	if usedBytes > byteLimit || controlReserveCeiling() > byteLimit-usedBytes || usedEntries >= entryLimit {
		return c.Fail(c.BudgetLimitReached, "control reserve high-water exceeds shared PAYLOAD quota")
	}
	allBytes, err := add(capacity.RetainedBytes, capacity.OutstandingBytes)
	if err != nil {
		return err
	}
	allEntries, err := add(capacity.RetainedEntries, capacity.OutstandingItems)
	if err != nil {
		return err
	}
	if allBytes > r.ledger.Limits.Bytes || controlReserveCeiling() > r.ledger.Limits.Bytes-allBytes || allEntries >= r.ledger.Limits.Entries {
		return c.Fail(c.BudgetLimitReached, "control reserve high-water exceeds shared physical quota")
	}
	minimum := r.ledger.Limits.LowWaterBytes
	outstanding, err := outstandingForVolumeTx(tx, volume)
	if err != nil {
		return err
	}
	if available < minimum || outstanding > available-minimum || standbyBytes > available-minimum-outstanding {
		return c.Fail(c.BudgetLimitReached, "owner volume lacks measured free space for control reserve high-water")
	}
	if capacity.Allocations+2 > maxLedgerRecords || standbyBytes > math.MaxInt64 || measured.ChargedBytes > math.MaxInt64 {
		return c.Fail(c.BudgetLimitReached, "shared physical quota reservation catalog is full")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quota_allocations(id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries,receipt)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.baseID, "store", controlReserveFamily, c.HashBytes([]byte("control-reserve-base:"+r.baseID)), target, volume, parent, parentIdentity, targetIdentity, 1, string(Payload), "RETAINED", measured.ChargedBytes, 1, measured.ChargedBytes, 1, raw)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quota_allocations(id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.standby, "store", controlReserveFamily, c.HashBytes([]byte("control-reserve-standby:"+r.baseID)), target, volume, parent, parentIdentity, targetIdentity, 1, string(Payload), "STANDBY", standbyBytes, 0, 0, 0)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO quota_control_reserves(base_id,standby_id,relative_path,file_identity,root_identity,volume_identity) VALUES(?,?,?,?,?,?)", r.baseID, r.standby, "control.reserve", controlReserveReceiptIdentity(raw), targetIdentity, volume)
	return err
}

func controlReserveReceiptIdentity(raw []byte) string {
	var inventory Inventory
	if strictJSON(raw, &inventory) != nil || len(inventory.Files) != 1 {
		return ""
	}
	return inventory.Files[0].Identity
}

func controlReservePhysicalFromReceipt(raw []byte) uint64 {
	var inventory Inventory
	if strictJSON(raw, &inventory) != nil {
		return 0
	}
	return controlReservePhysical(inventory)
}

func controlReserveReceiptStillCurrent(oldRaw []byte, measured Inventory) error {
	if len(measured.Files) != 1 || measured.Files[0].Path != "control.reserve" || measured.Entries != 1 || measured.Files[0].Identity == "" {
		return c.Fail(c.StoreIntegrityError, "control reserve metric receipt is malformed")
	}
	if len(oldRaw) == 0 {
		return nil
	}
	var old Inventory
	if strictJSON(oldRaw, &old) != nil {
		return c.Fail(c.StoreIntegrityError, "control reserve retained receipt is invalid")
	}
	canonical, err := jsonMarshalInventory(old)
	if err != nil || string(canonical) != string(oldRaw) || len(old.Files) != 1 || old.Files[0].Path != "control.reserve" || old.RootPath != measured.RootPath || old.RootIdentity != measured.RootIdentity || old.VolumeIdentity != measured.VolumeIdentity || old.Files[0].Identity != measured.Files[0].Identity {
		return c.Fail(c.StaleAuthority, "control reserve owner or inode binding changed")
	}
	oldPhysical := controlReservePhysical(old)
	oldCharge, chargeErr := controlReserveCharge(oldPhysical)
	if chargeErr != nil || old.Entries != 1 || old.ChargedBytes != oldCharge || old.AllocatedBytes != old.Files[0].Allocated || old.LogicalBytes != old.Files[0].LogicalBytes || old.Files[0].ChargedBytes != oldCharge || old.Files[0].Digest != controlReserveMetricDigest(old.Files[0].Identity, old.Files[0].LogicalBytes, old.Files[0].Allocated) {
		return c.Fail(c.StoreIntegrityError, "control reserve retained high-water metrics are invalid")
	}
	return nil
}

func finishControlReserveReceipt(ctx context.Context, tx *sql.Tx, root *os.Root, baseID, standbyID string, oldBytes, oldEntries int64, oldRaw []byte, measured Inventory) error {
	if oldBytes < 0 || oldEntries != 1 {
		return c.Fail(c.StoreIntegrityError, "control reserve retained counters are invalid")
	}
	if err := controlReserveReceiptStillCurrent(oldRaw, measured); err != nil {
		return err
	}
	var old Inventory
	if strictJSON(oldRaw, &old) != nil {
		return c.Fail(c.StoreIntegrityError, "control reserve high-water receipt is invalid")
	}
	physical := controlReservePhysical(old)
	if current := controlReservePhysical(measured); current > physical {
		physical = current
	}
	charge, err := controlReserveCharge(physical)
	if err != nil {
		return err
	}
	logical := old.LogicalBytes
	if measured.LogicalBytes > logical {
		logical = measured.LogicalBytes
	}
	allocated := old.AllocatedBytes
	if measured.AllocatedBytes > allocated {
		allocated = measured.AllocatedBytes
	}
	identity := old.Files[0].Identity
	receipt := FileReceipt{Path: "control.reserve", Digest: controlReserveMetricDigest(identity, logical, allocated), Identity: identity, LogicalBytes: logical, Allocated: allocated, ChargedBytes: charge}
	highWater := Inventory{RootPath: measured.RootPath, RootIdentity: measured.RootIdentity, VolumeIdentity: measured.VolumeIdentity, AllocatedBytes: allocated, LogicalBytes: logical, ChargedBytes: charge, Entries: 1, Files: []FileReceipt{receipt}}
	if _, err = validateInventory(highWater); err != nil {
		return err
	}
	if charge > math.MaxInt64 {
		return c.Fail(c.BudgetLimitReached, "control reserve retained charge exceeds bounded counters")
	}
	raw, err := jsonMarshalInventory(highWater)
	if err != nil {
		return err
	}
	if err = admitReceiptMutation(root, raw); err != nil {
		return err
	}
	standbyBytes := controlReserveCeiling() - charge
	result, err := tx.ExecContext(ctx, "UPDATE quota_allocations SET request_digest=?,upper_bytes=?,upper_entries=1,retained_bytes=?,retained_entries=1,receipt=? WHERE id=? AND status='RETAINED'", c.HashBytes([]byte("control-reserve-base:"+baseID)), charge, charge, raw, baseID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "control reserve base receipt changed during refresh")
	}
	result, err = tx.ExecContext(ctx, "UPDATE quota_allocations SET status='STANDBY',request_digest=?,upper_bytes=?,upper_entries=0,retained_bytes=0,retained_entries=0,receipt=NULL WHERE id=? AND status IN ('STANDBY','WRITING')", c.HashBytes([]byte("control-reserve-standby:"+baseID)), standbyBytes, standbyID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "control reserve standby changed during refresh")
	}
	return nil
}

func ensureControlReserveSchema(db *sql.DB) error {
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='quota_control_reserves'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		if _, err := db.Exec(controlReserveDDL); err != nil {
			return err
		}
	} else if exists != 1 {
		return c.Fail(c.StoreIntegrityError, "physical control reserve table is ambiguous")
	}
	rows, err := db.Query("PRAGMA table_info(quota_control_reserves)")
	if err != nil {
		return err
	}
	want := []string{"base_id", "standby_id", "relative_path", "file_identity", "root_identity", "volume_identity"}
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
	if len(columns) != len(want) {
		return c.Fail(c.StoreIntegrityError, "physical control reserve schema is invalid")
	}
	for i := range want {
		if columns[i] != want[i] {
			return c.Fail(c.StoreIntegrityError, "physical control reserve schema is invalid")
		}
	}
	var invalid int
	if err = db.QueryRow(`SELECT COUNT(*) FROM quota_control_reserves AS relation
		LEFT JOIN quota_allocations AS base ON base.id=relation.base_id
		LEFT JOIN quota_allocations AS standby ON standby.id=relation.standby_id
		WHERE base.id IS NULL OR standby.id IS NULL OR relation.relative_path<>'control.reserve'
		OR base.status<>'RETAINED' OR standby.status NOT IN ('STANDBY','WRITING')
		OR base.writer<>'store' OR standby.writer<>'store' OR base.family<>'CONTROL_RESERVE' OR standby.family<>'CONTROL_RESERVE'
		OR base.class<>'PAYLOAD' OR standby.class<>'PAYLOAD' OR base.target_root<>standby.target_root
		OR base.target_identity<>relation.root_identity OR base.volume_identity<>relation.volume_identity
		OR standby.volume_identity<>relation.volume_identity OR base.retained_entries<>1 OR standby.upper_entries<>0`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return c.Fail(c.StoreIntegrityError, "typed physical control reserve binding is invalid")
	}
	return nil
}
