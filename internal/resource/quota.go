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
	maxLogicalFileSize = 1 << 48
	metadataAdmission  = 64 << 10
	lockedFileSlack    = 64 << 10
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
	ID            string `json:"id,omitempty"`
	Writer        string `json:"writer"`
	Family        string `json:"family"`
	RequestDigest string `json:"request_digest"`
	TargetRoot    string `json:"target_root"`
	UpperBytes    uint64 `json:"upper_bytes"`
	UpperEntries  uint64 `json:"upper_entries"`
	Class         Class  `json:"class"`
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
	mu        sync.Mutex
	root      *os.Root
	db        *sql.DB
	lockName  string
	closed    bool
	Authority Authority
	Limits    Limits
	LedgerID  string
}

type Allocation struct {
	ledger     *Ledger
	ID         string
	targetRoot string
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
	if err = admitMetadata(root, metadataAdmission); err != nil {
		return nil, err
	}
	lock, err := acquireLock(root)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
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
	ledger := &Ledger{root: root, db: db, lockName: "physical-quota.lock", Authority: authority, Limits: limits, LedgerID: stored.LedgerID}
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
	reserve, err := diskguard.OpenRoot(root)
	if err != nil {
		return err
	}
	return errors.Join(reserve.Admit(bytes, true), reserve.Close())
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
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA wal_autocheckpoint=1"} {
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
	}
	return nil
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
			if admissionErr := admitMetadata(l.root, metadataAdmission); admissionErr != nil {
				err = errors.Join(err, admissionErr)
			}
			_, checkpointErr := l.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
			err = errors.Join(err, checkpointErr, lock.Close())
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
	if err := admitMetadata(l.root, metadataAdmission); err != nil {
		return err
	}
	lock, err := l.acquire(ctx)
	if err != nil {
		return err
	}
	return errors.Join(fn(), lock.Close())
}

func (l *Ledger) Reserve(ctx context.Context, request Request) (Allocation, error) {
	if request.ID == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return Allocation{}, err
		}
		request.ID = hex.EncodeToString(nonce[:])
	}
	if !validID(request.ID) || len(request.Writer) == 0 || len(request.Writer) > 64 || len(request.Family) > 128 || !c.ValidDigest(request.RequestDigest) || request.UpperBytes > 1<<48 || request.UpperEntries == 0 || request.UpperEntries > 1<<40 || (request.Class != Payload && request.Class != Control) {
		return Allocation{}, c.Fail(c.InvalidArgument, "invalid physical quota allocation request")
	}
	target, volume, parentPath, parentID, targetID, targetExisted, available, err := targetVolume(request.TargetRoot)
	if err != nil {
		return Allocation{}, err
	}
	var allocation Allocation
	err = l.withLock(ctx, func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
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
		return tx.Commit()
	})
	if err != nil {
		return Allocation{}, err
	}
	allocation = Allocation{ledger: l, ID: request.ID, targetRoot: target}
	return allocation, nil
}

func (a Allocation) Begin(ctx context.Context) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota reservation required")
	}
	return a.ledger.withLock(ctx, func() error {
		result, err := a.ledger.db.ExecContext(ctx, "UPDATE quota_allocations SET status='WRITING' WHERE id=? AND status='RESERVED'", a.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return c.Fail(c.StaleAuthority, "physical quota reservation is missing or already dispatched")
		}
		return nil
	})
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
		_, err = tx.ExecContext(ctx, "UPDATE quota_allocations SET status='RETAINED',retained_bytes=?,retained_entries=?,receipt=? WHERE id=? AND status='WRITING'", measured.ChargedBytes, measured.Entries, raw, a.ID)
		if err != nil {
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
		rows, err := l.db.QueryContext(ctx, "SELECT id,target_root,parent_path,parent_identity,volume_identity FROM quota_allocations WHERE status='WRITING'")
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
	id := c.HashBytes([]byte("physical-quota-seed-v1:" + l.Authority.ID + ":" + measured.RootIdentity))[:32]
	return l.withLock(ctx, func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
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

func validateRetainedReopen(ctx context.Context, tx *sql.Tx, measured Inventory) error {
	rows, err := tx.QueryContext(ctx, "SELECT status,upper_bytes,upper_entries,retained_bytes,retained_entries,receipt,target_root FROM quota_allocations")
	if err != nil {
		return err
	}
	defer rows.Close()
	known := map[string]FileReceipt{}
	var charged, entries uint64
	unknown := false
	for rows.Next() {
		var status, target string
		var upperBytes, upperEntries, retainedBytes, retainedEntries int64
		var raw []byte
		if err = rows.Scan(&status, &upperBytes, &upperEntries, &retainedBytes, &retainedEntries, &raw, &target); err != nil {
			return err
		}
		if upperBytes < 0 || upperEntries < 0 || retainedBytes < 0 || retainedEntries < 0 {
			return c.Fail(c.StoreIntegrityError, "invalid physical allocation counters")
		}
		if status == "RESERVED" || status == "WRITING" {
			if filepath.Clean(target) == filepath.Clean(measured.RootPath) {
				unknown = true
				charged, err = add(charged, uint64(upperBytes))
				if err != nil {
					return err
				}
				entries, err = add(entries, uint64(upperEntries))
				if err != nil {
					return err
				}
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
		prefix := ""
		if inventory.RootIdentity != measured.RootIdentity {
			relative, relErr := filepath.Rel(measured.RootPath, inventory.RootPath)
			if relErr != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			prefix = filepath.ToSlash(relative) + "/"
		}
		if inventory.VolumeIdentity != measured.VolumeIdentity {
			return c.Fail(c.StaleAuthority, "retained physical volume changed")
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
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if measured.ChargedBytes > charged || measured.Entries > entries {
		return c.Fail(c.StaleAuthority, fmt.Sprintf("physical tree exceeds retained or outstanding admission (bytes=%d/%d entries=%d/%d)", measured.ChargedBytes, charged, measured.Entries, entries))
	}
	unadmitted := make([]string, 0)
	for _, file := range measured.Files {
		if expected, ok := known[file.Path]; ok {
			if file != expected {
				return c.Fail(c.StaleAuthority, "retained physical file identity or bytes changed")
			}
		} else if !unknown {
			unadmitted = append(unadmitted, file.Path)
		}
	}
	if len(unadmitted) != 0 {
		writeQuotaTreeDiagnostic(measured, known, charged, entries, unknown, unadmitted)
		return c.Fail(c.StaleAuthority, "physical tree contains an unadmitted file")
	}
	return nil
}

func validateInventory(inventory Inventory) (Inventory, error) {
	if inventory.RootPath == "" || !filepath.IsAbs(inventory.RootPath) || !c.ValidDigest(inventory.RootIdentity) || !c.ValidDigest(inventory.VolumeIdentity) || inventory.Entries > 1<<40 || inventory.AllocatedBytes > 1<<48 || inventory.LogicalBytes > 1<<48 || inventory.ChargedBytes > 1<<48 || inventory.ChargedBytes < inventory.AllocatedBytes || inventory.ChargedBytes < inventory.LogicalBytes {
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
		if file.Path <= previous || filepath.IsAbs(file.Path) || clean == ".." || strings.HasPrefix(clean, "../") || !c.ValidDigest(file.Digest) || !c.ValidDigest(file.Identity) || file.Allocated > file.ChargedBytes || file.LogicalBytes > file.ChargedBytes {
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
	return inventory, nil
}

func (l *Ledger) Capacity(ctx context.Context) (Capacity, error) {
	var result Capacity
	err := l.withReadLock(ctx, func() error {
		var err error
		result, err = capacityQuery(ctx, l.db, l.Limits)
		return err
	})
	return result, err
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
	return errors.Join(fn(), lock.Close())
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
		if status == "RESERVED" || status == "WRITING" {
			result.Unknown++
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
		rows, err := l.db.QueryContext(ctx, "SELECT id,status,receipt,free_before FROM quota_allocations WHERE status IN ('RETAINED','FREE_PENDING') AND receipt IS NOT NULL AND target_root=?", rootPath)
		if err != nil {
			return err
		}
		type candidate struct {
			id, status string
			before     uint64
			inv        Inventory
		}
		matches := []candidate{}
		for rows.Next() {
			var item candidate
			var raw []byte
			var freeBefore int64
			if err = rows.Scan(&item.id, &item.status, &raw, &freeBefore); err != nil {
				rows.Close()
				return err
			}
			if freeBefore < 0 || strictJSON(raw, &item.inv) != nil || item.inv.RootIdentity != rootIdentity {
				continue
			}
			item.before = uint64(freeBefore)
			for _, receipt := range item.inv.Files {
				if receipt.Path == relative && receipt.Digest == digest && receipt.LogicalBytes == uint64(logicalBytes) {
					matches = append(matches, candidate{item.id, item.status, item.before, item.inv})
					break
				}
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(matches) == 0 {
			return nil
		}
		if len(matches) != 1 {
			return c.Fail(c.StoreIntegrityError, "physical free receipt is ambiguous")
		}
		match := matches[0]
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
		var freeBefore = match.before
		info, statErr := root.Lstat(filepath.FromSlash(relative))
		if statErr == nil {
			measured, measureErr := measureFile(root, relative)
			if measureErr != nil {
				return measureErr
			}
			if measured.Identity != receipt.Identity || measured.Digest != receipt.Digest || measured.LogicalBytes != receipt.LogicalBytes || measured.Allocated != receipt.Allocated || measured.ChargedBytes != receipt.ChargedBytes {
				return c.Fail(c.StaleBase, "physical free receipt inode or content changed")
			}
			freeBefore, err = fileguard.AvailableBytes(root)
			if err != nil {
				return err
			}
			if err = l.setFreePending(ctx, match.id, freeBefore); err != nil {
				return err
			}
			// Persisting the intent can allocate ledger/WAL pages on the same
			// volume. Establish the deletion baseline after that write, then
			// persist it while updating the already allocated ledger row.
			freeBefore, err = fileguard.AvailableBytes(root)
			if err != nil {
				return err
			}
			if err = l.setFreePending(ctx, match.id, freeBefore); err != nil {
				return err
			}
			if _, err = fileguard.RemoveBoundIdentity(root, filepath.FromSlash(relative), info, digest, logicalBytes, limit); err != nil {
				return err
			}
			removed = true
		} else if errors.Is(statErr, os.ErrNotExist) {
			if match.status != "FREE_PENDING" {
				return nil
			}
			parent := filepath.Dir(filepath.FromSlash(relative))
			if err = fileguard.SyncParents(root, parent); err != nil {
				return err
			}
		} else {
			return statErr
		}
		if err = fileguard.SyncParents(root, filepath.Dir(filepath.FromSlash(relative))); err != nil {
			return err
		}
		available, err := fileguard.AvailableBytes(root)
		if err != nil {
			return err
		}
		if receipt.Allocated > 0 && (receipt.Allocated > math.MaxUint64-freeBefore || available < freeBefore+receipt.Allocated) {
			// Unlink and capacity reclamation are separate outcomes. NTFS resident
			// streams and open mappings may not return a whole free-space unit.
			// Deletion may finish while FREE_PENDING keeps every byte charged.
			return nil
		}
		remainingFiles := make([]FileReceipt, 0, len(match.inv.Files)-1)
		for _, item := range match.inv.Files {
			if item.Path != relative {
				remainingFiles = append(remainingFiles, item)
			}
		}
		if match.inv.ChargedBytes < receipt.ChargedBytes || match.inv.AllocatedBytes < receipt.Allocated || match.inv.LogicalBytes < receipt.LogicalBytes || match.inv.Entries < 1 {
			return c.Fail(c.StoreIntegrityError, "physical free receipt exceeds retained inventory")
		}
		match.inv.ChargedBytes -= receipt.ChargedBytes
		match.inv.AllocatedBytes -= receipt.Allocated
		match.inv.LogicalBytes -= receipt.LogicalBytes
		match.inv.Entries--
		match.inv.Files = remainingFiles
		remainingRaw, err := json.Marshal(match.inv)
		if err != nil {
			return err
		}
		status := "RETAINED"
		if len(remainingFiles) == 0 && match.inv.Entries == 0 {
			status = "RELEASED"
		}
		result, err := l.db.ExecContext(ctx, "UPDATE quota_allocations SET status=?,retained_bytes=?,retained_entries=?,receipt=?,free_after=? WHERE id=? AND status='FREE_PENDING'", status, match.inv.ChargedBytes, match.inv.Entries, remainingRaw, available, match.id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return c.Fail(c.StaleAuthority, "physical free intent changed during receipt")
		}
		removed = true
		return nil
	})
	return removed, err
}

func (l *Ledger) setFreePending(ctx context.Context, id string, before uint64) error {
	if before > math.MaxInt64 {
		return c.Fail(c.UnsupportedCapability, "free-space receipt exceeds bounded counter")
	}
	result, err := l.db.ExecContext(ctx, "UPDATE quota_allocations SET status='FREE_PENDING',free_before=? WHERE id=? AND status IN ('RETAINED','FREE_PENDING')", before, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return c.Fail(c.StaleAuthority, "physical allocation no longer admits a free intent")
	}
	return nil
}

func outstandingForVolumeTx(tx *sql.Tx, volume string) (uint64, error) {
	rows, err := tx.Query("SELECT upper_bytes FROM quota_allocations WHERE volume_identity=? AND status IN ('RESERVED','WRITING')", volume)
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
