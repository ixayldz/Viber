package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// PrivacyAuthority is a write-once bounded metadata CAS. Its digest is stored
// as a numeric marker in the existing metadata catalog, so deleting a sidecar
// cannot reset the authority and backups retain the pin transactionally.
func (s *Store) PrivacyAuthority(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.privacyAuthorityLocked(ctx)
}

func (s *Store) privacyAuthorityLocked(ctx context.Context) ([]byte, error) {
	if s.db == nil {
		return nil, os.ErrClosed
	}
	rows, err := s.db.QueryContext(ctx, "SELECT key,value FROM meta WHERE key LIKE 'privacy_authority_v1:%'")
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
			err = c.Fail(c.StoreIntegrityError, "ambiguous privacy authority")
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || count == 0 {
		return nil, err
	}
	digest := strings.TrimPrefix(key, "privacy_authority_v1:")
	if !c.ValidDigest(digest) {
		return nil, c.Fail(c.StoreIntegrityError, "invalid privacy authority marker")
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, "SELECT body FROM payloads WHERE task_id='' AND digest=?", digest).Scan(&raw); err != nil {
		return nil, err
	}
	if len(raw) > 4096 || c.HashBytes(raw) != digest {
		return nil, c.Fail(c.StoreIntegrityError, "privacy authority CAS mismatch")
	}
	return raw, nil
}

func (s *Store) BindPrivacyAuthority(ctx context.Context, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 4096 {
		return c.Fail(c.InvalidArgument, "bounded privacy authority required")
	}
	existing, err := s.privacyAuthorityLocked(ctx)
	if err != nil {
		return err
	}
	if existing != nil {
		if string(existing) != string(raw) {
			return c.Fail(c.Conflict, "privacy authority cannot be replaced")
		}
		return nil
	}
	if err = s.disk.Admit(1<<20, true); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	digest := c.HashBytes(raw)
	if _, err = tx.ExecContext(ctx, "INSERT INTO payloads(task_id,digest,body) VALUES('',?,?)", digest, raw); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,1)", "privacy_authority_v1:"+digest); err != nil {
		return err
	}
	return tx.Commit()
}
