// Package workspace currently provides a bounded, non-mutating engineering
// preview. It is not an OS sandbox and is not exposed as an untrusted tool.
package workspace

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

func DefaultLimits() Limits {
	return Limits{MaxFiles: 10000, MaxFileBytes: 2 << 20, MaxTotalBytes: 32 << 20}
}

type Capture struct {
	Snapshot c.Snapshot        `json:"snapshot"`
	Contents map[string][]byte `json:"-"`
}

var excludedDirs = map[string]bool{".git": true, "node_modules": true, ".tools": true, ".cache": true, ".viber": true, ".viber-local": true, ".aws": true, ".ssh": true, ".venv": true, "vendor": true, "dist": true, "build": true, "bin": true}

func excludedFile(name string) bool {
	n := strings.ToLower(name)
	return n == ".env" || strings.HasPrefix(n, ".env.") || strings.HasSuffix(n, ".pem") || strings.HasSuffix(n, ".key") || strings.HasSuffix(n, ".p12") || strings.HasSuffix(n, ".sqlite") || strings.HasSuffix(n, ".sqlite-wal") || strings.HasSuffix(n, ".sqlite-shm")
}

// CaptureDirectory performs two exact scans, then compares all manifest bytes.
// Matching scans remain BEST_EFFORT; they do not prove historical atomicity.
func CaptureDirectory(root string, limits Limits) (Capture, error) {
	if limits.MaxFiles <= 0 || limits.MaxFiles > 100000 || limits.MaxFileBytes <= 0 || limits.MaxFileBytes > 64<<20 || limits.MaxTotalBytes <= 0 || limits.MaxTotalBytes > 1<<30 {
		return Capture{}, c.Fail(c.InvalidArgument, "positive capture limits required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Capture{}, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Capture{}, err
	}
	rootInfo, err := os.Stat(canonical)
	if err != nil {
		return Capture{}, err
	}
	if !rootInfo.IsDir() {
		return Capture{}, c.Fail(c.InvalidArgument, "capture root must be a directory")
	}
	first, err := scan(canonical, limits)
	if err != nil {
		return Capture{}, err
	}
	second, err := scan(canonical, limits)
	if err != nil {
		return Capture{}, err
	}
	if first.Snapshot.Digest != second.Snapshot.Digest {
		return Capture{}, c.Fail(c.Conflict, "workspace changed during bounded capture")
	}
	return second, nil
}
func scan(root string, limits Limits) (Capture, error) {
	result := Capture{Snapshot: c.Snapshot{SchemaVersion: c.SchemaVersion, Root: root, Consistency: "BEST_EFFORT", Entries: []c.Entry{}, Exclusions: []string{}, Directories: []string{}}, Contents: map[string][]byte{}}
	total := int64(0)
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		relative, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !policy.SafePath(relative) {
			return c.Fail(c.PolicyDenied, "unsafe or nonportable path in capture")
		}
		key := strings.ToLower(relative)
		if seen[key] {
			return c.Fail(c.UnsupportedCapability, "case alias collision")
		}
		seen[key] = true
		if d.IsDir() && excludedDirs[strings.ToLower(d.Name())] {
			result.Snapshot.Exclusions = append(result.Snapshot.Exclusions, relative+"/")
			return filepath.SkipDir
		}
		if d.IsDir() {
			if len(result.Snapshot.Directories) >= limits.MaxFiles {
				return c.Fail(c.UnsupportedCapability, "directory quota exceeded")
			}
			result.Snapshot.Directories = append(result.Snapshot.Directories, relative)
			return nil
		}
		if excludedFile(d.Name()) {
			result.Snapshot.Exclusions = append(result.Snapshot.Exclusions, relative)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return c.Fail(c.UnsupportedCapability, "symlink, reparse or special entry requires capture backend")
		}
		if len(result.Snapshot.Entries) >= limits.MaxFiles {
			return c.Fail(c.UnsupportedCapability, "capture file quota exceeded")
		}
		if info.Size() > limits.MaxFileBytes || info.Size() > limits.MaxTotalBytes-total {
			return c.Fail(c.UnsupportedCapability, "capture byte quota exceeded")
		}
		file, err := os.Open(p)
		if err != nil {
			return err
		}
		opened, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			file.Close()
			return c.Fail(c.Conflict, "capture path changed")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, limits.MaxFileBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(len(data)) > limits.MaxFileBytes || int64(len(data)) > limits.MaxTotalBytes-total {
			return c.Fail(c.UnsupportedCapability, "capture byte quota exceeded during read")
		}
		// Binary source can be represented later by an explicit scoped backend.
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			result.Snapshot.Exclusions = append(result.Snapshot.Exclusions, relative)
			return nil
		}
		total += int64(len(data))
		result.Contents[relative] = data
		result.Snapshot.Entries = append(result.Snapshot.Entries, c.Entry{Path: relative, Hash: c.HashBytes(data), Size: int64(len(data)), Mode: uint32(info.Mode().Perm())})
		return nil
	})
	if err != nil {
		return Capture{}, err
	}
	err = seal(&result.Snapshot)
	return result, err
}
func seal(s *c.Snapshot) error {
	if s.Entries == nil {
		s.Entries = []c.Entry{}
	}
	if s.Exclusions == nil {
		s.Exclusions = []string{}
	}
	if s.Directories == nil {
		s.Directories = []string{}
	}
	sort.Slice(s.Entries, func(i, j int) bool { return s.Entries[i].Path < s.Entries[j].Path })
	sort.Strings(s.Exclusions)
	sort.Strings(s.Directories)
	s.Digest = ""
	d, err := c.Digest(*s)
	if err != nil {
		return err
	}
	s.Digest = d
	return nil
}
func VerifyCapture(capture Capture) error {
	s := capture.Snapshot
	s.Entries = append([]c.Entry(nil), s.Entries...)
	s.Exclusions = append([]string(nil), s.Exclusions...)
	s.Directories = append([]string(nil), s.Directories...)
	if s.SchemaVersion != c.SchemaVersion || s.Consistency != "BEST_EFFORT" || !c.ValidDigest(s.Digest) {
		return c.Fail(c.InvalidArgument, "invalid snapshot")
	}
	expected := s.Digest
	if err := seal(&s); err != nil {
		return err
	}
	if s.Digest != expected {
		return c.Fail(c.StoreIntegrityError, "snapshot digest mismatch")
	}
	seen := map[string]bool{}
	for _, e := range s.Entries {
		if !policy.SafePath(e.Path) || seen[strings.ToLower(e.Path)] {
			return c.Fail(c.InvalidArgument, "invalid or duplicate snapshot path")
		}
		seen[strings.ToLower(e.Path)] = true
		data, ok := capture.Contents[e.Path]
		if !ok || int64(len(data)) != e.Size || c.HashBytes(data) != e.Hash {
			return c.Fail(c.StoreIntegrityError, "snapshot content unavailable or corrupt")
		}
	}
	if len(capture.Contents) != len(s.Entries) {
		return c.Fail(c.StoreIntegrityError, "unmanifested snapshot content")
	}
	return nil
}
func ListingDigest(s c.Snapshot, directory string) (string, error) {
	if directory != "." && !policy.SafePath(directory) {
		return "", c.Fail(c.InvalidArgument, "invalid listing path")
	}
	paths := []string{}
	prefix := ""
	if directory != "." {
		prefix = directory + "/"
	}
	for _, entry := range s.Entries {
		if strings.HasPrefix(entry.Path, prefix) {
			paths = append(paths, entry.Path)
		}
	}
	for _, dir := range s.Directories {
		if strings.HasPrefix(dir, prefix) {
			paths = append(paths, "directory:"+dir)
		}
	}
	for _, excluded := range s.Exclusions {
		if strings.HasPrefix(excluded, prefix) {
			paths = append(paths, "excluded:"+excluded)
		}
	}
	sort.Strings(paths)
	return c.Digest(paths)
}

func entryMap(s c.Snapshot) map[string]c.Entry {
	m := map[string]c.Entry{}
	for _, e := range s.Entries {
		m[e.Path] = e
	}
	return m
}
func withinExcluded(s c.Snapshot, p string) bool {
	for _, e := range s.Exclusions {
		if p == e || strings.HasSuffix(e, "/") && strings.HasPrefix(p, e) {
			return true
		}
	}
	for _, part := range strings.Split(p, "/") {
		if excludedDirs[strings.ToLower(part)] || excludedFile(part) {
			return true
		}
	}
	return false
}
func capturedPathExists(s c.Snapshot, p string) bool {
	for _, e := range s.Entries {
		if strings.EqualFold(e.Path, p) || strings.HasPrefix(strings.ToLower(e.Path), strings.ToLower(p)+"/") {
			return true
		}
	}
	for _, d := range s.Directories {
		if strings.EqualFold(d, p) || strings.HasPrefix(strings.ToLower(d), strings.ToLower(p)+"/") {
			return true
		}
	}
	return false
}
func stale(message string) error {
	return c.Fail(c.StaleBase, fmt.Sprintf("proposal precondition failed: %s", message))
}
