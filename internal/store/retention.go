package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const retentionCursorPrefix = "retention_cursor_v1:"

func (s *Store) retentionCursorLocked(ctx context.Context) (*c.RetentionCursor, []byte, error) {
	raw, err := s.privacyMetadataLocked(ctx, retentionCursorPrefix)
	if err != nil || raw == nil {
		return nil, raw, err
	}
	var cursor c.RetentionCursor
	if len(raw) > 1024 || c.DecodeStrict(raw, &cursor) != nil || cursor.Validate() != nil {
		return nil, nil, c.Fail(c.StoreIntegrityError, "retention scheduling metadata is invalid")
	}
	canonical, err := c.CanonicalV1(cursor)
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, nil, c.Fail(c.StoreIntegrityError, "retention scheduling metadata is noncanonical")
	}
	return &cursor, raw, nil
}

func (s *Store) RetentionCursor(ctx context.Context) (*c.RetentionCursor, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retentionCursorLocked(ctx)
}

// One replaceable metadata CAS, not an append-only task event. Removing its old
// task=” payload cannot erase task content or the write-once privacy authority.
// Neither a cursor nor its loss grants deletion authority.
func (s *Store) SetRetentionCursor(ctx context.Context, expected []byte, next c.RetentionCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	if next.Validate() != nil || len(expected) > 1024 {
		return c.Fail(c.InvalidArgument, "bounded retention scheduling CAS required")
	}
	_, prior, err := s.retentionCursorLocked(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(prior, expected) {
		return c.Fail(c.StaleRequest, "retention scheduling cursor changed")
	}
	authority, err := s.privacyAuthorityLocked(ctx)
	if err != nil {
		return err
	}
	authorityDigest, digestErr := c.Digest(json.RawMessage(authority))
	if authority == nil || digestErr != nil || authorityDigest != next.AuthorityDigest {
		return c.Fail(c.StaleAuthority, "retention cursor requires the current pinned authority")
	}
	raw, err := c.CanonicalV1(next)
	if err != nil {
		return err
	}
	if bytes.Equal(raw, prior) {
		return nil
	}
	// Optional scheduling must not consume emergency deletion/control reserve.
	if err = s.disk.Admit(int64(len(raw))*2+64<<10, false); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	digest := c.HashBytes(raw)
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO payloads(task_id,digest,body) VALUES('',?,?)", digest, raw); err != nil {
		return err
	}
	var actual []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM payloads WHERE task_id='' AND digest=?", digest).Scan(&actual); err != nil || !bytes.Equal(actual, raw) {
		return errors.Join(err, c.Fail(c.StoreIntegrityError, "retention cursor CAS collision"))
	}
	if prior != nil {
		old := c.HashBytes(prior)
		result, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key=? AND value=1", retentionCursorPrefix+old)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return errors.Join(err, c.Fail(c.StaleRequest, "retention cursor lost its current marker"))
		}
		// Protect any other metadata reference, even in a corrupted store.
		if _, err = tx.ExecContext(ctx, "DELETE FROM payloads WHERE task_id='' AND digest=? AND NOT EXISTS (SELECT 1 FROM meta WHERE key LIKE ?)", old, "%:"+old); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,1)", retentionCursorPrefix+digest); err != nil {
		return err
	}
	return tx.Commit()
}
