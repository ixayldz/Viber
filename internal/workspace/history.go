package workspace

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
)

type HistoryLimits struct {
	CaptureLimits  Limits
	SensitivePaths []string
	MaxCommits     int
	MaxObjects     int
	MaxTreeEntries int
	MaxObjectBytes int64
	MaxTotalBytes  int64
}

func DefaultHistoryLimits() HistoryLimits {
	return HistoryLimits{CaptureLimits: DefaultLimits(), MaxCommits: 256, MaxObjects: 12000,
		MaxTreeEntries: 50000, MaxObjectBytes: 16 << 20, MaxTotalBytes: 128 << 20}
}

type HistoryChange struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	BeforeObject string `json:"before_object,omitempty"`
	AfterObject  string `json:"after_object,omitempty"`
	BeforeMode   uint32 `json:"before_mode,omitempty"`
	AfterMode    uint32 `json:"after_mode,omitempty"`
}
type HistoryCommit struct {
	ObjectID     string          `json:"object_id"`
	CommitterUTC string          `json:"committer_utc"`
	Changes      []HistoryChange `json:"changes"`
}
type GitHistory struct {
	SchemaVersion               int             `json:"schema_version"`
	SourceSnapshotDigest        string          `json:"source_snapshot_digest"`
	GitStateDigest              string          `json:"git_state_digest"`
	HeadObject                  string          `json:"head_object"`
	ObjectFormat                string          `json:"object_format"`
	CutoffUTC                   string          `json:"cutoff_utc"`
	Consistency                 string          `json:"capture_consistency"`
	Scope                       string          `json:"scope"`
	Coverage                    string          `json:"coverage"`
	ObjectStoreDigest           string          `json:"object_store_digest"`
	FirstParentDiffsOnly        bool            `json:"first_parent_diffs_only"`
	Truncated                   bool            `json:"truncated"`
	OmittedSensitivePathEntries int             `json:"omitted_sensitive_path_entries"`
	Unknowns                    []string        `json:"unknowns"`
	Commits                     []HistoryCommit `json:"commits"`
	ReportDigest                string          `json:"report_digest"`
}
type historyObject struct {
	kind string
	data []byte
}
type historyFile struct {
	oid  string
	mode uint32
}
type historyReader struct {
	format  string
	width   int
	root    *os.Root
	limits  HistoryLimits
	objects int
	bytes   int64
	entries int
	cache   map[string]historyObject
	packs   map[string]*historyPack
	packed  map[string]historyLocation
}
type historyPack struct {
	name      string
	data      []byte
	byOffset  map[uint64]string
	endOffset map[uint64]uint64
}
type historyLocation struct {
	pack   string
	offset uint64
}
type historyPackFingerprint struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}
type missingHistoryObject struct{}

func (missingHistoryObject) Error() string { return "loose object missing" }

// CaptureGitHistory captures first-parent diffs at or before cutoff. It does
// not run Git or repository-provided code and never reads commit messages.
func CaptureGitHistory(source Capture, cutoff time.Time, limits HistoryLimits) (GitHistory, error) {
	if err := VerifyCapture(source); err != nil {
		return GitHistory{}, err
	}
	if source.Snapshot.Git == nil {
		return GitHistory{}, c.Fail(c.UnsupportedCapability, "Git-bound capture required")
	}
	if cutoff.IsZero() || limits.MaxCommits < 1 || limits.MaxCommits > 1000 ||
		limits.MaxObjects < 1 || limits.MaxObjects > 100000 ||
		limits.MaxTreeEntries < 1 || limits.MaxTreeEntries > 500000 ||
		limits.MaxObjectBytes < 1 || limits.MaxObjectBytes > 64<<20 ||
		limits.MaxTotalBytes < 1 || limits.MaxTotalBytes > 1<<30 {
		return GitHistory{}, c.Fail(c.InvalidArgument, "valid cutoff and bounded history limits required")
	}
	if limits.CaptureLimits.MaxFiles == 0 {
		limits.CaptureLimits = DefaultLimits()
	}
	denied := append(append([]string{}, limits.CaptureLimits.SensitivePaths...), limits.SensitivePaths...)
	for _, scope := range denied {
		if strings.EqualFold(scope, ".git") || strings.HasPrefix(strings.ToLower(scope), ".git/") {
			return GitHistory{}, c.Fail(c.UnsupportedCapability, "history requires restricted Git metadata")
		}
	}
	current, err := CaptureRepository(source.Snapshot.Root, limits.CaptureLimits)
	if err != nil {
		return GitHistory{}, err
	}
	if current.Snapshot.Digest != source.Snapshot.Digest {
		return GitHistory{}, c.Fail(c.Conflict, "source changed before history capture")
	}
	state := *source.Snapshot.Git
	stateDigest, err := c.Digest(state)
	if err != nil {
		return GitHistory{}, err
	}
	cutoff = cutoff.UTC().Truncate(time.Second)
	out := GitHistory{SchemaVersion: c.SchemaVersion, SourceSnapshotDigest: source.Snapshot.Digest,
		GitStateDigest: stateDigest, HeadObject: state.HeadObject, ObjectFormat: state.ObjectFormat,
		CutoffUTC: cutoff.Format(time.RFC3339), Consistency: "BEST_EFFORT",
		Scope:    "CAPTURED_HEAD_FIRST_PARENT_COMMITS_AT_OR_BEFORE_CUTOFF",
		Coverage: "COMPLETE_FIRST_PARENT_WINDOW", FirstParentDiffsOnly: true,
		Unknowns: []string{}, Commits: []HistoryCommit{}}
	addUnknown := func(s string) {
		for _, x := range out.Unknowns {
			if x == s {
				return
			}
		}
		out.Unknowns = append(out.Unknowns, s)
	}
	if state.HeadObject == "" {
		out.Coverage = "UNKNOWN_NO_HEAD_COMMIT"
		out.Unknowns = append(out.Unknowns, "HEAD_HAS_NO_COMMIT")
		return sealGitHistory(out)
	}
	reader, err := newHistoryReader(state.CommonDir, state.ObjectFormat, limits)
	if err != nil {
		return GitHistory{}, err
	}
	defer reader.root.Close()
	storeDigest, storeKnown, err := historyPackStoreDigest(reader.root)
	if err != nil {
		return GitHistory{}, err
	}
	if storeKnown {
		out.ObjectStoreDigest = storeDigest
	} else {
		addUnknown("PACK_STORE_FINGERPRINT_UNAVAILABLE")
	}
	packed, packUnknown, err := reader.loadPacks()
	if err != nil {
		return GitHistory{}, err
	}
	if packUnknown != "" {
		addUnknown(packUnknown)
	}
	for _, item := range []struct{ path, reason string }{
		{"shallow", "SHALLOW_REPOSITORY_HISTORY_TRUNCATED"},
		{filepath.Join("objects", "info", "alternates"), "ALTERNATE_OBJECT_DATABASE_NOT_RESOLVED"},
		{filepath.Join("info", "grafts"), "GRAFTED_HISTORY_NOT_APPLIED"},
		{filepath.Join("refs", "replace"), "REPLACE_REFS_NOT_APPLIED"},
	} {
		if _, e := reader.root.Lstat(item.path); e == nil {
			addUnknown(item.reason)
		} else if !errors.Is(e, os.ErrNotExist) {
			return GitHistory{}, e
		}
	}
	seen, oid := map[string]bool{}, state.HeadObject
	for oid != "" {
		if seen[oid] {
			return GitHistory{}, c.Fail(c.StoreIntegrityError, "Git first-parent cycle")
		}
		seen[oid] = true
		obj, e := reader.read(oid)
		if e != nil {
			var missing missingHistoryObject
			if errors.As(e, &missing) {
				if packed {
					addUnknown("PACKED_GIT_OBJECT_MISSING_OR_UNRESOLVED_HISTORY_UNKNOWN")
				} else {
					addUnknown("MISSING_LOOSE_GIT_OBJECT_HISTORY_UNKNOWN")
				}
				break
			}
			return GitHistory{}, e
		}
		commit, e := parseCommitHistory(obj, reader.width)
		if e != nil {
			return GitHistory{}, e
		}
		stamp := time.Unix(commit.when, 0)
		if stamp.After(cutoff) {
			if len(commit.parents) == 0 {
				break
			}
			oid = commit.parents[0]
			continue
		}
		if len(out.Commits) >= limits.MaxCommits {
			out.Truncated = true
			addUnknown("COMMIT_LIMIT_REACHED")
			break
		}
		after, e := reader.tree(commit.tree, denied, &out.OmittedSensitivePathEntries)
		if e != nil {
			var missing missingHistoryObject
			if errors.As(e, &missing) {
				if packed {
					addUnknown("PACKED_GIT_OBJECT_MISSING_OR_UNRESOLVED_HISTORY_UNKNOWN")
				} else {
					addUnknown("MISSING_LOOSE_GIT_OBJECT_HISTORY_UNKNOWN")
				}
				break
			}
			return GitHistory{}, e
		}
		before := map[string]historyFile{}
		if len(commit.parents) > 0 {
			parentObject, e := reader.read(commit.parents[0])
			if e != nil {
				var missing missingHistoryObject
				if errors.As(e, &missing) {
					if packed {
						addUnknown("PACKED_GIT_OBJECT_MISSING_OR_UNRESOLVED_HISTORY_UNKNOWN")
					} else {
						addUnknown("MISSING_LOOSE_GIT_OBJECT_HISTORY_UNKNOWN")
					}
					break
				}
				return GitHistory{}, e
			}
			parent, e := parseCommitHistory(parentObject, reader.width)
			if e != nil {
				return GitHistory{}, e
			}
			before, e = reader.tree(parent.tree, denied, &out.OmittedSensitivePathEntries)
			if e != nil {
				var missing missingHistoryObject
				if errors.As(e, &missing) {
					if packed {
						addUnknown("PACKED_GIT_OBJECT_MISSING_OR_UNRESOLVED_HISTORY_UNKNOWN")
					} else {
						addUnknown("MISSING_LOOSE_GIT_OBJECT_HISTORY_UNKNOWN")
					}
					break
				}
				return GitHistory{}, e
			}
		}
		out.Commits = append(out.Commits, HistoryCommit{ObjectID: oid,
			CommitterUTC: stamp.UTC().Format(time.RFC3339), Changes: diffHistoryTrees(before, after)})
		if len(commit.parents) == 0 {
			oid = ""
		} else {
			oid = commit.parents[0]
		}
	}
	sort.Strings(out.Unknowns)
	if len(out.Unknowns) != 0 {
		out.Coverage = "PARTIAL_OR_UNKNOWN"
	}
	final, err := CaptureRepository(source.Snapshot.Root, limits.CaptureLimits)
	if err != nil {
		return GitHistory{}, err
	}
	if final.Snapshot.Digest != source.Snapshot.Digest {
		return GitHistory{}, c.Fail(c.Conflict, "source or Git metadata changed during history capture")
	}
	if out.ObjectStoreDigest != "" {
		finalRoot, rootErr := os.OpenRoot(state.CommonDir)
		if rootErr != nil {
			return GitHistory{}, rootErr
		}
		finalStoreDigest, known, digestErr := historyPackStoreDigest(finalRoot)
		_ = finalRoot.Close()
		if digestErr != nil {
			return GitHistory{}, digestErr
		}
		if !known || finalStoreDigest != out.ObjectStoreDigest {
			return GitHistory{}, c.Fail(c.Conflict, "Git packed object store changed during history capture")
		}
	}
	return sealGitHistory(out)
}

func ValidateGitHistory(report GitHistory, source Capture) error {
	if err := VerifyCapture(source); err != nil {
		return err
	}
	if source.Snapshot.Git == nil {
		return c.Fail(c.InvalidArgument, "Git-bound source required")
	}
	stateDigest, err := c.Digest(*source.Snapshot.Git)
	if err != nil {
		return err
	}
	if report.SchemaVersion != c.SchemaVersion || report.SourceSnapshotDigest != source.Snapshot.Digest ||
		report.GitStateDigest != stateDigest || report.HeadObject != source.Snapshot.Git.HeadObject ||
		report.ObjectFormat != source.Snapshot.Git.ObjectFormat || report.Consistency != "BEST_EFFORT" ||
		report.Scope != "CAPTURED_HEAD_FIRST_PARENT_COMMITS_AT_OR_BEFORE_CUTOFF" ||
		!report.FirstParentDiffsOnly || (report.Coverage != "COMPLETE_FIRST_PARENT_WINDOW" &&
		report.Coverage != "PARTIAL_OR_UNKNOWN" && report.Coverage != "UNKNOWN_NO_HEAD_COMMIT") {
		return c.Fail(c.StoreIntegrityError, "history is not bound to the captured Git source")
	}
	if report.ObjectStoreDigest == "" {
		if report.Coverage == "COMPLETE_FIRST_PARENT_WINDOW" {
			return c.Fail(c.StoreIntegrityError, "complete history lacks object-store evidence")
		}
	} else {
		if !validObject(report.ObjectStoreDigest) || len(report.ObjectStoreDigest) != 64 {
			return c.Fail(c.InvalidArgument, "invalid packed object-store digest")
		}
		root, rootErr := os.OpenRoot(source.Snapshot.Git.CommonDir)
		if rootErr != nil {
			return rootErr
		}
		currentStoreDigest, known, digestErr := historyPackStoreDigest(root)
		_ = root.Close()
		if digestErr != nil {
			return digestErr
		}
		if !known || currentStoreDigest != report.ObjectStoreDigest {
			return c.Fail(c.StoreIntegrityError, "packed object store changed since history capture")
		}
	}
	if report.Coverage == "COMPLETE_FIRST_PARENT_WINDOW" && (len(report.Unknowns) != 0 || report.Truncated) {
		return c.Fail(c.StoreIntegrityError, "complete history coverage conflicts with unknowns or truncation")
	}
	cutoff, err := time.Parse(time.RFC3339, report.CutoffUTC)
	if err != nil || cutoff.UTC().Format(time.RFC3339) != report.CutoffUTC {
		return c.Fail(c.InvalidArgument, "invalid history cutoff")
	}
	if len(report.Commits) > 1000 || len(report.Unknowns) > 100 {
		return c.Fail(c.UnsupportedCapability, "history report quota exceeded")
	}
	seenCommits := map[string]bool{}
	for _, commit := range report.Commits {
		if !validObject(commit.ObjectID) || len(commit.ObjectID) != len(report.HeadObject) || len(commit.Changes) > 500000 {
			return c.Fail(c.InvalidArgument, "invalid history commit")
		}
		if seenCommits[commit.ObjectID] {
			return c.Fail(c.InvalidArgument, "duplicate history commit")
		}
		seenCommits[commit.ObjectID] = true
		when, e := time.Parse(time.RFC3339, commit.CommitterUTC)
		if e != nil || when.UTC().Format(time.RFC3339) != commit.CommitterUTC || when.After(cutoff) {
			return c.Fail(c.InvalidArgument, "invalid history timestamp")
		}
		last := ""
		for _, change := range commit.Changes {
			if !policy.SafePath(change.Path) || (last != "" && change.Path <= last) ||
				(change.Kind != "ADDED" && change.Kind != "DELETED" && change.Kind != "MODIFIED" && change.Kind != "MODE_CHANGED") {
				return c.Fail(c.InvalidArgument, "invalid history change")
			}
			last = change.Path
		}
	}
	expected := report.ReportDigest
	report.ReportDigest = ""
	actual, err := c.Digest(report)
	if err != nil {
		return err
	}
	if expected != actual {
		return c.Fail(c.StoreIntegrityError, "history digest mismatch")
	}
	return nil
}

type commitFields struct {
	tree    string
	parents []string
	when    int64
}

func parseCommitHistory(obj historyObject, width int) (commitFields, error) {
	if obj.kind != "commit" {
		return commitFields{}, c.Fail(c.StoreIntegrityError, "history reference is not a commit")
	}
	end := bytes.Index(obj.data, []byte("\n\n"))
	if end < 0 || end > 1<<20 {
		return commitFields{}, c.Fail(c.StoreIntegrityError, "invalid commit headers")
	}
	var result commitFields
	found := false
	for _, line := range strings.Split(string(obj.data[:end]), "\n") {
		if strings.HasPrefix(line, "tree ") {
			if result.tree != "" {
				return commitFields{}, c.Fail(c.StoreIntegrityError, "duplicate commit tree")
			}
			result.tree = strings.TrimPrefix(line, "tree ")
		} else if strings.HasPrefix(line, "parent ") {
			parent := strings.TrimPrefix(line, "parent ")
			if !validObject(parent) || len(parent) != width*2 {
				return commitFields{}, c.Fail(c.StoreIntegrityError, "invalid commit parent")
			}
			result.parents = append(result.parents, parent)
		} else if strings.HasPrefix(line, "committer ") {
			fields := strings.Fields(strings.TrimPrefix(line, "committer "))
			if len(fields) < 3 {
				return commitFields{}, c.Fail(c.StoreIntegrityError, "invalid committer header")
			}
			stamp, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
			if err != nil || stamp < -62135596800 || stamp > 253402300799 || !validCommitTimezone(fields[len(fields)-1]) {
				return commitFields{}, c.Fail(c.StoreIntegrityError, "invalid committer timestamp")
			}
			result.when, found = stamp, true
		}
	}
	if !validObject(result.tree) || len(result.tree) != width*2 || !found {
		return commitFields{}, c.Fail(c.StoreIntegrityError, "incomplete commit metadata")
	}
	return result, nil
}
func validCommitTimezone(s string) bool {
	if len(s) != 5 || (s[0] != '+' && s[0] != '-') {
		return false
	}
	h, e1 := strconv.Atoi(s[1:3])
	m, e2 := strconv.Atoi(s[3:])
	return e1 == nil && e2 == nil && h <= 23 && m <= 59
}
func newHistoryReader(common, format string, limits HistoryLimits) (*historyReader, error) {
	width := 20
	if format == "sha256" {
		width = 32
	} else if format != "sha1" {
		return nil, c.Fail(c.UnsupportedCapability, "unsupported Git object format")
	}
	root, err := os.OpenRoot(common)
	if err != nil {
		return nil, err
	}
	return &historyReader{format: format, width: width, root: root, limits: limits,
		cache: map[string]historyObject{}, packs: map[string]*historyPack{}, packed: map[string]historyLocation{}}, nil
}
func packedHistoryPresent(root *os.Root) (bool, bool) {
	pack, err := root.OpenRoot(filepath.Join("objects", "pack"))
	if errors.Is(err, os.ErrNotExist) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	defer pack.Close()
	entries, err := fs.ReadDir(pack.FS(), ".")
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".pack") {
			return true, true
		}
	}
	return false, true
}

// historyPackStoreDigest binds the report to the exact pack and index bytes
// observed. It hashes a bounded inventory without interpreting Git metadata.
func historyPackStoreDigest(root *os.Root) (string, bool, error) {
	pack, err := root.OpenRoot(filepath.Join("objects", "pack"))
	if errors.Is(err, os.ErrNotExist) {
		digest, digestErr := c.Digest([]historyPackFingerprint{})
		return digest, true, digestErr
	}
	if err != nil {
		return "", false, nil
	}
	defer pack.Close()
	entries, err := fs.ReadDir(pack.FS(), ".")
	if err != nil {
		return "", false, nil
	}
	fingerprints := []historyPackFingerprint{}
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".pack") && !strings.HasSuffix(name, ".idx")) {
			continue
		}
		info, statErr := pack.Lstat(name)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 1<<30 {
			return "", false, nil
		}
		total += info.Size()
		if total > 1<<30 || len(fingerprints) >= 8192 {
			return "", false, nil
		}
		file, openErr := pack.Open(name)
		if openErr != nil {
			return "", false, nil
		}
		opened, statErr := file.Stat()
		if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || fileguard.SingleLink(file) != nil {
			_ = file.Close()
			return "", false, nil
		}
		h := sha256.New()
		copied, copyErr := io.Copy(h, io.LimitReader(file, info.Size()+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || copied != info.Size() {
			return "", false, nil
		}
		fingerprints = append(fingerprints, historyPackFingerprint{Name: name, Size: info.Size(), Digest: hex.EncodeToString(h.Sum(nil))})
	}
	sort.Slice(fingerprints, func(i, j int) bool { return fingerprints[i].Name < fingerprints[j].Name })
	digest, err := c.Digest(fingerprints)
	return digest, err == nil, err
}

func (r *historyReader) loadPacks() (bool, string, error) {
	packRoot, err := r.root.OpenRoot(filepath.Join("objects", "pack"))
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "PACK_STORE_STATE_UNREADABLE_HISTORY_UNKNOWN", nil
	}
	defer packRoot.Close()
	entries, err := fs.ReadDir(packRoot.FS(), ".")
	if err != nil {
		return false, "PACK_STORE_STATE_UNREADABLE_HISTORY_UNKNOWN", nil
	}
	packNames, indexNames := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".pack") {
			packNames[strings.TrimSuffix(name, ".pack")] = true
		} else if strings.HasSuffix(name, ".idx") {
			indexNames[strings.TrimSuffix(name, ".idx")] = true
		}
	}
	if len(packNames) == 0 && len(indexNames) == 0 {
		return false, "", nil
	}
	unknown := ""
	setUnknown := func(value string) {
		if unknown == "" {
			unknown = value
		}
	}
	for stem := range packNames {
		if !indexNames[stem] {
			setUnknown("PACK_WITHOUT_INDEX_HISTORY_PARTIAL")
		}
	}
	for stem := range indexNames {
		if !packNames[stem] {
			setUnknown("INDEX_WITHOUT_PACK_HISTORY_PARTIAL")
		}
	}
	stems := make([]string, 0, len(packNames))
	for stem := range packNames {
		if indexNames[stem] {
			stems = append(stems, stem)
		}
	}
	sort.Strings(stems)
	for _, stem := range stems {
		objectName := strings.TrimPrefix(stem, "pack-")
		if objectName == stem || !validObject(objectName) || len(objectName) != r.width*2 {
			return true, "", c.Fail(c.StoreIntegrityError, "invalid Git pack filename")
		}
		idxPath, packPath := stem+".idx", stem+".pack"
		remaining := r.limits.MaxTotalBytes - r.bytes
		if remaining <= 0 {
			setUnknown("PACK_BYTE_LIMIT_REACHED_HISTORY_PARTIAL")
			continue
		}
		idx, idxErr := fileguard.ReadRegular(packRoot, idxPath, min64(remaining, 64<<20))
		if errors.Is(idxErr, os.ErrNotExist) {
			setUnknown("PACK_INDEX_DISAPPEARED_HISTORY_PARTIAL")
			continue
		}
		if idxErr != nil {
			if historyUnsupported(idxErr) {
				setUnknown("PACK_INDEX_LIMIT_OR_TYPE_UNSUPPORTED_HISTORY_PARTIAL")
				continue
			}
			return true, "", idxErr
		}
		remaining -= int64(len(idx))
		packData, packErr := fileguard.ReadRegular(packRoot, packPath, remaining)
		if errors.Is(packErr, os.ErrNotExist) {
			setUnknown("PACK_DISAPPEARED_HISTORY_PARTIAL")
			continue
		}
		if packErr != nil {
			if historyUnsupported(packErr) {
				setUnknown("PACK_BYTE_LIMIT_OR_TYPE_UNSUPPORTED_HISTORY_PARTIAL")
				continue
			}
			return true, "", packErr
		}
		if int64(len(idx)+len(packData)) > r.limits.MaxTotalBytes-r.bytes {
			setUnknown("PACK_BYTE_LIMIT_REACHED_HISTORY_PARTIAL")
			continue
		}
		r.bytes += int64(len(idx) + len(packData))
		pack, parseErr := parseHistoryPack(objectName, idx, packData, r.width, 250000)
		if parseErr != nil {
			if historyUnsupported(parseErr) {
				setUnknown("PACK_FORMAT_OR_INDEX_UNSUPPORTED_HISTORY_PARTIAL")
				continue
			}
			return true, "", parseErr
		}
		if len(r.packed)+len(pack.byOffset) > 250000 {
			setUnknown("PACK_INDEX_OBJECT_LIMIT_REACHED_HISTORY_PARTIAL")
			continue
		}
		pack.name, pack.data = stem, packData
		r.packs[stem] = pack
		for offset, oid := range pack.byOffset {
			if prior, exists := r.packed[oid]; exists && (prior.pack != stem || prior.offset != offset) {
				return true, "", c.Fail(c.StoreIntegrityError, "duplicate object in Git pack store")
			}
			r.packed[oid] = historyLocation{pack: stem, offset: offset}
		}
	}
	return true, unknown, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func historyUnsupported(err error) bool {
	var coded *c.Error
	return errors.As(err, &coded) && coded.Code == c.UnsupportedCapability
}

func (r *historyReader) read(oid string) (historyObject, error) {
	return r.readContext(oid, 0, map[historyLocation]bool{})
}

func (r *historyReader) readContext(oid string, depth int, active map[historyLocation]bool) (historyObject, error) {
	if !validObject(oid) || len(oid) != r.width*2 {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "invalid object reference")
	}
	if object, ok := r.cache[oid]; ok {
		return object, nil
	}
	compressed, err := fileguard.ReadRegular(r.root, filepath.Join("objects", oid[:2], oid[2:]), r.limits.MaxObjectBytes)
	if errors.Is(err, os.ErrNotExist) {
		location, ok := r.packed[oid]
		if !ok {
			return historyObject{}, missingHistoryObject{}
		}
		return r.readPacked(location, oid, depth, active)
	}
	if err != nil {
		return historyObject{}, err
	}
	r.objects++
	if r.objects > r.limits.MaxObjects {
		return historyObject{}, c.Fail(c.UnsupportedCapability, "history object quota exceeded")
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "invalid loose object compression")
	}
	raw, readErr := io.ReadAll(io.LimitReader(zr, r.limits.MaxObjectBytes+128))
	closeErr := zr.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) > r.limits.MaxObjectBytes+128 {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "invalid or oversized loose object")
	}
	r.bytes += int64(len(raw) + len(compressed))
	if r.bytes > r.limits.MaxTotalBytes {
		return historyObject{}, c.Fail(c.UnsupportedCapability, "history byte quota exceeded")
	}
	zero := bytes.IndexByte(raw, 0)
	if zero <= 0 {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "invalid loose object header")
	}
	fields := strings.SplitN(string(raw[:zero]), " ", 2)
	if len(fields) != 2 {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "invalid loose object header")
	}
	size, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || size < 0 || size != int64(len(raw)-zero-1) {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "loose object length mismatch")
	}
	var digest []byte
	if r.format == "sha256" {
		sum := sha256.Sum256(raw)
		digest = sum[:]
	} else {
		sum := sha1.Sum(raw)
		digest = sum[:]
	}
	if hex.EncodeToString(digest) != oid {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "loose object hash mismatch")
	}
	object := historyObject{fields[0], raw[zero+1:]}
	r.cache[oid] = object
	return object, nil
}

func parseHistoryPack(stem string, idx, pack []byte, width int, maxIndexObjects int) (*historyPack, error) {
	unsupported := func(message string) error { return c.Fail(c.UnsupportedCapability, message) }
	bad := func(message string) error { return c.Fail(c.StoreIntegrityError, message) }
	if len(pack) < 12+width || string(pack[:4]) != "PACK" {
		return nil, bad("invalid Git pack header")
	}
	version := binary.BigEndian.Uint32(pack[4:8])
	if version != 2 && version != 3 {
		return nil, unsupported("unsupported Git pack version")
	}
	packCount := binary.BigEndian.Uint32(pack[8:12])
	packSum := gitObjectHash(pack[:len(pack)-width], width)
	packTrailer := pack[len(pack)-width:]
	if !bytes.Equal(packSum, packTrailer) || hex.EncodeToString(packTrailer) != stem {
		return nil, bad("Git pack checksum or filename mismatch")
	}
	if len(idx) < 8+256*4+2*width || binary.BigEndian.Uint32(idx[:4]) != 0xff744f63 {
		return nil, unsupported("unsupported Git pack index version")
	}
	indexVersion := binary.BigEndian.Uint32(idx[4:8])
	if indexVersion != 2 {
		return nil, unsupported("unsupported Git pack index version")
	}
	indexSum := gitObjectHash(idx[:len(idx)-width], width)
	if !bytes.Equal(indexSum, idx[len(idx)-width:]) {
		return nil, bad("Git pack index checksum mismatch")
	}
	bodyEnd := len(idx) - 2*width
	if !bytes.Equal(idx[bodyEnd:bodyEnd+width], packTrailer) {
		return nil, bad("Git pack index references another pack")
	}
	fanout := make([]uint32, 256)
	last := uint32(0)
	for i := range fanout {
		value := binary.BigEndian.Uint32(idx[8+i*4 : 12+i*4])
		if value < last {
			return nil, bad("invalid Git pack index fanout")
		}
		fanout[i], last = value, value
	}
	count := int(last)
	if count != int(packCount) || count > maxIndexObjects {
		if count > maxIndexObjects {
			return nil, unsupported("Git pack index object limit exceeded")
		}
		return nil, bad("Git pack object count mismatch")
	}
	nameStart := 8 + 256*4
	crcStart := nameStart + count*width
	offsetStart := crcStart + count*4
	largeStart := offsetStart + count*4
	if largeStart > bodyEnd || (bodyEnd-largeStart)%8 != 0 {
		return nil, bad("invalid Git pack index length")
	}
	largeCount := (bodyEnd - largeStart) / 8
	byOffset := make(map[uint64]string, count)
	byOID := make(map[string]uint64, count)
	nameCounts := make([]uint32, 256)
	largeUsed := make([]bool, largeCount)
	for i := 0; i < count; i++ {
		nameAt := nameStart + i*width
		name := idx[nameAt : nameAt+width]
		if i > 0 {
			priorAt := nameAt - width
			if bytes.Compare(idx[priorAt:priorAt+width], name) >= 0 {
				return nil, bad("Git pack index object names are not strictly sorted")
			}
		}
		nameCounts[name[0]]++
		shortOffset := binary.BigEndian.Uint32(idx[offsetStart+i*4 : offsetStart+i*4+4])
		var offset uint64
		if shortOffset&0x80000000 == 0 {
			offset = uint64(shortOffset)
		} else {
			largeIndex := int(shortOffset & 0x7fffffff)
			if largeIndex < 0 || largeIndex >= largeCount {
				return nil, bad("Git pack large offset index out of range")
			}
			largeUsed[largeIndex] = true
			offset = binary.BigEndian.Uint64(idx[largeStart+largeIndex*8 : largeStart+largeIndex*8+8])
		}
		if offset < 12 || offset >= uint64(len(pack)-width) {
			return nil, bad("Git pack index offset out of range")
		}
		oid := hex.EncodeToString(name)
		byOffset[offset] = oid
		byOID[oid] = offset
	}
	var cumulative uint32
	for i := 0; i < 256; i++ {
		cumulative += nameCounts[i]
		if fanout[i] != cumulative {
			return nil, bad("Git pack index fanout does not match names")
		}
	}
	for _, used := range largeUsed {
		if !used {
			return nil, bad("unused Git pack large offset entry")
		}
	}
	if len(byOffset) != count {
		return nil, bad("duplicate Git pack entry offset")
	}
	offsets := make([]uint64, 0, len(byOffset))
	for offset := range byOffset {
		offsets = append(offsets, offset)
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	if count > 0 && offsets[0] != 12 {
		return nil, bad("Git pack has unindexed bytes before its first object")
	}
	for i := 0; i < count; i++ {
		nameAt := nameStart + i*width
		oid := hex.EncodeToString(idx[nameAt : nameAt+width])
		offset, ok := byOID[oid]
		if !ok {
			return nil, bad("Git pack index offset mapping failed")
		}
		position := sort.Search(len(offsets), func(j int) bool { return offsets[j] >= offset })
		if position >= len(offsets) || offsets[position] != offset {
			return nil, bad("Git pack index offset mapping failed")
		}
		end := uint64(len(pack) - width)
		if position+1 < len(offsets) {
			end = offsets[position+1]
		}
		if end <= offset || crc32.ChecksumIEEE(pack[offset:end]) != binary.BigEndian.Uint32(idx[crcStart+i*4:crcStart+i*4+4]) {
			return nil, bad("Git pack entry CRC mismatch")
		}
	}
	endOffset := make(map[uint64]uint64, count)
	for i, offset := range offsets {
		end := uint64(len(pack) - width)
		if i+1 < len(offsets) {
			end = offsets[i+1]
		}
		endOffset[offset] = end
	}
	if count == 0 && len(pack) != 12+width {
		return nil, bad("empty Git pack has trailing bytes")
	}
	return &historyPack{byOffset: byOffset, endOffset: endOffset}, nil
}

func gitObjectHash(raw []byte, width int) []byte {
	if width == 32 {
		sum := sha256.Sum256(raw)
		return sum[:]
	}
	sum := sha1.Sum(raw)
	return sum[:]
}

func (r *historyReader) readPacked(location historyLocation, oid string, depth int, active map[historyLocation]bool) (historyObject, error) {
	if object, ok := r.cache[oid]; ok {
		return object, nil
	}
	if depth > 32 || active[location] {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "Git pack delta cycle or depth exceeded")
	}
	pack := r.packs[location.pack]
	if pack == nil || location.offset >= uint64(len(pack.data)) {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "Git pack location missing")
	}
	active[location] = true
	defer delete(active, location)
	r.objects++
	if r.objects > r.limits.MaxObjects {
		return historyObject{}, c.Fail(c.UnsupportedCapability, "history object quota exceeded")
	}
	objectType, declaredSize, baseOffset, baseOID, payload, entryEnd, err := decodeHistoryPackEntry(pack.data, int(location.offset), r.width, r.limits.MaxObjectBytes)
	if err != nil {
		return historyObject{}, err
	}
	if uint64(entryEnd) != pack.endOffset[location.offset] {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "Git packed object boundary mismatch")
	}
	r.bytes += int64(len(payload))
	if r.bytes > r.limits.MaxTotalBytes {
		return historyObject{}, c.Fail(c.UnsupportedCapability, "history byte quota exceeded")
	}
	var object historyObject
	if objectType == 6 || objectType == 7 {
		var base historyObject
		if objectType == 6 {
			if baseOffset == 0 || baseOffset >= location.offset {
				return historyObject{}, c.Fail(c.StoreIntegrityError, "invalid Git OFS delta base")
			}
			baseOID, _ = pack.byOffset[baseOffset]
			if baseOID == "" {
				return historyObject{}, c.Fail(c.StoreIntegrityError, "Git OFS delta base is not indexed")
			}
			base, err = r.readPacked(historyLocation{pack: location.pack, offset: baseOffset}, baseOID, depth+1, active)
		} else {
			base, err = r.readContext(baseOID, depth+1, active)
		}
		if err != nil {
			return historyObject{}, err
		}
		data, applyErr := applyHistoryDelta(base.data, payload, r.limits.MaxObjectBytes)
		if applyErr != nil {
			return historyObject{}, applyErr
		}
		object = historyObject{kind: base.kind, data: data}
	} else {
		kind := map[uint8]string{1: "commit", 2: "tree", 3: "blob", 4: "tag"}[objectType]
		if kind == "" || int64(len(payload)) != declaredSize {
			return historyObject{}, c.Fail(c.StoreIntegrityError, "invalid Git packed object type or size")
		}
		object = historyObject{kind: kind, data: payload}
	}
	if hashHistoryObject(object, r.width) != oid {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "Git packed object hash mismatch")
	}
	if objectType == 6 || objectType == 7 {
		r.bytes += int64(len(object.data))
		if r.bytes > r.limits.MaxTotalBytes {
			return historyObject{}, c.Fail(c.UnsupportedCapability, "history byte quota exceeded")
		}
	}
	r.cache[oid] = object
	return object, nil
}

func decodeHistoryPackEntry(pack []byte, offset, width int, maxObjectBytes int64) (objectType uint8, size int64, baseOffset uint64, baseOID string, payload []byte, end int, err error) {
	bad := func() (uint8, int64, uint64, string, []byte, int, error) {
		return 0, 0, 0, "", nil, 0, c.Fail(c.StoreIntegrityError, "invalid Git packed object entry")
	}
	if offset < 12 || offset >= len(pack) {
		return bad()
	}
	reader := bytes.NewReader(pack[offset:])
	first, e := reader.ReadByte()
	if e != nil {
		return bad()
	}
	objectType = (first >> 4) & 7
	size = int64(first & 0x0f)
	shift := uint(4)
	current := first
	for current&0x80 != 0 {
		current, e = reader.ReadByte()
		if e != nil || shift > 56 {
			return bad()
		}
		size |= int64(current&0x7f) << shift
		shift += 7
	}
	if size < 0 || size > maxObjectBytes || (objectType != 1 && objectType != 2 && objectType != 3 && objectType != 4 && objectType != 6 && objectType != 7) {
		return bad()
	}
	if objectType == 6 {
		b, readErr := reader.ReadByte()
		if readErr != nil || b == 0 {
			return bad()
		}
		baseOffset = uint64(b & 0x7f)
		for b&0x80 != 0 {
			b, readErr = reader.ReadByte()
			if readErr != nil || baseOffset > (1<<56) {
				return bad()
			}
			baseOffset = ((baseOffset + 1) << 7) | uint64(b&0x7f)
		}
		if baseOffset == 0 || baseOffset > uint64(offset) {
			return bad()
		}
		baseOffset = uint64(offset) - baseOffset
	} else if objectType == 7 {
		base := make([]byte, width)
		if _, e = io.ReadFull(reader, base); e != nil {
			return bad()
		}
		baseOID = hex.EncodeToString(base)
	}
	zr, e := zlib.NewReader(reader)
	if e != nil {
		return bad()
	}
	payload, e = io.ReadAll(io.LimitReader(zr, maxObjectBytes+1))
	closeErr := zr.Close()
	if e != nil || closeErr != nil || int64(len(payload)) != size || int64(len(payload)) > maxObjectBytes {
		return bad()
	}
	end = len(pack) - reader.Len()
	return objectType, size, baseOffset, baseOID, payload, end, nil
}

func applyHistoryDelta(base, delta []byte, max int64) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, c.Fail(c.StoreIntegrityError, "invalid Git delta instructions") }
	at := 0
	readVarint := func() (uint64, bool) {
		var value uint64
		shift := uint(0)
		for at < len(delta) && shift <= 56 {
			b := delta[at]
			at++
			value |= uint64(b&0x7f) << shift
			if b&0x80 == 0 {
				return value, true
			}
			shift += 7
		}
		return 0, false
	}
	sourceSize, ok := readVarint()
	if !ok || sourceSize != uint64(len(base)) {
		return bad()
	}
	targetSize, ok := readVarint()
	if !ok || targetSize > uint64(max) {
		return bad()
	}
	out := make([]byte, 0, int(targetSize))
	for at < len(delta) {
		command := delta[at]
		at++
		if command&0x80 != 0 {
			var copyOffset, copySize uint32
			for i := uint(0); i < 4; i++ {
				if command&(1<<i) != 0 {
					if at >= len(delta) {
						return bad()
					}
					copyOffset |= uint32(delta[at]) << (8 * i)
					at++
				}
			}
			for i := uint(0); i < 3; i++ {
				if command&(1<<(4+i)) != 0 {
					if at >= len(delta) {
						return bad()
					}
					copySize |= uint32(delta[at]) << (8 * i)
					at++
				}
			}
			if copySize == 0 {
				copySize = 0x10000
			}
			if uint64(copyOffset)+uint64(copySize) > uint64(len(base)) || uint64(len(out))+uint64(copySize) > targetSize {
				return bad()
			}
			out = append(out, base[copyOffset:copyOffset+copySize]...)
		} else {
			if command == 0 || int(command) > len(delta)-at || uint64(len(out))+uint64(command) > targetSize {
				return bad()
			}
			out = append(out, delta[at:at+int(command)]...)
			at += int(command)
		}
	}
	if uint64(len(out)) != targetSize {
		return bad()
	}
	return out, nil
}

func hashHistoryObject(object historyObject, width int) string {
	header := []byte(fmt.Sprintf("%s %d\x00", object.kind, len(object.data)))
	raw := make([]byte, 0, len(header)+len(object.data))
	raw = append(raw, header...)
	raw = append(raw, object.data...)
	return hex.EncodeToString(gitObjectHash(raw, width))
}
func (r *historyReader) tree(treeOID string, denied []string, omitted *int) (map[string]historyFile, error) {
	result := map[string]historyFile{}
	seenPaths := map[string]string{}
	var walk func(string, string) error
	walk = func(oid, prefix string) error {
		obj, err := r.read(oid)
		if err != nil {
			return err
		}
		if obj.kind != "tree" {
			return c.Fail(c.StoreIntegrityError, "tree reference has wrong object type")
		}
		for at := 0; at < len(obj.data); {
			space := bytes.IndexByte(obj.data[at:], ' ')
			if space <= 0 {
				return c.Fail(c.StoreIntegrityError, "invalid tree entry")
			}
			mode64, e := strconv.ParseUint(string(obj.data[at:at+space]), 8, 32)
			if e != nil {
				return c.Fail(c.StoreIntegrityError, "invalid tree mode")
			}
			at += space + 1
			nul := bytes.IndexByte(obj.data[at:], 0)
			if nul <= 0 || len(obj.data)-at-nul-1 < r.width {
				return c.Fail(c.StoreIntegrityError, "invalid tree path or object ID")
			}
			name := string(obj.data[at : at+nul])
			at += nul + 1
			child := hex.EncodeToString(obj.data[at : at+r.width])
			at += r.width
			path := name
			if prefix != "" {
				path = prefix + "/" + name
			}
			if strings.ContainsAny(name, "/\x00") || name == "." || name == ".." || !policy.SafePath(path) {
				return c.Fail(c.UnsupportedCapability, "unsafe tree path")
			}
			key := strings.ToLower(path)
			if prior, exists := seenPaths[key]; exists && prior != path {
				return c.Fail(c.UnsupportedCapability, "case-alias tree paths are unsupported")
			}
			if seenPaths[key] != "" {
				return c.Fail(c.StoreIntegrityError, "duplicate Git tree path")
			}
			seenPaths[key] = path
			r.entries++
			if r.entries > r.limits.MaxTreeEntries {
				return c.Fail(c.UnsupportedCapability, "tree entry quota exceeded")
			}
			mode := uint32(mode64)
			if mode != 0040000 && mode != 0100644 && mode != 0100755 && mode != 0120000 && mode != 0160000 {
				return c.Fail(c.UnsupportedCapability, "unsupported Git tree mode")
			}
			if policy.DeniedPath(path, denied) {
				*omitted = *omitted + 1
				continue
			}
			if mode == 0040000 {
				if err := walk(child, path); err != nil {
					return err
				}
				continue
			}
			result[path] = historyFile{child, mode}
		}
		return nil
	}
	if err := walk(treeOID, ""); err != nil {
		return nil, err
	}
	return result, nil
}
func diffHistoryTrees(before, after map[string]historyFile) []HistoryChange {
	set := map[string]bool{}
	for path := range before {
		set[path] = true
	}
	for path := range after {
		set[path] = true
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := []HistoryChange{}
	for _, path := range paths {
		a, hasA := before[path]
		b, hasB := after[path]
		if hasA && hasB && a == b {
			continue
		}
		change := HistoryChange{Path: path}
		switch {
		case !hasA:
			change.Kind = "ADDED"
		case !hasB:
			change.Kind = "DELETED"
		case a.mode != b.mode && a.oid == b.oid:
			change.Kind = "MODE_CHANGED"
		default:
			change.Kind = "MODIFIED"
		}
		if hasA {
			change.BeforeObject, change.BeforeMode = a.oid, a.mode
		}
		if hasB {
			change.AfterObject, change.AfterMode = b.oid, b.mode
		}
		out = append(out, change)
	}
	return out
}
func sealGitHistory(report GitHistory) (GitHistory, error) {
	sort.Strings(report.Unknowns)
	report.ReportDigest = ""
	digest, err := c.Digest(report)
	if err != nil {
		return GitHistory{}, err
	}
	report.ReportDigest = digest
	return report, nil
}
