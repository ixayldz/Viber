// Package resource implements a durable physical write budget shared by every
// owner bound to one external privacy authority. Reservations are made before a
// writer creates its first directory or temporary file. An unfinished
// reservation remains charged after process death.
package resource

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/diskguard"
	"github.com/ixayldz/Viber/internal/fileguard"
	_ "modernc.org/sqlite"
)

const (
	ledgerSchema       = 1
	maxLedgerRecords   = 100000
	maxInventoryItems  = 1 << 20
	maxInventoryBytes  = 8 << 20
	maxLogicalFileSize = 1 << 48
	metadataAdmission  = 64 << 10
	lockedFileSlack    = 64 << 10
	maxLedgerPages     = 32768
	maxLedgerDBBytes   = int64(maxLedgerPages * 4096)
	maxLedgerWALBytes  = int64(16 << 20)
	maxLedgerSHMBytes  = int64(1 << 20)
	defaultQuotaBytes  = 8 << 30
	defaultQuotaItems  = 1 << 20
	defaultControlSize = 32 << 20
	defaultControlItem = 1024
	defaultLowWater    = 64 << 20
	directoryCharge    = 4096
)

type Class string

const (
	Payload Class = "PAYLOAD"
	Control Class = "CONTROL"
)

type Authority struct {
	ID           string
	Directory    string
	PhysicalRoot string
}

type Limits struct {
	Bytes          uint64 `json:"bytes"`
	Entries        uint64 `json:"entries"`
	ControlBytes   uint64 `json:"control_bytes"`
	ControlEntries uint64 `json:"control_entries"`
	LowWaterBytes  uint64 `json:"low_water_bytes"`
}

func DefaultLimits() Limits {
	return Limits{Bytes: defaultQuotaBytes, Entries: defaultQuotaItems, ControlBytes: defaultControlSize, ControlEntries: defaultControlItem, LowWaterBytes: defaultLowWater}
}

func (l Limits) validate() error {
	if l.Bytes < 1<<20 || l.Bytes > 1<<48 || l.Entries < 1 || l.Entries > 1<<40 || l.ControlBytes < 1 || l.ControlBytes >= l.Bytes || l.ControlEntries < 1 || l.ControlEntries >= l.Entries || l.LowWaterBytes > 1<<48 {
		return c.Fail(c.InvalidArgument, "invalid shared physical quota limits")
	}
	return nil
}

type Request struct {
	ID     string `json:"id,omitempty"`
	Writer string `json:"writer"`
	Family string `json:"family"`
	// AncestorFamily names the only outer writer family this nested request
	// may inherit. Empty disables ancestor coverage. It is caller policy, not
	// part of the durable allocation envelope.
	AncestorFamily string `json:"-"`
	RequestDigest  string `json:"request_digest"`
	TargetRoot     string `json:"target_root"`
	UpperBytes     uint64 `json:"upper_bytes"`
	UpperEntries   uint64 `json:"upper_entries"`
	Class          Class  `json:"class"`
}

type FileReceipt struct {
	Path         string `json:"path"`
	Digest       string `json:"digest"`
	Identity     string `json:"identity"`
	LogicalBytes uint64 `json:"logical_bytes"`
	Allocated    uint64 `json:"allocated_bytes"`
	ChargedBytes uint64 `json:"charged_bytes"`
}

type Inventory struct {
	RootPath       string        `json:"root_path"`
	RootIdentity   string        `json:"root_identity"`
	VolumeIdentity string        `json:"volume_identity"`
	AllocatedBytes uint64        `json:"allocated_bytes"`
	LogicalBytes   uint64        `json:"logical_bytes"`
	ChargedBytes   uint64        `json:"charged_bytes"`
	Entries        uint64        `json:"entries"`
	Files          []FileReceipt `json:"files,omitempty"`
}

type Capacity struct {
	SchemaVersion    int    `json:"schema_version"`
	Coverage         string `json:"coverage"`
	LimitBytes       uint64 `json:"limit_bytes"`
	LimitEntries     uint64 `json:"limit_entries"`
	ControlBytes     uint64 `json:"control_bytes"`
	ControlEntries   uint64 `json:"control_entries"`
	RetainedBytes    uint64 `json:"retained_bytes"`
	OutstandingBytes uint64 `json:"outstanding_bytes"`
	RetainedEntries  uint64 `json:"retained_entries"`
	OutstandingItems uint64 `json:"outstanding_entries"`
	Unknown          int    `json:"uncertain_allocations"`
	Allocations      int    `json:"allocations"`
}

// ReconcileResult reports exact free intents resolved without hiding ones for
// which the filesystem has not proved that the reservation was returned.
type ReconcileResult struct {
	ReclaimedFiles int `json:"reclaimed_files"`
	CancelledFiles int `json:"cancelled_files"`
	PendingFiles   int `json:"pending_files"`
	LegacyUnknown  int `json:"legacy_unknown"`
}

type anchor struct {
	SchemaVersion    int    `json:"schema_version"`
	AuthorityID      string `json:"authority_id"`
	PhysicalRoot     string `json:"physical_root"`
	LedgerID         string `json:"ledger_id"`
	DatabaseIdentity string `json:"database_identity"`
}

type authorityGenesis struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Directory     string `json:"directory"`
	PhysicalRoot  string `json:"physical_root"`
}

type Ledger struct {
	mu               sync.Mutex
	root             *os.Root
	db               *sql.DB
	lockName         string
	closed           bool
	Authority        Authority
	Limits           Limits
	LedgerID         string
	DatabaseIdentity string
}

type Allocation struct {
	ledger     *Ledger
	ID         string
	targetRoot string
}

type ReservationScope string

const (
	ExactReservationScope    ReservationScope = "EXACT"
	AncestorReservationScope ReservationScope = "ANCESTOR"
)

// ReservationMatch reports an existing reservation covering a requested
// output, or the new RESERVED allocation created for that request. Exact
// reservations belong to the writer and must be begun/committed by it.
// Ancestor reservations belong to an enclosing writer and must never be
// committed by a nested writer.
type ReservationMatch struct {
	Allocation Allocation
	Scope      ReservationScope
	Status     string
	Created    bool
	// UpperBytes and UpperEntries are the immutable bounds admitted with the
	// allocation. Intent-backed retry callers reuse these exact bounds only
	// after validating their saved stable binding.
	UpperBytes   uint64
	UpperEntries uint64
	// RequestDigest is the immutable digest admitted with the original exact
	// allocation. Intent-backed retries may submit a newer observation while
	// reusing the original payload digest.
	RequestDigest string
}

func Open(authority Authority, limits Limits) (*Ledger, error) {
	return OpenPinned(authority, limits, "")
}

// OpenPinned attaches only to the exact authority ledger already witnessed by
// an owner store. An empty expected ID is accepted only for first binding.
func OpenPinned(authority Authority, limits Limits, expectedLedgerID string) (*Ledger, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	if expectedLedgerID != "" && !validID(expectedLedgerID) {
		return nil, c.Fail(c.InvalidArgument, "invalid pinned physical quota ledger ID")
	}
	if err := validateAuthority(authority); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(authority.Directory)
	if err != nil {
		return nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil || filepath.Clean(canonical) != filepath.Clean(authority.Directory) {
		return nil, c.Fail(c.StaleAuthority, "physical quota authority path changed")
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = root.Close()
		}
	}()
	if err = fileguard.Private(root); err != nil {
		return nil, err
	}
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil || physical != authority.PhysicalRoot {
		return nil, c.Fail(c.StaleAuthority, "physical quota authority root was replaced")
	}
	if err = verifyAuthorityGenesis(root, authority); err != nil {
		return nil, err
	}
	lock, err := acquireLock(root)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = admitMetadata(root, metadataAdmission); err != nil {
		return nil, err
	}
	for _, name := range []string{"physical-quota.anchor", "physical-quota.sqlite", "physical-quota.sqlite-wal", "physical-quota.sqlite-shm", "physical-quota.lock"} {
		if err = fileguard.RegularPath(root, name, true); err != nil {
			return nil, err
		}
	}
	anchorRaw, anchorErr := fileguard.ReadRegular(root, "physical-quota.anchor", 4096)
	if anchorErr != nil && !errors.Is(anchorErr, os.ErrNotExist) {
		return nil, anchorErr
	}
	anchorExists := anchorErr == nil
	if anchorExists {
		var saved anchor
		if strictJSON(anchorRaw, &saved) != nil {
			return nil, c.Fail(c.StaleAuthority, "physical quota anchor binding is invalid")
		}
		canonical, canonicalErr := json.Marshal(saved)
		if canonicalErr != nil || !bytes.Equal(canonical, anchorRaw) || saved.SchemaVersion != ledgerSchema || saved.AuthorityID != authority.ID || saved.PhysicalRoot != authority.PhysicalRoot || !validID(saved.LedgerID) || !c.ValidDigest(saved.DatabaseIdentity) {
			return nil, c.Fail(c.StaleAuthority, "physical quota anchor binding is invalid")
		}
		if expectedLedgerID != "" && saved.LedgerID != expectedLedgerID {
			return nil, c.Fail(c.StaleAuthority, "physical quota ledger differs from owner-store pin")
		}
	} else if expectedLedgerID != "" {
		return nil, c.Fail(c.StaleAuthority, "owner-pinned physical quota anchor is missing")
	}
	dbExists := true
	if _, statErr := root.Lstat("physical-quota.sqlite"); errors.Is(statErr, os.ErrNotExist) {
		dbExists = false
	} else if statErr != nil {
		return nil, statErr
	}
	if anchorExists && !dbExists {
		return nil, c.Fail(c.StaleAuthority, "pinned physical quota database is missing")
	}
	if !anchorExists {
		if dbExists {
			return nil, c.Fail(c.StoreIntegrityError, "unanchored physical quota database exists")
		}
		if err = createDatabase(root, authority, limits); err != nil {
			return nil, err
		}
	}
	db, err := openDatabase(root)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	cleanupDB := true
	defer func() {
		if cleanupDB {
			_ = db.Close()
		}
	}()
	if err = configureDatabase(db); err != nil {
		return nil, err
	}
	stored, err := readMeta(db)
	if err != nil {
		return nil, err
	}
	if stored.AuthorityID != authority.ID || stored.PhysicalRoot != authority.PhysicalRoot || stored.Limits != limits {
		return nil, c.Fail(c.StaleAuthority, "physical quota ledger genesis differs from current authority")
	}
	databaseIdentity, err := fileIdentity(root, "physical-quota.sqlite")
	if err != nil {
		return nil, err
	}
	if anchorExists {
		var saved anchor
		if strictJSON(anchorRaw, &saved) != nil || saved.LedgerID != stored.LedgerID || saved.DatabaseIdentity != databaseIdentity {
			return nil, c.Fail(c.StaleAuthority, "physical quota database identity differs from its anchor")
		}
	} else {
		var count int
		if err = db.QueryRow("SELECT COUNT(*) FROM quota_allocations").Scan(&count); err != nil || count != 0 {
			return nil, c.Fail(c.StoreIntegrityError, "unanchored physical quota ledger is not empty")
		}
		saved := anchor{SchemaVersion: ledgerSchema, AuthorityID: authority.ID, PhysicalRoot: authority.PhysicalRoot, LedgerID: stored.LedgerID, DatabaseIdentity: databaseIdentity}
		raw, encodeErr := json.Marshal(saved)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if err = writeExclusive(root, "physical-quota.anchor", raw); err != nil {
			return nil, err
		}
	}
	if err = checkDatabaseFiles(root); err != nil {
		return nil, err
	}
	if err = ensureFreeIntentSchema(db); err != nil {
		return nil, err
	}
	if err = ensureReservationIntentSchema(db); err != nil {
		return nil, err
	}
	if err = ensureMutableReplacementSchema(db); err != nil {
		return nil, err
	}
	if err = ensureControlReserveSchema(db); err != nil {
		return nil, err
	}
	if err = ensureHandoffSchema(db); err != nil {
		return nil, err
	}
	if err = ensureQuotaPartitionSchema(db); err != nil {
		return nil, err
	}
	if err = validateQuotaPartitionRecords(db, stored.LedgerID); err != nil {
		return nil, err
	}
	if err = validateHandoffRecordsOnOpen(db, root, authority, stored.LedgerID); err != nil {
		return nil, err
	}
	ledger := &Ledger{root: root, db: db, lockName: "physical-quota.lock", Authority: authority, Limits: limits, LedgerID: stored.LedgerID, DatabaseIdentity: databaseIdentity}
	if _, err = ledger.reconcileFreePendingLocked(context.Background()); err != nil {
		return nil, err
	}
	if err = ledger.verifyPinnedMetadata(); err != nil {
		return nil, err
	}
	cleanupDB = false
	closeOnError = false
	return ledger, nil
}

func validateAuthority(authority Authority) error {
	if len(authority.ID) != 32 || !c.ValidDigest(c.HashBytes([]byte(authority.ID))) || !c.ValidDigest(authority.PhysicalRoot) || !filepath.IsAbs(authority.Directory) || len(authority.Directory) > 2048 {
		return c.Fail(c.InvalidArgument, "invalid physical quota authority descriptor")
	}
	for _, r := range authority.ID {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return c.Fail(c.InvalidArgument, "invalid physical quota authority identity")
		}
	}
	return nil
}

func verifyAuthorityGenesis(root *os.Root, expected Authority) error {
	raw, err := fileguard.ReadRegular(root, "authority.json", 4096)
	if err != nil {
		return err
	}
	var genesis authorityGenesis
	if strictJSON(raw, &genesis) != nil || genesis.SchemaVersion != 1 || genesis.ID != expected.ID || genesis.Directory != expected.Directory || genesis.PhysicalRoot != expected.PhysicalRoot {
		return c.Fail(c.StaleAuthority, "physical quota authority genesis mismatch")
	}
	return nil
}

func strictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func acquireLock(root *os.Root) (*os.File, error) {
	for i := 0; i < 500; i++ {
		lock, err := fileguard.Lock(root, "physical-quota.lock")
		if err == nil {
			return lock, nil
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StoreOwned {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, c.Fail(c.StoreOwned, "physical quota broker remained busy")
}

func (l *Ledger) lock() (*os.File, error) {
	return fileguard.LockExisting(l.root, l.lockName)
}

func (l *Ledger) acquire(ctx context.Context) (*os.File, error) {
	for i := 0; i < 500; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := l.lock()
		if err == nil {
			return lock, nil
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StoreOwned {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, c.Fail(c.StoreOwned, "physical quota broker remained busy")
}

func admitMetadata(root *os.Root, bytes int64) error {
	if bytes < 0 || bytes > 64<<20 {
		return c.Fail(c.InvalidArgument, "physical quota metadata admission exceeds bounded profile")
	}
	reserve, err := diskguard.OpenRoot(root)
	if err != nil {
		return err
	}
	return errors.Join(reserve.Admit(bytes, true), reserve.Close())
}

// admitReceiptMutation covers the old/new SQLite row plus bounded WAL/page
// metadata before replacing a potentially large canonical inventory receipt.
func admitReceiptMutation(root *os.Root, raw []byte) error {
	if len(raw) > maxInventoryBytes {
		return c.Fail(c.BudgetLimitReached, "physical quota receipt exceeds metadata write profile")
	}
	bytes := int64(len(raw))*2 + metadataAdmission
	return admitMetadata(root, bytes)
}

func admitCheckpointMetadata(root *os.Root) error {
	info, err := root.Lstat("physical-quota.sqlite-wal")
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() == 0) {
		return admitMetadata(root, metadataAdmission)
	}
	if err != nil {
		return err
	}
	if info.Size() < 0 || info.Size() > maxLedgerWALBytes {
		return c.Fail(c.BudgetLimitReached, "physical quota WAL exceeds checkpoint admission profile")
	}
	return admitMetadata(root, info.Size()+metadataAdmission)
}

func openDatabase(root *os.Root) (*sql.DB, error) {
	path := filepath.Join(root.Name(), "physical-quota.sqlite")
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	return sql.Open("sqlite", u.String())
}

func configureDatabase(db *sql.DB) error {
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA wal_autocheckpoint=1", "PRAGMA journal_size_limit=16777216", "PRAGMA temp_store=MEMORY", "PRAGMA mmap_size=0"} {
		if _, err := db.Exec(pragma); err != nil {
			return err
		}
	}
	var mode, synchronous string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		return c.Fail(c.StoreIntegrityError, "physical quota SQLite WAL profile unavailable")
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != "2" {
		return c.Fail(c.StoreIntegrityError, "physical quota SQLite FULL durability unavailable")
	}
	var maxPages, pageSize, pageCount, walLimit int64
	if err := db.QueryRow("PRAGMA max_page_count=32768").Scan(&maxPages); err != nil || maxPages < 1 || maxPages > maxLedgerPages {
		return c.Fail(c.UnsupportedCapability, "bounded physical quota SQLite database unavailable")
	}
	if err := db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil || pageSize != 4096 {
		return c.Fail(c.StoreIntegrityError, "invalid physical quota SQLite page profile")
	}
	if err := db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil || pageCount < 1 || pageCount > maxPages {
		return c.Fail(c.BudgetLimitReached, "physical quota ledger exceeded its bounded database profile")
	}
	if err := db.QueryRow("PRAGMA journal_size_limit").Scan(&walLimit); err != nil || walLimit < 0 || walLimit > maxLedgerWALBytes {
		return c.Fail(c.UnsupportedCapability, "bounded physical quota WAL unavailable")
	}
	var tempStore int
	if err := db.QueryRow("PRAGMA temp_store").Scan(&tempStore); err != nil || tempStore != 2 {
		return c.Fail(c.UnsupportedCapability, "memory-bounded physical quota SQLite temp profile unavailable")
	}
	return nil
}

type databaseMeta struct {
	AuthorityID  string
	PhysicalRoot string
	LedgerID     string
	Limits       Limits
}

func createDatabase(root *os.Root, authority Authority, limits Limits) error {
	if err := admitMetadata(root, 2<<20); err != nil {
		return err
	}
	db, err := openDatabase(root)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = configureDatabase(db); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	ledgerID := hex.EncodeToString(nonce[:])
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		"CREATE TABLE quota_meta (singleton INTEGER PRIMARY KEY CHECK(singleton=1),schema_version INTEGER NOT NULL,authority_id TEXT NOT NULL,physical_root TEXT NOT NULL,ledger_id TEXT NOT NULL,limit_bytes INTEGER NOT NULL,limit_entries INTEGER NOT NULL,control_bytes INTEGER NOT NULL,control_entries INTEGER NOT NULL,low_water_bytes INTEGER NOT NULL)",
		"CREATE TABLE quota_allocations (id TEXT PRIMARY KEY,writer TEXT NOT NULL,family TEXT NOT NULL,request_digest TEXT NOT NULL,target_root TEXT NOT NULL,volume_identity TEXT NOT NULL,parent_path TEXT NOT NULL,parent_identity TEXT NOT NULL,target_identity TEXT NOT NULL,target_existed INTEGER NOT NULL,class TEXT NOT NULL,status TEXT NOT NULL,upper_bytes INTEGER NOT NULL,upper_entries INTEGER NOT NULL,retained_bytes INTEGER NOT NULL,retained_entries INTEGER NOT NULL,receipt BLOB,free_before INTEGER NOT NULL DEFAULT 0,free_after INTEGER NOT NULL DEFAULT 0)",
		"CREATE INDEX quota_allocations_status ON quota_allocations(status)",
		freeIntentsDDL,
		quotaReservationIntentsDDL,
		mutableReplacementDDL,
		controlReserveDDL,
		"PRAGMA user_version=1",
	}
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	_, err = tx.Exec("INSERT INTO quota_meta VALUES(1,?,?,?,?,?,?,?,?,?)", ledgerSchema, authority.ID, authority.PhysicalRoot, ledgerID, limits.Bytes, limits.Entries, limits.ControlBytes, limits.ControlEntries, limits.LowWaterBytes)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return checkDatabaseFiles(root)
}

const freeIntentsDDL = "CREATE TABLE quota_free_intents (allocation_id TEXT PRIMARY KEY REFERENCES quota_allocations(id),path TEXT NOT NULL,file_digest TEXT NOT NULL,file_identity TEXT NOT NULL,logical_bytes INTEGER NOT NULL,allocated_bytes INTEGER NOT NULL,charged_bytes INTEGER NOT NULL,free_before INTEGER NOT NULL,volume_identity TEXT NOT NULL,root_identity TEXT NOT NULL)"

// ensureFreeIntentSchema upgrades pre-ledger-intent databases in place. Old
// FREE_PENDING rows without a member identity remain charged as legacy unknown;
// the missing identity is never guessed from the current namespace.
func ensureFreeIntentSchema(db *sql.DB) error {
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='quota_free_intents'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		if _, err := db.Exec(freeIntentsDDL); err != nil {
			return err
		}
	} else if exists != 1 {
		return c.Fail(c.StoreIntegrityError, "physical quota free-intent table is ambiguous")
	}
	rows, err := db.Query("PRAGMA table_info(quota_free_intents)")
	if err != nil {
		return err
	}
	want := []string{"allocation_id", "path", "file_digest", "file_identity", "logical_bytes", "allocated_bytes", "charged_bytes", "free_before", "volume_identity", "root_identity"}
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
		return c.Fail(c.StoreIntegrityError, "physical quota free-intent schema is invalid")
	}
	for i := range want {
		if columns[i] != want[i] {
			return c.Fail(c.StoreIntegrityError, "physical quota free-intent schema is invalid")
		}
	}
	var triggers int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='quota_free_intents'").Scan(&triggers); err != nil {
		return err
	}
	if triggers != 0 {
		return c.Fail(c.StoreIntegrityError, "physical quota free-intent triggers are not permitted")
	}
	var dangling int
	if err = db.QueryRow("SELECT COUNT(*) FROM quota_free_intents AS intent LEFT JOIN quota_allocations AS allocation ON allocation.id=intent.allocation_id WHERE allocation.id IS NULL OR allocation.status<>'FREE_PENDING'").Scan(&dangling); err != nil {
		return err
	}
	if dangling != 0 {
		return c.Fail(c.StoreIntegrityError, "physical quota free intent is detached from a pending allocation")
	}
	return nil
}

func readMeta(db *sql.DB) (databaseMeta, error) {
	var meta databaseMeta
	var schema int
	var bytes, entries, controlBytes, controlEntries, lowWater int64
	err := db.QueryRow("SELECT schema_version,authority_id,physical_root,ledger_id,limit_bytes,limit_entries,control_bytes,control_entries,low_water_bytes FROM quota_meta WHERE singleton=1").Scan(&schema, &meta.AuthorityID, &meta.PhysicalRoot, &meta.LedgerID, &bytes, &entries, &controlBytes, &controlEntries, &lowWater)
	if err != nil {
		return meta, err
	}
	if schema != ledgerSchema || !validID(meta.LedgerID) || bytes < 0 || entries < 0 || controlBytes < 0 || controlEntries < 0 || lowWater < 0 {
		return meta, c.Fail(c.StoreIntegrityError, "physical quota ledger metadata is invalid")
	}
	meta.Limits = Limits{Bytes: uint64(bytes), Entries: uint64(entries), ControlBytes: uint64(controlBytes), ControlEntries: uint64(controlEntries), LowWaterBytes: uint64(lowWater)}
	if err = meta.Limits.validate(); err != nil {
		return meta, c.Fail(c.StoreIntegrityError, "physical quota ledger limits are invalid")
	}
	return meta, nil
}

func checkDatabaseFiles(root *os.Root) error {
	for _, name := range []string{"physical-quota.sqlite", "physical-quota.sqlite-wal", "physical-quota.sqlite-shm", "physical-quota.lock"} {
		if err := fileguard.RegularPath(root, name, true); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			if name == "physical-quota.sqlite" || name == "physical-quota.lock" {
				return c.Fail(c.StaleAuthority, "pinned physical quota metadata file is missing")
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Size() < 0 {
			return c.Fail(c.StoreIntegrityError, "physical quota metadata size is invalid")
		}
		limit := maxLedgerSHMBytes
		switch name {
		case "physical-quota.sqlite":
			limit = maxLedgerDBBytes
		case "physical-quota.sqlite-wal":
			limit = maxLedgerWALBytes
		case "physical-quota.lock":
			limit = 4096
		}
		if info.Size() > limit {
			return c.Fail(c.BudgetLimitReached, "physical quota metadata exceeded its bounded file profile")
		}
	}
	if _, err := root.Lstat("physical-quota.sqlite-journal"); err == nil {
		return c.Fail(c.StoreIntegrityError, "unexpected physical quota rollback journal")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l *Ledger) verifyPinnedMetadata() error {
	if l == nil || l.root == nil || l.DatabaseIdentity == "" {
		return c.Fail(c.StaleAuthority, "physical quota ledger binding is incomplete")
	}
	rootIdentity, err := fileguard.DirectoryIdentity(l.root)
	if err != nil {
		return err
	}
	if rootIdentity != l.Authority.PhysicalRoot {
		return c.Fail(c.StaleAuthority, "physical quota authority root identity changed")
	}
	if err = verifyAuthorityGenesis(l.root, l.Authority); err != nil {
		return err
	}
	raw, err := fileguard.ReadRegular(l.root, "physical-quota.anchor", 4096)
	if err != nil {
		return err
	}
	var saved anchor
	if strictJSON(raw, &saved) != nil {
		return c.Fail(c.StaleAuthority, "physical quota anchor binding is invalid")
	}
	canonical, err := json.Marshal(saved)
	if err != nil || !bytes.Equal(canonical, raw) || saved.SchemaVersion != ledgerSchema || saved.AuthorityID != l.Authority.ID || saved.PhysicalRoot != l.Authority.PhysicalRoot || saved.LedgerID != l.LedgerID || saved.DatabaseIdentity != l.DatabaseIdentity {
		return c.Fail(c.StaleAuthority, "physical quota anchor binding changed")
	}
	databaseIdentity, err := fileIdentity(l.root, "physical-quota.sqlite")
	if err != nil {
		return err
	}
	if databaseIdentity != l.DatabaseIdentity {
		return c.Fail(c.StaleAuthority, "physical quota database inode changed after binding")
	}
	return checkDatabaseFiles(l.root)
}

func writeExclusive(root *os.Root, name string, raw []byte) error {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return fileguard.SyncDirectory(root, ".")
}

func fileIdentity(root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err = fileguard.SingleLink(f); err != nil {
		return "", err
	}
	return fileguard.FileIdentity(f)
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (l *Ledger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	var err error
	if l.db != nil {
		if lock, lockErr := l.lock(); lockErr == nil {
			if verifyErr := l.verifyPinnedMetadata(); verifyErr != nil {
				err = errors.Join(err, verifyErr)
			} else if admissionErr := admitCheckpointMetadata(l.root); admissionErr != nil {
				err = errors.Join(err, admissionErr)
			} else {
				_, checkpointErr := l.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
				err = errors.Join(err, checkpointErr, l.verifyPinnedMetadata())
			}
			err = errors.Join(err, lock.Close())
		} else {
			err = errors.Join(err, lockErr)
		}
		err = errors.Join(err, l.db.Close())
	}
	if l.root != nil {
		err = errors.Join(err, l.root.Close())
	}
	return err
}

func (l *Ledger) withLock(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := l.acquire(ctx)
	if err != nil {
		return err
	}
	if err = admitMetadata(l.root, metadataAdmission); err != nil {
		return errors.Join(err, lock.Close())
	}
	if err = l.verifyPinnedMetadata(); err != nil {
		return errors.Join(err, lock.Close())
	}
	operationErr := fn()
	metadataErr := l.verifyPinnedMetadata()
	return errors.Join(operationErr, metadataErr, lock.Close())
}

func (l *Ledger) Reserve(ctx context.Context, request Request) (Allocation, error) {
	match, err := l.reserveOrMatch(ctx, request, false)
	return match.Allocation, err
}

// ReserveOrMatch atomically reuses a matching RESERVED/WRITING allocation,
// reports a same-class WRITING ancestor, or creates a new reservation. Exact
// matches compare the complete request envelope so a retry with changed bytes,
// family, class, or upper bound cannot inherit an earlier charge.
func (l *Ledger) ReserveOrMatch(ctx context.Context, request Request) (ReservationMatch, error) {
	return l.reserveOrMatch(ctx, request, true)
}

func (l *Ledger) reserveOrMatch(ctx context.Context, request Request, matchExisting bool) (ReservationMatch, error) {
	match, _, err := l.reserveOrMatchIntent(ctx, request, matchExisting, nil)
	return match, err
}

func (l *Ledger) reserveOrMatchIntent(ctx context.Context, request Request, matchExisting bool, intent *reservationIntentInput) (ReservationMatch, []byte, error) {
	if request.ID == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return ReservationMatch{}, nil, err
		}
		request.ID = hex.EncodeToString(nonce[:])
	}
	if !validID(request.ID) || len(request.Writer) == 0 || len(request.Writer) > 64 || len(request.Family) > 128 || len(request.AncestorFamily) > 128 || !c.ValidDigest(request.RequestDigest) || request.UpperBytes > 1<<48 || request.UpperEntries == 0 || request.UpperEntries > 1<<40 || (request.Class != Payload && request.Class != Control) {
		return ReservationMatch{}, nil, c.Fail(c.InvalidArgument, "invalid physical quota allocation request")
	}
	if intent != nil && (len(intent.Binding) == 0 || len(intent.Binding) > maxQuotaIntentBindingBytes || len(intent.Payload) == 0 || len(intent.Payload) > maxQuotaIntentPayloadBytes || !c.ValidDigest(intent.BindingDigest) || intent.BindingDigest != c.HashBytes(intent.Binding) || !c.ValidDigest(intent.PayloadDigest) || intent.PayloadDigest != c.HashBytes(intent.Payload) || request.RequestDigest != intent.PayloadDigest) {
		return ReservationMatch{}, nil, c.Fail(c.InvalidArgument, "invalid bounded physical quota reservation intent")
	}
	target, volume, parentPath, parentID, targetID, targetExisted, available, err := targetVolume(request.TargetRoot)
	if err != nil {
		return ReservationMatch{}, nil, err
	}
	var match ReservationMatch
	var matchedPayload []byte
	err = l.withLock(ctx, func() error {
		if intent != nil {
			if err := admitMetadata(l.root, quotaIntentAdmissionBytes(len(intent.Binding), len(intent.Payload))); err != nil {
				return err
			}
		}
		if _, err := l.reconcileFreePendingLocked(ctx); err != nil {
			return err
		}
		currentTarget, currentVolume, currentParent, currentParentID, currentTargetID, currentExisted, currentAvailable, err := targetVolume(request.TargetRoot)
		if err != nil {
			return err
		}
		if currentTarget != target || currentVolume != volume || currentParent != parentPath || currentParentID != parentID || currentTargetID != targetID || currentExisted != targetExisted {
			return c.Fail(c.StaleAuthority, "physical quota target changed before reservation")
		}
		available = currentAvailable
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if matchExisting {
			existing, payload, found, findErr := matchReservationTx(ctx, tx, l, request, target, intent)
			if findErr != nil {
				return findErr
			}
			if found {
				match = existing
				matchedPayload = payload
				return nil
			}
		}
		if err = checkPartitionAdmissionTx(ctx, tx, request, target, targetExisted); err != nil {
			return err
		}
		capacity, err := capacityTx(tx, l.Limits)
		if err != nil {
			return err
		}
		bytesLimit, entriesLimit := l.Limits.Bytes-l.Limits.ControlBytes, l.Limits.Entries-l.Limits.ControlEntries
		usedBytes, usedEntries := capacity.RetainedBytes, capacity.RetainedEntries
		if request.Class == Payload {
			usedBytes, err = add(capacity.PayloadBytes, capacity.PayloadOutstandingBytes)
			if err == nil {
				usedEntries, err = add(capacity.PayloadEntries, capacity.PayloadOutstandingEntries)
			}
			bytesLimit, entriesLimit = l.Limits.Bytes-l.Limits.ControlBytes, l.Limits.Entries-l.Limits.ControlEntries
		} else {
			usedBytes, err = add(capacity.ControlUsedBytes, capacity.ControlOutstandingBytes)
			if err == nil {
				usedEntries, err = add(capacity.ControlUsedEntries, capacity.ControlOutstandingEntries)
			}
			bytesLimit, entriesLimit = l.Limits.ControlBytes, l.Limits.ControlEntries
		}
		if err != nil {
			return err
		}
		if usedBytes > bytesLimit || request.UpperBytes > bytesLimit-usedBytes || usedEntries > entriesLimit || request.UpperEntries > entriesLimit-usedEntries {
			return c.Fail(c.BudgetLimitReached, "shared physical quota exhausted; outstanding and retained allocations remain charged")
		}
		allBytes, err := add(capacity.RetainedBytes, capacity.OutstandingBytes)
		if err != nil {
			return err
		}
		allEntries, err := add(capacity.RetainedEntries, capacity.OutstandingItems)
		if err != nil {
			return err
		}
		if allBytes > l.Limits.Bytes || request.UpperBytes > l.Limits.Bytes-allBytes || allEntries > l.Limits.Entries || request.UpperEntries > l.Limits.Entries-allEntries {
			return c.Fail(c.BudgetLimitReached, "shared physical quota exhausted; control reserve is protected")
		}
		outstanding, err := outstandingForVolumeTx(tx, volume)
		if err != nil {
			return err
		}
		minimum := l.Limits.LowWaterBytes
		if request.Class == Control {
			minimum = 0
		}
		if available < minimum || outstanding > available-minimum || request.UpperBytes > available-minimum-outstanding {
			return c.Fail(c.BudgetLimitReached, "target volume lacks measured free space for this reservation")
		}
		if capacity.Allocations >= maxLedgerRecords {
			return c.Fail(c.BudgetLimitReached, "shared physical quota reservation catalog is full")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO quota_allocations(id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", request.ID, request.Writer, request.Family, request.RequestDigest, target, volume, parentPath, parentID, targetID, targetExisted, string(request.Class), "RESERVED", request.UpperBytes, request.UpperEntries, 0, 0)
		if err != nil {
			return err
		}
		if intent != nil {
			if err = insertReservationIntentTx(ctx, tx, request.ID, *intent); err != nil {
				return err
			}
			matchedPayload = bytes.Clone(intent.Payload)
		}
		match = ReservationMatch{Allocation: Allocation{ledger: l, ID: request.ID, targetRoot: target}, Scope: ExactReservationScope, Status: "RESERVED", Created: true, UpperBytes: request.UpperBytes, UpperEntries: request.UpperEntries, RequestDigest: request.RequestDigest}
		return tx.Commit()
	})
	if err != nil {
		return ReservationMatch{}, nil, err
	}
	return match, matchedPayload, nil
}

type reservationCandidate struct {
	id, writer, family, digest, target, volume, parentPath, parentIdentity, targetIdentity, class, status string
	upperBytes, upperEntries                                                                              int64
	targetExisted                                                                                         int
}

func matchReservationTx(ctx context.Context, tx *sql.Tx, ledger *Ledger, request Request, target string, intent *reservationIntentInput) (ReservationMatch, []byte, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries
		FROM quota_allocations WHERE status IN ('RESERVED','WRITING','RETAINED','FREE_PENDING','RELEASED')
		AND id NOT IN (
			SELECT base_id FROM quota_mutable_replacements UNION SELECT standby_id FROM quota_mutable_replacements
			UNION SELECT base_id FROM quota_control_reserves UNION SELECT standby_id FROM quota_control_reserves
			UNION SELECT member_id FROM quota_partition_members
		)`)
	if err != nil {
		return ReservationMatch{}, nil, false, err
	}
	exact := []reservationCandidate{}
	ancestors := []reservationCandidate{}
	for rows.Next() {
		var candidate reservationCandidate
		if err = rows.Scan(&candidate.id, &candidate.writer, &candidate.family, &candidate.digest, &candidate.target, &candidate.volume, &candidate.parentPath, &candidate.parentIdentity, &candidate.targetIdentity, &candidate.targetExisted, &candidate.class, &candidate.status, &candidate.upperBytes, &candidate.upperEntries); err != nil {
			rows.Close()
			return ReservationMatch{}, nil, false, err
		}
		allocation := Allocation{ledger: ledger, targetRoot: candidate.target}
		if !allocation.Covers(target) {
			continue
		}
		if !validID(candidate.id) || !filepath.IsAbs(candidate.target) || !filepath.IsAbs(candidate.parentPath) || !c.ValidDigest(candidate.volume) || !c.ValidDigest(candidate.parentIdentity) || !c.ValidDigest(candidate.targetIdentity) || candidate.targetExisted < 0 || candidate.targetExisted > 1 || (candidate.class != string(Payload) && candidate.class != string(Control)) || candidate.upperBytes < 0 || candidate.upperEntries < 0 {
			rows.Close()
			return ReservationMatch{}, nil, false, c.Fail(c.StoreIntegrityError, "physical quota reservation binding is corrupt")
		}
		if filepath.Clean(candidate.target) == filepath.Clean(target) {
			exact = append(exact, candidate)
		} else {
			ancestors = append(ancestors, candidate)
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return ReservationMatch{}, nil, false, err
	}
	if len(exact)+len(ancestors) > 1 {
		return ReservationMatch{}, nil, false, c.Fail(c.StoreIntegrityError, "nested physical quota reservations overlap")
	}
	if len(exact) == 1 {
		candidate := exact[0]
		if candidate.writer != request.Writer || candidate.family != request.Family || (intent == nil && candidate.digest != request.RequestDigest) || candidate.class != string(request.Class) || uint64(candidate.upperBytes) != request.UpperBytes || uint64(candidate.upperEntries) != request.UpperEntries {
			return ReservationMatch{}, nil, false, c.Fail(c.StaleAuthority, "physical quota retry envelope differs from its existing allocation")
		}
		if err = verifyMatchedReservation(candidate, true); err != nil {
			return ReservationMatch{}, nil, false, err
		}
		var payload []byte
		if intent != nil {
			payload, err = readMatchedReservationIntentTx(ctx, tx, candidate, *intent)
			if err != nil {
				return ReservationMatch{}, nil, false, err
			}
		}
		return ReservationMatch{Allocation: Allocation{ledger: ledger, ID: candidate.id, targetRoot: candidate.target}, Scope: ExactReservationScope, Status: candidate.status, UpperBytes: uint64(candidate.upperBytes), UpperEntries: uint64(candidate.upperEntries), RequestDigest: candidate.digest}, payload, true, nil
	}
	if len(ancestors) == 1 {
		candidate := ancestors[0]
		if candidate.status != "WRITING" {
			return ReservationMatch{}, nil, false, c.Fail(c.StaleAuthority, "an enclosing physical quota allocation is not dispatched")
		}
		if request.AncestorFamily == "" || candidate.family != request.AncestorFamily {
			return ReservationMatch{}, nil, false, c.Fail(c.StaleAuthority, "nested writer has no trusted enclosing family relationship")
		}
		if candidate.class != string(request.Class) {
			return ReservationMatch{}, nil, false, c.Fail(c.StaleAuthority, "nested writer class differs from its enclosing physical reservation")
		}
		if uint64(candidate.upperBytes) < request.UpperBytes || uint64(candidate.upperEntries) < request.UpperEntries {
			return ReservationMatch{}, nil, false, c.Fail(c.StaleAuthority, "nested writer envelope exceeds its enclosing physical reservation")
		}
		if err = verifyMatchedReservation(candidate, false); err != nil {
			return ReservationMatch{}, nil, false, err
		}
		return ReservationMatch{Allocation: Allocation{ledger: ledger, ID: candidate.id, targetRoot: candidate.target}, Scope: AncestorReservationScope, Status: candidate.status, UpperBytes: uint64(candidate.upperBytes), UpperEntries: uint64(candidate.upperEntries), RequestDigest: candidate.digest}, nil, true, nil
	}
	return ReservationMatch{}, nil, false, nil
}

func verifyMatchedReservation(candidate reservationCandidate, exact bool) error {
	if candidate.status == "RELEASED" {
		return c.Fail(c.StaleAuthority, "physical quota output path was already released")
	}
	if candidate.status == "RETAINED" || candidate.status == "FREE_PENDING" {
		if exact {
			return nil
		}
		return c.Fail(c.StaleAuthority, "a retained physical quota root cannot admit nested writes")
	}
	if candidate.status != "RESERVED" && candidate.status != "WRITING" {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation has an unknown lifecycle")
	}
	if candidate.status == "RESERVED" || candidate.targetExisted == 1 {
		_, err := verifyReservedTarget(candidate.target, candidate.volume, candidate.parentPath, candidate.parentIdentity, candidate.targetIdentity, candidate.targetExisted)
		return err
	}
	parent, err := os.OpenRoot(candidate.parentPath)
	if err != nil {
		return err
	}
	identity, identityErr := fileguard.DirectoryIdentity(parent)
	volume, volumeErr := fileguard.VolumeIdentity(parent)
	closeErr := parent.Close()
	if err = errors.Join(identityErr, volumeErr, closeErr); err != nil {
		return err
	}
	if identity != candidate.parentIdentity || volume != candidate.volume {
		return c.Fail(c.StaleAuthority, "active physical quota reservation parent changed")
	}
	return nil
}

func (a Allocation) Begin(ctx context.Context) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota reservation required")
	}
	return a.ledger.withLock(ctx, func() error {
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var status, target, volume, parentPath, parentIdentity, targetIdentity, class string
		var targetExisted int
		var upperBytes int64
		if err = tx.QueryRowContext(ctx, "SELECT status,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,upper_bytes FROM quota_allocations WHERE id=?", a.ID).Scan(&status, &target, &volume, &parentPath, &parentIdentity, &targetIdentity, &targetExisted, &class, &upperBytes); err != nil {
			return err
		}
		if status != "RESERVED" || filepath.Clean(target) != filepath.Clean(a.targetRoot) || (class != string(Payload) && class != string(Control)) || upperBytes < 0 {
			return c.Fail(c.StaleAuthority, "physical quota reservation is missing or already dispatched")
		}
		available, err := verifyReservedTarget(target, volume, parentPath, parentIdentity, targetIdentity, targetExisted)
		if err != nil {
			return err
		}
		outstanding, err := outstandingForVolumeTx(tx, volume)
		if err != nil {
			return err
		}
		minimum := a.ledger.Limits.LowWaterBytes
		if class == string(Control) {
			minimum = 0
		}
		if available < minimum || outstanding > available-minimum {
			return c.Fail(c.BudgetLimitReached, "target volume no longer has measured free space for all outstanding reservations")
		}
		result, err := tx.ExecContext(ctx, "UPDATE quota_allocations SET status='WRITING' WHERE id=? AND status='RESERVED'", a.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return c.Fail(c.StaleAuthority, "physical quota reservation is missing or already dispatched")
		}
		return tx.Commit()
	})
}

func verifyReservedTarget(target, volume, parentPath, parentIdentity, targetIdentity string, targetExisted int) (uint64, error) {
	if !filepath.IsAbs(target) || !filepath.IsAbs(parentPath) || !c.ValidDigest(volume) || !c.ValidDigest(parentIdentity) || !c.ValidDigest(targetIdentity) || (targetExisted != 0 && targetExisted != 1) {
		return 0, c.Fail(c.StoreIntegrityError, "physical quota reservation target binding is invalid")
	}
	resolved, err := fileguard.ResolveProspective(target)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(target) {
		return 0, c.Fail(c.StaleAuthority, "physical quota target path changed after reservation")
	}
	if targetExisted == 1 {
		root, openErr := os.OpenRoot(target)
		if openErr != nil {
			return 0, c.Fail(c.StaleAuthority, "reserved physical target root disappeared")
		}
		identity, identityErr := fileguard.DirectoryIdentity(root)
		actualVolume, volumeErr := fileguard.VolumeIdentity(root)
		available, availableErr := fileguard.AvailableBytes(root)
		closeErr := root.Close()
		if err = errors.Join(identityErr, volumeErr, availableErr, closeErr); err != nil {
			return 0, err
		}
		if identity != targetIdentity || identity != parentIdentity || actualVolume != volume {
			return 0, c.Fail(c.StaleAuthority, "reserved physical target root identity changed")
		}
		return available, nil
	}
	if _, err = os.Lstat(target); err == nil {
		return 0, c.Fail(c.StaleAuthority, "reserved physical target was created before writer dispatch")
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	resolvedParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil || filepath.Clean(resolvedParent) != filepath.Clean(parentPath) {
		return 0, c.Fail(c.StaleAuthority, "reserved physical target parent path changed")
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return 0, err
	}
	identity, identityErr := fileguard.DirectoryIdentity(parent)
	actualVolume, volumeErr := fileguard.VolumeIdentity(parent)
	available, availableErr := fileguard.AvailableBytes(parent)
	closeErr := parent.Close()
	if err = errors.Join(identityErr, volumeErr, availableErr, closeErr); err != nil {
		return 0, err
	}
	if identity != parentIdentity || identity != targetIdentity || actualVolume != volume {
		return 0, c.Fail(c.StaleAuthority, "reserved physical target parent identity changed")
	}
	return available, nil
}

func (a Allocation) Commit(ctx context.Context, inventory Inventory) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota reservation required")
	}
	measured, err := validateInventory(inventory)
	if err != nil {
		return err
	}
	return a.ledger.withLock(ctx, func() error {
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var pendingHandoff int
		var partitionMember int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quota_partitions WHERE allocation_id=?) OR
			EXISTS(SELECT 1 FROM quota_partition_members WHERE member_id=?)`, a.ID, a.ID).Scan(&partitionMember); err != nil {
			return err
		}
		if partitionMember != 0 {
			return c.Fail(c.StaleAuthority, "partition member receipts must be settled by their atomic partition transaction")
		}
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_handoffs WHERE allocation_id=? AND state IN ('PREPARED','STAGE_BOUND')", a.ID).Scan(&pendingHandoff); err != nil {
			return err
		}
		if pendingHandoff != 0 {
			return c.Fail(c.StaleAuthority, "pending physical quota handoff must settle at its final path")
		}
		var status, target, volume, parentPath, targetIdentity string
		var targetExisted int
		var upperBytes, upperEntries int64
		if err = tx.QueryRowContext(ctx, "SELECT status,target_root,volume_identity,parent_path,target_identity,target_existed,upper_bytes,upper_entries FROM quota_allocations WHERE id=?", a.ID).Scan(&status, &target, &volume, &parentPath, &targetIdentity, &targetExisted, &upperBytes, &upperEntries); err != nil {
			return err
		}
		if status != "WRITING" || filepath.Clean(target) != filepath.Clean(measured.RootPath) || volume != measured.VolumeIdentity {
			return c.Fail(c.StaleAuthority, "physical quota receipt is not bound to the reserved root")
		}
		if targetExisted == 1 {
			if measured.RootIdentity != targetIdentity {
				return c.Fail(c.StaleAuthority, "physical output root inode changed after reservation")
			}
		} else {
			parent, openErr := os.OpenRoot(parentPath)
			if openErr != nil {
				return openErr
			}
			parentIdentity, identityErr := fileguard.DirectoryIdentity(parent)
			closeErr := parent.Close()
			if identityErr != nil || closeErr != nil {
				return errors.Join(identityErr, closeErr)
			}
			if parentIdentity != targetIdentity {
				return c.Fail(c.StaleAuthority, "physical output parent changed after reservation")
			}
		}
		raw, err := json.Marshal(measured)
		if err != nil {
			return err
		}
		if err = admitReceiptMutation(a.ledger.root, raw); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE quota_allocations SET status='RETAINED',retained_bytes=?,retained_entries=?,receipt=? WHERE id=? AND status='WRITING'", measured.ChargedBytes, measured.Entries, raw, a.ID)
		if err != nil {
			return err
		}
		if err = eraseReservationIntentPayloadTx(ctx, tx, a.ID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if measured.ChargedBytes > uint64(upperBytes) || measured.Entries > uint64(upperEntries) {
			return c.Fail(c.BudgetLimitReached, "writer exceeded its physical quota reservation; measured overrun remains charged")
		}
		return nil
	})
}

// ValidateRetained checks that inventory exactly matches the immutable receipt
// already stored for this allocation. It does not change the ledger; callers
// use it to make a completed retry idempotent without treating a receipt as a
// fresh reservation or committing it twice.
func (a Allocation) ValidateRetained(ctx context.Context, inventory Inventory) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	measured, err := validateInventory(inventory)
	if err != nil {
		return err
	}
	receipt, err := json.Marshal(measured)
	if err != nil {
		return err
	}
	return a.ledger.withReadLock(ctx, func() error {
		var status, target, volume string
		var retainedBytes, retainedEntries int64
		var stored []byte
		if err := a.ledger.db.QueryRowContext(ctx, "SELECT status,target_root,volume_identity,retained_bytes,retained_entries,receipt FROM quota_allocations WHERE id=?", a.ID).Scan(&status, &target, &volume, &retainedBytes, &retainedEntries, &stored); err != nil {
			return err
		}
		if status != "RETAINED" {
			return c.Fail(c.StaleAuthority, "physical quota allocation has no retained receipt")
		}
		if filepath.Clean(target) != filepath.Clean(measured.RootPath) || volume != measured.VolumeIdentity {
			return c.Fail(c.StaleAuthority, "retained physical receipt is bound to a different root")
		}
		if retainedBytes < 0 || retainedEntries < 0 || uint64(retainedBytes) != measured.ChargedBytes || uint64(retainedEntries) != measured.Entries {
			return c.Fail(c.StoreIntegrityError, "retained physical receipt counters are inconsistent")
		}
		if !bytes.Equal(stored, receipt) {
			return c.Fail(c.StaleAuthority, "retained physical receipt differs from the current output root")
		}
		return nil
	})
}

// Covers reports whether target is contained by the durable reservation root.
// It is used only to avoid double-reserving an inner file in a larger atomic
// bundle reservation; Commit still validates the exact root and inode.
func (a Allocation) Covers(target string) bool {
	if a.ledger == nil || a.targetRoot == "" || target == "" || !filepath.IsAbs(target) {
		return false
	}
	root, err := filepath.Abs(a.targetRoot)
	if err != nil {
		return false
	}
	child, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// EnclosingReservation returns the unique durable WRITING reservation that
// contains target. It lets a compound writer reserve one outer bundle and have
// nested writers contribute to that same measured receipt instead of charging
// the same bytes twice. A merely RESERVED allocation does not cover writes.
func (l *Ledger) EnclosingReservation(ctx context.Context, target string) (Allocation, bool, error) {
	if target == "" || !filepath.IsAbs(target) {
		return Allocation{}, false, c.Fail(c.InvalidArgument, "absolute physical target required")
	}
	target, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return Allocation{}, false, err
	}
	var found Allocation
	count := 0
	err = l.withReadLock(ctx, func() error {
		rows, err := l.db.QueryContext(ctx, "SELECT id,target_root,parent_path,parent_identity,volume_identity FROM quota_allocations WHERE status='WRITING' AND id NOT IN (SELECT standby_id FROM quota_mutable_replacements UNION SELECT standby_id FROM quota_control_reserves)")
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, rootPath, parentPath, parentID, volume string
			if err = rows.Scan(&id, &rootPath, &parentPath, &parentID, &volume); err != nil {
				rows.Close()
				return err
			}
			allocation := Allocation{ledger: l, ID: id, targetRoot: rootPath}
			if !allocation.Covers(target) {
				continue
			}
			if !validID(id) || !filepath.IsAbs(rootPath) || !filepath.IsAbs(parentPath) || !c.ValidDigest(parentID) || !c.ValidDigest(volume) {
				rows.Close()
				return c.Fail(c.StoreIntegrityError, "active physical quota reservation is corrupt")
			}
			// Recheck the exact directory and volume identity recorded at
			// admission before a nested writer relies on this reservation.
			parent, openErr := os.OpenRoot(parentPath)
			if openErr != nil {
				rows.Close()
				return openErr
			}
			actualParentID, identityErr := fileguard.DirectoryIdentity(parent)
			actualVolume, volumeErr := fileguard.VolumeIdentity(parent)
			closeErr := parent.Close()
			if err = errors.Join(identityErr, volumeErr, closeErr); err != nil {
				rows.Close()
				return err
			}
			if actualParentID != parentID || actualVolume != volume {
				rows.Close()
				return c.Fail(c.StaleAuthority, "active physical quota reservation parent changed")
			}
			found = allocation
			count++
		}
		return errors.Join(rows.Err(), rows.Close())
	})
	if err != nil {
		return Allocation{}, false, err
	}
	if count > 1 {
		return Allocation{}, false, c.Fail(c.StoreIntegrityError, "nested physical quota reservations overlap")
	}
	return found, count == 1, nil
}

// ImportRetained records content that already exists before this ledger is
// first attached to a writer. It does not authorize a write and may place the
// current retained total over the configured limit; subsequent reservations
// then fail closed. Reopening the same rooted tree is idempotent only when its
// measured inventory is unchanged.
func (l *Ledger) ImportRetained(ctx context.Context, writer, family string, inventory Inventory) error {
	if l == nil || len(writer) == 0 || len(writer) > 64 || len(family) == 0 || len(family) > 128 {
		return c.Fail(c.InvalidArgument, "bounded retained physical inventory required")
	}
	measured, err := validateInventory(inventory)
	if err != nil {
		return err
	}
	target, volume, parentPath, parentID, targetID, existed, _, err := targetVolume(measured.RootPath)
	if err != nil || existed != 1 || filepath.Clean(target) != filepath.Clean(measured.RootPath) || targetID != measured.RootIdentity || volume != measured.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "retained physical inventory root changed")
	}
	if _, covered, coverErr := l.EnclosingReservation(ctx, target); coverErr != nil {
		return coverErr
	} else if covered {
		// The outer allocation's final receipt accounts for all descendants.
		return nil
	}
	receipt, err := json.Marshal(measured)
	if err != nil {
		return err
	}
	digest := c.HashBytes(receipt)
	// Filesystem identities can be reused after an unrelated tree is deleted.
	// A seed names the canonical path AND its physical identity; inode equality
	// alone must never authorize a new evaluator/restore tree.
	id := retainedSeedID(l.Authority.ID, measured.RootPath, measured.RootIdentity)
	legacyID := c.HashBytes([]byte("physical-quota-seed-v1:" + l.Authority.ID + ":" + measured.RootIdentity))[:32]
	return l.withLock(ctx, func() error {
		if _, err := l.reconcileFreePendingLocked(ctx); err != nil {
			return err
		}
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// Preserve exact same-path legacy accounting without importing it twice.
		// A legacy receipt for another path stays charged and cannot admit this tree.
		var legacyTarget string
		legacyErr := tx.QueryRowContext(ctx, "SELECT target_root FROM quota_allocations WHERE id=?", legacyID).Scan(&legacyTarget)
		if legacyErr == nil && filepath.Clean(legacyTarget) == filepath.Clean(target) {
			id = legacyID
		} else if legacyErr != nil && !errors.Is(legacyErr, sql.ErrNoRows) {
			return legacyErr
		}
		var status, storedDigest string
		var stored []byte
		queryErr := tx.QueryRowContext(ctx, "SELECT status,request_digest,receipt FROM quota_allocations WHERE id=?", id).Scan(&status, &storedDigest, &stored)
		if queryErr == nil {
			// A seed imports legacy bytes once. Subsequent admitted publications
			// have separate receipts; they must not turn reopening into a second
			// seed, nor require the growing tree to equal its initial inventory.
			if status != "RETAINED" && status != "FREE_PENDING" && status != "RELEASED" {
				return c.Fail(c.StaleAuthority, "retained physical seed has an invalid lifecycle")
			}
			if err = validateRetainedReopen(ctx, tx, measured); err != nil {
				return err
			}
			return tx.Rollback()
		}
		if !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		if err = admitReceiptMutation(l.root, receipt); err != nil {
			return err
		}
		capacity, err := capacityTx(tx, l.Limits)
		if err != nil {
			return err
		}
		if capacity.Allocations >= maxLedgerRecords || measured.ChargedBytes > math.MaxInt64 || measured.Entries > math.MaxInt64 {
			return c.Fail(c.BudgetLimitReached, "retained physical inventory exceeds the bounded ledger")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO quota_allocations(id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries,retained_bytes,retained_entries,receipt) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", id, writer, family, digest, target, volume, parentPath, parentID, targetID, 1, string(Payload), "RETAINED", measured.ChargedBytes, measured.Entries, measured.ChargedBytes, measured.Entries, receipt)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

func retainedSeedID(authority, rootPath, rootIdentity string) string {
	return c.HashBytes([]byte("physical-quota-seed-v2:" + authority + "\x00" + filepath.Clean(rootPath) + "\x00" + rootIdentity))[:32]
}

func validateRetainedReopen(ctx context.Context, tx *sql.Tx, measured Inventory) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,writer,family,status,upper_bytes,upper_entries,retained_bytes,retained_entries,receipt,target_root,free_before FROM quota_allocations WHERE id NOT IN (SELECT standby_id FROM quota_mutable_replacements UNION SELECT standby_id FROM quota_control_reserves)")
	if err != nil {
		return err
	}
	defer rows.Close()
	known := map[string]FileReceipt{}
	expectedOwners := map[string]uint64{}
	strictMissing := map[string]uint64{}
	pendingMissing := map[string]uint64{}
	type pendingValidation struct {
		allocation pendingAllocation
		prefix     string
	}
	pending := make([]pendingValidation, 0, 2)
	unknownRoots := make([]string, 0, 2)
	var charged, entries uint64
	for rows.Next() {
		var id, writer, family, status, target string
		var upperBytes, upperEntries, retainedBytes, retainedEntries, freeBefore int64
		var raw []byte
		if err = rows.Scan(&id, &writer, &family, &status, &upperBytes, &upperEntries, &retainedBytes, &retainedEntries, &raw, &target, &freeBefore); err != nil {
			return err
		}
		if upperBytes < 0 || upperEntries < 0 || retainedBytes < 0 || retainedEntries < 0 || freeBefore < 0 {
			return c.Fail(c.StoreIntegrityError, "invalid physical allocation counters")
		}
		if status == "RESERVED" || status == "WRITING" {
			relative := ""
			if partitionPathWithin(measured.RootPath, target) {
				var relOK bool
				relative, relOK = partitionRelativePath(measured.RootPath, target)
				if !relOK {
					continue
				}
			} else if partitionPathWithin(target, measured.RootPath) {
				// An admitted ancestor root owns the whole measured subtree.
				relative = "."
			} else {
				continue
			}
			unknownRoots = append(unknownRoots, filepath.ToSlash(relative))
			charged, err = add(charged, uint64(upperBytes))
			if err != nil {
				return err
			}
			entries, err = add(entries, uint64(upperEntries))
			if err != nil {
				return err
			}
			continue
		}
		if status != "RETAINED" && status != "FREE_PENDING" {
			continue
		}
		var inventory Inventory
		if strictJSON(raw, &inventory) != nil {
			return c.Fail(c.StoreIntegrityError, "invalid retained physical receipt")
		}
		gcRoot := filepath.Join(measured.RootPath, "maintenance", "gc")
		gcOwnerRoot := filepath.Join(measured.RootPath, "owner", "maintenance", "gc")
		// Privacy and retention may erase retained task/archive roots while
		// their old byte charge remains conservative. Typed GC receipts keep a
		// stricter presence rule here; committed static/report members are
		// checked by the partition-specific exact-inventory validation.
		strictGCMaintenance := writer == "agent" && family == "GC_MAINTENANCE" && filepath.Clean(target) == filepath.Clean(inventory.RootPath) &&
			((filepath.Clean(target) != filepath.Clean(gcRoot) && partitionPathWithin(gcRoot, target)) ||
				(filepath.Clean(target) != filepath.Clean(gcOwnerRoot) && partitionPathWithin(gcOwnerRoot, target)))
		prefix := ""
		if filepath.Clean(inventory.RootPath) == filepath.Clean(measured.RootPath) {
			if inventory.RootIdentity != measured.RootIdentity {
				return c.Fail(c.StaleAuthority, "retained physical tree identity changed at its registered path")
			}
		} else {
			relative, relErr := filepath.Rel(measured.RootPath, inventory.RootPath)
			if relErr != nil || filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			prefix = filepath.ToSlash(relative) + "/"
		}
		if inventory.VolumeIdentity != measured.VolumeIdentity {
			return c.Fail(c.StaleAuthority, "retained physical volume changed")
		}
		if filepath.Clean(inventory.RootPath) != filepath.Clean(measured.RootPath) {
			if err = verifyPartitionDirectory(inventory.RootPath, inventory.RootIdentity, inventory.VolumeIdentity); err != nil {
				if !errors.Is(err, os.ErrNotExist) || status != "FREE_PENDING" && strictGCMaintenance {
					return c.Fail(c.StaleAuthority, "retained physical receipt root identity changed")
				}
			}
		}
		charged, err = add(charged, uint64(retainedBytes))
		if err != nil {
			return err
		}
		entries, err = add(entries, uint64(retainedEntries))
		if err != nil {
			return err
		}
		for _, file := range inventory.Files {
			file.Path = prefix + file.Path
			if previous, ok := known[file.Path]; ok && previous != file {
				return c.Fail(c.StoreIntegrityError, "conflicting physical file receipts")
			}
			known[file.Path] = file
			if !statOnlyFile(filepath.FromSlash(file.Path)) {
				expectedOwners[file.Path]++
				if strictGCMaintenance {
					strictMissing[file.Path]++
				}
			}
		}
		if status == "FREE_PENDING" {
			pending = append(pending, pendingValidation{allocation: pendingAllocation{
				ID: id, Target: target, Volume: inventory.VolumeIdentity, Status: status,
				RetainedBytes: retainedBytes, RetainedEntries: retainedEntries, Before: freeBefore, Receipt: raw,
			}, prefix: prefix})
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range pending {
		intent, found, intentErr := readFreeIntentForValidation(ctx, tx, item.allocation.ID)
		if intentErr != nil {
			return intentErr
		}
		if !found {
			// Legacy pending receipts have no exact member identity. They remain
			// charged as UNKNOWN, but cannot authorize a missing retained file.
			continue
		}
		saved, validateErr := validatePendingIntent(item.allocation, intent)
		if validateErr != nil {
			return validateErr
		}
		pendingPath := item.prefix + filepath.ToSlash(intent.Path)
		var exact FileReceipt
		foundExact := false
		for _, file := range saved.Files {
			if file.Path == intent.Path {
				exact = file
				foundExact = true
				break
			}
		}
		exact.Path = pendingPath
		if !foundExact || !sameReceipt(known[pendingPath], exact) {
			return c.Fail(c.StoreIntegrityError, "pending free member is not bound to its retained physical path")
		}
		pendingMissing[pendingPath]++
	}
	if measured.ChargedBytes > charged || measured.Entries > entries {
		return c.Fail(c.StaleAuthority, fmt.Sprintf("physical tree exceeds retained or outstanding admission (bytes=%d/%d entries=%d/%d)", measured.ChargedBytes, charged, measured.Entries, entries))
	}
	unadmitted := make([]string, 0)
	seen := make(map[string]struct{}, len(measured.Files))
	for _, file := range measured.Files {
		seen[file.Path] = struct{}{}
		if expected, ok := known[file.Path]; ok {
			if file != expected {
				return c.Fail(c.StaleAuthority, fmt.Sprintf("retained physical file identity or bytes changed: %s", file.Path))
			}
		} else {
			admitted := false
			for _, root := range unknownRoots {
				if root == "." || partitionNameAtOrBelow(file.Path, root) {
					admitted = true
					break
				}
			}
			if !admitted {
				unadmitted = append(unadmitted, file.Path)
			}
		}
	}
	for path, strictOwners := range strictMissing {
		if _, exists := seen[path]; exists {
			continue
		}
		if strictOwners == 0 || pendingMissing[path] != expectedOwners[path] {
			return c.Fail(c.StaleAuthority, fmt.Sprintf("retained physical file is missing without an exact pending free intent: %s", path))
		}
	}
	if len(unadmitted) != 0 {
		writeQuotaTreeDiagnostic(measured, known, charged, entries, len(unknownRoots) != 0, unadmitted)
		return c.Fail(c.StaleAuthority, "physical tree contains an unadmitted file")
	}
	return nil
}

func validateInventory(inventory Inventory) (Inventory, error) {
	if inventory.RootPath == "" || len(inventory.RootPath) > 2048 || !filepath.IsAbs(inventory.RootPath) || !c.ValidDigest(inventory.RootIdentity) || !c.ValidDigest(inventory.VolumeIdentity) || inventory.Entries > 1<<40 || inventory.AllocatedBytes > 1<<48 || inventory.LogicalBytes > 1<<48 || inventory.ChargedBytes > 1<<48 || inventory.ChargedBytes < inventory.AllocatedBytes || inventory.ChargedBytes < inventory.LogicalBytes || len(inventory.Files) > maxInventoryItems {
		return Inventory{}, c.Fail(c.InvalidArgument, "invalid measured physical inventory")
	}
	resolved, err := fileguard.ResolveProspective(inventory.RootPath)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(inventory.RootPath) {
		return Inventory{}, c.Fail(c.StaleBase, "physical receipt root changed")
	}
	previous := ""
	var allocated, logical, charged uint64
	for _, file := range inventory.Files {
		clean := path.Clean(filepath.ToSlash(file.Path))
		if file.Path <= previous || len(file.Path) > 4096 || filepath.IsAbs(file.Path) || clean == ".." || strings.HasPrefix(clean, "../") || !c.ValidDigest(file.Digest) || !c.ValidDigest(file.Identity) || file.Allocated > file.ChargedBytes || file.LogicalBytes > file.ChargedBytes {
			return Inventory{}, c.Fail(c.InvalidArgument, "invalid physical file receipt")
		}
		previous = file.Path
		var addErr error
		allocated, addErr = add(allocated, file.Allocated)
		if addErr != nil {
			return Inventory{}, addErr
		}
		logical, addErr = add(logical, file.LogicalBytes)
		if addErr != nil {
			return Inventory{}, addErr
		}
		charged, addErr = add(charged, file.ChargedBytes)
		if addErr != nil {
			return Inventory{}, addErr
		}
	}
	if len(inventory.Files) > 0 && (allocated > inventory.AllocatedBytes || logical > inventory.LogicalBytes || charged > inventory.ChargedBytes) {
		return Inventory{}, c.Fail(c.StoreIntegrityError, "physical receipt totals do not match file inventory")
	}
	raw, err := json.Marshal(inventory)
	if err != nil {
		return Inventory{}, err
	}
	if len(raw) > maxInventoryBytes {
		return Inventory{}, c.Fail(c.BudgetLimitReached, "physical inventory receipt exceeds the bounded ledger profile")
	}
	return inventory, nil
}

func (l *Ledger) Capacity(ctx context.Context) (Capacity, error) {
	var result Capacity
	err := l.withLock(ctx, func() error {
		var err error
		if _, err = l.reconcileFreePendingLocked(ctx); err != nil {
			return err
		}
		result, err = capacityQuery(ctx, l.db, l.Limits)
		return err
	})
	return result, err
}

// ReconcileFreePending replays durable exact deletion intents. A missing path
// is reclaimed only after its parent chain is synced and the same volume
// reports its available bytes at or above the recorded baseline plus the exact
// allocated bytes and metadata-write margin. Intents without an exact member
// identity remain charged as legacy unknown.
func (l *Ledger) ReconcileFreePending(ctx context.Context) (ReconcileResult, error) {
	var result ReconcileResult
	err := l.withLock(ctx, func() error {
		var err error
		result, err = l.reconcileFreePendingLocked(ctx)
		return err
	})
	return result, err
}

type freeIntent struct {
	Path, Digest, Identity, Volume, Root string
	Logical, Allocated, Charged, Before  uint64
}

type pendingAllocation struct {
	ID, Target, Volume, Status             string
	RetainedBytes, RetainedEntries, Before int64
	Receipt                                []byte
}

func (l *Ledger) reconcileFreePendingLocked(ctx context.Context) (ReconcileResult, error) {
	var result ReconcileResult
	rows, err := l.db.QueryContext(ctx, "SELECT id,target_root,volume_identity,status,retained_bytes,retained_entries,receipt,free_before FROM quota_allocations WHERE status='FREE_PENDING' ORDER BY id")
	if err != nil {
		return result, err
	}
	pending := make([]pendingAllocation, 0)
	for rows.Next() {
		var item pendingAllocation
		if err = rows.Scan(&item.ID, &item.Target, &item.Volume, &item.Status, &item.RetainedBytes, &item.RetainedEntries, &item.Receipt, &item.Before); err != nil {
			rows.Close()
			return result, err
		}
		pending = append(pending, item)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	for _, allocation := range pending {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		intent, found, intentErr := l.readFreeIntent(ctx, allocation.ID)
		if intentErr != nil {
			return result, intentErr
		}
		if !found {
			result.LegacyUnknown++
			result.PendingFiles++
			continue
		}
		inventory, err := validatePendingIntent(allocation, intent)
		if err != nil {
			return result, err
		}
		root, resolved, err := openPendingRoot(allocation.Target)
		if errors.Is(err, os.ErrNotExist) {
			result.PendingFiles++
			continue
		}
		if err != nil {
			return result, err
		}
		if filepath.Clean(resolved) != filepath.Clean(allocation.Target) {
			root.Close()
			return result, c.Fail(c.StaleAuthority, "physical free-intent root path changed")
		}
		rootIdentity, identityErr := fileguard.DirectoryIdentity(root)
		volumeIdentity, volumeErr := fileguard.VolumeIdentity(root)
		if err = errors.Join(identityErr, volumeErr); err != nil {
			root.Close()
			return result, err
		}
		if rootIdentity != inventory.RootIdentity || volumeIdentity != inventory.VolumeIdentity || rootIdentity != intent.Root || volumeIdentity != intent.Volume {
			root.Close()
			return result, c.Fail(c.StaleAuthority, "physical free-intent root or volume identity changed")
		}
		memberPath := filepath.FromSlash(intent.Path)
		_, statErr := root.Lstat(memberPath)
		if statErr == nil {
			measured, measureErr := measureFile(root, memberPath)
			if measureErr != nil {
				root.Close()
				return result, measureErr
			}
			if !sameReceipt(measured, receiptFromIntent(intent)) {
				root.Close()
				return result, c.Fail(c.StaleBase, "physical free-intent file identity or content changed")
			}
			closeErr := root.Close()
			if closeErr != nil {
				return result, closeErr
			}
			if err = l.cancelFreeIntent(ctx, allocation, intent); err != nil {
				return result, err
			}
			result.CancelledFiles++
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			root.Close()
			return result, statErr
		}
		if err = syncExistingParents(root, memberPath); err != nil {
			root.Close()
			return result, err
		}
		available, spaceErr := fileguard.AvailableBytes(root)
		if spaceErr != nil {
			_ = root.Close()
			result.PendingFiles++
			continue
		}
		if available > math.MaxInt64 || !freeCapacityProven(intent, available) {
			_ = root.Close()
			result.PendingFiles++
			continue
		}
		// Recheck absence after the volume query and parent sync, immediately
		// before publishing the receipt. This narrows the namespace race while
		// retaining the original exact intent and inode binding.
		if _, statErr = root.Lstat(memberPath); statErr == nil {
			_ = root.Close()
			result.PendingFiles++
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			_ = root.Close()
			return result, statErr
		}
		closeErr := root.Close()
		if closeErr != nil {
			return result, closeErr
		}
		if err = l.finishFreeIntent(ctx, allocation, inventory, intent, available); err != nil {
			return result, err
		}
		result.ReclaimedFiles++
	}
	return result, nil
}

func (l *Ledger) readFreeIntent(ctx context.Context, id string) (freeIntent, bool, error) {
	var intent freeIntent
	if !validID(id) {
		return intent, false, c.Fail(c.StoreIntegrityError, "invalid physical free-intent allocation ID")
	}
	var logical, allocated, charged, before int64
	err := l.db.QueryRowContext(ctx, "SELECT path,file_digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,free_before,volume_identity,root_identity FROM quota_free_intents WHERE allocation_id=?", id).Scan(&intent.Path, &intent.Digest, &intent.Identity, &logical, &allocated, &charged, &before, &intent.Volume, &intent.Root)
	if errors.Is(err, sql.ErrNoRows) {
		return intent, false, nil
	}
	if err != nil {
		return intent, false, err
	}
	if logical < 0 || allocated < 0 || charged < 0 || before < 0 {
		return intent, false, c.Fail(c.StoreIntegrityError, "negative physical free-intent counter")
	}
	intent.Logical, intent.Allocated, intent.Charged, intent.Before = uint64(logical), uint64(allocated), uint64(charged), uint64(before)
	if !validRelativeReceiptPath(intent.Path) || !c.ValidDigest(intent.Digest) || !c.ValidDigest(intent.Identity) || !c.ValidDigest(intent.Volume) || !c.ValidDigest(intent.Root) || intent.Charged < intent.Logical || intent.Charged < intent.Allocated {
		return intent, false, c.Fail(c.StoreIntegrityError, "invalid physical free-intent receipt")
	}
	return intent, true, nil
}

func validatePendingIntent(allocation pendingAllocation, intent freeIntent) (Inventory, error) {
	var inventory Inventory
	if allocation.Status != "FREE_PENDING" || allocation.Before < 0 || uint64(allocation.Before) != intent.Before || allocation.RetainedBytes < 0 || allocation.RetainedEntries < 0 || strictJSON(allocation.Receipt, &inventory) != nil {
		return Inventory{}, c.Fail(c.StoreIntegrityError, "physical free intent does not match its allocation")
	}
	canonical, err := json.Marshal(inventory)
	if err != nil || !bytes.Equal(canonical, allocation.Receipt) {
		return Inventory{}, c.Fail(c.StoreIntegrityError, "physical free-intent inventory is not canonical")
	}
	if _, err = validateInventory(inventory); err != nil {
		return Inventory{}, err
	}
	if filepath.Clean(inventory.RootPath) != filepath.Clean(allocation.Target) || inventory.VolumeIdentity != allocation.Volume || inventory.RootIdentity != intent.Root || inventory.VolumeIdentity != intent.Volume || inventory.ChargedBytes != uint64(allocation.RetainedBytes) || inventory.Entries != uint64(allocation.RetainedEntries) {
		return Inventory{}, c.Fail(c.StoreIntegrityError, "physical free-intent allocation counters differ from its receipt")
	}
	count := 0
	for _, file := range inventory.Files {
		if file.Path == intent.Path && sameReceipt(file, receiptFromIntent(intent)) {
			count++
		}
	}
	if count != 1 {
		return Inventory{}, c.Fail(c.StoreIntegrityError, "physical free-intent member is not uniquely present in its inventory")
	}
	return inventory, nil
}

func receiptFromIntent(intent freeIntent) FileReceipt {
	return FileReceipt{Path: intent.Path, Digest: intent.Digest, Identity: intent.Identity, LogicalBytes: intent.Logical, Allocated: intent.Allocated, ChargedBytes: intent.Charged}
}

func sameReceipt(left, right FileReceipt) bool {
	return left == right
}

func validRelativeReceiptPath(name string) bool {
	if name == "" || filepath.IsAbs(name) {
		return false
	}
	clean := path.Clean(filepath.ToSlash(name))
	return clean == filepath.ToSlash(name) && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func openPendingRoot(name string) (*os.Root, string, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, "", c.Fail(c.StoreIntegrityError, "physical free-intent root path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, "", err
	}
	return root, resolved, nil
}

func syncExistingParents(root *os.Root, name string) error {
	parent := filepath.Dir(name)
	for {
		info, err := root.Lstat(parent)
		if err == nil {
			if !info.IsDir() {
				return c.Fail(c.Conflict, "physical free-intent parent namespace changed")
			}
			return fileguard.SyncParents(root, parent)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if parent == "." {
			return err
		}
		parent = filepath.Dir(parent)
	}
}

func freeCapacityProven(intent freeIntent, available uint64) bool {
	need := intent.Allocated
	if need > math.MaxUint64-metadataAdmission {
		return false
	}
	need += metadataAdmission
	if intent.Before > math.MaxUint64-need {
		return false
	}
	return available >= intent.Before+need
}

func (l *Ledger) cancelFreeIntent(ctx context.Context, allocation pendingAllocation, intent freeIntent) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE quota_allocations SET status='RETAINED',free_before=0,free_after=0 WHERE id=? AND status='FREE_PENDING' AND receipt=? AND free_before=?", allocation.ID, allocation.Receipt, intent.Before)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "physical free intent changed during cancellation")
	}
	result, err = tx.ExecContext(ctx, "DELETE FROM quota_free_intents WHERE allocation_id=? AND path=? AND file_digest=? AND file_identity=? AND free_before=?", allocation.ID, intent.Path, intent.Digest, intent.Identity, intent.Before)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "physical free-intent identity changed during cancellation")
	}
	return tx.Commit()
}

func (l *Ledger) finishFreeIntent(ctx context.Context, allocation pendingAllocation, inventory Inventory, intent freeIntent, available uint64) error {
	if inventory.ChargedBytes < intent.Charged || inventory.AllocatedBytes < intent.Allocated || inventory.LogicalBytes < intent.Logical || inventory.Entries == 0 {
		return c.Fail(c.StoreIntegrityError, "physical free-intent exceeds retained inventory")
	}
	remaining := make([]FileReceipt, 0, len(inventory.Files)-1)
	for _, file := range inventory.Files {
		if file.Path != intent.Path {
			remaining = append(remaining, file)
		}
	}
	inventory.ChargedBytes -= intent.Charged
	inventory.AllocatedBytes -= intent.Allocated
	inventory.LogicalBytes -= intent.Logical
	inventory.Entries--
	inventory.Files = remaining
	raw, err := json.Marshal(inventory)
	if err != nil {
		return err
	}
	if err = admitReceiptMutation(l.root, raw); err != nil {
		return err
	}
	status := "RETAINED"
	if inventory.Entries == 0 && len(inventory.Files) == 0 {
		status = "RELEASED"
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE quota_allocations SET status=?,retained_bytes=?,retained_entries=?,receipt=?,free_before=0,free_after=? WHERE id=? AND status='FREE_PENDING' AND receipt=? AND free_before=?", status, inventory.ChargedBytes, inventory.Entries, raw, available, allocation.ID, allocation.Receipt, intent.Before)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "physical free allocation changed before receipt commit")
	}
	result, err = tx.ExecContext(ctx, "DELETE FROM quota_free_intents WHERE allocation_id=? AND path=? AND file_digest=? AND file_identity=? AND free_before=?", allocation.ID, intent.Path, intent.Digest, intent.Identity, intent.Before)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "physical free intent changed before receipt commit")
	}
	var partitionKind string
	partitionErr := tx.QueryRowContext(ctx, "SELECT kind FROM quota_partition_members WHERE member_id=?", allocation.ID).Scan(&partitionKind)
	if partitionErr == nil {
		if partitionKind != "ARCHIVE" {
			return c.Fail(c.StoreIntegrityError, "immutable physical partition member cannot be released")
		}
		result, err = tx.ExecContext(ctx, "UPDATE quota_partition_members SET receipt_digest=? WHERE member_id=? AND kind='ARCHIVE'", c.HashBytes(raw), allocation.ID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "physical archive partition receipt changed before commit")
		}
	} else if !errors.Is(partitionErr, sql.ErrNoRows) {
		return partitionErr
	}
	return tx.Commit()
}

func (l *Ledger) withReadLock(ctx context.Context, fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return os.ErrClosed
	}
	lock, err := l.acquire(ctx)
	if err != nil {
		return err
	}
	if err = l.verifyPinnedMetadata(); err != nil {
		return errors.Join(err, lock.Close())
	}
	operationErr := fn()
	metadataErr := l.verifyPinnedMetadata()
	return errors.Join(operationErr, metadataErr, lock.Close())
}

func capacityQuery(ctx context.Context, db *sql.DB, limits Limits) (Capacity, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Capacity{}, err
	}
	defer tx.Rollback()
	result, err := capacityTx(tx, limits)
	if err != nil {
		return Capacity{}, err
	}
	result.Capacity.SchemaVersion, result.Capacity.Coverage = 1, "AUTHORITY_SHARED_DURABLE_LEDGER"
	if err = tx.Commit(); err != nil {
		return Capacity{}, err
	}
	return result.Capacity, nil
}

type capacityParts struct {
	Capacity
	PayloadBytes              uint64
	PayloadEntries            uint64
	ControlUsedBytes          uint64
	ControlUsedEntries        uint64
	PayloadOutstandingBytes   uint64
	PayloadOutstandingEntries uint64
	ControlOutstandingBytes   uint64
	ControlOutstandingEntries uint64
}

func capacityTx(tx *sql.Tx, limits Limits) (capacityParts, error) {
	result := capacityParts{Capacity: Capacity{LimitBytes: limits.Bytes, LimitEntries: limits.Entries, ControlBytes: limits.ControlBytes, ControlEntries: limits.ControlEntries}}
	rows, err := tx.Query("SELECT class,status,upper_bytes,upper_entries,retained_bytes,retained_entries FROM quota_allocations")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var class, status string
		var upperBytes, upperEntries, retainedBytes, retainedEntries int64
		if err = rows.Scan(&class, &status, &upperBytes, &upperEntries, &retainedBytes, &retainedEntries); err != nil {
			rows.Close()
			return result, err
		}
		if upperBytes < 0 || upperEntries < 0 || retainedBytes < 0 || retainedEntries < 0 || (class != string(Payload) && class != string(Control)) {
			rows.Close()
			return result, c.Fail(c.StoreIntegrityError, "physical quota allocation ledger is corrupt")
		}
		result.Allocations++
		if status == "RESERVED" || status == "WRITING" || status == "STANDBY" {
			if status != "STANDBY" {
				result.Unknown++
			}
			result.OutstandingBytes, err = add(result.OutstandingBytes, uint64(upperBytes))
			if err == nil {
				result.OutstandingItems, err = add(result.OutstandingItems, uint64(upperEntries))
			}
			if err == nil && class == string(Payload) {
				result.PayloadOutstandingBytes, err = add(result.PayloadOutstandingBytes, uint64(upperBytes))
				if err == nil {
					result.PayloadOutstandingEntries, err = add(result.PayloadOutstandingEntries, uint64(upperEntries))
				}
			} else if err == nil {
				result.ControlOutstandingBytes, err = add(result.ControlOutstandingBytes, uint64(upperBytes))
				if err == nil {
					result.ControlOutstandingEntries, err = add(result.ControlOutstandingEntries, uint64(upperEntries))
				}
			}
		} else if status == "RETAINED" || status == "FREE_PENDING" {
			if status == "FREE_PENDING" {
				result.Unknown++
			}
			result.RetainedBytes, err = add(result.RetainedBytes, uint64(retainedBytes))
			if err == nil {
				result.RetainedEntries, err = add(result.RetainedEntries, uint64(retainedEntries))
			}
			if err == nil && class == string(Payload) {
				result.PayloadBytes, err = add(result.PayloadBytes, uint64(retainedBytes))
				if err == nil {
					result.PayloadEntries, err = add(result.PayloadEntries, uint64(retainedEntries))
				}
			} else if err == nil {
				result.ControlUsedBytes, err = add(result.ControlUsedBytes, uint64(retainedBytes))
				if err == nil {
					result.ControlUsedEntries, err = add(result.ControlUsedEntries, uint64(retainedEntries))
				}
			}
		} else if status == "RELEASED" {
			continue
		} else {
			rows.Close()
			return result, c.Fail(c.StoreIntegrityError, "physical quota allocation has an unknown state")
		}
		if err != nil {
			rows.Close()
			return result, err
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	return result, nil
}

// ReleaseFile only drops a retained file charge after the same rooted inode,
// digest, and byte measurements have been unlinked and the volume reports the
// reserved bytes free. FREE_PENDING survives a crash between intent and receipt.
func (l *Ledger) ReleaseFile(ctx context.Context, root *os.Root, name, digest string, logicalBytes, limit int64) (bool, error) {
	if root == nil || name == "" || !c.ValidDigest(digest) || logicalBytes < 0 || logicalBytes > limit || limit < 0 {
		return false, c.Fail(c.InvalidArgument, "exact physical free receipt required")
	}
	relative := filepath.ToSlash(name)
	if filepath.IsAbs(name) || path.Clean(relative) == ".." || strings.HasPrefix(path.Clean(relative), "../") {
		return false, c.Fail(c.PolicyDenied, "physical release path escapes its rooted store")
	}
	var removed bool
	err := l.withLock(ctx, func() error {
		rootPath, err := filepath.Abs(root.Name())
		if err != nil {
			return err
		}
		rootPath, err = filepath.EvalSymlinks(rootPath)
		if err != nil {
			return err
		}
		rootIdentity, err := fileguard.DirectoryIdentity(root)
		if err != nil {
			return err
		}
		volumeIdentity, err := fileguard.VolumeIdentity(root)
		if err != nil {
			return err
		}
		rows, err := l.db.QueryContext(ctx, `SELECT id,status,receipt,free_before FROM quota_allocations
			WHERE status IN ('RETAINED','FREE_PENDING') AND receipt IS NOT NULL AND target_root=? AND id NOT IN (
				SELECT member_id FROM quota_partition_members WHERE kind!='ARCHIVE'
			)`, rootPath)
		if err != nil {
			return err
		}
		type candidate struct {
			id, status string
			before     uint64
			inv        Inventory
			intent     *freeIntent
		}
		candidates := []candidate{}
		for rows.Next() {
			var item candidate
			var raw []byte
			var freeBefore int64
			if err = rows.Scan(&item.id, &item.status, &raw, &freeBefore); err != nil {
				rows.Close()
				return err
			}
			if freeBefore < 0 || strictJSON(raw, &item.inv) != nil || item.inv.RootIdentity != rootIdentity || item.inv.VolumeIdentity != volumeIdentity || filepath.Clean(item.inv.RootPath) != filepath.Clean(rootPath) {
				continue
			}
			item.before = uint64(freeBefore)
			for _, receipt := range item.inv.Files {
				if receipt.Path == relative && receipt.Digest == digest && receipt.LogicalBytes == uint64(logicalBytes) {
					candidates = append(candidates, item)
					break
				}
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		if len(candidates) != 1 {
			return c.Fail(c.StoreIntegrityError, "physical free receipt is ambiguous")
		}
		match := candidates[0]
		var receipt FileReceipt
		for _, file := range match.inv.Files {
			if file.Path == relative {
				receipt = file
				break
			}
		}
		if receipt.Identity == "" {
			return c.Fail(c.StoreIntegrityError, "physical allocation receipt lacks inode binding")
		}
		intent, hasIntent, err := l.readFreeIntent(ctx, match.id)
		if err != nil {
			return err
		}
		if hasIntent {
			if match.status != "FREE_PENDING" || !sameReceipt(receipt, receiptFromIntent(intent)) || intent.Root != rootIdentity || intent.Volume != volumeIdentity || intent.Before != match.before {
				return c.Fail(c.StoreIntegrityError, "physical free intent differs from the retained file receipt")
			}
			match.intent = &intent
		} else if match.status == "FREE_PENDING" {
			// Older ledgers stored only a volume baseline. The caller's exact
			// retained file receipt upgrades that legacy intent without guessing
			// which member was in flight.
			legacy := freeIntent{Path: receipt.Path, Digest: receipt.Digest, Identity: receipt.Identity, Logical: receipt.LogicalBytes, Allocated: receipt.Allocated, Charged: receipt.ChargedBytes, Before: match.before, Volume: volumeIdentity, Root: rootIdentity}
			if err = l.beginFreeIntent(ctx, match.id, legacy); err != nil {
				return err
			}
			match.intent = &legacy
		} else if match.status != "RETAINED" {
			return c.Fail(c.StoreIntegrityError, "physical allocation is not retained for release")
		}
		info, statErr := root.Lstat(filepath.FromSlash(relative))
		if statErr == nil {
			measured, measureErr := measureFile(root, relative)
			if measureErr != nil {
				return measureErr
			}
			if measured.Identity != receipt.Identity || measured.Digest != receipt.Digest || measured.LogicalBytes != receipt.LogicalBytes || measured.Allocated != receipt.Allocated || measured.ChargedBytes != receipt.ChargedBytes {
				return c.Fail(c.StaleBase, "physical free receipt inode or content changed")
			}
			if match.status == "RETAINED" {
				freeBefore, freeErr := fileguard.AvailableBytes(root)
				if freeErr != nil {
					return freeErr
				}
				newIntent := freeIntent{Path: receipt.Path, Digest: receipt.Digest, Identity: receipt.Identity, Logical: receipt.LogicalBytes, Allocated: receipt.Allocated, Charged: receipt.ChargedBytes, Before: freeBefore, Volume: volumeIdentity, Root: rootIdentity}
				if err = l.beginFreeIntent(ctx, match.id, newIntent); err != nil {
					return err
				}
				match.intent = &newIntent
			}
			deleted, removeErr := fileguard.RemoveBoundIdentity(root, filepath.FromSlash(relative), info, digest, logicalBytes, limit)
			if removeErr != nil {
				return removeErr
			}
			if deleted {
				removed = true
			}
			if err = fileguard.SyncParents(root, filepath.Dir(filepath.FromSlash(relative))); err != nil {
				return err
			}
		} else if errors.Is(statErr, os.ErrNotExist) {
			if match.status != "FREE_PENDING" {
				return nil
			}
			if match.intent == nil {
				return c.Fail(c.StoreIntegrityError, "physical free intent member identity is missing")
			}
			if err = syncExistingParents(root, filepath.FromSlash(relative)); err != nil {
				return err
			}
		} else {
			return statErr
		}
		reconciled, err := l.reconcileFreePendingLocked(ctx)
		if err != nil {
			return err
		}
		if reconciled.ReclaimedFiles > 0 {
			removed = true
		}
		return nil
	})
	return removed, err
}

func (l *Ledger) beginFreeIntent(ctx context.Context, id string, intent freeIntent) error {
	if !validID(id) || !validRelativeReceiptPath(intent.Path) || !c.ValidDigest(intent.Digest) || !c.ValidDigest(intent.Identity) || !c.ValidDigest(intent.Root) || !c.ValidDigest(intent.Volume) || intent.Charged < intent.Logical || intent.Charged < intent.Allocated || intent.Before > math.MaxInt64 || intent.Logical > math.MaxInt64 || intent.Allocated > math.MaxInt64 || intent.Charged > math.MaxInt64 {
		return c.Fail(c.UnsupportedCapability, "free-space receipt exceeds bounded counter")
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE quota_allocations SET status='FREE_PENDING',free_before=? WHERE id=? AND status IN ('RETAINED','FREE_PENDING')", intent.Before, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "physical allocation no longer admits a free intent")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO quota_free_intents(allocation_id,path,file_digest,file_identity,logical_bytes,allocated_bytes,charged_bytes,free_before,volume_identity,root_identity) VALUES(?,?,?,?,?,?,?,?,?,?)", id, intent.Path, intent.Digest, intent.Identity, intent.Logical, intent.Allocated, intent.Charged, intent.Before, intent.Volume, intent.Root)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func outstandingForVolumeTx(tx *sql.Tx, volume string) (uint64, error) {
	rows, err := tx.Query("SELECT upper_bytes FROM quota_allocations WHERE volume_identity=? AND status IN ('RESERVED','WRITING','STANDBY')", volume)
	if err != nil {
		return 0, err
	}
	var total uint64
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return 0, err
		}
		if n < 0 {
			rows.Close()
			return 0, c.Fail(c.StoreIntegrityError, "negative physical quota reservation")
		}
		total, err = add(total, uint64(n))
		if err != nil {
			rows.Close()
			return 0, err
		}
	}
	return total, errors.Join(rows.Err(), rows.Close())
}

func add(a, b uint64) (uint64, error) {
	if b > math.MaxUint64-a || a+b > 1<<52 {
		return 0, c.Fail(c.StoreIntegrityError, "physical quota integer overflow")
	}
	return a + b, nil
}

func targetVolume(name string) (string, string, string, string, string, int, uint64, error) {
	if name == "" || !filepath.IsAbs(name) {
		return "", "", "", "", "", 0, 0, c.Fail(c.InvalidArgument, "absolute physical target root required")
	}
	target, err := fileguard.ResolveProspective(name)
	if err != nil {
		return "", "", "", "", "", 0, 0, err
	}
	current := target
	targetExisted := 1
	if info, statErr := os.Stat(target); statErr == nil {
		if !info.IsDir() {
			return "", "", "", "", "", 0, 0, c.Fail(c.PolicyDenied, "physical quota target must be a directory")
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		current = filepath.Dir(target)
		targetExisted = 0
		for {
			parentInfo, parentErr := os.Stat(current)
			if parentErr == nil {
				if !parentInfo.IsDir() {
					return "", "", "", "", "", 0, 0, c.Fail(c.InvalidArgument, "physical quota target ancestor must be a directory")
				}
				break
			}
			if !errors.Is(parentErr, os.ErrNotExist) || filepath.Dir(current) == current {
				return "", "", "", "", "", 0, 0, c.Fail(c.InvalidArgument, "physical quota target has no existing directory ancestor")
			}
			current = filepath.Dir(current)
		}
	} else {
		return "", "", "", "", "", 0, 0, statErr
	}
	root, err := os.OpenRoot(current)
	if err != nil {
		return "", "", "", "", "", 0, 0, err
	}
	defer root.Close()
	volume, err := fileguard.VolumeIdentity(root)
	if err != nil {
		return "", "", "", "", "", 0, 0, err
	}
	identity, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return "", "", "", "", "", 0, 0, err
	}
	available, err := fileguard.AvailableBytes(root)
	return target, volume, current, identity, identity, targetExisted, available, err
}

func Measure(root *os.Root, includeFiles bool) (Inventory, error) {
	if root == nil {
		return Inventory{}, c.Fail(c.InvalidArgument, "rooted physical output required")
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
	result := Inventory{RootPath: rootPath, RootIdentity: identity, VolumeIdentity: volume, Files: []FileReceipt{}}
	result.Entries = 1
	result.ChargedBytes = directoryCharge
	if includeFiles {
		result.Files = make([]FileReceipt, 0, 128)
	}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if entry.IsDir() {
			var addErr error
			result.Entries, addErr = add(result.Entries, 1)
			if addErr == nil {
				result.ChargedBytes, addErr = add(result.ChargedBytes, directoryCharge)
			}
			if addErr != nil || result.Entries > maxInventoryItems {
				return c.Fail(c.BudgetLimitReached, "physical output entry inventory exceeds bounded profile")
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return c.Fail(c.UnsupportedCapability, "nonregular entry in physical output inventory")
		}
		if statOnlyFile(name) {
			info, statErr := root.Lstat(filepath.FromSlash(name))
			if statErr != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxLogicalFileSize {
				return c.Fail(c.StoreIntegrityError, "active owner metadata changed during physical inventory")
			}
			charge := uint64(info.Size())
			if charge == 0 {
				charge = directoryCharge
			} else if charge <= maxLogicalFileSize-lockedFileSlack {
				charge += lockedFileSlack
			}
			result.Entries++
			var addErr error
			result.LogicalBytes, addErr = add(result.LogicalBytes, uint64(info.Size()))
			if addErr == nil {
				result.ChargedBytes, addErr = add(result.ChargedBytes, charge)
			}
			if addErr != nil || result.Entries > maxInventoryItems {
				return c.Fail(c.BudgetLimitReached, "physical output entry inventory exceeds bounded profile")
			}
			// These live lock/database files can be byte-range locked against a
			// read handle on Windows. They remain charged by stat size and have no
			// deletion receipt until their owner closes and remeasures them.
			return nil
		}
		file, err := measureFile(root, filepath.ToSlash(name))
		if err != nil {
			return err
		}
		result.Entries++
		result.AllocatedBytes, err = add(result.AllocatedBytes, file.Allocated)
		if err == nil {
			result.LogicalBytes, err = add(result.LogicalBytes, file.LogicalBytes)
		}
		if err == nil {
			result.ChargedBytes, err = add(result.ChargedBytes, file.ChargedBytes)
		}
		if err != nil {
			return err
		}
		if includeFiles {
			result.Files = append(result.Files, file)
		}
		if result.Entries > maxInventoryItems || result.ChargedBytes > 1<<48 {
			return c.Fail(c.BudgetLimitReached, "physical output inventory exceeds bounded profile")
		}
		return nil
	})
	if err != nil {
		return Inventory{}, err
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

func statOnlyFile(name string) bool {
	switch filepath.ToSlash(name) {
	case "control.reserve", "owner/control.reserve":
		return true
	}
	switch filepath.Base(filepath.FromSlash(name)) {
	case "owner.lock", "archive.lock", "state.sqlite", "state.sqlite-wal", "state.sqlite-shm":
		return true
	default:
		return false
	}
}

func MeasureFile(root *os.Root, name string) (FileReceipt, error) {
	clean := path.Clean(filepath.ToSlash(name))
	if root == nil || name == "" || filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return FileReceipt{}, c.Fail(c.InvalidArgument, "rooted physical file required")
	}
	return measureFile(root, name)
}

func MeasureFileInventory(root *os.Root, name string) (Inventory, error) {
	if root == nil {
		return Inventory{}, c.Fail(c.InvalidArgument, "rooted physical output required")
	}
	receipt, err := MeasureFile(root, name)
	if err != nil {
		return Inventory{}, err
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
	return Inventory{RootPath: rootPath, RootIdentity: identity, VolumeIdentity: volume, AllocatedBytes: receipt.Allocated, LogicalBytes: receipt.LogicalBytes, ChargedBytes: receipt.ChargedBytes, Entries: 1, Files: []FileReceipt{receipt}}, nil
}

func measureFile(root *os.Root, name string) (FileReceipt, error) {
	before, err := root.Lstat(filepath.FromSlash(name))
	if err != nil {
		return FileReceipt{}, err
	}
	if !before.Mode().IsRegular() {
		return FileReceipt{}, c.Fail(c.UnsupportedCapability, "physical receipt requires a regular file")
	}
	f, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return FileReceipt{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return FileReceipt{}, c.Fail(c.StaleBase, "physical receipt file changed during measurement")
	}
	if err = fileguard.SingleLink(f); err != nil {
		return FileReceipt{}, err
	}
	// Measurement opens the file read-only. Durable writers sync before calling
	// Measure; a read-only descriptor cannot be flushed on Windows.
	identity, err := fileguard.FileIdentity(f)
	if err != nil {
		return FileReceipt{}, err
	}
	allocated, err := fileguard.AllocatedBytes(f)
	if err != nil || allocated < 0 {
		return FileReceipt{}, c.Fail(c.UnsupportedCapability, "physical file allocation measurement unavailable")
	}
	hash := sha256.New()
	logical, err := io.Copy(hash, io.LimitReader(f, maxLogicalFileSize+1))
	if err != nil || logical > maxLogicalFileSize || opened.Size() != logical {
		return FileReceipt{}, c.Fail(c.StoreIntegrityError, fmt.Sprintf("physical receipt file size changed for %s (stat=%d read=%d): %v", name, opened.Size(), logical, err))
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	charged := uint64(allocated)
	if uint64(logical) > charged {
		charged = uint64(logical)
	}
	return FileReceipt{Path: filepath.ToSlash(name), Digest: digest, Identity: identity, LogicalBytes: uint64(logical), Allocated: uint64(allocated), ChargedBytes: charged}, nil
}

func (l *Ledger) String() string {
	return fmt.Sprintf("physical quota ledger %s for authority %s", l.LedgerID, l.Authority.ID)
}
