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
	PrivacyAuthority  *PrivacyAuthority  `json:"privacy_authority,omitempty"`
	SchemaVersion     int                `json:"schema_version"`
	Store             store.SnapshotInfo `json:"store"`
	DeletionPolicy    string             `json:"deletion_policy"`
	DeletionWatermark int64              `json:"deletion_watermark"`
	Files             []BackupFile       `json:"files"`
}

func backupFileLimit(name string) int64 {
	if gcMaintenancePath(name) {
		return 2 << 20
	}
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
	legacy := m.SchemaVersion == 1 && m.PrivacyAuthority == nil && m.DeletionPolicy == "UNSUPPORTED_NO_DELETIONS" && m.DeletionWatermark == 0
	current := m.SchemaVersion == 2 && m.PrivacyAuthority != nil && m.PrivacyAuthority.validate() == nil && m.DeletionPolicy == "CURRENT_EXTERNAL_AUTHORITY_REQUIRED" && m.DeletionWatermark >= 0 && m.Store.SchemaVersion == 2
	if !(legacy || current) || !validFormat || m.Store.ReducerVersion != c.ReducerVersion || m.Store.StoreSeq < 0 || (m.Store.StoreSeq == 0 && m.Store.TailHash != "") || (m.Store.StoreSeq > 0 && !c.ValidDigest(m.Store.TailHash)) || len(m.Files) == 0 || len(m.Files) > backupMaxFiles {
		return c.Fail(c.UnsupportedCapability, "unsupported or invalid backup manifest")
	}
	total := int64(0)
	previous := ""
	database := false
	migrationFiles := 0
	for _, f := range m.Files {
		valid := strings.HasPrefix(f.Path, "artifacts/") && artifact.ImmutablePath(strings.TrimPrefix(f.Path, "artifacts/")) || gcMaintenancePath(f.Path)
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
		if state.Deletion != nil {
			if state.Deletion.Status != "PURGED" {
				return nil, c.Fail(c.PolicyDenied, "complete pending privacy purge before backup")
			}
			continue
		}
		for after := int64(0); ; {
			page, err := s.Journal.AccountHistory(ctx, state.TaskID, after, 64)

			if err != nil {
				return nil, err
			}
			for _, record := range page.Records {
				if _, _, err := s.loadDocument(record.State); err != nil {
					return nil, err
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
		if states[ref.TaskID].Deletion != nil {
			continue
		}
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
		if err = validateNativeLease(doc); err != nil {
			return nil, err
		}
		if err = s.validateNativeCleanup(doc); err != nil {
			return nil, err
		}
		if err = s.validateAttempt(doc); err != nil {
			return nil, err
		}
		if err = s.validateFinalArtifact(doc); err != nil {
			return nil, err
		}
		if err = s.validateChecks(doc); err != nil {
			return nil, err
		}
		if err = s.validateProtection(doc); err != nil {
			return nil, err
		}
		if err = s.validateContext(doc); err != nil {
			return nil, err
		}
		if err = s.validateContinuity(doc); err != nil {
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

// Backup requires an unused output path. Current backups carry a separately
// pinned deletion authority; old watermarks cannot authorize restored content.
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
	sourceInfo, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return m, err
	}
	if sourceInfo.SchemaVersion == 2 {
		if err = s.ensurePrivacy(ctx); err != nil {
			return m, err
		}
		m.SchemaVersion, m.DeletionPolicy, m.PrivacyAuthority = 2, "CURRENT_EXTERNAL_AUTHORITY_REQUIRED", s.privacy
		authorityRoot, err := openPrivacyRoot(*s.privacy)
		if err != nil {
			return m, err
		}
		authorityLock, err := privacyLock(ctx, authorityRoot)
		if err != nil {
			authorityRoot.Close()
			return m, err
		}
		view, err := readPrivacy(authorityRoot)
		err = errors.Join(err, authorityLock.Close(), authorityRoot.Close())
		if err != nil {
			return m, err
		}
		m.DeletionWatermark = view.Watermark
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
	if m.SchemaVersion == 2 {
		states, err := s.Journal.Replay(ctx, "")
		if err != nil {
			return m, err
		}
		tasks := []string{}
		for task, state := range states {
			if state.Deletion == nil {
				tasks = append(tasks, task)
			}
		}
		if err = s.allocateManagedCopy(ctx, root, "BACKUP", tasks); err != nil {
			return m, err
		}
	}
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
	if err = s.visitGCMaintenance(ctx, func(name string, raw []byte) error { return add(name, raw, true) }); err != nil {
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
	if m.SchemaVersion == 2 {
		copyID, err := fileguard.DirectoryIdentity(root)
		if err != nil {
			return m, err
		}
		states, err := s.Journal.Replay(ctx, "")
		if err != nil {
			return m, err
		}
		copy := ManagedCopy{Kind: "BACKUP", Directory: target, PhysicalRoot: copyID, Files: append([]BackupFile{}, m.Files...), Tasks: []string{}}
		copy.Files = append(copy.Files, BackupFile{"backup.json", c.HashBytes(raw), int64(len(raw))})
		sort.Slice(copy.Files, func(i, j int) bool { return copy.Files[i].Path < copy.Files[j].Path })
		for task, state := range states {
			if state.Deletion == nil {
				copy.Tasks = append(copy.Tasks, task)
			}
		}
		sort.Strings(copy.Tasks)
		if err = s.registerManagedCopy(ctx, copy); err != nil {
			return m, err
		}
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
	return restoreBackup(ctx, backup, destination, false)
}

// Legacy migration validation is confined to a disposable private destination;
// it is never a public restore capability or an authorization to run its tasks.
func restoreBackup(ctx context.Context, backup, destination string, legacyMigrationValidation bool) (BackupManifest, error) {
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
	// V1 stores are immutable/read-only recovery inputs. A legacy V2 backup
	// has no authoritative watermark and cannot become an active restored owner.
	if m.SchemaVersion == 1 && !(legacyMigrationValidation && m.Store.SchemaVersion == 1) {
		return m, c.Fail(c.UnsupportedCapability, "legacy backup lacks current deletion authority; only isolated migration validation is supported")
	}
	if m.SchemaVersion == 2 {
		operation, err := privacyOperation(ctx, *m.PrivacyAuthority)
		if err != nil {
			return m, err
		}
		defer operation.Close()
		if err = checkBackupAuthority(ctx, m, raw, backup); err != nil {
			return m, err
		}
	}
	target, err := fileguard.ResolveProspective(destination)
	if err != nil {
		return m, err
	}
	if m.PrivacyAuthority != nil && !fileguard.Disjoint(target, m.PrivacyAuthority.Directory) {
		return m, c.Fail(c.PolicyDenied, "restore destination overlaps the deletion authority")
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
		if gcMaintenancePath(f.Path) {
			if err = validateGCRecord(f.Path, raw); err != nil {
				return m, err
			}
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
	if m.SchemaVersion == 2 {
		registry, err := openPrivacyRoot(*m.PrivacyAuthority)
		if err != nil {
			return m, err
		}
		lock, err := privacyLock(ctx, registry)
		if err != nil {
			registry.Close()
			return m, err
		}
		view, err := readPrivacy(registry)
		if err == nil {
			tasks := []string{}
			registered := false
			canonicalBackup, resolveErr := filepath.EvalSymlinks(backup)
			if resolveErr == nil {
				canonicalBackup, resolveErr = filepath.Abs(canonicalBackup)
			}
			err = resolveErr
			for _, copy := range view.Copies {
				if copy.Kind == "BACKUP" && copy.Directory == canonicalBackup {
					tasks = append(tasks, copy.Tasks...)
					registered = true
					break
				}
			}
			if !registered {
				err = errors.Join(err, c.Fail(c.PolicyDenied, "restore allocation requires a registered source backup"))
			}
			physical, identityErr := fileguard.DirectoryIdentity(stage)
			err = errors.Join(err, identityErr)
			if err == nil {
				err = appendPrivacy(registry, view, privacyRecord{Type: "MANAGED_COPY", Copy: &ManagedCopy{Kind: "RESTORE_STAGE", Directory: stagePath, PhysicalRoot: physical, Tasks: tasks, Files: []BackupFile{}}})
			}
		}
		err = errors.Join(err, lock.Close(), registry.Close())
		if err != nil {
			return m, err
		}
	}
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
	if _, err = createRuntimeInstance(stagePath); err != nil {
		return m, err
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
	if m.SchemaVersion == 2 {
		if err = registerRestoredScope(ctx, *m.PrivacyAuthority, restored.instance, target); err != nil {
			return m, err
		}
	}
	return m, fileguard.RenameNoReplace(parent, stageName, filepath.Base(target))
}
