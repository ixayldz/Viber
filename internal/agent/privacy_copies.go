package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"os"
	"path/filepath"
)

func (s *Session) registerManagedCopy(ctx context.Context, copy ManagedCopy) error {
	if err := validateManagedCopy(copy); err != nil {
		return err
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return err
	}
	for _, task := range copy.Tasks {
		if _, deleted := view.Deletions[task]; deleted {
			return c.Fail(c.PolicyDenied, "deleted content cannot be published in a managed copy")
		}
	}
	for _, existing := range view.Copies {
		if existing.Directory == copy.Directory {
			if len(existing.Files) != 0 || existing.Kind != copy.Kind || existing.PhysicalRoot != copy.PhysicalRoot || !sameManagedTasks(existing.Tasks, copy.Tasks) {
				return c.Fail(c.Conflict, "managed output path cannot be registered twice")
			}
		}
	}
	return appendPrivacy(root, view, privacyRecord{Type: "MANAGED_COPY", Copy: &copy})
}

func checkBackupAuthority(ctx context.Context, manifest BackupManifest, raw []byte, backup string) error {
	root, err := openPrivacyRoot(*manifest.PrivacyAuthority)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return err
	}
	if view.Watermark != manifest.DeletionWatermark {
		return c.Fail(c.PolicyDenied, "backup predates current deletion watermark; old content cannot be restored")
	}
	backupRoot, err := os.OpenRoot(backup)
	if err != nil {
		return err
	}
	defer backupRoot.Close()
	physical, err := fileguard.DirectoryIdentity(backupRoot)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(backup)
	if err != nil {
		return err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return err
	}
	for _, copy := range view.Copies {
		if copy.Kind != "BACKUP" || copy.Directory != canonical || copy.PhysicalRoot != physical {
			continue
		}
		for _, f := range copy.Files {
			if f.Path == "backup.json" && f.Digest == c.HashBytes(raw) && f.Size == int64(len(raw)) {
				return nil
			}
		}
	}
	return c.Fail(c.PolicyDenied, "backup is not registered with the current deletion authority")
}
