package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// PrivacyCheckpointPin is the owner-store witness for one external privacy
// checkpoint. It is committed in the same SQLite metadata catalog as the
// write-once privacy authority so a restored/rolled-back external registry
// cannot become current merely by replacing files at the old path.
//
// PREPARED pins are written to every registered owner before publishing the
// external current anchor. ACTIVE pins acknowledge that anchor. A PREPARED pin
// is valid only while the registry's durable migration intent proves the same
// anchor transition.
type PrivacyCheckpointPin struct {
	SchemaVersion   int    `json:"schema_version"`
	Phase           string `json:"phase"`
	AuthorityDigest string `json:"authority_digest"`
	AnchorDigest    string `json:"anchor_digest"`
	PreviousAnchor  string `json:"previous_anchor_digest,omitempty"`
	Generation      int64  `json:"generation"`
	Sequence        int64  `json:"sequence"`
	Tail            string `json:"tail"`
	Watermark       int64  `json:"watermark"`
	InstanceID      string `json:"instance_id"`
	PhysicalRoot    string `json:"physical_root"`
	RestoreBinding  string `json:"restore_binding,omitempty"`
}

func (p PrivacyCheckpointPin) validate() error {
	validID := func(value string) bool {
		if len(value) != 32 {
			return false
		}
		for _, r := range value {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
		return true
	}
	if p.SchemaVersion != 1 || (p.Phase != "PREPARED" && p.Phase != "ACTIVE") ||
		!c.ValidDigest(p.AuthorityDigest) || !c.ValidDigest(p.AnchorDigest) ||
		p.Generation < 1 || p.Sequence < 0 || p.Watermark < 0 ||
		(p.Sequence == 0 && p.Tail != "") || (p.Sequence > 0 && !c.ValidDigest(p.Tail)) ||
		!validID(p.InstanceID) || !c.ValidDigest(p.PhysicalRoot) {
		return c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint pin")
	}
	if p.Generation == 1 {
		if p.PreviousAnchor != "" {
			return c.Fail(c.StoreIntegrityError, "genesis checkpoint pin has a previous anchor")
		}
	} else if !c.ValidDigest(p.PreviousAnchor) {
		return c.Fail(c.StoreIntegrityError, "checkpoint pin lost its previous anchor")
	}
	if p.RestoreBinding != "" && !c.ValidDigest(p.RestoreBinding) {
		return c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint restore binding")
	}
	return nil
}

func samePrivacyCheckpointPin(a, b *PrivacyCheckpointPin) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s *Store) privacyCheckpointPinLocked(ctx context.Context) (*PrivacyCheckpointPin, error) {
	if s.db == nil {
		return nil, os.ErrClosed
	}
	return privacyCheckpointPinFromDB(ctx, s.db)
}

func privacyCheckpointPinFromDB(ctx context.Context, db *sql.DB) (*PrivacyCheckpointPin, error) {
	rows, err := db.QueryContext(ctx, "SELECT key,value FROM meta WHERE substr(key,1,?)=?", len("privacy_checkpoint_v1:"), "privacy_checkpoint_v1:")
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
			err = c.Fail(c.StoreIntegrityError, "ambiguous privacy checkpoint pin")
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || count == 0 {
		return nil, err
	}
	digest := strings.TrimPrefix(key, "privacy_checkpoint_v1:")
	if !c.ValidDigest(digest) {
		return nil, c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint marker")
	}
	var raw []byte
	if err = db.QueryRowContext(ctx, "SELECT body FROM payloads WHERE task_id='' AND digest=?", digest).Scan(&raw); err != nil {
		return nil, err
	}
	var pin PrivacyCheckpointPin
	if len(raw) > 4096 || c.HashBytes(raw) != digest || c.DecodeStrict(raw, &pin) != nil || pin.validate() != nil {
		return nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint pin CAS mismatch")
	}
	canonical, canonicalErr := c.CanonicalV1(pin)
	if canonicalErr != nil || string(canonical) != string(raw) {
		return nil, c.Fail(c.StoreIntegrityError, "noncanonical privacy checkpoint pin")
	}
	return &pin, nil
}

// ReadPrivacyCheckpointOwnerAt reads only immutable authority/checkpoint
// metadata from a live owner database without acquiring its exclusive writer
// lease. SQLite WAL supplies a consistent read snapshot while another owner
// has the store open; this API performs no schema, generation, or projection
// writes.
func ReadPrivacyCheckpointOwnerAt(ctx context.Context, directory string) ([]byte, *PrivacyCheckpointPin, error) {
	root, err := filepath.Abs(directory)
	if err != nil {
		return nil, nil, err
	}
	privateRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer privateRoot.Close()
	if err = fileguard.Private(privateRoot); err != nil {
		return nil, nil, err
	}
	for _, name := range []string{"state.sqlite", "state.sqlite-wal", "state.sqlite-shm"} {
		if err = fileguard.RegularPath(privateRoot, name, true); err != nil {
			return nil, nil, err
		}
	}
	uriPath := filepath.ToSlash(filepath.Join(root, "state.sqlite"))
	if filepath.VolumeName(root) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	u.RawQuery = "mode=ro"
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return nil, nil, err
	}
	authority, err := privacyMetadataFromDB(ctx, db, "privacy_authority_v1:")
	if err != nil {
		return nil, nil, err
	}
	pin, err := privacyCheckpointPinFromDB(ctx, db)
	return authority, pin, err
}

// PrivacyCheckpointPin reads the owner-store witness. A missing pin is only
// valid for legacy authorities that have not published a checkpoint.
func (s *Store) PrivacyCheckpointPin(ctx context.Context) (*PrivacyCheckpointPin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.privacyCheckpointPinLocked(ctx)
}

// CompareAndSwapPrivacyCheckpointPin commits one exact checkpoint transition.
// The caller holds the external privacy-family lock and supplies the previously
// read pin; it cannot silently replace a concurrent owner-store revision.
func (s *Store) CompareAndSwapPrivacyCheckpointPin(ctx context.Context, expected, next *PrivacyCheckpointPin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	if next == nil || next.validate() != nil {
		return c.Fail(c.InvalidArgument, "valid next privacy checkpoint pin required")
	}
	current, err := s.privacyCheckpointPinLocked(ctx)
	if err != nil {
		return err
	}
	if !samePrivacyCheckpointPin(current, expected) {
		return c.Fail(c.StaleBase, "privacy checkpoint pin changed before CAS")
	}
	if current != nil {
		if current.AuthorityDigest != next.AuthorityDigest || current.InstanceID != next.InstanceID || current.PhysicalRoot != next.PhysicalRoot ||
			(next.Generation != current.Generation+1 && !(next.Generation == current.Generation && current.Phase == "PREPARED" && next.Phase == "ACTIVE" && next.AnchorDigest == current.AnchorDigest)) {
			return c.Fail(c.StaleAuthority, "privacy checkpoint pin transition is not contiguous")
		}
		if next.Generation == current.Generation+1 {
			if current.Phase != "ACTIVE" || next.PreviousAnchor != current.AnchorDigest || next.Phase != "PREPARED" || next.RestoreBinding != current.RestoreBinding {
				return c.Fail(c.StaleAuthority, "new checkpoint pin does not extend current anchor")
			}
		}
		if next.Generation == current.Generation {
			expected := *current
			expected.Phase = "ACTIVE"
			if current.Phase != "PREPARED" || *next != expected {
				return c.Fail(c.StaleAuthority, "checkpoint pin activation changed its prepared binding")
			}
		}
	} else if next.Generation != 1 || next.Phase != "PREPARED" || next.PreviousAnchor != "" {
		return c.Fail(c.StaleAuthority, "initial checkpoint pin is not a prepared genesis")
	}
	return s.writePrivacyCheckpointPinLocked(ctx, current, next)
}

// RebindRestoredPrivacyCheckpointPin attaches a validated restore stage to the
// current external anchor and its newly allocated physical runtime identity.
// The caller must hold the family lock and validate the exact registered
// backup/current watermark before invoking this CAS.
func (s *Store) RebindRestoredPrivacyCheckpointPin(ctx context.Context, expected, next *PrivacyCheckpointPin, restoreBinding string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	if next == nil || next.validate() != nil || next.Phase != "ACTIVE" || !c.ValidDigest(restoreBinding) || next.RestoreBinding != restoreBinding {
		return c.Fail(c.InvalidArgument, "validated restore checkpoint binding required")
	}
	current, err := s.privacyCheckpointPinLocked(ctx)
	if err != nil {
		return err
	}
	if !samePrivacyCheckpointPin(current, expected) {
		return c.Fail(c.StaleBase, "restored privacy checkpoint pin changed before rebind")
	}
	if current != nil {
		if current.AuthorityDigest != next.AuthorityDigest || current.Phase != "ACTIVE" ||
			next.Generation < current.Generation || next.Sequence < current.Sequence || next.Watermark < current.Watermark ||
			(next.Generation == current.Generation && next.AnchorDigest != current.AnchorDigest) ||
			next.RestoreBinding == current.RestoreBinding {
			return c.Fail(c.StaleAuthority, "restored checkpoint would regress or reuse an owner binding")
		}
		if next.InstanceID == current.InstanceID && next.PhysicalRoot == current.PhysicalRoot && next.Generation == current.Generation {
			return c.Fail(c.StaleAuthority, "restored owner rebind did not advance its checkpoint anchor")
		}
	}
	return s.writePrivacyCheckpointPinLocked(ctx, current, next)
}

func (s *Store) writePrivacyCheckpointPinLocked(ctx context.Context, current, next *PrivacyCheckpointPin) error {
	if err := s.disk.Admit(1<<20, true); err != nil {
		return err
	}
	raw, err := c.CanonicalV1(next)
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
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint payload digest collision")
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM meta WHERE key LIKE ?", "privacy_checkpoint_v1:%"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,1)", "privacy_checkpoint_v1:"+digest); err != nil {
		return err
	}
	if current != nil {
		oldRaw, _ := c.CanonicalV1(current)
		oldDigest := c.HashBytes(oldRaw)
		if oldDigest != digest {
			if _, err = tx.ExecContext(ctx, "DELETE FROM payloads WHERE task_id='' AND digest=?", oldDigest); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// PrivacyAuthority is a write-once bounded metadata CAS. Its digest is stored
// as a numeric marker in the existing metadata catalog, so deleting a sidecar
// cannot reset the authority and backups retain the pin transactionally.
func (s *Store) PrivacyAuthority(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.privacyAuthorityLocked(ctx)
}

func (s *Store) privacyAuthorityLocked(ctx context.Context) ([]byte, error) {
	return s.privacyMetadataLocked(ctx, "privacy_authority_v1:")
}

func (s *Store) privacyMetadataLocked(ctx context.Context, prefix string) ([]byte, error) {
	if s.db == nil {
		return nil, os.ErrClosed
	}
	return privacyMetadataFromDB(ctx, s.db, prefix)
}

func privacyMetadataFromDB(ctx context.Context, db *sql.DB, prefix string) ([]byte, error) {
	rows, err := db.QueryContext(ctx, "SELECT key,value FROM meta WHERE substr(key,1,?)=?", len(prefix), prefix)
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
	digest := strings.TrimPrefix(key, prefix)
	if !c.ValidDigest(digest) {
		return nil, c.Fail(c.StoreIntegrityError, "invalid privacy authority marker")
	}
	var raw []byte
	if err = db.QueryRowContext(ctx, "SELECT body FROM payloads WHERE task_id='' AND digest=?", digest).Scan(&raw); err != nil {
		return nil, err
	}
	if len(raw) > 4096 || c.HashBytes(raw) != digest {
		return nil, c.Fail(c.StoreIntegrityError, "privacy authority CAS mismatch")
	}
	return raw, nil
}

// PendingPrivacyAuthority pins an unbound allocation before any physical
// reserve or genesis bytes are written. It is not a current deletion authority.
func (s *Store) PendingPrivacyAuthority(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.privacyMetadataLocked(ctx, "privacy_allocation_v1:")
}

func (s *Store) ReservePrivacyAuthority(ctx context.Context, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 4096 {
		return c.Fail(c.InvalidArgument, "bounded privacy allocation required")
	}
	active, err := s.privacyAuthorityLocked(ctx)
	if err != nil {
		return err
	}
	if active != nil {
		return c.Fail(c.Conflict, "current authority cannot allocate another root")
	}
	pending, err := s.privacyMetadataLocked(ctx, "privacy_allocation_v1:")
	if err != nil {
		return err
	}
	if pending != nil {
		if string(pending) != string(raw) {
			return c.Fail(c.Conflict, "pending privacy allocation cannot be replaced")
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
	if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,1)", "privacy_allocation_v1:"+digest); err != nil {
		return err
	}
	return tx.Commit()
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
	pending, err := s.privacyMetadataLocked(ctx, "privacy_allocation_v1:")
	if err != nil {
		return err
	}
	if pending != nil && string(pending) != string(raw) {
		return c.Fail(c.Conflict, "authority differs from durable physical allocation")
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
	if pending == nil {
		if _, err = tx.ExecContext(ctx, "INSERT INTO payloads(task_id,digest,body) VALUES('',?,?)", digest, raw); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,1)", "privacy_authority_v1:"+digest); err != nil {
		return err
	}
	if pending != nil {
		if _, err = tx.ExecContext(ctx, "DELETE FROM meta WHERE key=?", "privacy_allocation_v1:"+digest); err != nil {
			return err
		}
	}
	return tx.Commit()
}
