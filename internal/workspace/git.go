package workspace

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
)

type gitCapture struct {
	state   c.GitState
	index   []byte
	ignores map[string][]byte
}

// CaptureRepository reads metadata natively. No Git executable, helper, hook,
// filter, fsmonitor, credential program, config include or pager is invoked.
// Supported: SHA-1/SHA-256, index v2/v3/v4, ordinary and linked worktree roots.
// Sparse/split indexes, unmerged stages and submodules are explicitly denied.
func CaptureRepository(root string, limits Limits) (Capture, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Capture{}, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return Capture{}, err
	}
	first, err := readGit(absolute)
	if err != nil {
		return Capture{}, err
	}
	result, err := captureWithGit(absolute, limits, first)
	if err != nil {
		return Capture{}, err
	}
	second, err := readGit(absolute)
	if err != nil {
		return Capture{}, err
	}
	a, _ := c.Digest(struct {
		State   c.GitState
		Ignores map[string][]byte
	}{first.state, first.ignores})
	b, _ := c.Digest(struct {
		State   c.GitState
		Ignores map[string][]byte
	}{second.state, second.ignores})
	if a != b {
		return Capture{}, c.Fail(c.Conflict, "Git metadata changed during capture")
	}
	return result, nil
}
func readGit(root string) (gitCapture, error) {
	result := gitCapture{ignores: map[string][]byte{}}
	source, err := os.OpenRoot(root)
	if err != nil {
		return result, err
	}
	defer source.Close()
	info, err := source.Lstat(".git")
	if err != nil {
		return result, err
	}
	gitdir := filepath.Join(root, ".git")
	if info.Mode().IsRegular() {
		raw, err := fileguard.ReadRegular(source, ".git", 4096)
		if err != nil {
			return result, err
		}
		if !bytes.HasPrefix(raw, []byte("gitdir: ")) || bytes.Count(bytes.TrimSpace(raw), []byte("\n")) != 0 {
			return result, c.Fail(c.UnsupportedCapability, "unsupported Git directory locator")
		}
		locator := strings.TrimSpace(string(raw[len("gitdir: "):]))
		if !filepath.IsAbs(locator) {
			locator = filepath.Join(root, locator)
		}
		gitdir, err = filepath.Abs(locator)
		if err != nil {
			return result, err
		}
	} else if !info.IsDir() {
		return result, c.Fail(c.UnsupportedCapability, "Git directory must not be a symlink")
	}
	gitdir, err = filepath.EvalSymlinks(gitdir)
	if err != nil {
		return result, err
	}
	meta, err := os.OpenRoot(gitdir)
	if err != nil {
		return result, err
	}
	defer meta.Close()
	common := gitdir
	raw, err := fileguard.ReadRegular(meta, "commondir", 4096)
	if err == nil {
		locator := strings.TrimSpace(string(raw))
		if locator == "" || strings.ContainsAny(locator, "\r\n\x00") {
			return result, c.Fail(c.InvalidArgument, "invalid common directory")
		}
		if !filepath.IsAbs(locator) {
			locator = filepath.Join(gitdir, locator)
		}
		common, err = filepath.EvalSymlinks(locator)
		if err != nil {
			return result, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	shared, err := os.OpenRoot(common)
	if err != nil {
		return result, err
	}
	defer shared.Close()
	result.state = c.GitState{GitDir: gitdir, CommonDir: common, Entries: []c.GitIndexEntry{}, ObjectFormat: "sha1"}
	raw, err = fileguard.ReadRegular(meta, "index", 32<<20)
	if err == nil {
		result.index = raw
		result.state.IndexPresent = true
		result.state.IndexDigest = c.HashBytes(raw)
		entries, format, err := parseIndex(raw)
		if err != nil {
			return result, err
		}
		result.state.Entries = entries
		result.state.ObjectFormat = format
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	head, err := fileguard.ReadRegular(meta, "HEAD", 4096)
	if err != nil {
		return result, err
	}
	headText := strings.TrimSpace(string(head))
	if strings.HasPrefix(headText, "ref: ") {
		ref := strings.TrimPrefix(headText, "ref: ")
		if !strings.HasPrefix(ref, "refs/heads/") || !policy.SafePath(ref) {
			return result, c.Fail(c.UnsupportedCapability, "unsupported HEAD ref")
		}
		result.state.HeadRef = ref
		raw, err := fileguard.ReadRegular(shared, filepath.FromSlash(ref), 4096)
		if err == nil {
			result.state.HeadObject = strings.TrimSpace(string(raw))
		} else if errors.Is(err, os.ErrNotExist) {
			packed, packedErr := fileguard.ReadRegular(shared, "packed-refs", 8<<20)
			if packedErr == nil {
				for _, line := range strings.Split(string(packed), "\n") {
					fields := strings.Fields(line)
					if len(fields) == 2 && fields[1] == ref {
						if result.state.HeadObject != "" {
							return result, c.Fail(c.StoreIntegrityError, "duplicate packed ref")
						}
						result.state.HeadObject = fields[0]
					}
				}
			} else if !errors.Is(packedErr, os.ErrNotExist) {
				return result, packedErr
			}
		} else {
			return result, err
		}
	} else {
		result.state.HeadObject = headText
	}
	if result.state.HeadObject != "" {
		oid := result.state.HeadObject
		if !validObject(oid) {
			return result, c.Fail(c.StoreIntegrityError, "invalid HEAD object")
		}
		if len(oid) == 64 {
			if result.state.IndexPresent && result.state.ObjectFormat != "sha256" {
				return result, c.Fail(c.StoreIntegrityError, "HEAD/index object format mismatch")
			}
			result.state.ObjectFormat = "sha256"
		} else if result.state.ObjectFormat != "sha1" {
			return result, c.Fail(c.StoreIntegrityError, "HEAD/index object format mismatch")
		}
	} else if result.state.HeadRef == "" {
		return result, c.Fail(c.StoreIntegrityError, "empty detached HEAD")
	}
	// Ignore text is interpreted by our bounded parser. Repo configuration is
	// never loaded. User/system core.excludesFile is outside this capture scope.
	raw, err = fileguard.ReadRegular(shared, filepath.Join("info", "exclude"), 1<<20)
	if err == nil {
		result.ignores[".git/info/exclude"] = raw
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	return result, nil
}
func validObject(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(raw) == value
}

func parseIndex(raw []byte) ([]c.GitIndexEntry, string, error) {
	bad := func() ([]c.GitIndexEntry, string, error) {
		return nil, "", c.Fail(c.StoreIntegrityError, "invalid Git index")
	}
	if len(raw) < 32 || string(raw[:4]) != "DIRC" {
		return bad()
	}
	version := binary.BigEndian.Uint32(raw[4:8])
	if version < 2 || version > 4 {
		return nil, "", c.Fail(c.UnsupportedCapability, "unsupported index version")
	}
	hashSize, format := 20, "sha1"
	h1 := sha1.Sum(raw[:len(raw)-20])
	if !bytes.Equal(h1[:], raw[len(raw)-20:]) {
		if len(raw) < 44 {
			return bad()
		}
		h256 := sha256.Sum256(raw[:len(raw)-32])
		if !bytes.Equal(h256[:], raw[len(raw)-32:]) {
			return bad()
		}
		hashSize, format = 32, "sha256"
	}
	count := binary.BigEndian.Uint32(raw[8:12])
	if count > 100000 {
		return nil, "", c.Fail(c.UnsupportedCapability, "index entry quota exceeded")
	}
	end, offset := len(raw)-hashSize, 12
	entries := make([]c.GitIndexEntry, 0, count)
	previous := ""
	seen := map[string]bool{}
	for n := uint32(0); n < count; n++ {
		start := offset
		fixed := 40 + hashSize + 2
		if fixed > end-offset {
			return bad()
		}
		mode := binary.BigEndian.Uint32(raw[offset+24 : offset+28])
		oid := hex.EncodeToString(raw[offset+40 : offset+40+hashSize])
		flags := binary.BigEndian.Uint16(raw[offset+40+hashSize : offset+fixed])
		offset += fixed
		if flags&0x3000 != 0 {
			return nil, "", c.Fail(c.Conflict, "unmerged Git index requires user resolution")
		}
		extended := uint16(0)
		if flags&0x4000 != 0 {
			if version < 3 || end-offset < 2 {
				return bad()
			}
			extended = binary.BigEndian.Uint16(raw[offset : offset+2])
			offset += 2
			if extended&0x4000 != 0 {
				return nil, "", c.Fail(c.UnsupportedCapability, "sparse/skip-worktree index unsupported")
			}
			if extended & ^uint16(0x6000) != 0 {
				return bad()
			}
		}
		remove := 0
		if version == 4 {
			if offset >= end {
				return bad()
			}
			next := raw[offset]
			offset++
			remove = int(next & 0x7f)
			for next&0x80 != 0 {
				if offset >= end || remove > 1<<20 {
					return bad()
				}
				next = raw[offset]
				offset++
				remove = ((remove + 1) << 7) + int(next&0x7f)
			}
			if remove > len(previous) {
				return bad()
			}
		}
		nul := bytes.IndexByte(raw[offset:end], 0)
		if nul < 0 {
			return bad()
		}
		name := string(raw[offset : offset+nul])
		offset += nul + 1
		if version == 4 {
			name = previous[:len(previous)-remove] + name
		} else {
			padded := start + ((offset-start+7)/8)*8
			if padded > end {
				return bad()
			}
			for _, b := range raw[offset:padded] {
				if b != 0 {
					return bad()
				}
			}
			offset = padded
		}
		if !utf8.ValidString(name) || !policy.SafePath(name) || previous != "" && name <= previous || seen[strings.ToLower(name)] || flags&0xfff != 0xfff && int(flags&0xfff) != len(name) {
			return bad()
		}
		for _, part := range strings.Split(name, "/") {
			if strings.EqualFold(part, ".git") {
				return bad()
			}
		}
		if mode != 0100644 && mode != 0100755 {
			return nil, "", c.Fail(c.UnsupportedCapability, "symlink/gitlink/sparse index mode unsupported")
		}
		seen[strings.ToLower(name)] = true
		previous = name
		entries = append(entries, c.GitIndexEntry{Path: name, ObjectID: oid, Mode: mode, IntentToAdd: extended&0x2000 != 0})
	}
	for offset < end {
		if end-offset < 8 {
			return bad()
		}
		signature := raw[offset : offset+4]
		size := binary.BigEndian.Uint32(raw[offset+4 : offset+8])
		offset += 8
		if uint64(size) > uint64(end-offset) {
			return bad()
		}
		if signature[0] < 'A' || signature[0] > 'Z' {
			return nil, "", c.Fail(c.UnsupportedCapability, "mandatory Git index extension unsupported")
		}
		offset += int(size)
	}
	return entries, format, nil
}

func captureWithGit(root string, limits Limits, g gitCapture) (Capture, error) {
	first, err := scanWithGit(root, limits, &g)
	if err != nil {
		return Capture{}, err
	}
	second, err := scanWithGit(root, limits, &g)
	if err != nil {
		return Capture{}, err
	}
	if first.Snapshot.Digest != second.Snapshot.Digest {
		return Capture{}, c.Fail(c.Conflict, "workspace or ignore sources changed during capture")
	}
	second.IndexBytes = append([]byte(nil), g.index...)
	return second, nil
}

func gitPathTracked(g *gitCapture, p string) bool {
	for _, e := range g.state.Entries {
		if e.Path == p {
			return true
		}
	}
	return false
}
func gitDirectoryTracked(g *gitCapture, p string) bool {
	for _, e := range g.state.Entries {
		if strings.HasPrefix(e.Path, p+"/") {
			return true
		}
	}
	return false
}
func ignoreSourceDirectory(name string) string {
	if name == ".git/info/exclude" {
		return "."
	}
	return path.Dir(name)
}
