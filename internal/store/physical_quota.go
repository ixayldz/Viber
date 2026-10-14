package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/resource"
)

// PhysicalQuotaBinding is a write-once owner-store witness for the authority
// shared physical ledger. The external anchor protects the database inode;
// this CAS prevents a restored or rolled-back owner from silently adopting a
// replacement ledger at the same authority path.
type PhysicalQuotaBinding struct {
	SchemaVersion int    `json:"schema_version"`
	AuthorityID   string `json:"authority_id"`
	Directory     string `json:"directory"`
	PhysicalRoot  string `json:"physical_root"`
	LedgerID      string `json:"ledger_id"`
}

type physicalQuotaAuthority struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Directory     string `json:"directory"`
	PhysicalRoot  string `json:"physical_root"`
}

func (p PhysicalQuotaBinding) validate() error {
	if p.SchemaVersion != 1 || !validPhysicalQuotaID(p.AuthorityID) || !validPhysicalQuotaID(p.LedgerID) || !c.ValidDigest(p.PhysicalRoot) || !filepath.IsAbs(p.Directory) || len(p.Directory) > 2048 || filepath.Clean(p.Directory) != p.Directory {
		return c.Fail(c.InvalidArgument, "invalid physical quota owner binding")
	}
	return nil
}

func validPhysicalQuotaID(id string) bool {
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

func (s *Store) physicalQuotaBindingLocked(ctx context.Context) (*PhysicalQuotaBinding, error) {
	if s.db == nil {
		return nil, os.ErrClosed
	}
	return physicalQuotaBindingFromDB(ctx, s.db)
}

// physicalQuotaBindingFromDB is shared by the live owner and the read-only
// backup inspector. Marker and CAS validation must stay identical across both
// paths so a backup cannot be accepted under a looser contract.
func physicalQuotaBindingFromDB(ctx context.Context, db *sql.DB) (*PhysicalQuotaBinding, error) {
	if db == nil {
		return nil, os.ErrClosed
	}
	return physicalQuotaBindingFromSnapshot(ctx, db)
}

func physicalQuotaBindingFromConn(ctx context.Context, conn *sql.Conn) (*PhysicalQuotaBinding, error) {
	if conn == nil {
		return nil, os.ErrClosed
	}
	return physicalQuotaBindingFromSnapshot(ctx, conn)
}

type physicalQuotaReadOnlyBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func physicalQuotaBindingFromSnapshot(ctx context.Context, beginner physicalQuotaReadOnlyBeginner) (*PhysicalQuotaBinding, error) {
	tx, err := beginner.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	binding, err := physicalQuotaBindingFromQuery(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return binding, nil
}

type physicalQuotaBindingQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func physicalQuotaBindingFromQuery(ctx context.Context, queryer physicalQuotaBindingQueryer) (*PhysicalQuotaBinding, error) {
	const markerPrefix = "physical_quota_v1:"
	rows, err := queryer.QueryContext(ctx, `SELECT substr(CAST(key AS TEXT),?,64),length(CAST(key AS BLOB)),typeof(key),typeof(value),
		CASE WHEN typeof(value)='integer' THEN value ELSE -1 END
		FROM meta WHERE substr(CAST(key AS TEXT),1,?)=? LIMIT 2`, len(markerPrefix)+1, len(markerPrefix), markerPrefix)
	if err != nil {
		return nil, err
	}
	var digest string
	count := 0
	for rows.Next() {
		var keyBytes int64
		var keyType, valueType string
		var value int64
		if err = rows.Scan(&digest, &keyBytes, &keyType, &valueType, &value); err != nil {
			break
		}
		count++
		if count > 1 || keyType != "text" || keyBytes != int64(len(markerPrefix)+64) || valueType != "integer" || value != 1 || !c.ValidDigest(digest) {
			err = c.Fail(c.StoreIntegrityError, "ambiguous physical quota owner binding")
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || count == 0 {
		return nil, err
	}
	var bodyType string
	var bodyBytes int64
	var raw []byte
	if err = queryer.QueryRowContext(ctx, `SELECT typeof(body),length(CAST(body AS BLOB)),substr(CAST(body AS BLOB),1,4097)
		FROM payloads WHERE task_id='' AND digest=? LIMIT 1`, digest).Scan(&bodyType, &bodyBytes, &raw); err != nil {
		return nil, err
	}
	if bodyType != "blob" || bodyBytes <= 0 || bodyBytes > 4096 || int64(len(raw)) != bodyBytes {
		return nil, c.Fail(c.StoreIntegrityError, "physical quota owner CAS exceeds its bound")
	}
	var binding PhysicalQuotaBinding
	if len(raw) > 4096 || c.HashBytes(raw) != digest || c.DecodeStrict(raw, &binding) != nil || binding.validate() != nil {
		return nil, c.Fail(c.StoreIntegrityError, "physical quota owner CAS mismatch")
	}
	canonical, err := c.CanonicalV1(binding)
	if err != nil || string(canonical) != string(raw) {
		return nil, c.Fail(c.StoreIntegrityError, "noncanonical physical quota owner binding")
	}
	return &binding, nil
}

func (s *Store) PhysicalQuotaBinding(ctx context.Context) (*PhysicalQuotaBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.physicalQuotaBindingLocked(ctx)
}

// BindPhysicalQuota pins the current authority and ledger exactly once. An
// identical binding is idempotent; an alternate ID or root is never adopted.
func (s *Store) BindPhysicalQuota(ctx context.Context, binding PhysicalQuotaBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := binding.validate(); err != nil {
		return err
	}
	rawAuthority, err := s.privacyAuthorityLocked(ctx)
	if err != nil {
		return err
	}
	if rawAuthority == nil {
		return c.Fail(c.StaleAuthority, "current privacy authority is not bound")
	}
	var authority physicalQuotaAuthority
	if c.DecodeStrict(rawAuthority, &authority) != nil || authority.SchemaVersion != 1 || authority.ID != binding.AuthorityID || authority.Directory != binding.Directory || authority.PhysicalRoot != binding.PhysicalRoot {
		return c.Fail(c.StaleAuthority, "physical quota binding differs from current privacy authority")
	}
	existing, err := s.physicalQuotaBindingLocked(ctx)
	if err != nil {
		return err
	}
	if existing != nil {
		if *existing != binding {
			return c.Fail(c.StaleAuthority, "physical quota binding cannot be replaced")
		}
		return nil
	}
	if err := s.writableLocked(); err != nil {
		return err
	}
	raw, err := c.CanonicalV1(binding)
	if err != nil {
		return err
	}
	digest := c.HashBytes(raw)
	requestDigest := c.HashBytes([]byte("store-bind-physical-quota-sqlite:" + digest))
	if err = s.admitSQLiteMutation(ctx, 1<<20, true, requestDigest, uint64(len(raw)+64<<10)); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO payloads(task_id,digest,body) VALUES('',?,?) ON CONFLICT(task_id,digest) DO NOTHING", digest, raw); err != nil {
		return err
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM payloads WHERE task_id='' AND digest=?", digest).Scan(&stored); err != nil || string(stored) != string(raw) {
		return c.Fail(c.StoreIntegrityError, "physical quota binding payload collision")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,1)", "physical_quota_v1:"+digest); err != nil {
		return err
	}
	return tx.Commit()
}

// AttachPhysicalQuota makes the owner store use the broker already pinned by
// its metadata CAS. Replacing an attached ledger is forbidden.
func (s *Store) AttachPhysicalQuota(ledger *resource.Ledger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return os.ErrClosed
	}
	if ledger == nil || s.physical != nil {
		return c.Fail(c.InvalidArgument, "one shared physical quota ledger required")
	}
	binding, err := s.physicalQuotaBindingLocked(context.Background())
	if err != nil {
		return err
	}
	if binding == nil || binding.AuthorityID != ledger.Authority.ID || binding.Directory != ledger.Authority.Directory || binding.PhysicalRoot != ledger.Authority.PhysicalRoot || binding.LedgerID != ledger.LedgerID {
		return c.Fail(c.StaleAuthority, "attached physical quota ledger differs from owner-store pin")
	}
	if err = s.attachMutableSQLite(ledger); err != nil {
		s.mutable = nil
		s.controlReserve = nil
		_ = s.disk.SetAdmissionHook(nil)
		_ = s.disk.SetReserveReconcileHook(nil)
		return err
	}
	s.physical = ledger
	return nil
}
