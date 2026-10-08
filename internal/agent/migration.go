package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

// validateMigrationBackup exercises fresh restore, journal replay and historical
// CAS/input closure. Its private temporary copy never runs a task or effect.
func validateMigrationBackup(ctx context.Context, backup string, expected BackupManifest) (resultErr error) {
	directory, err := os.MkdirTemp("", "viber-migration-")
	if err != nil {
		return err
	}
	parent, err := os.OpenRoot(filepath.Dir(directory))
	if err != nil {
		return errors.Join(err, os.Remove(directory))
	}
	defer parent.Close()
	defer func() { resultErr = errors.Join(resultErr, parent.RemoveAll(filepath.Base(directory))) }()
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	err = fileguard.Private(root)
	err = errors.Join(err, root.Close())
	if err != nil {
		return err
	}
	restored, err := RestoreBackup(ctx, backup, filepath.Join(directory, "validated-store"))
	if err != nil {
		return err
	}
	a, err := c.Digest(expected)
	if err != nil {
		return err
	}
	b, err := c.Digest(restored)
	if err != nil {
		return err
	}
	if a != b {
		return c.Fail(c.StoreIntegrityError, "migration backup changed during restore validation")
	}
	return nil
}

func migrationBackupManifest(backup string) (BackupManifest, error) {
	var result BackupManifest
	root, err := os.OpenRoot(backup)
	if err != nil {
		return result, err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, "backup.json", 8<<20)
	if err != nil {
		return result, err
	}
	if err = c.DecodeStrict(raw, &result); err != nil {
		return result, err
	}
	return result, validateBackupManifest(result)
}

func (s *Session) migrationQuiescent(ctx context.Context) error {
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return err
	}
	for _, state := range states {
		_, doc, err := s.Load(ctx, state.TaskID)
		if err != nil {
			return err
		}
		if state.Execution != c.Terminated {
			return c.Fail(c.PolicyDenied, "finish or cancel all active tasks before migration")
		}
		if doc.Pending != nil || doc.UnknownEffect || doc.Budget.ReservedInput != 0 || doc.Budget.ReservedOutput != 0 {
			return c.Fail(c.PolicyDenied, "unknown effects or unresolved reservations must be reconciled before migration")
		}
	}
	return nil
}

// Migrate requires an exclusive owner. It creates or reuses a matching v1
// backup and validates actual restore before publishing the durable intent.
// Retries bind the original command ID, path, manifest and source snapshot.
func (s *Session) Migrate(ctx context.Context, id, backupOutput string) (store.MigrationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result store.MigrationReceipt
	if !utf8.ValidString(id) || !utf8.ValidString(backupOutput) || id == "" || len(id) > 128 || strings.ContainsAny(id, "\x00\r\n") || backupOutput == "" {
		return result, c.Fail(c.InvalidArgument, "bounded immutable migration ID and backup path required")
	}
	if _, _, _, found, err := s.Journal.CommandReceipt(ctx, id); err != nil {
		return result, err
	} else if found {
		return result, c.Fail(c.CommandIDConflict, "migration ID already belongs to a journal command")
	}
	backup, err := fileguard.ResolveProspective(backupOutput)
	if err != nil {
		return result, err
	}
	pending, complete, err := s.Journal.MigrationStatus(ctx)
	if err != nil {
		return result, err
	}
	if pending != nil && (pending.ID != id || pending.BackupPath != backup) {
		return result, c.Fail(c.CommandIDConflict, "migration retry requires the original command ID and backup path")
	}
	if pending != nil && complete != nil {
		return s.Journal.Migrate(ctx, *pending)
	}
	if err = s.migrationQuiescent(ctx); err != nil {
		return result, err
	}
	source, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return result, err
	}
	if pending == nil && source.SchemaVersion != 1 {
		return result, c.Fail(c.UnsupportedCapability, "store is current; no schema migration needed")
	}
	roots, err := s.backupClosure(ctx)
	if err != nil {
		return result, err
	}
	if err = disjointRoots(backup, append(roots, s.directory)); err != nil {
		return result, err
	}
	var manifest BackupManifest
	if _, err = os.Lstat(backup); errors.Is(err, os.ErrNotExist) {
		if pending != nil {
			return result, c.Fail(c.StoreIntegrityError, "migration recovery backup is missing")
		}
		manifest, err = s.backupLocked(ctx, backup)
	} else if err == nil {
		manifest, err = migrationBackupManifest(backup)
	}
	if err != nil {
		return result, err
	}
	expected := source
	if pending != nil {
		expected = pending.Source
	}
	if manifest.Store != expected || manifest.Store.SchemaVersion != 1 {
		return result, c.Fail(c.StaleBase, "migration backup journal does not match its source")
	}
	digest, err := c.Digest(manifest)
	if err != nil {
		return result, err
	}
	intent := store.MigrationIntent{SchemaVersion: 1, ID: id, FromVersion: 1, ToVersion: 2, BackupPath: backup, BackupDigest: digest, Source: manifest.Store}
	if err = intent.Validate(); err != nil {
		return result, err
	}
	if pending != nil {
		a, _ := c.Digest(intent)
		b, _ := c.Digest(*pending)
		if a != b {
			return result, c.Fail(c.StoreIntegrityError, "migration recovery backup differs from durable intent")
		}
	}
	if err = validateMigrationBackup(ctx, backup, manifest); err != nil {
		return result, err
	}
	return s.Journal.Migrate(ctx, intent)
}
