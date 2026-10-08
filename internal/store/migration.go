package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const MigrationIntentFile = ".migrations/v1-v2-intent.json"
const MigrationCompleteFile = ".migrations/v1-v2-complete.json"
const migrationRecovery = "store migration recovery required; writes disabled; use store-migrate with the original command ID and backup"

type MigrationIntent struct {
	SchemaVersion int          `json:"schema_version"`
	ID            string       `json:"command_id"`
	FromVersion   int          `json:"from_version"`
	ToVersion     int          `json:"to_version"`
	BackupPath    string       `json:"backup_path"`
	BackupDigest  string       `json:"backup_manifest_digest"`
	Source        SnapshotInfo `json:"source_snapshot"`
}

func (i MigrationIntent) Validate() error {
	if i.SchemaVersion != 1 || i.FromVersion != 1 || i.ToVersion != 2 || !utf8.ValidString(i.ID) || !utf8.ValidString(i.BackupPath) || i.ID == "" || len(i.ID) > 128 || strings.ContainsAny(i.ID, "\x00\r\n") || !filepath.IsAbs(i.BackupPath) || !c.ValidDigest(i.BackupDigest) || i.Source.SchemaVersion != 1 || i.Source.FormatDigest != "" || i.Source.ReducerVersion != c.ReducerVersion || i.Source.StoreSeq < 0 || i.Source.StoreSeq == 0 && i.Source.TailHash != "" || i.Source.StoreSeq > 0 && !c.ValidDigest(i.Source.TailHash) {
		return c.Fail(c.InvalidArgument, "invalid immutable migration request")
	}
	return nil
}

type MigrationReceipt struct {
	SchemaVersion int             `json:"schema_version"`
	Intent        MigrationIntent `json:"intent"`
	Target        SnapshotInfo    `json:"target_snapshot"`
	Status        string          `json:"status"`
}

func migrationRoot(directory string) (*os.Root, error) { return os.OpenRoot(directory) }
func readMigration(root *os.Root) (*MigrationIntent, *MigrationReceipt, error) {
	raw, err := fileguard.ReadRegular(root, MigrationIntentFile, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		if _, other := root.Lstat(MigrationCompleteFile); !errors.Is(other, os.ErrNotExist) {
			return nil, nil, c.Fail(c.StoreIntegrityError, "migration marker lacks intent")
		}
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var intent MigrationIntent
	if err = c.DecodeStrict(raw, &intent); err != nil {
		return nil, nil, c.Fail(c.StoreIntegrityError, "corrupt migration intent")
	}
	if err = intent.Validate(); err != nil {
		return nil, nil, err
	}
	raw, err = fileguard.ReadRegular(root, MigrationCompleteFile, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return &intent, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var receipt MigrationReceipt
	if err = c.DecodeStrict(raw, &receipt); err != nil {
		return nil, nil, c.Fail(c.StoreIntegrityError, "corrupt migration completion")
	}
	a, _ := c.Digest(intent)
	b, _ := c.Digest(receipt.Intent)
	if receipt.SchemaVersion != 1 || receipt.Status != "COMMITTED" || a != b || receipt.Target.SchemaVersion != 2 || receipt.Target.ReducerVersion != c.ReducerVersion || !c.ValidDigest(receipt.Target.FormatDigest) || receipt.Target.StoreSeq != intent.Source.StoreSeq || receipt.Target.TailHash != intent.Source.TailHash {
		return nil, nil, c.Fail(c.StoreIntegrityError, "migration completion binding mismatch")
	}
	return &intent, &receipt, nil
}
func (s *Store) checkMigrationLocked(ctx context.Context) error {
	root, err := migrationRoot(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	intent, receipt, err := readMigration(root)
	if err != nil {
		return err
	}
	if intent == nil {
		if s.schema == 2 {
			format, _, err := s.formatLocked(ctx)
			if err != nil {
				return err
			}
			if format.Migration != nil {
				return c.Fail(c.StoreIntegrityError, "committed migration sidecars missing")
			}
		}
		return nil
	}
	if !fileguard.Disjoint(s.directory, intent.BackupPath) {
		return c.Fail(c.PolicyDenied, "migration backup overlaps store")
	}
	if s.schema == 2 {
		format, digest, err := s.formatLocked(ctx)
		if err != nil {
			return err
		}
		if format.Migration == nil {
			return c.Fail(c.StoreIntegrityError, "schema migration has no committed lineage")
		}
		a, _ := c.Digest(intent)
		b, _ := c.Digest(format.Migration)
		if a != b || receipt != nil && receipt.Target.FormatDigest != digest {
			return c.Fail(c.StoreIntegrityError, "database/sidecar migration mismatch")
		}
	} else if s.schema != 1 || receipt != nil {
		return c.Fail(c.StoreIntegrityError, "migration completion disagrees with store version")
	}
	if receipt == nil {
		current, err := s.snapshotInfoLocked(ctx)
		if err != nil {
			return err
		}
		if current.StoreSeq != intent.Source.StoreSeq || current.TailHash != intent.Source.TailHash {
			return c.Fail(c.StoreIntegrityError, "journal changed during migration recovery")
		}
		s.readOnlyReason = migrationRecovery
	}
	return nil
}
func (s *Store) migrationStage(stage string) error {
	if s.migrationFault != nil {
		return s.migrationFault(stage)
	}
	return nil
}

// Migrate consumes a complete, independently validated application backup. The
// caller must prove raw input/artifact closure and reject unresolved effects.
// No automatic migration happens in Open; a pending transition is read-only.
func (s *Store) Migrate(ctx context.Context, intent MigrationIntent) (MigrationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt := MigrationReceipt{}
	if s.db == nil {
		return receipt, os.ErrClosed
	}
	if err := intent.Validate(); err != nil {
		return receipt, err
	}
	if !fileguard.Disjoint(s.directory, intent.BackupPath) {
		return receipt, c.Fail(c.PolicyDenied, "migration backup overlaps live store")
	}
	if err := s.verifyProjectionsLocked(ctx); err != nil {
		return receipt, err
	}
	var commandCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM commands WHERE command_id=?", intent.ID).Scan(&commandCount); err != nil {
		return receipt, err
	}
	if commandCount != 0 {
		return receipt, c.Fail(c.CommandIDConflict, "migration ID already belongs to a journal command")
	}
	root, err := migrationRoot(s.directory)
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	pending, complete, err := readMigration(root)
	if err != nil {
		return receipt, err
	}
	if pending != nil {
		a, _ := c.Digest(intent)
		b, _ := c.Digest(*pending)
		if a != b {
			return receipt, c.Fail(c.CommandIDConflict, "migration retry must use its original immutable request")
		}
		if complete != nil {
			if err = s.checkMigrationLocked(ctx); err != nil {
				return receipt, err
			}
			s.readOnlyReason = ""
			return *complete, nil
		}
	} else if s.schema != 1 {
		return receipt, c.Fail(c.UnsupportedCapability, "store is already current; only v1 to v2 migration is supported")
	}
	current, err := s.snapshotInfoLocked(ctx)
	if err != nil {
		return receipt, err
	}
	if s.schema == 2 {
		format, digest, err := s.formatLocked(ctx)
		if err != nil {
			return receipt, err
		}
		if format.Migration == nil {
			return receipt, c.Fail(c.StoreIntegrityError, "committed migration lineage missing")
		}
		a, _ := c.Digest(intent)
		b, _ := c.Digest(*format.Migration)
		if a != b || current.StoreSeq != intent.Source.StoreSeq || current.TailHash != intent.Source.TailHash || current.FormatDigest != digest {
			return receipt, c.Fail(c.StoreIntegrityError, "pending committed migration binding changed")
		}
	} else {
		if current != intent.Source {
			return receipt, c.Fail(c.StaleBase, "migration backup no longer matches store journal")
		}
		states, err := s.replayLocked(ctx, "")
		if err != nil {
			return receipt, err
		}
		for _, state := range states {
			if state.Execution != c.Terminated {
				return receipt, c.Fail(c.PolicyDenied, "finish or cancel active tasks before schema migration")
			}
		}
		var pages, pageSize int64
		if err = s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
			return receipt, err
		}
		if err = s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
			return receipt, err
		}
		if pages < 0 || pageSize <= 0 || pages > (256<<20)/pageSize {
			return receipt, c.Fail(c.UnsupportedCapability, "store exceeds migration profile")
		}
		available, err := fileguard.AvailableBytes(root)
		if err != nil {
			return receipt, err
		}
		if s.migrationSpace != nil {
			available = s.migrationSpace()
		}
		required := uint64(pages*pageSize)*3 + (32 << 20)
		if available < required {
			return receipt, c.Fail(c.UnsupportedCapability, "insufficient migration space; work stopped before schema writes")
		}
		if pending == nil {
			raw, err := c.CanonicalV1(intent)
			if err != nil {
				return receipt, err
			}
			// Publication may succeed before directory sync reports an error.
			// Stop admission before publishing, including that ambiguous outcome.
			s.readOnlyReason = migrationRecovery
			if err = fileguard.Publish(root, MigrationIntentFile, raw); err != nil {
				return receipt, err
			}
		}
		if err = s.migrationStage("after-intent"); err != nil {
			return receipt, err
		}
		format, err := newFormat(&intent)
		if err != nil {
			return receipt, err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return receipt, err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, formatTable); err != nil {
			return receipt, err
		}
		if err = insertFormat(ctx, tx, format); err != nil {
			return receipt, err
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
			return receipt, err
		}
		if err = s.migrationStage("before-commit"); err != nil {
			return receipt, err
		}
		if err = tx.Commit(); err != nil {
			// A COMMIT error can be ambiguous. Keep admission stopped and reconcile
			// actual user_version on retry rather than attempting the DDL twice.
			reconcile, reconcileCancel := context.WithTimeout(context.Background(), 5*time.Second)
			var actual int
			if checkErr := s.db.QueryRowContext(reconcile, "PRAGMA user_version").Scan(&actual); checkErr == nil && (actual == 1 || actual == 2) {
				s.schema = actual
			}
			reconcileCancel()
			return receipt, err
		}
		s.schema = 2
		if err = s.migrationStage("after-commit"); err != nil {
			return receipt, err
		}
		current, err = s.snapshotInfoLocked(ctx)
		if err != nil {
			return receipt, err
		}
	}
	receipt = MigrationReceipt{SchemaVersion: 1, Intent: intent, Target: current, Status: "COMMITTED"}
	if err = s.migrationStage("before-marker"); err != nil {
		return receipt, err
	}
	raw, err := c.CanonicalV1(receipt)
	if err != nil {
		return receipt, err
	}
	if err = fileguard.Publish(root, MigrationCompleteFile, raw); err != nil {
		return receipt, err
	}
	s.readOnlyReason = ""
	return receipt, nil
}

func (s *Store) MigrationStatus(ctx context.Context) (*MigrationIntent, *MigrationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, nil, os.ErrClosed
	}
	root, err := migrationRoot(s.directory)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	return readMigration(root)
}
