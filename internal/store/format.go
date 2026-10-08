package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const formatTable = "CREATE TABLE format_manifest (singleton INTEGER PRIMARY KEY CHECK(singleton=1),digest TEXT NOT NULL,body BLOB NOT NULL)"

type FormatManifest struct {
	SchemaVersion  int              `json:"schema_version"`
	ReducerVersion int              `json:"reducer_version"`
	StoreID        string           `json:"store_id"`
	CatalogDigest  string           `json:"catalog_digest"`
	Migration      *MigrationIntent `json:"migration,omitempty"`
}

func randomStoreID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}
func validStoreID(id string) bool {
	raw, err := hex.DecodeString(id)
	return err == nil && len(raw) == 16 && hex.EncodeToString(raw) == id
}
func newFormat(intent *MigrationIntent) (FormatManifest, error) {
	id, err := randomStoreID()
	return FormatManifest{SchemaVersion: 2, ReducerVersion: c.ReducerVersion, StoreID: id, CatalogDigest: c.HashBytes([]byte(formatTable)), Migration: intent}, err
}
func insertFormat(ctx context.Context, tx *sql.Tx, format FormatManifest) error {
	raw, err := c.CanonicalV1(format)
	if err != nil {
		return err
	}
	digest, err := c.Digest(format)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO format_manifest VALUES(1,?,?)", digest, raw)
	return err
}
func (s *Store) formatLocked(ctx context.Context) (FormatManifest, string, error) {
	var result FormatManifest
	if s.db == nil {
		return result, "", c.Fail(c.StoreIntegrityError, "store closed")
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return result, "", err
	}
	if version != s.schema {
		return result, "", c.Fail(c.StoreIntegrityError, "store schema changed outside migration")
	}
	if s.schema == 1 {
		var count int
		err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE name='format_manifest'").Scan(&count)
		if err == nil && count != 0 {
			err = c.Fail(c.StoreIntegrityError, "legacy schema contains an uncommitted format table")
		}
		return result, "", err
	}
	if s.schema != 2 {
		return result, "", c.Fail(c.UnsupportedCapability, "unsupported store format")
	}
	var digest string
	var raw []byte
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM format_manifest").Scan(&count); err != nil || count != 1 {
		return result, "", c.Fail(c.StoreIntegrityError, "store format manifest missing or ambiguous")
	}
	if err := s.db.QueryRowContext(ctx, "SELECT digest,body FROM format_manifest WHERE singleton=1").Scan(&digest, &raw); err != nil {
		return result, "", err
	}
	if err := c.DecodeStrict(raw, &result); err != nil {
		return result, "", c.Fail(c.StoreIntegrityError, "corrupt store format manifest")
	}
	actual, err := c.Digest(result)
	if err != nil {
		return result, "", err
	}
	if result.SchemaVersion != 2 || result.ReducerVersion != c.ReducerVersion || !validStoreID(result.StoreID) || result.CatalogDigest != c.HashBytes([]byte(formatTable)) || digest != actual {
		return result, "", c.Fail(c.StoreIntegrityError, "store format/version/digest binding mismatch")
	}
	if result.Migration != nil {
		if err := result.Migration.Validate(); err != nil {
			return result, "", err
		}
	}
	return result, digest, nil
}
func (s *Store) writableLocked() error {
	if s.db == nil {
		return c.Fail(c.StoreIntegrityError, "store closed")
	}
	if s.readOnlyReason != "" {
		return c.Fail(c.UnsupportedCapability, s.readOnlyReason)
	}
	return nil
}
func (s *Store) Writable() error        { s.mu.Lock(); defer s.mu.Unlock(); return s.writableLocked() }
func (s *Store) ReadOnlyReason() string { s.mu.Lock(); defer s.mu.Unlock(); return s.readOnlyReason }
