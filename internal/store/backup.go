package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// SnapshotInfo binds logical journal contents, not mutable owner generation.
type SnapshotInfo struct {
	SchemaVersion  int    `json:"schema_version"`
	ReducerVersion int    `json:"reducer_version"`
	StoreSeq       int64  `json:"store_seq"`
	FormatDigest   string `json:"format_digest,omitempty"`
	TailHash       string `json:"tail_hash"`
}
type DocumentRef struct {
	TaskID string
	Digest string
}

func (s *Store) snapshotInfoLocked(ctx context.Context) (SnapshotInfo, error) {
	info := SnapshotInfo{SchemaVersion: s.schema, ReducerVersion: c.ReducerVersion}
	if s.db == nil {
		return info, os.ErrClosed
	}
	if err := s.verifyProjectionsLocked(ctx); err != nil {
		return info, err
	}
	_, formatDigest, err := s.formatLocked(ctx)
	if err != nil {
		return info, err
	}
	info.FormatDigest = formatDigest
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='store_seq'").Scan(&info.StoreSeq); err != nil {
		return info, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT event_hash FROM events ORDER BY store_seq DESC LIMIT 1").Scan(&info.TailHash)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return info, err
}
func (s *Store) SnapshotInfo(ctx context.Context) (SnapshotInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotInfoLocked(ctx)
}

// SnapshotDatabase uses SQLite's transactional snapshot; copying a live WAL
// database file is never a backup. The caller publishes its bundle manifest last.
func (s *Store) SnapshotDatabase(ctx context.Context, directory string) (SnapshotInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := s.snapshotInfoLocked(ctx)
	if err != nil {
		return info, err
	}
	if !fileguard.Disjoint(s.directory, directory) {
		return info, c.Fail(c.PolicyDenied, "backup and live store overlap")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return info, err
	}
	defer root.Close()
	if err = fileguard.Private(root); err != nil {
		return info, err
	}
	var pages, pageSize int64
	if err = s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return info, err
	}
	if err = s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return info, err
	}
	if pageSize < 1 || pages > (256<<20)/pageSize {
		return info, c.Fail(c.UnsupportedCapability, "database exceeds backup profile")
	}
	f, err := root.OpenFile("state.sqlite", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return info, err
	}
	if err = f.Close(); err != nil {
		return info, err
	}
	if _, err = s.db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(root.Name(), "state.sqlite")); err != nil {
		return info, err
	}
	f, err = root.OpenFile("state.sqlite", os.O_RDWR, 0)
	if err != nil {
		return info, err
	}
	err = f.Sync()
	err = errors.Join(err, f.Close())
	if err == nil {
		err = fileguard.SyncParents(root, ".")
	}
	return info, err
}

// DocumentReferences includes historical event pointers, so a backup also
// preserves raw intent and candidate evidence needed by historical replay.
func (s *Store) DocumentReferences(ctx context.Context) ([]DocumentRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, os.ErrClosed
	}
	rows, err := s.db.QueryContext(ctx, "SELECT task_id,body FROM payloads ORDER BY task_id,digest")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []DocumentRef{}
	seen := map[DocumentRef]bool{}
	for rows.Next() {
		var task string
		var raw []byte
		var p c.EventPayload
		if err = rows.Scan(&task, &raw); err != nil {
			return nil, err
		}
		if err = c.DecodeStrict(raw, &p); err != nil {
			return nil, err
		}
		if p.DocumentDigest != "" {
			ref := DocumentRef{task, p.DocumentDigest}
			if !seen[ref] {
				refs = append(refs, ref)
				seen[ref] = true
			}
		}
	}
	return refs, rows.Err()
}
