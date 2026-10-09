package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	root  *os.Root
	lease *os.File
	name  string
}

func (o *privacyOwner) Close() error {
	if o == nil || o.root == nil {
		return nil
	}
	err := o.lease.Close()
	o.root = closePrivacyRoot(o.root, &err)
	return err
}
func closePrivacyRoot(root *os.Root, err *error) *os.Root {
	*err = errors.Join(*err, root.Close())
	return nil
}

type ManagedCopy struct {
	Kind         string       `json:"kind"`
	Directory    string       `json:"directory"`
	PhysicalRoot string       `json:"physical_root"`
	Files        []BackupFile `json:"files"`
	Tasks        []string     `json:"tasks"`
}
type privacyRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Sequence      int64            `json:"sequence"`
	Previous      string           `json:"previous"`
	Type          string           `json:"type"`
	Copy          *ManagedCopy     `json:"copy,omitempty"`
	Command       *DeletionCommand `json:"command,omitempty"`
	Attempt       *privacyAttempt  `json:"attempt,omitempty"`
}
type privacyView struct {
	Sequence  int64
	Tail      string
	Watermark int64
	Copies    []ManagedCopy
	Deletions map[string]DeletionCommand
	Attempts  []privacyAttempt
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
	view := privacyView{Deletions: map[string]DeletionCommand{}, Copies: []ManagedCopy{}}
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
	for _, name := range names {
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
			if record.Copy == nil || record.Command != nil || record.Attempt != nil || validateManagedCopy(*record.Copy) != nil {
				return view, c.Fail(c.StoreIntegrityError, "managed copy record invalid")
			}
			replaced := false
			for i, prior := range view.Copies {
				if prior.Directory != record.Copy.Directory {
					continue
				}
				if len(prior.Files) != 0 || prior.Kind != record.Copy.Kind || prior.PhysicalRoot != record.Copy.PhysicalRoot || !sameManagedTasks(prior.Tasks, record.Copy.Tasks) {
					return view, c.Fail(c.StoreIntegrityError, "managed allocation cannot be rebound")
				}
				view.Copies[i] = *record.Copy
				replaced = true
				break
			}
			if !replaced {
				view.Copies = append(view.Copies, *record.Copy)
			}
		case "DELETE_INTENT":
			if record.Command == nil || record.Copy != nil || record.Attempt != nil || validateDeletionCommand(*record.Command) != nil {
				return view, c.Fail(c.StoreIntegrityError, "privacy intent invalid")
			}
			command := *record.Command
			if _, exists := view.Deletions[command.Plan.TaskID]; exists || command.Plan.Watermark != view.Watermark+1 {
				return view, c.Fail(c.StoreIntegrityError, "privacy intent duplicate or watermark gap")
			}
			view.Watermark++
			view.Deletions[command.Plan.TaskID] = command
		case "ATTEMPT_LINEAGE":
			if record.Attempt == nil || record.Command != nil || record.Copy != nil || record.Attempt.validate() != nil {
				return view, c.Fail(c.StoreIntegrityError, "invalid durable attempt lineage")
			}
			for _, prior := range view.Attempts {
				if prior.Scope.PhysicalRoot == record.Attempt.Scope.PhysicalRoot && prior.ChildTask == record.Attempt.ChildTask {
					return view, c.Fail(c.StoreIntegrityError, "attempt lineage identity was registered twice")
				}
			}
			if _, deleted := view.Deletions[record.Attempt.ParentTask]; deleted {
				return view, c.Fail(c.StoreIntegrityError, "attempt was allocated after parent deletion")
			}
			if _, deleted := view.Deletions[record.Attempt.ChildTask]; deleted {
				return view, c.Fail(c.StoreIntegrityError, "attempt was allocated after child deletion")
			}
			view.Attempts = append(view.Attempts, *record.Attempt)
		default:
			return view, c.Fail(c.StoreIntegrityError, "unknown privacy journal record")
		}
		view.Sequence = record.Sequence
		view.Tail = c.HashBytes(raw)
	}
	return view, nil
}
func appendPrivacy(root *os.Root, view privacyView, record privacyRecord) error {
	record.SchemaVersion, record.Sequence, record.Previous = 1, view.Sequence+1, view.Tail
	if record.Sequence > 2048 {
		return c.Fail(c.BudgetLimitReached, "privacy journal full")
	}
	raw, err := c.CanonicalV1(record)
	if err != nil {
		return err
	}
	if len(raw) > 8<<20 {
		return c.Fail(c.BudgetLimitReached, "privacy record too large")
	}
	// Enforce the aggregate budget before publication, including temporary bytes.
	dir, err := root.Open("records")
	if err != nil {
		return err
	}
	entries, err := dir.Readdir(2049)
	err = errors.Join(err, dir.Close())
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	total := int64(len(raw)) * 2
	for _, entry := range entries {
		total += entry.Size()
	}
	if total > 64<<20 {
		return c.Fail(c.BudgetLimitReached, "privacy metadata capacity exhausted")
	}
	return fileguard.Publish(root, filepath.Join("records", fmt.Sprintf("%010d.json", record.Sequence)), raw)
}
func validateManagedCopy(copy ManagedCopy) error {
	if (copy.Kind != "BACKUP" && copy.Kind != "RESTORE_STAGE" && copy.Kind != "EXPORT" && copy.Kind != "DELIVERY_PREVIEW" && copy.Kind != "SUPPORT") || !filepath.IsAbs(copy.Directory) || len(copy.Directory) > 2048 || !c.ValidDigest(copy.PhysicalRoot) || len(copy.Files) > backupMaxFiles+1 || len(copy.Tasks) > 4096 {
		return c.Fail(c.StoreIntegrityError, "invalid managed copy")
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
	if kind == "SUPPORT" {
		return name == "support.json"
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
	if _, err = readPrivacy(root); err != nil {
		root.Close()
		return err
	}
	if err = root.MkdirAll("owners", 0700); err != nil {
		root.Close()
		return err
	}
	name := filepath.Join("owners", s.instance.ID+".lock")
	lease, err := fileguard.Lock(root, name)
	if err != nil {
		root.Close()
		return err
	}
	directory, err := filepath.EvalSymlinks(s.directory)
	if err == nil {
		directory, err = filepath.Abs(directory)
	}
	if err == nil {
		err = publishPrivacyScope(root, privacyScope{s.instance.ID, directory, s.instance.PhysicalRoot})
	}
	if err != nil {
		lease.Close()
		root.Close()
		return err
	}
	s.privacy, s.privacyOwner = &authority, &privacyOwner{root, lease, name}
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
	directory, err := fileguard.ResolveProspective(filepath.Join(filepath.Dir(s.directory), ".viber-privacy-"+newID("")))
	if err != nil {
		return err
	}
	// An unbound pre-existing directory is never adopted as current authority.
	root, err := freshPrivate(directory)
	if err != nil {
		return c.Fail(c.PolicyDenied, "privacy authority initialization requires a fresh external directory")
	}
	defer root.Close()
	id, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return err
	}
	authority := PrivacyAuthority{1, newID(""), directory, id}
	if err = root.Mkdir("records", 0700); err != nil {
		return err
	}
	raw, err := c.CanonicalV1(authority)
	if err != nil {
		return err
	}
	if err = fileguard.Publish(root, "authority.json", raw); err != nil {
		return err
	}
	if err = s.Journal.BindPrivacyAuthority(ctx, raw); err != nil {
		return err
	}
	return s.attachPrivacy(ctx)
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
		if len(name) != 37 || !strings.HasSuffix(name, ".lock") {
			return c.Fail(c.StoreIntegrityError, "foreign privacy owner record")
		}
		path := filepath.Join("owners", name)
		if path == current {
			continue
		}
		probe, err := fileguard.Lock(root, path)
		if err != nil {
			return c.Fail(c.StoreOwned, "another restored owner is active; close all family owners before deletion")
		}
		probe.Close()
	}
	return nil
}
