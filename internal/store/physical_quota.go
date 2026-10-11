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
	rows, err := s.db.QueryContext(ctx, "SELECT key,value FROM meta WHERE key LIKE ?", "physical_quota_v1:%")
	if err != nil {
		return nil, err
	}
	var key string
	count := 0
	for rows.Next() {
		var value int64
		if err = rows.Scan(&key, &value); err != nil {
			break
		}
		count++
		if count > 1 || value != 1 {
			err = c.Fail(c.StoreIntegrityError, "ambiguous physical quota owner binding")
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || count == 0 {
		return nil, err
	}
	digest := strings.TrimPrefix(key, "physical_quota_v1:")
	if !c.ValidDigest(digest) {
		return nil, c.Fail(c.StoreIntegrityError, "invalid physical quota owner marker")
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, "SELECT body FROM payloads WHERE task_id='' AND digest=?", digest).Scan(&raw); err != nil {
		return nil, err
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
	if err = s.disk.Admit(1<<20, true); err != nil {
		return err
	}
	raw, err := c.CanonicalV1(binding)
	if err != nil {
		return err
	}
	digest := c.HashBytes(raw)
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
		_ = s.disk.SetAdmissionHook(nil)
		return err
	}
	s.physical = ledger
	return nil
}
