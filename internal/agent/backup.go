package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

const backupMaxBytes int64 = 512 << 20
const backupMaxFiles = 32768

type BackupFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type BackupManifest struct {
	SchemaVersion     int                `json:"schema_version"`
	Store             store.SnapshotInfo `json:"store"`
	DeletionPolicy    string             `json:"deletion_policy"`
	DeletionWatermark int64              `json:"deletion_watermark"`
	Files             []BackupFile       `json:"files"`
}

func backupFileLimit(name string) int64 {
	if name == "metadata/migrations/v1-v2-intent.json" || name == "metadata/migrations/v1-v2-complete.json" {
		return 1 << 20
	}
	if name == "database/state.sqlite" {
		return 256 << 20
	}
	return 64 << 20
}
func validateBackupManifest(m BackupManifest) error {
	validFormat := m.Store.SchemaVersion == 1 && m.Store.FormatDigest == "" || m.Store.SchemaVersion == 2 && c.ValidDigest(m.Store.FormatDigest)
	if m.SchemaVersion != 1 || !validFormat || m.Store.ReducerVersion != c.ReducerVersion || m.Store.StoreSeq < 0 || (m.Store.StoreSeq == 0 && m.Store.TailHash != "") || (m.Store.StoreSeq > 0 && !c.ValidDigest(m.Store.TailHash)) || m.DeletionPolicy != "UNSUPPORTED_NO_DELETIONS" || m.DeletionWatermark != 0 || len(m.Files) == 0 || len(m.Files) > backupMaxFiles {
		return c.Fail(c.UnsupportedCapability, "unsupported or invalid backup manifest")
	}
	total := int64(0)
	previous := ""
	database := false
	migrationFiles := 0
	for _, f := range m.Files {
		valid := strings.HasPrefix(f.Path, "artifacts/") && artifact.ImmutablePath(strings.TrimPrefix(f.Path, "artifacts/"))
		if f.Path == "metadata/migrations/v1-v2-intent.json" || f.Path == "metadata/migrations/v1-v2-complete.json" {
			valid = true
			migrationFiles++
		}
		if f.Path == "database/state.sqlite" {
			valid = true
			database = true
		}
		if !valid || f.Path <= previous || !c.ValidDigest(f.Digest) || f.Size < 0 || f.Size > backupFileLimit(f.Path) || f.Size > backupMaxBytes-total {
			return c.Fail(c.StoreIntegrityError, "invalid backup path, ordering, digest or quota")
		}
		total += f.Size
		previous = f.Path
	}
	if migrationFiles != 0 && (migrationFiles != 2 || m.Store.SchemaVersion != 2) {
		return c.Fail(c.StoreIntegrityError, "incomplete or legacy migration metadata")
	}
	if !database {
		return c.Fail(c.StoreIntegrityError, "backup database missing")
	}
	return nil
}

// backupClosure checks current and historical document pointers, raw inputs,
// fixtures, responses and snapshots. It does not run a model or any task effect.
func (s *Session) backupClosure(ctx context.Context) ([]string, error) {
	refs, err := s.Journal.DocumentReferences(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, state := range states {
		for after := int64(0); ; {
			page, err := s.Journal.History(ctx, state.TaskID, after, 256)
			if err != nil {
				return nil, err
			}
			for _, record := range page.Records {
				if record.Payload.Tokens != nil {
					if _, _, err := s.Inspect(ctx, state.TaskID, record.Event.TaskSeq); err != nil {
						return nil, err
					}
				}
				if record.Event.Type == "InputRecorded" && record.Payload.InputDigest != "" {
					if _, err := s.pendingInput(ctx, state.TaskID, record.Event.ID); err != nil {
						return nil, err
					}
				}
			}
			if !page.HasMore {
				break
			}
			after = page.Next
		}
		if _, _, err = s.Load(ctx, state.TaskID); err != nil {
			return nil, err
		}
	}
	roots := map[string]bool{}
	for _, ref := range refs {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := s.Archive.GetBytes(ref.TaskID, ref.Digest)
		if err != nil {
			return nil, err
		}
		var doc Document
		if err = c.DecodeStrict(raw, &doc); err != nil {
			return nil, err
		}
		if doc.SchemaVersion != 1 || doc.TaskID != ref.TaskID || doc.Spec.TaskID != ref.TaskID || doc.Baseline.TaskID != ref.TaskID || doc.Candidate.TaskID != ref.TaskID {
			return nil, c.Fail(c.StoreIntegrityError, "historical task document binding mismatch")
		}
		if err = s.validateProtection(doc); err != nil {
			return nil, err
		}
		if err = s.validateContext(doc); err != nil {
			return nil, err
		}
		if err = s.validateRuntime(doc); err != nil {
			return nil, err
		}
		if err = s.validatePlan(doc); err != nil {
			return nil, err
		}
		if err = doc.Spec.Validate(); err != nil {
			return nil, err
		}
		if err = doc.Budget.Validate(); err != nil {
			return nil, err
		}
		for _, input := range doc.Spec.Inputs {
			raw, err = s.Archive.GetBytes(ref.TaskID, input.Digest)
			if err != nil || int64(len(raw)) != input.ByteLength {
				return nil, c.Fail(c.StoreIntegrityError, "backup raw intent missing")
			}
		}
		if doc.Runtime == nil {
			fixtureRaw, err := s.Archive.GetBytes(ref.TaskID, doc.FixtureDigest)
			if err != nil {
				return nil, err
			}
			fixture, err := ParseFixture(fixtureRaw)
			if err != nil {
				return nil, err
			}
			if doc.FixtureCursor < 0 || doc.FixtureCursor > int64(len(fixture.Turns)) {
				return nil, c.Fail(c.StoreIntegrityError, "fixture cursor invalid")
			}

		}
		if doc.LastResponseBlob != "" {
			if _, err = s.Archive.GetBytes(ref.TaskID, doc.LastResponseBlob); err != nil {
				return nil, err
			}
		}
		for _, snapshot := range []artifact.Ref{doc.Baseline, doc.Candidate} {
			capture, err := s.Archive.Get(snapshot)
			if err != nil {
				return nil, err
			}
			roots[capture.Snapshot.Root] = true
		}
	}
	result := []string{}
	for root := range roots {
		result = append(result, root)
	}
	sort.Strings(result)
	return result, nil
}
func disjointRoots(target string, roots []string) error {
	for _, root := range roots {
		if !fileguard.Disjoint(target, root) {
			return c.Fail(c.PolicyDenied, "backup/restore destination overlaps source or store")
		}
	}
	return nil
}
func freshPrivate(directory string) (*os.Root, error) {
	if err := os.Mkdir(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	if err = fileguard.Private(root); err != nil {
		root.Close()
		return nil, err
	}
	parent, err := os.OpenRoot(filepath.Dir(directory))
	if err != nil {
		root.Close()
		return nil, err
	}
	err = fileguard.SyncDirectory(parent, ".")
	parent.Close()
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

// Backup requires an unused output path with an existing parent. No deletions or
// GC are supported in this profile; all published immutable objects are retained.
func (s *Session) Backup(ctx context.Context, output string) (BackupManifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backupLocked(ctx, output)
}
func (s *Session) backupLocked(ctx context.Context, output string) (BackupManifest, error) {
	m := BackupManifest{SchemaVersion: 1, DeletionPolicy: "UNSUPPORTED_NO_DELETIONS", Files: []BackupFile{}}
	if err := s.Journal.Writable(); err != nil {
		return m, err
	}
	roots, err := s.backupClosure(ctx)
	if err != nil {
		return m, err
	}
	roots = append(roots, s.directory)
	if err = disjointRoots(output, roots); err != nil {
		return m, err
	}
	target, err := fileguard.ResolveProspective(output)
	if err != nil {
		return m, err
	}
	root, err := freshPrivate(target)
	if err != nil {
		return m, err
	}
	defer root.Close()
	if err = root.Mkdir("database", 0700); err != nil {
		return m, err
	}
	m.Store, err = s.Journal.SnapshotDatabase(ctx, filepath.Join(target, "database"))
	if err != nil {
		return m, err
	}
	total := int64(0)
	add := func(name string, raw []byte, publish bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(m.Files) >= backupMaxFiles || int64(len(raw)) > backupMaxBytes-total {
			return c.Fail(c.UnsupportedCapability, "backup quota exceeded")
		}
		if publish {
			if err := fileguard.Publish(root, filepath.FromSlash(name), raw); err != nil {
				return err
			}
		}
		total += int64(len(raw))
		m.Files = append(m.Files, BackupFile{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
		return nil
	}
	raw, err := fileguard.ReadRegular(root, filepath.Join("database", "state.sqlite"), 256<<20)
	if err != nil {
		return m, err
	}
	if err = add("database/state.sqlite", raw, false); err != nil {
		return m, err
	}
	if err = s.Archive.VisitImmutable(ctx, func(name string, raw []byte) error { return add("artifacts/"+name, raw, true) }); err != nil {
		return m, err
	}
	intent, receipt, err := s.Journal.MigrationStatus(ctx)
	if err != nil {
		return m, err
	}
	if intent != nil {
		if receipt == nil {
			return m, c.Fail(c.PolicyDenied, "cannot back up an incomplete migration")
		}
		for name, value := range map[string]any{"metadata/migrations/v1-v2-intent.json": intent, "metadata/migrations/v1-v2-complete.json": receipt} {
			raw, err := c.CanonicalV1(value)
			if err != nil {
				return m, err
			}
			if err = add(name, raw, true); err != nil {
				return m, err
			}
		}
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	if err = validateBackupManifest(m); err != nil {
		return m, err
	}
	raw, err = c.CanonicalV1(m)
	if err != nil {
		return m, err
	}
	if err = fileguard.Publish(root, "backup.json", raw); err != nil {
		return m, err
	}
	// Parent publication is part of the durability boundary on supported filesystems.
	parent, err := os.OpenRoot(filepath.Dir(target))
	if err != nil {
		return m, err
	}
	defer parent.Close()
	return m, fileguard.SyncDirectory(parent, ".")
}

func readBackupFile(root *os.Root, f BackupFile) ([]byte, error) {
	raw, err := fileguard.ReadRegular(root, filepath.FromSlash(f.Path), backupFileLimit(f.Path))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != f.Size || c.HashBytes(raw) != f.Digest {
		return nil, c.Fail(c.StoreIntegrityError, "backup content digest or size mismatch")
	}
	return raw, nil
}

// RestoreBackup validates the whole bundle before staging, then verifies replay
// and semantic blob closure before publishing a new private store. Pending
// effects/reservations remain pending; restore never authorizes their retry.
func RestoreBackup(ctx context.Context, backup, destination string) (BackupManifest, error) {
	var m BackupManifest
	if !fileguard.Disjoint(backup, destination) {
		return m, c.Fail(c.PolicyDenied, "backup and restore destination overlap")
	}
	root, err := os.OpenRoot(backup)
	if err != nil {
		return m, err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, "backup.json", 8<<20)
	if err != nil {
		return m, err
	}
	if err = c.DecodeStrict(raw, &m); err != nil {
		return m, err
	}
	if err = validateBackupManifest(m); err != nil {
		return m, err
	}
	target, err := fileguard.ResolveProspective(destination)
	if err != nil {
		return m, err
	}
	if _, err = os.Lstat(target); err == nil {
		return m, c.Fail(c.Conflict, "restore destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return m, err
	}
	// Preflight every digest and embedded source root before creating anything.
	for _, f := range m.Files {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		raw, err = readBackupFile(root, f)
		if err != nil {
			return m, err
		}
		if strings.Contains(f.Path, "/snapshots/") {
			var snapshot artifact.Manifest
			if err = c.DecodeStrict(raw, &snapshot); err != nil {
				return m, err
			}
			if err = disjointRoots(target, []string{snapshot.Snapshot.Root}); err != nil {
				return m, err
			}
		}
	}
	parent, err := os.OpenRoot(filepath.Dir(target))
	if err != nil {
		return m, err
	}
	defer parent.Close()
	stageName := newID(".restore-")
	stagePath := filepath.Join(parent.Name(), stageName)
	stage, err := freshPrivate(stagePath)
	if err != nil {
		return m, err
	}
	defer stage.Close()
	for _, f := range m.Files {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		raw, err = readBackupFile(root, f)
		if err != nil {
			return m, err
		}
		name := f.Path
		if name == "metadata/migrations/v1-v2-intent.json" {
			name = store.MigrationIntentFile
		}
		if name == "metadata/migrations/v1-v2-complete.json" {
			name = store.MigrationCompleteFile
		}
		if name == "database/state.sqlite" {
			name = "state.sqlite"
		}
		if err = fileguard.Publish(stage, filepath.FromSlash(name), raw); err != nil {
			return m, err
		}
	}
	restored, err := Open(ctx, stagePath)
	if err != nil {
		return m, err
	}
	info, checkErr := restored.Journal.SnapshotInfo(ctx)
	if checkErr == nil && info != m.Store {
		checkErr = c.Fail(c.StoreIntegrityError, "backup journal identity mismatch")
	}
	var roots []string
	if checkErr == nil {
		roots, checkErr = restored.backupClosure(ctx)
	}
	if checkErr == nil {
		checkErr = disjointRoots(target, roots)
	}
	checkErr = errors.Join(checkErr, restored.Close())
	if checkErr != nil {
		return m, checkErr
	}
	// Close may checkpoint a WAL generated during validation; sync final database.
	database, err := stage.OpenFile("state.sqlite", os.O_RDWR, 0)
	if err != nil {
		return m, err
	}
	err = errors.Join(database.Sync(), database.Close())
	if err != nil {
		return m, err
	}
	if err = fileguard.SyncDirectory(stage, "."); err != nil {
		return m, err
	}
	if err = stage.Close(); err != nil {
		return m, err
	}
	if err = ctx.Err(); err != nil {
		return m, err
	}
	return m, fileguard.RenameNoReplace(parent, stageName, filepath.Base(target))
}
