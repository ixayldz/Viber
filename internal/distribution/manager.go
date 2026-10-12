package distribution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
)

type Selection struct {
	SchemaVersion  int    `json:"schema_version"`
	Sequence       int    `json:"sequence"`
	Previous       string `json:"previous_digest,omitempty"`
	Action         string `json:"action"`
	Revision       string `json:"revision,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	ArchiveDigest  string `json:"archive_digest,omitempty"`
	Platform       string `json:"platform"`
	StoreFormat    int    `json:"store_format"`
	ReducerVersion int    `json:"reducer_version"`
}
type Status struct {
	SchemaVersion int       `json:"schema_version"`
	Active        Selection `json:"active"`
	ActiveDigest  string    `json:"active_digest,omitempty"`
	Installed     []string  `json:"installed"`
	Binary        string    `json:"binary,omitempty"`
	Channel       string    `json:"channel"`
	ReleaseReady  bool      `json:"release_ready"`
}
type Manager struct {
	root   *os.Root
	lock   *os.File
	prefix string
}

const MaxProgramBytes int64 = 512 << 20

func (m *Manager) inventory(name string, visit func(string, os.FileInfo) error) error {
	visited := 0
	return m.inventoryWalk(name, visit, &visited)
}
func (m *Manager) inventoryWalk(name string, visit func(string, os.FileInfo) error, visited *int) error {
	if strings.Count(filepath.ToSlash(name), "/") > 16 {
		return c.Fail(c.BudgetLimitReached, "program directory depth exceeded")
	}
	entries, err := readDirectory(m.root, name)
	if err != nil {
		return err
	}
	if len(entries) > 1024 {
		return c.Fail(c.BudgetLimitReached, "program directory entry bound exceeded")
	}
	for _, entry := range entries {
		*visited++
		if *visited > 16*MaxFiles+1100 {
			return c.Fail(c.BudgetLimitReached, "program total directory entry bound exceeded")
		}
		relative := filepath.Join(name, entry.Name())
		info, err := m.root.Lstat(relative)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return c.Fail(c.PolicyDenied, "program symlink denied")
		}
		if info.IsDir() {
			if err = m.inventoryWalk(relative, visit, visited); err != nil {
				return err
			}
		} else if info.Mode().IsRegular() {
			if err = visit(relative, info); err != nil {
				return err
			}
		} else {
			return c.Fail(c.PolicyDenied, "program special file denied")
		}
	}
	return nil
}

func (m *Manager) admission(bundle Bundle) error {
	var total int64
	count := 0
	if err := m.inventory(".", func(_ string, info os.FileInfo) error {
		count++
		if count > 16*MaxFiles+1024 || info.Size() > MaxProgramBytes-total {
			return c.Fail(c.BudgetLimitReached, "managed program bytes/entry bound exceeded")
		}
		total += info.Size()
		return nil
	}); err != nil {
		return err
	}
	var newBytes int64
	for _, raw := range bundle.Files {
		newBytes += int64(len(raw))
	}
	if newBytes > MaxProgramBytes-total {
		return c.Fail(c.BudgetLimitReached, "managed program disk quota exceeded")
	}
	available, err := fileguard.AvailableBytes(m.root)
	if err != nil {
		return err
	}
	if available < uint64(newBytes)+2<<20 {
		return c.Fail(c.BudgetLimitReached, "program update requires complete staging space and metadata margin")
	}
	return nil
}

func Open(prefix string) (*Manager, error) {
	abs, err := filepath.Abs(prefix)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(abs, 0700); err != nil {
			return nil, err
		}
		info, err = os.Lstat(abs)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, c.Fail(c.InvalidArgument, "fresh or existing managed install directory required")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	closeError := func(err error) (*Manager, error) { root.Close(); return nil, err }
	entries, err := readDirectory(root, ".")
	if err != nil {
		return closeError(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".publish-") && len(entry.Name()) == len(".publish-")+32 && entry.Type().IsRegular() {
			continue
		}
		if entry.Name() != "versions" && entry.Name() != "selections" && entry.Name() != "retirements" && entry.Name() != "stores" && entry.Name() != "update.lock" {
			return closeError(c.Fail(c.PolicyDenied, "install directory contains unmanaged files"))
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.Name() != "update.lock" && !entry.IsDir() || entry.Name() == "update.lock" && !entry.Type().IsRegular() {
			return closeError(c.Fail(c.PolicyDenied, "install directory contains replaced metadata"))
		}
	}
	if err = fileguard.Private(root); err != nil {
		return closeError(err)
	}
	lock, err := fileguard.Lock(root, "update.lock")
	if err != nil {
		return closeError(err)
	}
	m := &Manager{root: root, lock: lock, prefix: abs}
	for _, name := range []string{"versions", "selections", "retirements", "stores"} {
		if err = root.MkdirAll(name, 0700); err != nil {
			m.Close()
			return nil, err
		}
	}
	if _, err = m.Status(); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}
func (m *Manager) Close() error { return errors.Join(m.lock.Close(), m.root.Close()) }
func readDirectory(root *os.Root, name string) ([]os.DirEntry, error) {
	dir, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(1025)
	if err == io.EOF {
		err = nil
	}
	return entries, err
}
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "viber.exe"
	}
	return "viber"
}
func (m *Manager) version(revision, digest string) (Manifest, error) {
	var manifest Manifest
	if !Revision(revision) {
		return manifest, c.Fail(c.StoreIntegrityError, "invalid installed revision")
	}
	name := filepath.Join("versions", revision, "package.json")
	raw, err := fileguard.ReadRegular(m.root, name, 1<<20)
	if err != nil {
		return manifest, err
	}
	if c.DecodeStrict(raw, &manifest) != nil || manifest.Revision != revision || manifest.Platform != runtime.GOOS+"/"+runtime.GOARCH || manifest.SchemaVersion != 1 || manifest.ReleaseReady || manifest.Channel != "engineering" || c.HashBytes(raw) != digest || validateRuntimeCapabilities(manifest.RuntimeCapabilities) != nil {
		return manifest, c.Fail(c.StoreIntegrityError, "installed manifest identity changed")
	}
	if len(manifest.Files) > MaxFiles {
		return manifest, c.Fail(c.StoreIntegrityError, "installed inventory quota exceeded")
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Files {
		if !safeMember(entry.Path) || seen[strings.ToLower(entry.Path)] || entry.Bytes < 0 || entry.Bytes > MaxArchiveBytes || !c.ValidDigest(entry.Digest) {
			return manifest, c.Fail(c.StoreIntegrityError, "installed path/inventory invalid")
		}
		seen[strings.ToLower(entry.Path)] = true
		raw, err := fileguard.ReadRegular(m.root, filepath.Join("versions", revision, filepath.FromSlash(entry.Path)), MaxArchiveBytes)
		if err != nil || int64(len(raw)) != entry.Bytes || c.HashBytes(raw) != entry.Digest {
			return manifest, c.Fail(c.StoreIntegrityError, "installed bytes changed")
		}
	}
	if !seen[binaryName()] {
		return manifest, c.Fail(c.StoreIntegrityError, "installed binary missing")
	}
	return manifest, nil
}
func safeMember(name string) bool { return name != "package.json" && policy.SafePath(name) }
func (m *Manager) Status() (Status, error) {
	s := Status{SchemaVersion: 1, Installed: []string{}, Channel: "engineering"}
	entries, err := readDirectory(m.root, "selections")
	if err != nil {
		return s, err
	}
	if len(entries) > 1024 {
		return s, c.Fail(c.BudgetLimitReached, "selection history bound reached")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for i, entry := range entries {
		expected := fmt.Sprintf("%020d.json", i+1)
		if entry.Name() != expected || !entry.Type().IsRegular() {
			return s, c.Fail(c.StoreIntegrityError, "selection sequence/type invalid")
		}
		raw, err := fileguard.ReadRegular(m.root, filepath.Join("selections", expected), 16<<10)
		if err != nil {
			return s, err
		}
		var selected Selection
		if c.DecodeStrict(raw, &selected) != nil || selected.SchemaVersion != 1 || selected.Sequence != i+1 || selected.Previous != s.ActiveDigest || selected.Platform != runtime.GOOS+"/"+runtime.GOARCH || selected.StoreFormat != 2 || selected.ReducerVersion != c.ReducerVersion || selected.Action != "INSTALL" && selected.Action != "ROLLBACK" && selected.Action != "UNINSTALL" && selected.Action != "DEACTIVATE" {
			return s, c.Fail(c.StoreIntegrityError, "invalid selection chain")
		}
		if selected.Action == "UNINSTALL" || selected.Action == "DEACTIVATE" {
			if selected.Revision != "" || selected.ManifestDigest != "" || selected.ArchiveDigest != "" {
				return s, c.Fail(c.StoreIntegrityError, "uninstall contains active binding")
			}
		} else if !Revision(selected.Revision) || !c.ValidDigest(selected.ManifestDigest) || !c.ValidDigest(selected.ArchiveDigest) {
			return s, c.Fail(c.StoreIntegrityError, "selection package binding invalid")
		}
		s.Active = selected
		s.ActiveDigest = c.HashBytes(raw)
	}
	versions, err := readDirectory(m.root, "versions")
	if err != nil {
		return s, err
	}
	if len(versions) > 16 {
		return s, c.Fail(c.BudgetLimitReached, "installed version bound reached")
	}
	for _, entry := range versions {
		if !entry.IsDir() || !Revision(entry.Name()) {
			return s, c.Fail(c.StoreIntegrityError, "unmanaged version directory")
		}
		s.Installed = append(s.Installed, entry.Name())
	}
	sort.Strings(s.Installed)
	if s.Active.Revision != "" {
		manifest, err := m.version(s.Active.Revision, s.Active.ManifestDigest)
		if err != nil {
			return s, err
		}
		if !compatible(manifest) {
			return s, c.Fail(c.UnsupportedCapability, "active package store/reducer compatibility missing")
		}
		s.Binary = filepath.Join(m.prefix, "versions", s.Active.Revision, binaryName())
	}
	return s, nil
}
func compatible(m Manifest) bool {
	return m.StoreFormatMin > 0 && m.StoreFormatMin <= 2 && m.StoreFormatMax >= 2 && m.StoreFormatMax <= 2 && m.ReducerVersion == c.ReducerVersion && validateRuntimeCapabilities(m.RuntimeCapabilities) == nil
}
func (m *Manager) publish(state Status, selection Selection) (Status, error) {
	selection.SchemaVersion = 1
	selection.Sequence = state.Active.Sequence + 1
	selection.Previous = state.ActiveDigest
	selection.Platform = runtime.GOOS + "/" + runtime.GOARCH
	selection.StoreFormat = 2
	selection.ReducerVersion = c.ReducerVersion
	raw, err := c.CanonicalV1(selection)
	if err != nil {
		return state, err
	}
	if err = fileguard.Publish(m.root, filepath.Join("selections", fmt.Sprintf("%020d.json", selection.Sequence)), raw); err != nil {
		return state, err
	}
	return m.Status()
}

// Install only accepts the already-verified immutable bundle bytes. Verification
// is performed by the CLI on the same byte snapshot before this method is called.
func (m *Manager) Install(ctx context.Context, bundle Bundle) (state Status, resultErr error) {
	state, resultErr = m.Status()
	if resultErr != nil {
		return state, resultErr
	}
	manifest, err := verifiedBundleManifest(bundle)
	if err != nil {
		return state, err
	}
	locked, required, err := m.lockRegisteredStoreCapabilities(ctx)
	if err != nil {
		return state, err
	}
	defer func() { resultErr = errors.Join(resultErr, closeStoreLocks(locked)) }()
	if err = requireRuntimeCapabilities(manifest, required); err != nil {
		return state, err
	}
	return m.install(ctx, bundle, state)
}

func (m *Manager) install(ctx context.Context, bundle Bundle, state Status) (Status, error) {
	var err error
	if err = ctx.Err(); err != nil {
		return state, err
	}
	if !compatible(bundle.Manifest) {
		return state, c.Fail(c.UnsupportedCapability, "package requires explicit compatible store/reducer manifest; legacy archives cannot auto-update")
	}
	if _, lookupErr := m.root.Lstat(filepath.Join("retirements", bundle.Manifest.Revision+".json")); lookupErr == nil {
		return state, c.Fail(c.PolicyDenied, "retired revision requires a new managed prefix for explicit reinstall")
	} else if !errors.Is(lookupErr, os.ErrNotExist) {
		return state, lookupErr
	}
	if bundle.Manifest.Platform != runtime.GOOS+"/"+runtime.GOARCH || !Revision(bundle.Manifest.Revision) || c.HashBytes(bundle.Files["package.json"]) != bundle.ManifestDigest {
		return state, c.Fail(c.InvalidArgument, "verified native bundle required")
	}
	repair := false
	for _, revision := range state.Installed {
		if revision == bundle.Manifest.Revision {
			if _, err = m.version(revision, bundle.ManifestDigest); err != nil {
				repair = true
				break
			}
			if state.Active.Revision == revision {
				return state, nil
			}
			return m.publish(state, Selection{Action: "INSTALL", Revision: revision, ManifestDigest: bundle.ManifestDigest, ArchiveDigest: bundle.ArchiveDigest})
		}
	}
	if !repair && len(state.Installed) >= 16 {
		return state, c.Fail(c.BudgetLimitReached, "installed version bound reached; select or remove an owned version")
	}
	versionRoot := filepath.Join("versions", bundle.Manifest.Revision)
	if err = m.admission(bundle); err != nil {
		return state, err
	}
	if !repair {
		err = m.root.Mkdir(versionRoot, 0700)
	}
	if err != nil && !repair {
		return state, err
	}
	if repair {
		if err = m.inventory(versionRoot, func(name string, info os.FileInfo) error {
			relative, err := filepath.Rel(versionRoot, name)
			if err != nil {
				return err
			}
			expected, ok := bundle.Files[filepath.ToSlash(relative)]
			if !ok {
				return c.Fail(c.Conflict, "interrupted version contains unowned files")
			}
			actual, err := fileguard.ReadRegular(m.root, name, MaxArchiveBytes)
			if err != nil || info.Size() != int64(len(expected)) || c.HashBytes(actual) != c.HashBytes(expected) {
				return c.Fail(c.Conflict, "interrupted version has changed bytes; automatic overwrite denied")
			}
			return nil
		}); err != nil {
			return state, err
		}
	}
	// Files are immutable no-replace publications. A crash never changes active
	// selection; an interrupted version is quarantined until exact-byte repair.
	names := make([]string, 0, len(bundle.Files))
	for name := range bundle.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			return state, err
		}
		if name != "package.json" && !safeMember(name) {
			return state, c.Fail(c.InvalidArgument, "bundle member denied")
		}
		target := filepath.Join(versionRoot, filepath.FromSlash(name))
		if repair {
			if _, lookupErr := m.root.Lstat(target); lookupErr == nil {
				if name == binaryName() {
					if err = m.root.Chmod(target, 0700); err != nil {
						return state, err
					}
				}
				continue
			} else if !errors.Is(lookupErr, os.ErrNotExist) {
				return state, lookupErr
			}
		}
		if err = fileguard.Publish(m.root, target, bundle.Files[name]); err != nil {
			return state, err
		}
		if name == binaryName() {
			if err = m.root.Chmod(target, 0700); err != nil {
				return state, err
			}
		}
	}
	if _, err = m.version(bundle.Manifest.Revision, bundle.ManifestDigest); err != nil {
		return state, err
	}
	return m.publish(state, Selection{Action: "INSTALL", Revision: bundle.Manifest.Revision, ManifestDigest: bundle.ManifestDigest, ArchiveDigest: bundle.ArchiveDigest})
}
func (m *Manager) Rollback(revision string) (state Status, resultErr error) {
	state, resultErr = m.Status()
	if resultErr != nil {
		return state, resultErr
	}
	locked, required, err := m.lockRegisteredStoreCapabilities(context.Background())
	if err != nil {
		return state, err
	}
	defer func() { resultErr = errors.Join(resultErr, closeStoreLocks(locked)) }()
	return m.rollback(revision, state, required)
}

func (m *Manager) rollback(revision string, state Status, required []string) (Status, error) {
	var err error
	if !Revision(revision) {
		return state, c.Fail(c.InvalidArgument, "exact installed rollback revision required")
	}
	raw, err := fileguard.ReadRegular(m.root, filepath.Join("versions", revision, "package.json"), 1<<20)
	if err != nil {
		return state, err
	}
	digest := c.HashBytes(raw)
	manifest, err := m.version(revision, digest)
	if err != nil {
		return state, err
	}
	if !compatible(manifest) {
		return state, c.Fail(c.UnsupportedCapability, "rollback incompatible with current store/reducer")
	}
	if err = requireRuntimeCapabilities(manifest, required); err != nil {
		return state, err
	}
	archive := ""
	for i := 1; i <= state.Active.Sequence; i++ {
		raw, err = fileguard.ReadRegular(m.root, filepath.Join("selections", fmt.Sprintf("%020d.json", i)), 16<<10)
		if err != nil {
			return state, err
		}
		var selection Selection
		if c.DecodeStrict(raw, &selection) != nil {
			return state, c.Fail(c.StoreIntegrityError, "invalid retained selection")
		}
		if selection.Revision == revision && selection.ManifestDigest == digest {
			archive = selection.ArchiveDigest
		}
	}
	if archive == "" {
		return state, c.Fail(c.PolicyDenied, "rollback target has no verified install receipt")
	}
	return m.publish(state, Selection{Action: "ROLLBACK", Revision: revision, ManifestDigest: digest, ArchiveDigest: archive})
}

// Deactivate removes program selection, leaving version archives and the audit
// chain available for explicit reinstall. It never deletes task-store data.
func (m *Manager) Deactivate() (Status, error) {
	state, err := m.Status()
	if err != nil {
		return state, err
	}
	return m.publish(state, Selection{Action: "DEACTIVATE"})
}
