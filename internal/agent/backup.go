package agent

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
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
	gcTotal := int64(0)
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
		if gcMaintenancePath(f.Path) {
			if f.Size > gcMaintenanceMaxBytes-gcTotal {
				return c.Fail(c.BudgetLimitReached, "backup GC maintenance byte quota exceeded")
			}
			gcTotal += f.Size
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
func (s *Session) backupLocked(ctx context.Context, output string) (result BackupManifest, resultErr error) {
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
		// Hold the shared privacy operation lease from before the database
		// snapshot through managed-copy registration. Otherwise a checkpoint
		// could retire the old pin's projection after the snapshot but before
		// this backup becomes discoverable in the registry.
		operation, operationErr := privacyOperation(ctx, *s.privacy)
		if operationErr != nil {
			return m, operationErr
		}
		defer func() { resultErr = errors.Join(resultErr, operation.Close()) }()
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
	var allocation resource.Allocation
	if s.PhysicalQuota != nil {
		upperBytes, upperEntries, requestDigest, estimateErr := s.backupPhysicalEstimate(ctx, sourceInfo)
		if estimateErr != nil {
			return m, estimateErr
		}
		allocation, err = s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "agent", Family: "BACKUP", RequestDigest: requestDigest, TargetRoot: target, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload})
		if err != nil {
			return m, err
		}
		if err = allocation.Begin(ctx); err != nil {
			return m, err
		}
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
	if s.PhysicalQuota != nil {
		m.Store, err = s.Journal.SnapshotDatabaseReserved(ctx, filepath.Join(target, "database"), allocation)
	} else {
		m.Store, err = s.Journal.SnapshotDatabase(ctx, filepath.Join(target, "database"))
	}
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
	if len(raw) > 8<<20 {
		return m, c.Fail(c.UnsupportedCapability, "backup manifest exceeds its bounded file profile")
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
	if err = fileguard.SyncDirectory(parent, "."); err != nil {
		return m, err
	}
	if s.PhysicalQuota != nil {
		inventory, measureErr := resource.Measure(root, true)
		if measureErr != nil {
			return m, measureErr
		}
		if err = allocation.Commit(ctx, inventory); err != nil {
			return m, err
		}
	}
	return m, nil
}

func (s *Session) backupPhysicalEstimate(ctx context.Context, sourceInfo store.SnapshotInfo) (uint64, uint64, string, error) {
	databaseBytes, err := s.Journal.SnapshotDatabaseUpperBytes(ctx)
	if err != nil {
		return 0, 0, "", err
	}
	objects, err := s.Archive.Inventory(ctx)
	if err != nil {
		return 0, 0, "", err
	}
	paths := []BackupFile{{Path: "database/state.sqlite", Digest: sourceInfo.TailHash, Size: int64(databaseBytes)}, {Path: "backup.json", Digest: c.HashBytes([]byte("backup-manifest-envelope")), Size: 8 << 20}}
	var payloadBytes uint64 = databaseBytes + 8<<20
	maxTemporary := databaseBytes
	for _, object := range objects {
		paths = append(paths, BackupFile{Path: "artifacts/" + object.Path, Digest: object.Digest, Size: object.Size})
		payloadBytes += uint64(object.Size)
		if uint64(object.Size) > maxTemporary {
			maxTemporary = uint64(object.Size)
		}
	}
	if err = s.visitGCMaintenance(ctx, func(name string, raw []byte) error {
		paths = append(paths, BackupFile{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
		payloadBytes += uint64(len(raw))
		if uint64(len(raw)) > maxTemporary {
			maxTemporary = uint64(len(raw))
		}
		return nil
	}); err != nil {
		return 0, 0, "", err
	}
	intent, receipt, err := s.Journal.MigrationStatus(ctx)
	if err != nil {
		return 0, 0, "", err
	}
	if intent != nil && receipt != nil {
		for name, value := range map[string]any{"metadata/migrations/v1-v2-intent.json": intent, "metadata/migrations/v1-v2-complete.json": receipt} {
			raw, encodeErr := c.CanonicalV1(value)
			if encodeErr != nil {
				return 0, 0, "", encodeErr
			}
			paths = append(paths, BackupFile{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
			payloadBytes += uint64(len(raw))
			if uint64(len(raw)) > maxTemporary {
				maxTemporary = uint64(len(raw))
			}
		}
	}
	if len(paths) > backupMaxFiles {
		return 0, 0, "", c.Fail(c.UnsupportedCapability, "backup physical entry estimate exceeds profile")
	}
	directories := map[string]bool{}
	for _, item := range paths {
		for directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(item.Path))); directory != "." && directory != ""; directory = filepath.ToSlash(filepath.Dir(directory)) {
			directories[directory] = true
		}
	}
	if maxTemporary < 64<<20 {
		maxTemporary = 64 << 20
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].Path < paths[j].Path })
	upperBytes := payloadBytes + maxTemporary + uint64(len(directories))*4096 + (64 << 10)
	upperEntries := uint64(len(paths) + len(directories) + 3)
	canonical, err := c.CanonicalV1(struct {
		SourceInfo store.SnapshotInfo `json:"source_info"`
		Paths      []BackupFile       `json:"paths"`
	}{SourceInfo: sourceInfo, Paths: paths})
	if err != nil {
		return 0, 0, "", err
	}
	return upperBytes, upperEntries, c.HashBytes(canonical), nil
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

// openLegacyRestoreValidationSession verifies a disposable legacy restore
// without registering its temporary directory as an owner of any external
// privacy authority inherited from the copied SQLite file. Legacy bundles do
// not carry a current authority binding, so opening them through Open would
// attach a fresh runtime at the staging path and leave that path pinned after
// the stage is renamed. Callers must close the returned journal and archive.
func openLegacyRestoreValidationSession(ctx context.Context, directory string, instance RuntimeInstance) (*Session, func() error, error) {
	journal, err := store.Open(ctx, directory)
	if err != nil {
		return nil, nil, err
	}
	archive, err := artifact.Open(filepath.Join(directory, "artifacts"))
	if err != nil {
		return nil, nil, errors.Join(err, journal.Close())
	}
	closeStores := func() error { return errors.Join(archive.Close(), journal.Close()) }
	session := &Session{directory: directory, Journal: journal, Archive: archive, instance: instance}
	rawAuthority, err := journal.PrivacyAuthority(ctx)
	if err != nil {
		return nil, nil, errors.Join(err, closeStores())
	}
	if rawAuthority != nil {
		var authority PrivacyAuthority
		if decodeErr := c.DecodeStrict(rawAuthority, &authority); decodeErr != nil || authority.validate() != nil {
			return nil, nil, errors.Join(c.Fail(c.StoreIntegrityError, "legacy restore privacy authority is invalid"), closeStores())
		}
		session.privacy = &authority
	}
	return session, closeStores, nil
}

// RestoreBackup validates the whole bundle before staging, then verifies replay
// and semantic blob closure before publishing a new private store. Pending
// effects/reservations remain pending; restore never authorizes their retry.
func RestoreBackup(ctx context.Context, backup, destination string) (BackupManifest, error) {
	return restoreBackup(ctx, backup, destination, false)
}

type restorePhysicalBinding struct {
	SchemaVersion      int    `json:"schema_version"`
	BackupRoot         string `json:"backup_root"`
	BackupRootIdentity string `json:"backup_root_identity"`
	ManifestDigest     string `json:"manifest_digest"`
	AuthorityDigest    string `json:"authority_digest"`
	TargetRoot         string `json:"target_root"`
	StageRoot          string `json:"stage_root"`
	AuthorityID        string `json:"authority_id"`
	PhysicalRoot       string `json:"physical_root"`
	DeletionWatermark  int64  `json:"deletion_watermark"`
	LedgerID           string `json:"ledger_id"`
}

func restoreManifestName(name string) string {
	switch name {
	case "metadata/migrations/v1-v2-intent.json":
		return store.MigrationIntentFile
	case "metadata/migrations/v1-v2-complete.json":
		return store.MigrationCompleteFile
	case "database/state.sqlite":
		return "state.sqlite"
	default:
		return filepath.FromSlash(name)
	}
}

func restoreAdd(left, right uint64) (uint64, error) {
	if right > math.MaxUint64-left {
		return 0, c.Fail(c.BudgetLimitReached, "restore physical quota envelope overflow")
	}
	return left + right, nil
}

func restorePhysicalEstimate(m BackupManifest) (uint64, uint64, error) {
	var payload, temporary uint64
	directories := map[string]bool{}
	for _, file := range m.Files {
		if file.Size < 0 {
			return 0, 0, c.Fail(c.StoreIntegrityError, "restore manifest contains a negative physical size")
		}
		size := uint64(file.Size)
		var err error
		payload, err = restoreAdd(payload, size)
		if err != nil {
			return 0, 0, err
		}
		if size > temporary {
			temporary = size
		}
		name := filepath.Clean(restoreManifestName(file.Path))
		for directory := filepath.Dir(name); directory != "." && directory != ""; directory = filepath.Dir(directory) {
			directories[directory] = true
		}
	}
	// fileguard.Publish uses a bounded temporary beside each final file. Keep
	// one complete largest payload charged concurrently with the final tree,
	// and retain the fixed owner bootstrap envelope for SQLite/control files.
	if temporary < 64<<20 {
		temporary = 64 << 20
	}
	startupBytes, startupEntries := resource.PartitionStartupEnvelope()
	upperBytes, err := restoreAdd(payload, temporary)
	if err == nil {
		upperBytes, err = restoreAdd(upperBytes, startupBytes)
	}
	if err == nil {
		upperBytes, err = restoreAdd(upperBytes, uint64(len(directories))*4096)
	}
	if err == nil {
		upperBytes, err = restoreAdd(upperBytes, 64<<10)
	}
	if err != nil {
		return 0, 0, err
	}
	upperEntries, err := restoreAdd(uint64(len(m.Files)), uint64(len(directories)))
	if err == nil {
		upperEntries, err = restoreAdd(upperEntries, 1+startupEntries)
	}
	if err != nil || upperBytes == 0 || upperEntries == 0 {
		if err == nil {
			err = c.Fail(c.BudgetLimitReached, "restore physical quota envelope is empty")
		}
		return 0, 0, err
	}
	return upperBytes, upperEntries, nil
}

func prepareRestorePhysicalQuota(ctx context.Context, root *os.Root, m BackupManifest, manifestRaw []byte, backup, backupIdentity, stage, target string) (*resource.Ledger, resource.Allocation, error) {
	if root == nil || m.SchemaVersion != 2 || m.PrivacyAuthority == nil {
		return nil, resource.Allocation{}, c.Fail(c.InvalidArgument, "current restore physical quota binding required")
	}
	var database BackupFile
	found := false
	for _, file := range m.Files {
		if file.Path == "database/state.sqlite" {
			database, found = file, true
			break
		}
	}
	if !found {
		return nil, resource.Allocation{}, c.Fail(c.StoreIntegrityError, "restore database manifest witness is missing")
	}
	pin, err := store.ReadPhysicalQuotaBindingFromBackup(ctx, root, backupIdentity, database.Digest, database.Size)
	if err != nil {
		return nil, resource.Allocation{}, err
	}
	if pin == nil {
		return nil, resource.Allocation{}, c.Fail(c.StaleAuthority, "restore backup has no pinned physical quota ledger")
	}
	authority := m.PrivacyAuthority
	if pin.AuthorityID != authority.ID || pin.Directory != authority.Directory || pin.PhysicalRoot != authority.PhysicalRoot {
		return nil, resource.Allocation{}, c.Fail(c.StaleAuthority, "restore backup physical quota pin differs from its privacy authority")
	}
	ledger, err := resource.OpenPinnedExisting(resource.Authority{ID: authority.ID, Directory: authority.Directory, PhysicalRoot: authority.PhysicalRoot}, pin.LedgerID)
	if err != nil {
		return nil, resource.Allocation{}, err
	}
	closeOnError := func(err error) (*resource.Ledger, resource.Allocation, error) {
		return nil, resource.Allocation{}, errors.Join(err, ledger.Close())
	}
	upperBytes, upperEntries, err := restorePhysicalEstimate(m)
	if err != nil {
		return closeOnError(err)
	}
	canonicalBackup, err := filepath.EvalSymlinks(backup)
	if err == nil {
		canonicalBackup, err = filepath.Abs(canonicalBackup)
	}
	if err != nil {
		return closeOnError(err)
	}
	authorityDigest, err := c.Digest(*authority)
	if err != nil {
		return closeOnError(err)
	}
	binding := restorePhysicalBinding{SchemaVersion: 1, BackupRoot: canonicalBackup, BackupRootIdentity: backupIdentity,
		ManifestDigest: c.HashBytes(manifestRaw), TargetRoot: target, StageRoot: stage, AuthorityID: authority.ID,
		AuthorityDigest: authorityDigest, PhysicalRoot: authority.PhysicalRoot, DeletionWatermark: m.DeletionWatermark, LedgerID: pin.LedgerID}
	requestDigest, err := c.Digest(binding)
	if err != nil {
		return closeOnError(err)
	}
	allocation, err := ledger.Reserve(ctx, resource.Request{Writer: "agent", Family: "RESTORE_OWNER", RequestDigest: requestDigest,
		TargetRoot: stage, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload})
	if err != nil {
		return closeOnError(err)
	}
	if err = allocation.PreparePartitionHandoff(ctx, target); err != nil {
		return closeOnError(err)
	}
	if err = allocation.Begin(ctx); err != nil {
		return closeOnError(err)
	}
	return ledger, allocation, nil
}

func restoredGCPartitionSpecs(records map[string][]byte) ([]resource.GCPartitionSpec, error) {
	// The caller has already validated every completion/receipt against its
	// canonical command. Derive the exact same upper bounds used by GC retries.
	var specs []resource.GCPartitionSpec
	for name, raw := range records {
		if !strings.HasSuffix(name, "/intent.json") {
			continue
		}
		var command GCCommand
		if err := c.DecodeStrict(raw, &command); err != nil {
			return nil, err
		}
		upperBytes, upperEntries, err := gcPhysicalEstimate(raw, len(command.Plan.Objects))
		if err != nil {
			return nil, err
		}
		specs = append(specs, resource.GCPartitionSpec{CommandID: c.HashBytes([]byte(command.CommandID)), RequestDigest: c.HashBytes(raw), UpperBytes: upperBytes, UpperEntries: upperEntries})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].CommandID < specs[j].CommandID })
	return specs, nil
}

// Legacy migration validation is confined to a disposable private destination;
// it is never a public restore capability or an authorization to run its tasks.
func restoreBackup(ctx context.Context, backup, destination string, legacyMigrationValidation bool) (manifest BackupManifest, resultErr error) {
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
	manifestRaw := append([]byte{}, raw...)
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
		defer func() { resultErr = errors.Join(resultErr, operation.Close()) }()
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
	gcRecords := map[string][]byte{}
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
			gcRecords[f.Path] = raw
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
	if err = validateGCMaintenanceRecords(gcRecords); err != nil {
		return m, err
	}
	gcSpecs, err := restoredGCPartitionSpecs(gcRecords)
	if err != nil {
		return m, err
	}
	parent, err := os.OpenRoot(filepath.Dir(target))
	if err != nil {
		return m, err
	}
	defer parent.Close()
	stageName := newID(".restore-")
	stagePath := filepath.Join(parent.Name(), stageName)
	stagePath, err = fileguard.ResolveProspective(stagePath)
	if err != nil {
		return m, err
	}
	var restoreQuota *resource.Ledger
	var restoreAllocation resource.Allocation
	if m.SchemaVersion == 2 {
		backupIdentity, identityErr := fileguard.DirectoryIdentity(root)
		if identityErr != nil {
			return m, identityErr
		}
		restoreQuota, restoreAllocation, err = prepareRestorePhysicalQuota(ctx, root, m, manifestRaw, backup, backupIdentity, stagePath, target)
		if err != nil {
			return m, err
		}
		defer func() { resultErr = errors.Join(resultErr, restoreQuota.Close()) }()
	}
	stage, err := freshPrivate(stagePath)
	if err != nil {
		return m, err
	}
	defer stage.Close()
	if restoreQuota != nil {
		if err = restoreAllocation.BindPartitionHandoffStage(ctx); err != nil {
			return m, err
		}
	}
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
	validationInstance, err := createRuntimeInstance(stagePath)
	if err != nil {
		return m, err
	}
	if m.SchemaVersion == 2 {
		instance, instanceErr := openRestoreRuntimeInstance(stagePath)
		if instanceErr != nil {
			return m, instanceErr
		}
		if err = initializePrivacyRestoreStage(ctx, *m.PrivacyAuthority, m, manifestRaw, backup, stagePath, target, instance); err != nil {
			return m, err
		}
	}
	var restored *Session
	var closeRestored func() error
	if legacyMigrationValidation && m.SchemaVersion == 1 {
		restored, closeRestored, err = openLegacyRestoreValidationSession(ctx, stagePath, validationInstance)
	} else if restoreQuota != nil {
		restored, err = openPreparedRestoreStage(ctx, stagePath, *m.PrivacyAuthority, restoreQuota, restoreAllocation)
		if err == nil {
			closeRestored = restored.Close
		}
	} else {
		restored, err = Open(ctx, stagePath)
		if err == nil {
			closeRestored = restored.Close
		}
	}
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
	if checkErr == nil && m.SchemaVersion == 2 {
		checkErr = preparePrivacyRestoreTarget(ctx, *m.PrivacyAuthority, stagePath, target, restored.instance, restored.Journal)
	}
	checkErr = errors.Join(checkErr, closeRestored())
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
	if restoreQuota != nil {
		if _, err = restoreAllocation.CommitPartitionHandoff(ctx, gcSpecs...); err != nil {
			return m, err
		}
	} else if err = fileguard.RenameNoReplace(parent, stageName, filepath.Base(target)); err != nil {
		return m, err
	}
	if m.SchemaVersion == 2 {
		if err = registerRestoredScope(ctx, *m.PrivacyAuthority, restored.instance, target); err != nil {
			return m, err
		}
	}
	return m, nil
}

// openPreparedRestoreStage opens a restored owner only while its exact outer
// allocation is WRITING.  Store and archive attachment then observe that
// enclosing reservation and contribute to the same pending receipt rather
// than importing the copied SQLite/archive bytes a second time.  The check is
// intentionally made before openSession: an unbound or already-settled stage
// must fail closed instead of becoming an unadmitted regular session.
func openPreparedRestoreStage(ctx context.Context, directory string, authority PrivacyAuthority, ledger *resource.Ledger, allocation resource.Allocation) (*Session, error) {
	if ledger == nil || allocation.ID == "" || directory == "" {
		return nil, c.Fail(c.InvalidArgument, "prepared restore physical allocation required")
	}
	enclosing, covered, err := ledger.EnclosingReservation(ctx, directory)
	if err != nil {
		return nil, err
	}
	if !covered || enclosing.ID != allocation.ID || !allocation.Covers(directory) {
		return nil, c.Fail(c.StaleAuthority, "restore stage is not covered by its active physical reservation")
	}
	session, err := openSession(ctx, directory, &authority)
	if err != nil {
		return nil, err
	}
	if session.PhysicalQuota == nil || session.PhysicalQuota.Authority.ID != ledger.Authority.ID || session.PhysicalQuota.LedgerID != ledger.LedgerID {
		return nil, errors.Join(c.Fail(c.StaleAuthority, "restore stage opened without its pinned physical quota ledger"), session.Close())
	}
	return session, nil
}

func openRestoreRuntimeInstance(directory string) (RuntimeInstance, error) {
	var instance RuntimeInstance
	root, err := os.OpenRoot(directory)
	if err != nil {
		return instance, err
	}
	defer root.Close()
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return instance, err
	}
	raw, err := fileguard.ReadRegular(root, runtimeInstanceFile, 1024)
	if err != nil || c.DecodeStrict(raw, &instance) != nil || instance.SchemaVersion != 1 || !validPrivacyInstanceID(instance.ID) || instance.PhysicalRoot != physical {
		return RuntimeInstance{}, c.Fail(c.StoreIntegrityError, "restore runtime instance is invalid")
	}
	canonical, err := c.CanonicalV1(instance)
	if err != nil || string(canonical) != string(raw) {
		return RuntimeInstance{}, c.Fail(c.StoreIntegrityError, "restore runtime instance is noncanonical")
	}
	return instance, nil
}
