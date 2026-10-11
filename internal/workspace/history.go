package workspace

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	packed, packKnown := packedHistoryPresent(reader.root)
	if !packKnown {
		addUnknown("PACK_STORE_STATE_UNREADABLE_HISTORY_UNKNOWN")
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
					addUnknown("PACKED_GIT_OBJECTS_NOT_PARSED_HISTORY_UNKNOWN")
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
					addUnknown("PACKED_GIT_OBJECTS_NOT_PARSED_HISTORY_UNKNOWN")
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
						addUnknown("PACKED_GIT_OBJECTS_NOT_PARSED_HISTORY_UNKNOWN")
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
						addUnknown("PACKED_GIT_OBJECTS_NOT_PARSED_HISTORY_UNKNOWN")
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
	if len(report.Commits) > 0 && report.Commits[0].ObjectID != report.HeadObject {
		return c.Fail(c.StoreIntegrityError, "history does not begin at captured HEAD")
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
	return &historyReader{format: format, width: width, root: root, limits: limits}, nil
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
func (r *historyReader) read(oid string) (historyObject, error) {
	if !validObject(oid) || len(oid) != r.width*2 {
		return historyObject{}, c.Fail(c.StoreIntegrityError, "invalid object reference")
	}
	r.objects++
	if r.objects > r.limits.MaxObjects {
		return historyObject{}, c.Fail(c.UnsupportedCapability, "history object quota exceeded")
	}
	compressed, err := fileguard.ReadRegular(r.root, filepath.Join("objects", oid[:2], oid[2:]), r.limits.MaxObjectBytes)
	if errors.Is(err, os.ErrNotExist) {
		return historyObject{}, missingHistoryObject{}
	}
	if err != nil {
		return historyObject{}, err
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
	return historyObject{fields[0], raw[zero+1:]}, nil
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
