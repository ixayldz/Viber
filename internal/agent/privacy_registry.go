package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

type PrivacyAuthority struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Directory     string `json:"directory"`
	PhysicalRoot  string `json:"physical_root"`
}
type privacyOwner struct {
	root      *os.Root
	lease     *os.File
	name      string
	operation *PrivacyAuthority
}

func (o *privacyOwner) Close() error {
	if o == nil || o.root == nil {
		return nil
	}
	var err error
	if o.operation != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		lock, lockErr := privacyLock(ctx, o.root)
		err = errors.Join(lockErr, o.lease.Close())
		if lockErr == nil {
			if err == nil {
				_, err = retirePrivacyOperationLeases(ctx, o.root, *o.operation, o.name)
			}
			err = errors.Join(err, lock.Close())
		}
	} else {
		err = o.lease.Close()
	}
	o.root = closePrivacyRoot(o.root, &err)
	return err
}
func closePrivacyRoot(root *os.Root, err *error) *os.Root {
	*err = errors.Join(*err, root.Close())
	return nil
}

type ManagedCopy struct {
	ExcludedStore *privacyScope `json:"excluded_owner_store,omitempty"`
	Kind          string        `json:"kind"`
	Directory     string        `json:"directory"`
	PhysicalRoot  string        `json:"physical_root"`
	Files         []BackupFile  `json:"files"`
	Tasks         []string      `json:"tasks"`
}
type privacyRecord struct {
	SchemaVersion int                     `json:"schema_version"`
	Sequence      int64                   `json:"sequence"`
	Previous      string                  `json:"previous"`
	Type          string                  `json:"type"`
	Copy          *ManagedCopy            `json:"copy,omitempty"`
	Command       *DeletionCommand        `json:"command,omitempty"`
	Attempt       *privacyAttempt         `json:"attempt,omitempty"`
	Retention     *privacyRetentionRecord `json:"retention,omitempty"`
}
type privacyView struct {
	Sequence           int64
	RecordCount        int64
	Tail               string
	Watermark          int64
	Copies             []ManagedCopy
	CopySequences      map[string]int64
	Deletions          map[string]DeletionCommand
	DeletionSequences  map[string]int64
	Attempts           []privacyAttempt
	AttemptSequences   map[string]int64
	Retentions         map[string]privacyRetentionRecord
	RetentionSequences map[string]int64
	RetentionCommands  map[string]privacyRetentionRecord
}

func (authority PrivacyAuthority) validate() error {
	if authority.SchemaVersion != 1 || len(authority.ID) != 32 || !c.ValidDigest(c.HashBytes([]byte(authority.ID))) || !c.ValidDigest(authority.PhysicalRoot) || !filepath.IsAbs(authority.Directory) || len(authority.Directory) > 2048 {
		return c.Fail(c.StoreIntegrityError, "invalid external privacy authority")
	}
	// IDs have the same strict lowercase hex grammar as native runtime instances.
	for _, r := range authority.ID {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return c.Fail(c.StoreIntegrityError, "invalid privacy authority ID")
		}
	}
	return nil
}
func openPrivacyRoot(authority PrivacyAuthority) (*os.Root, error) {
	if err := authority.validate(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(authority.Directory)
	if err != nil || !info.IsDir() {
		return nil, c.Fail(c.PolicyDenied, "current deletion authority is unavailable; restore/access cannot trust an old copy")
	}
	root, err := os.OpenRoot(authority.Directory)
	if err != nil {
		return nil, err
	}
	id, err := fileguard.DirectoryIdentity(root)
	if err == nil && id != authority.PhysicalRoot {
		err = c.Fail(c.StaleAuthority, "deletion authority physical identity changed")
	}
	if err == nil {
		err = fileguard.Private(root)
	}
	if err == nil {
		raw, readErr := fileguard.ReadRegular(root, "authority.json", 4096)
		expected, _ := c.CanonicalV1(authority)
		if readErr != nil || string(raw) != string(expected) {
			err = c.Fail(c.StoreIntegrityError, "deletion authority genesis mismatch")
		}
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}
func privacyLock(ctx context.Context, root *os.Root) (*os.File, error) {
	for {
		lock, err := fileguard.Lock(root, "registry.lock")
		if err == nil {
			return lock, nil
		}
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StoreOwned {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func readPrivacy(root *os.Root) (privacyView, error) {
	return readPrivacyMode(root, false)
}

func readPrivacyCheckpointRecoveryView(root *os.Root) (privacyView, error) {
	return readPrivacyMode(root, true)
}

func readPrivacyMode(root *os.Root, checkpointRecovery bool) (privacyView, error) {
	view, checkpoint, err := readPrivacyCheckpointBaseMode(root, checkpointRecovery)
	if err != nil {
		return view, err
	}
	dir, err := root.Open("records")
	if err != nil {
		return view, err
	}
	names, err := dir.Readdirnames(2049)
	err = errors.Join(func() error {
		if err == io.EOF {
			return nil
		}
		return err
	}(), dir.Close())
	if err != nil {
		return view, err
	}
	if len(names) > 2048 {
		return view, c.Fail(c.BudgetLimitReached, "privacy journal record quota exceeded")
	}
	sort.Strings(names)
	total := int64(0)
	// Replay-local indexes only: retain every record and dedup fact. No state or
	// authority digest can be reused by a later read/reopen. Schema bounds cap
	// these indexes (2048 records; at most 128 restored scopes per intent).
	copies := map[string]int{}
	for index, copy := range view.Copies {
		copies[copy.Directory] = index
	}
	attempts := map[struct{ Root, Task string }]bool{}
	for _, attempt := range view.Attempts {
		attempts[struct{ Root, Task string }{attempt.Scope.PhysicalRoot, attempt.ChildTask}] = true
	}
	reservedDeletionIDs := map[[32]byte]bool{}
	for _, command := range view.Deletions {
		for _, id := range deletionCommandIDs(command) {
			key := sha256.Sum256([]byte(id))
			if reservedDeletionIDs[key] {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint deletion namespace collision")
			}
			reservedDeletionIDs[key] = true
		}
	}
	var retentionGenesis []byte
	retentionAuthority := ""
	if checkpoint != nil {
		retentionGenesis, err = fileguard.ReadRegular(root, "authority.json", 4096)
		if err != nil {
			return view, err
		}
		var authority PrivacyAuthority
		if c.DecodeStrict(retentionGenesis, &authority) != nil || authority.validate() != nil {
			return view, c.Fail(c.StoreIntegrityError, "checkpoint authority unavailable or invalid")
		}
		retentionAuthority, _ = c.Digest(authority)
	}
	shadow := map[int64]privacyCheckpointSource{}
	var retirement *privacyCheckpointRetirement
	if checkpoint != nil {
		for _, source := range checkpoint.SourceRecords {
			shadow[source.Sequence] = source
		}
		anchor, anchorDigest, anchorErr := currentPrivacyCheckpointAnchor(root)
		if anchorErr != nil || anchor == nil {
			return view, c.Fail(c.StoreIntegrityError, "checkpoint source inventory has no current anchor")
		}
		retirement, _, err = readPrivacyCheckpointRetirement(root, *anchor, anchorDigest, *checkpoint)
		if err != nil {
			return view, err
		}
	}
	for _, name := range names {
		sequence, nameErr := strconv.ParseInt(strings.TrimSuffix(name, ".json"), 10, 64)
		if nameErr == nil && checkpoint != nil && sequence >= 1 && sequence <= checkpoint.Sequence {
			if sequence <= checkpoint.BaseSequence {
				return view, c.Fail(c.StoreIntegrityError, "retired privacy journal record reappeared before the current source range")
			}
			source, found := shadow[sequence]
			if !found || name != fmt.Sprintf("%010d.json", sequence) {
				return view, c.Fail(c.StoreIntegrityError, "unbound privacy checkpoint shadow record")
			}
			raw, readErr := fileguard.ReadRegular(root, filepath.Join("records", name), 16<<20)
			if readErr != nil || int64(len(raw)) != source.Size || c.HashBytes(raw) != source.Digest {
				return view, c.Fail(c.StoreIntegrityError, "privacy checkpoint source record changed")
			}
			total += int64(len(raw))
			if total > privacyMetadataLimit {
				return view, c.Fail(c.BudgetLimitReached, "privacy metadata quota exceeded")
			}
			delete(shadow, sequence)
			continue
		}
		if name != fmt.Sprintf("%010d.json", view.Sequence+1) {
			return view, c.Fail(c.StoreIntegrityError, "privacy journal gap or foreign record")
		}
		raw, err := fileguard.ReadRegular(root, filepath.Join("records", name), 16<<20)
		if err != nil {
			return view, err
		}
		total += int64(len(raw))
		if total > 64<<20 {
			return view, c.Fail(c.BudgetLimitReached, "privacy metadata quota exceeded")
		}
		var record privacyRecord
		if c.DecodeStrict(raw, &record) != nil || record.SchemaVersion != 1 || record.Sequence != view.Sequence+1 || record.Previous != view.Tail {
			return view, c.Fail(c.StoreIntegrityError, "privacy record chain invalid")
		}
		switch record.Type {
		case "MANAGED_COPY":
			if record.Copy == nil || record.Command != nil || record.Attempt != nil || record.Retention != nil || validateManagedCopy(*record.Copy) != nil {
				return view, c.Fail(c.StoreIntegrityError, "managed copy record invalid")
			}
			if i, replaced := copies[record.Copy.Directory]; replaced {
				prior := view.Copies[i]
				if len(prior.Files) != 0 || prior.Kind != record.Copy.Kind || prior.PhysicalRoot != record.Copy.PhysicalRoot || (!sameManagedTasks(prior.Tasks, record.Copy.Tasks) || !sameExcludedStore(prior.ExcludedStore, record.Copy.ExcludedStore)) {
					return view, c.Fail(c.StoreIntegrityError, "managed allocation cannot be rebound")
				}
				view.Copies[i] = *record.Copy
			} else {
				copies[record.Copy.Directory] = len(view.Copies)
				view.Copies = append(view.Copies, *record.Copy)
			}
			view.CopySequences[record.Copy.Directory] = record.Sequence
		case "DELETE_INTENT":
			if record.Command == nil || record.Copy != nil || record.Attempt != nil || record.Retention != nil || validateDeletionCommand(*record.Command) != nil {
				return view, c.Fail(c.StoreIntegrityError, "privacy intent invalid")
			}
			command := *record.Command
			if retentionDeletionCollision(view, command) {
				return view, c.Fail(c.StoreIntegrityError, "retention and deletion command identity collision")
			}
			for _, id := range deletionCommandIDs(command) {
				key := sha256.Sum256([]byte(id))
				if reservedDeletionIDs[key] {
					return view, c.Fail(c.StoreIntegrityError, "deletion command namespace was reserved twice")
				}
				reservedDeletionIDs[key] = true
			}
			if command.Expiry != nil {
				policy, exists := view.Retentions[command.Plan.TaskID]
				expected, _ := c.Digest(policy.Policy)
				actual, _ := c.Digest(command.Expiry.Policy)
				if !exists || actual != expected {
					return view, c.Fail(c.StoreIntegrityError, "expiry used stale or missing retention consent")
				}
			}
			if _, exists := view.Deletions[command.Plan.TaskID]; exists || command.Plan.Watermark != view.Watermark+1 {
				return view, c.Fail(c.StoreIntegrityError, "privacy intent duplicate or watermark gap")
			}
			view.Watermark++
			view.Deletions[command.Plan.TaskID] = command
			view.DeletionSequences[command.Plan.TaskID] = record.Sequence
		case "ATTEMPT_LINEAGE":
			if record.Attempt == nil || record.Command != nil || record.Copy != nil || record.Retention != nil || record.Attempt.validate() != nil {
				return view, c.Fail(c.StoreIntegrityError, "invalid durable attempt lineage")
			}
			key := struct{ Root, Task string }{record.Attempt.Scope.PhysicalRoot, record.Attempt.ChildTask}
			if attempts[key] {
				return view, c.Fail(c.StoreIntegrityError, "attempt lineage identity was registered twice")
			}
			attempts[key] = true
			if _, deleted := view.Deletions[record.Attempt.ParentTask]; deleted {
				return view, c.Fail(c.StoreIntegrityError, "attempt was allocated after parent deletion")
			}
			if _, deleted := view.Deletions[record.Attempt.ChildTask]; deleted {
				return view, c.Fail(c.StoreIntegrityError, "attempt was allocated after child deletion")
			}
			view.Attempts = append(view.Attempts, *record.Attempt)
			view.AttemptSequences[privacyAttemptKey(*record.Attempt)] = record.Sequence
		case "RETENTION_POLICY":
			if record.Retention == nil || record.Command != nil || record.Copy != nil || record.Attempt != nil || record.Retention.validate() != nil {
				return view, c.Fail(c.StoreIntegrityError, "invalid retention record")
			}
			retention := *record.Retention
			if retentionGenesis == nil {
				genesis, err := fileguard.ReadRegular(root, "authority.json", 4096)
				var authority PrivacyAuthority
				if err != nil || c.DecodeStrict(genesis, &authority) != nil || authority.validate() != nil {
					return view, c.Fail(c.StoreIntegrityError, "retention authority unavailable or invalid")
				}
				retentionGenesis = genesis
				retentionAuthority, _ = c.Digest(authority)
			}
			if retentionAuthority != retention.Policy.AuthorityDigest {
				return view, c.Fail(c.StoreIntegrityError, "retention authority mismatch")
			}
			if _, exists := view.Deletions[retention.Policy.TaskID]; exists {
				return view, c.Fail(c.StoreIntegrityError, "retention cannot revise a deletion intent")
			}
			if _, exists := view.RetentionCommands[retention.CommandID]; exists || retention.Policy.Revision != view.Retentions[retention.Policy.TaskID].Policy.Revision+1 {
				return view, c.Fail(c.StoreIntegrityError, "retention command duplicate or revision gap")
			}
			if reservedDeletionIDs[sha256.Sum256([]byte(retention.CommandID))] {
				return view, c.Fail(c.StoreIntegrityError, "retention command collides with deletion")
			}
			view.Retentions[retention.Policy.TaskID] = retention
			view.RetentionCommands[retention.CommandID] = retention
			view.RetentionSequences[retention.CommandID] = record.Sequence
		default:
			return view, c.Fail(c.StoreIntegrityError, "unknown privacy journal record")
		}
		view.Sequence = record.Sequence
		view.Tail = c.HashBytes(raw)
		view.RecordCount++
	}
	if len(shadow) != 0 && retirement == nil {
		return view, c.Fail(c.StoreIntegrityError, "privacy checkpoint source inventory is incomplete")
	}
	if retentionGenesis != nil {
		current, err := fileguard.ReadRegular(root, "authority.json", 4096)
		if err != nil || !bytes.Equal(current, retentionGenesis) {
			return view, c.Fail(c.StoreIntegrityError, "retention authority changed during replay")
		}
	}
	if !checkpointRecovery {
		genesis, err := fileguard.ReadRegular(root, "authority.json", 4096)
		var authority PrivacyAuthority
		if err != nil || c.DecodeStrict(genesis, &authority) != nil || authority.validate() != nil {
			return view, c.Fail(c.StoreIntegrityError, "privacy checkpoint authority unavailable or invalid")
		}
		pinCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pinErr := validateCurrentPrivacyPinsReadOnly(pinCtx, root, authority)
		cancel()
		if pinErr != nil {
			return view, pinErr
		}
	}
	return view, nil
}
func appendPrivacy(root *os.Root, view privacyView, record privacyRecord) error {
	record.SchemaVersion, record.Sequence, record.Previous = 1, view.Sequence+1, view.Tail
	if record.Sequence < 1 || record.Sequence > privacyCheckpointAnchorLimit*privacyRecordLimit {
		return c.Fail(c.BudgetLimitReached, "privacy sequence lineage bound reached")
	}
	raw, err := c.CanonicalV1(record)
	if err != nil {
		return err
	}
	if len(raw) > 8<<20 {
		return c.Fail(c.BudgetLimitReached, "privacy record too large")
	}
	// Metadata space and physically allocated control reserve are admitted
	// under the same registry lock held by every caller.
	retained, err := privacyMetadataBytes(root)
	if err != nil {
		return err
	}
	control := record.Type == "DELETE_INTENT"
	if err = privacyMetadataAdmission(view.RecordCount, retained, int64(len(raw))*2, control); err != nil {
		return err
	}
	if err = admitPrivacyDisk(root, int64(len(raw))*2, control); err != nil {
		return err
	}
	return fileguard.Publish(root, filepath.Join("records", fmt.Sprintf("%010d.json", record.Sequence)), raw)
}
func validateManagedCopy(copy ManagedCopy) error {
	if (copy.Kind != "BACKUP" && copy.Kind != "RESTORE_STAGE" && copy.Kind != "EXPORT" && copy.Kind != "TRAINING_EXPORT" && copy.Kind != "DELIVERY_PREVIEW" && copy.Kind != "SUPPORT" && copy.Kind != "EVALUATION_REPORT") || !filepath.IsAbs(copy.Directory) || len(copy.Directory) > 2048 || !c.ValidDigest(copy.PhysicalRoot) || len(copy.Files) > backupMaxFiles+1 || len(copy.Tasks) > 4096 {
		return c.Fail(c.StoreIntegrityError, "invalid managed copy")
	}
	if copy.Kind == "EVALUATION_REPORT" {
		if copy.ExcludedStore == nil || !validPrivacyScope(*copy.ExcludedStore) || copy.ExcludedStore.Directory != filepath.Join(copy.Directory, "owner") {
			return c.Fail(c.StoreIntegrityError, "evaluator report requires an exact separately managed owner")
		}
	} else if copy.ExcludedStore != nil {
		return c.Fail(c.StoreIntegrityError, "unexpected excluded store")
	}
	previous := ""
	for _, task := range copy.Tasks {
		if _, err := artifact.ScopedBlobPath(task, c.HashBytes(nil)); err != nil || task <= previous {
			return c.Fail(c.StoreIntegrityError, "invalid managed scope binding")
		}
		previous = task
	}
	previous = ""
	total := int64(0)
	for _, f := range copy.Files {
		if f.Path <= previous || !safeCopyPath(copy.Kind, f.Path) || !c.ValidDigest(f.Digest) || f.Size < 0 || f.Size > backupMaxBytes-total {
			return c.Fail(c.StoreIntegrityError, "invalid managed file binding")
		}
		total += f.Size
		previous = f.Path
	}
	return nil
}
func safeCopyPath(kind, name string) bool {
	if strings.HasPrefix(name, ".publish-") && len(name) == 41 {
		for _, r := range strings.TrimPrefix(name, ".publish-") {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
		return true
	}
	if kind == "EVALUATION_REPORT" {
		return name == "source.json" || name == "registration.json" || name == "result.json" || name == "manifest.json"
	}
	if kind == "SUPPORT" {
		return name == "support.json"
	}
	if kind == "TRAINING_EXPORT" {
		return name == "samples.json" || name == "manifest.json"
	}
	if kind == "RESTORE_STAGE" {
		if name == "state.sqlite" || name == "state.sqlite-wal" || name == "state.sqlite-shm" || name == "owner.lock" || name == "control.reserve" || name == runtimeInstanceFile || name == "artifacts/archive.lock" || name == store.MigrationIntentFile || name == store.MigrationCompleteFile {
			return true
		}
		return strings.HasPrefix(name, "artifacts/") && artifact.ImmutablePath(strings.TrimPrefix(name, "artifacts/")) || gcMaintenancePath(name)
	}
	if kind == "EXPORT" {
		if name == "manifest.json" || name == "report.json" || name == "final-artifact.json" || name == "changes.patch" {
			return true
		}
		return (strings.HasPrefix(name, "before/") || strings.HasPrefix(name, "after/")) && safeManagedRelative(strings.SplitN(name, "/", 2)[1])
	}
	if kind == "DELIVERY_PREVIEW" {
		if name == "manifest.json" || name == "preview.json" || name == "merged-snapshot.json" || name == "changes.patch" {
			return true
		}
		return (strings.HasPrefix(name, "merged/") || strings.HasPrefix(name, "current/")) && safeManagedRelative(strings.SplitN(name, "/", 2)[1])
	}
	// Only the exact backup manifest and its validated portable entries are owned.
	if name == "backup.json" || name == "database/state.sqlite" || name == "metadata/migrations/v1-v2-intent.json" || name == "metadata/migrations/v1-v2-complete.json" {
		return true
	}
	return strings.HasPrefix(name, "artifacts/") && artifact.ImmutablePath(strings.TrimPrefix(name, "artifacts/")) || gcMaintenancePath(name)
}

func (s *Session) attachPrivacy(ctx context.Context) error {
	raw, err := s.Journal.PrivacyAuthority(ctx)
	if err != nil || raw == nil {
		return err
	}
	var authority PrivacyAuthority
	if err = c.DecodeStrict(raw, &authority); err != nil {
		return err
	}
	root, err := openPrivacyRoot(authority)
	if err != nil {
		return err
	}
	lock, err := privacyLock(ctx, root)
	if err != nil {
		root.Close()
		return err
	}
	defer lock.Close()
	if err = recoverPrivacyCheckpoint(ctx, root, authority, s.Journal, s.instance, s.directory); err != nil {
		root.Close()
		return err
	}
	if _, err = readPrivacy(root); err != nil {
		root.Close()
		return err
	}
	if err = root.MkdirAll("owners", 0700); err != nil {
		root.Close()
		return err
	}
	directory, err := filepath.EvalSymlinks(s.directory)
	if err == nil {
		directory, err = filepath.Abs(directory)
	}
	if err != nil {
		root.Close()
		return err
	}
	name := filepath.Join("owners", s.instance.ID+".lock")
	scope := privacyScope{InstanceID: s.instance.ID, Directory: directory, PhysicalRoot: s.instance.PhysicalRoot}
	if _, err = admitPrivacyCatalog(root, name, filepath.Join("owners", scopeName(scope))); err != nil {
		root.Close()
		return err
	}
	lease, err := fileguard.Lock(root, name)
	if err != nil {
		root.Close()
		return err
	}
	if err = publishPrivacyScope(root, scope); err != nil {
		lease.Close()
		root.Close()
		return err
	}
	s.privacy, s.privacyOwner = &authority, &privacyOwner{root: root, lease: lease, name: name}
	return nil
}
func (s *Session) ensurePrivacy(ctx context.Context) error {
	if s.privacy != nil {
		return nil
	}
	if err := s.Journal.Writable(); err != nil {
		return err
	}
	info, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return err
	}
	if !c.ValidDigest(info.FormatDigest) {
		return c.Fail(c.UnsupportedCapability, "privacy requires migrated store format")
	}
	return s.initializePrivacyAuthority(ctx)
}
func (s *Session) contentAvailable(task string) error {
	if s.privacy == nil {
		return nil
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return err
	}
	defer root.Close()
	// Records are atomically published, append-only and contiguous. Taking the
	// registry lock prevents a read racing a delete admission/owner registration.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return err
	}
	if _, deleted := view.Deletions[task]; deleted {
		return c.Fail(c.PolicyDenied, "task content was deleted; only the minimal journal remains accessible")
	}
	for _, attempt := range view.Attempts {
		if attempt.ChildTask == task {
			if _, deleted := view.Deletions[attempt.ParentTask]; deleted {
				return c.Fail(c.PolicyDenied, "deleted parent revoked its unpublished derived task")
			}
		}
	}
	return nil
}
func onlyPrivacyOwner(root *os.Root, current string) error {
	dir, err := root.Open("owners")
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(8193)
	err = errors.Join(func() error {
		if err == io.EOF {
			return nil
		}

		return err
	}(), dir.Close())
	if err != nil {
		return err
	}
	if len(names) > 8192 {
		return c.Fail(c.BudgetLimitReached, "privacy owner quota exceeded")
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".json") && c.ValidDigest(strings.TrimSuffix(name, ".json")) {
			continue
		}
		if !validPrivacyLeaseFilename(name) {
			return c.Fail(c.StoreIntegrityError, "foreign privacy owner record")
		}
		path := filepath.Join("owners", name)
		if path == current {
			continue
		}
		probe, err := fileguard.LockExisting(root, path)
		if err != nil {
			return c.Fail(c.StoreOwned, "another restored owner is active; close all family owners before deletion")
		}
		probe.Close()
	}
	return nil
}
