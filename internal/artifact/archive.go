// Package artifact implements task-scoped immutable snapshot archives.
// Digest identifies bytes; callers still need task authorization. This API is
// trusted-kernel only, and is not exposed as a model-readable blob directory.
package artifact

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

var validTask = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Ref struct {
	SchemaVersion  int    `json:"schema_version"`
	TaskID         string `json:"task_id"`
	SnapshotDigest string `json:"snapshot_digest"`
}
type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	TaskID        string            `json:"task_id"`
	Snapshot      c.Snapshot        `json:"snapshot"`
	IndexBlob     string            `json:"index_blob"`
	IgnoreBlobs   map[string]string `json:"ignore_blobs"`
}
type Archive struct {
	mu     sync.Mutex
	root   *os.Root
	lock   *os.File
	closed bool
}

func Open(directory string) (*Archive, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err == nil && !info.IsDir() {
		return nil, c.Fail(c.PolicyDenied, "archive root must be a real directory")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	if err = fileguard.Private(root); err != nil {
		root.Close()
		return nil, err
	}
	lock, err := fileguard.Lock(root, "archive.lock")
	if err != nil {
		root.Close()
		return nil, err
	}
	return &Archive{root: root, lock: lock}, nil
}
func (a *Archive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	return errors.Join(a.lock.Close(), a.root.Close())
}
func validateRef(ref Ref) error {
	if ref.SchemaVersion != c.SchemaVersion || !taskValid(ref.TaskID) || !c.ValidDigest(ref.SnapshotDigest) {
		return c.Fail(c.InvalidArgument, "invalid scoped snapshot reference")
	}
	return nil
}
func blobPath(task, hash string) string { return filepath.Join("tasks", task, "blobs", hash[:2], hash) }
func manifestPath(ref Ref) string {
	return filepath.Join("tasks", ref.TaskID, "snapshots", ref.SnapshotDigest+".json")
}
func (a *Archive) publish(name string, raw []byte) error {
	old, err := fileguard.ReadRegular(a.root, name, 64<<20)
	if err == nil {
		if string(old) != string(raw) {
			return c.Fail(c.StoreIntegrityError, "immutable artifact collision")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fileguard.Publish(a.root, name, raw)
}
func (a *Archive) putBlob(task string, raw []byte) (string, error) {
	if len(raw) > 64<<20 {
		return "", c.Fail(c.InvalidArgument, "blob size exceeds archive profile")
	}
	hash := c.HashBytes(raw)
	return hash, a.publish(blobPath(task, hash), raw)
}
func (a *Archive) Put(task string, capture workspace.Capture) (Ref, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ref := Ref{SchemaVersion: c.SchemaVersion, TaskID: task, SnapshotDigest: capture.Snapshot.Digest}
	if a.closed {
		return ref, os.ErrClosed
	}
	if err := validateRef(ref); err != nil {
		return ref, err
	}
	if !fileguard.Disjoint(a.root.Name(), capture.Snapshot.Root) {
		return ref, c.Fail(c.PolicyDenied, "archive and source must not overlap")
	}
	if err := workspace.VerifyCapture(capture); err != nil {
		return ref, err
	}
	manifest := Manifest{SchemaVersion: c.SchemaVersion, TaskID: task, Snapshot: capture.Snapshot, IgnoreBlobs: map[string]string{}}
	for _, entry := range capture.Snapshot.Entries {
		hash, err := a.putBlob(task, capture.Contents[entry.Path])
		if err != nil {
			return ref, err
		}
		if hash != entry.Hash {
			return ref, c.Fail(c.StoreIntegrityError, "capture changed while publishing")
		}
	}
	if capture.Snapshot.Git != nil {
		if capture.Snapshot.Git.IndexPresent {
			hash, err := a.putBlob(task, capture.IndexBytes)
			if err != nil {
				return ref, err
			}
			manifest.IndexBlob = hash
		}
		for name, raw := range capture.IgnoreSources {
			hash, err := a.putBlob(task, raw)
			if err != nil {
				return ref, err
			}
			manifest.IgnoreBlobs[name] = hash
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return ref, err
	}
	if len(raw) > 8<<20 {
		return ref, c.Fail(c.InvalidArgument, "archive manifest quota exceeded")
	}
	// All referenced bytes are published before this manifest becomes visible.
	return ref, a.publish(manifestPath(ref), raw)
}
func (a *Archive) Get(ref Ref) (workspace.Capture, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return workspace.Capture{}, os.ErrClosed
	}
	return a.get(ref)
}
func (a *Archive) get(ref Ref) (workspace.Capture, error) {
	if err := validateRef(ref); err != nil {
		return workspace.Capture{}, err
	}
	raw, err := fileguard.ReadRegular(a.root, manifestPath(ref), 8<<20)
	if err != nil {
		return workspace.Capture{}, err
	}
	var manifest Manifest
	if err = c.DecodeStrict(raw, &manifest); err != nil {
		return workspace.Capture{}, err
	}
	if manifest.SchemaVersion != c.SchemaVersion || manifest.TaskID != ref.TaskID || manifest.Snapshot.Digest != ref.SnapshotDigest {
		return workspace.Capture{}, c.Fail(c.StoreIntegrityError, "scoped manifest binding mismatch")
	}
	result := workspace.Capture{Snapshot: manifest.Snapshot, Contents: map[string][]byte{}}
	// Validate all size/path envelopes before hydrating any potentially huge set.
	if len(manifest.Snapshot.Entries) > 100000 || len(manifest.IgnoreBlobs) > 10000 {
		return workspace.Capture{}, c.Fail(c.StoreIntegrityError, "manifest quota exceeded")
	}
	total := int64(0)
	for _, entry := range result.Snapshot.Entries {
		if !c.ValidDigest(entry.Hash) || entry.Size < 0 || entry.Size > 64<<20 || entry.Size > 1<<30-total {
			return workspace.Capture{}, c.Fail(c.StoreIntegrityError, "invalid archive blob envelope")
		}
		total += entry.Size
		data, err := a.readBlob(ref.TaskID, entry.Hash, entry.Size)
		if err != nil {
			return workspace.Capture{}, err
		}
		result.Contents[entry.Path] = data
	}
	if manifest.IndexBlob != "" {
		result.IndexBytes, err = a.readBlob(ref.TaskID, manifest.IndexBlob, 32<<20)
		if err != nil {
			return workspace.Capture{}, err
		}
	}
	if result.Snapshot.Git != nil {
		result.IgnoreSources = map[string][]byte{}
		for name, hash := range manifest.IgnoreBlobs {
			raw, err := a.readBlob(ref.TaskID, hash, 1<<20)
			if err != nil {
				return workspace.Capture{}, err
			}
			result.IgnoreSources[name] = raw
		}
	} else if len(manifest.IgnoreBlobs) != 0 {
		return workspace.Capture{}, c.Fail(c.StoreIntegrityError, "unbound ignore artifacts")
	}
	if err = workspace.VerifyCapture(result); err != nil {
		return workspace.Capture{}, err
	}
	return result, nil
}
func (a *Archive) readBlob(task, hash string, limit int64) ([]byte, error) {
	if !c.ValidDigest(hash) {
		return nil, c.Fail(c.StoreIntegrityError, "invalid blob digest")
	}
	raw, err := fileguard.ReadRegular(a.root, blobPath(task, hash), limit)
	if err != nil {
		return nil, c.Fail(c.StoreIntegrityError, "referenced blob missing or unreadable")
	}
	if c.HashBytes(raw) != hash {
		return nil, c.Fail(c.StoreIntegrityError, "referenced blob checksum mismatch")
	}
	return raw, nil
}

type Materialized struct {
	Ref                    Ref    `json:"ref"`
	SourceDirectory        string `json:"source_directory"`
	ManifestDigest         string `json:"manifest_digest"`
	SourceReadOnlyEnforced bool   `json:"source_read_only_enforced"`
}

// Materialize writes a fresh source directory under the private archive. The
// manifest is immutable; host readonly enforcement is NOT claimed. An execution
// backend must mount this exact directory readonly before trusted verification.
func (a *Archive) Materialize(ref Ref) (Materialized, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return Materialized{}, os.ErrClosed
	}
	capture, err := a.get(ref)
	if err != nil {
		return Materialized{}, err
	}
	if !fileguard.Disjoint(a.root.Name(), capture.Snapshot.Root) {
		return Materialized{}, c.Fail(c.PolicyDenied, "source and archive overlap")
	}
	target := filepath.Join("tasks", ref.TaskID, "candidates", ref.SnapshotDigest)
	result := Materialized{Ref: ref, SourceDirectory: filepath.Join(a.root.Name(), target, "source"), ManifestDigest: ref.SnapshotDigest}
	if _, err = a.root.Lstat(target); err == nil {
		return result, a.validateMaterialized(target, capture)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return result, err
	}
	stage := filepath.Join("tasks", ref.TaskID, "candidates", ".prepare-"+hex.EncodeToString(nonce[:]))
	if err = a.root.MkdirAll(filepath.Join(stage, "source"), 0755); err != nil {
		return result, err
	}
	// No recursive cleanup: a failed prepare remains an explicitly unpublished
	// orphan for later retention/reconciliation, never a valid candidate pointer.
	for _, dir := range capture.Snapshot.Directories {
		if err = a.root.MkdirAll(filepath.Join(stage, "source", filepath.FromSlash(dir)), 0755); err != nil {
			return result, err
		}
	}
	for _, entry := range capture.Snapshot.Entries {
		name := filepath.Join(stage, "source", filepath.FromSlash(entry.Path))
		mode := os.FileMode(entry.Mode)
		if capture.Snapshot.Git != nil {
			for _, tracked := range capture.Snapshot.Git.Entries {
				if tracked.Path == entry.Path {
					mode = os.FileMode(tracked.Mode & 0777)
					break
				}
			}
		}
		// Export permissions allow the non-root container UID to read source;
		// original modes remain in the immutable capture manifest. The enclosing
		// private archive ACL prevents host-wide source disclosure.
		mode = (mode & 0111) | 0444
		f, err := a.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return result, err
		}
		_, err = f.Write(capture.Contents[entry.Path])
		if err == nil {
			err = f.Chmod(mode)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return result, err
		}
	}
	raw, err := json.Marshal(ref)
	if err != nil {
		return result, err
	}
	if err = fileguard.Publish(a.root, filepath.Join(stage, "manifest.json"), raw); err != nil {
		return result, err
	}
	dirs := append([]string{}, capture.Snapshot.Directories...)
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if err = fileguard.SyncDirectory(a.root, filepath.Join(stage, "source", filepath.FromSlash(dir))); err != nil {
			return result, err
		}
	}
	if err = fileguard.SyncDirectory(a.root, filepath.Join(stage, "source")); err != nil {
		return result, err
	}
	if err = fileguard.SyncDirectory(a.root, stage); err != nil {
		return result, err
	}
	if err = a.validateMaterialized(stage, capture); err != nil {
		return result, err
	}
	if err = fileguard.RenameNoReplace(a.root, stage, target); err != nil {
		return result, err
	}
	if err = fileguard.SyncParents(a.root, filepath.Dir(target)); err != nil {
		return result, err
	}
	return result, a.validateMaterialized(target, capture)
}
func (a *Archive) validateMaterialized(candidate string, capture workspace.Capture) error {
	source, err := a.root.OpenRoot(filepath.Join(candidate, "source"))
	if err != nil {
		return err
	}
	defer source.Close()
	files := map[string]c.Entry{}
	directories := map[string]bool{}
	for _, entry := range capture.Snapshot.Entries {
		files[entry.Path] = entry
	}
	for _, dir := range capture.Snapshot.Directories {
		directories[dir] = true
	}
	count, dirCount := 0, 0
	err = fs.WalkDir(source.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if d.IsDir() {
			if !directories[name] {
				return c.Fail(c.StoreIntegrityError, "unexpected candidate directory")
			}
			dirCount++
			return nil
		}
		entry, ok := files[name]
		if !ok {
			return c.Fail(c.StoreIntegrityError, "unexpected candidate file")
		}
		raw, err := fileguard.ReadRegular(source, filepath.FromSlash(name), entry.Size)
		if err != nil {
			return err
		}
		info, err := source.Stat(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		expectedMode := os.FileMode(entry.Mode)
		if capture.Snapshot.Git != nil {
			for _, tracked := range capture.Snapshot.Git.Entries {
				if tracked.Path == entry.Path {
					expectedMode = os.FileMode(tracked.Mode & 0777)
					break
				}
			}
		}
		expectedMode = (expectedMode & 0111) | 0444
		if runtime.GOOS != "windows" && info.Mode().Perm() != expectedMode || runtime.GOOS == "windows" && info.Mode().Perm()&0222 != 0 {
			return c.Fail(c.StoreIntegrityError, "candidate permissions diverged from frozen export")
		}
		if c.HashBytes(raw) != entry.Hash {
			return c.Fail(c.StoreIntegrityError, "candidate bytes diverged from frozen manifest")
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count != len(files) || dirCount != len(directories) {
		return c.Fail(c.StoreIntegrityError, "candidate coverage incomplete")
	}
	raw, err := fileguard.ReadRegular(a.root, filepath.Join(candidate, "manifest.json"), 4096)
	if err != nil {
		return err
	}
	var ref Ref
	if err = c.DecodeStrict(raw, &ref); err != nil {
		return err
	}
	if validateRef(ref) != nil || ref.TaskID != strings.Split(filepath.ToSlash(candidate), "/")[1] || ref.SnapshotDigest != capture.Snapshot.Digest {
		return c.Fail(c.StoreIntegrityError, "candidate manifest binding mismatch")
	}
	return nil
}

func (a *Archive) List(task string) ([]Ref, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, os.ErrClosed
	}
	if !taskValid(task) {
		return nil, c.Fail(c.InvalidArgument, "invalid task scope")
	}
	directory, err := a.root.Open(filepath.Join("tasks", task, "snapshots"))
	if errors.Is(err, os.ErrNotExist) {
		return []Ref{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	names, err := directory.Readdirnames(100001)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(names) > 100000 {
		return nil, c.Fail(c.StoreIntegrityError, "archive list quota exceeded")
	}
	sort.Strings(names)
	refs := []Ref{}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("unexpected archive entry")
		}
		ref := Ref{SchemaVersion: c.SchemaVersion, TaskID: task, SnapshotDigest: strings.TrimSuffix(name, ".json")}
		if err := validateRef(ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// PutBytes/GetBytes are trusted-owner, task-scoped document/artifact operations.
// They cannot hydrate a different task merely because its digest is known.
func (a *Archive) PutBytes(task string, raw []byte) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", os.ErrClosed
	}
	if !taskValid(task) || len(raw) > 8<<20 {
		return "", c.Fail(c.InvalidArgument, "invalid scoped document")
	}
	return a.putBlob(task, raw)
}
func (a *Archive) GetBytes(task, digest string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, os.ErrClosed
	}
	if !taskValid(task) {
		return nil, c.Fail(c.PolicyDenied, "invalid task scope")
	}
	return a.readBlob(task, digest, 8<<20)
}

func taskValid(task string) bool { return validTask.MatchString(task) && policy.SafePath(task) }
