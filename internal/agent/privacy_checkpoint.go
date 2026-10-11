package agent

import (
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

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

const privacyCheckpointDirectory = "checkpoint"
const privacyCheckpointSchema = 1
const privacyCheckpointFileLimit int64 = privacyMetadataLimit
const privacyCheckpointAnchorLimit int64 = privacyRecordLimit

type privacyCheckpointSource struct {
	Sequence int64  `json:"sequence"`
	Digest   string `json:"digest"`
	Size     int64  `json:"size"`
}
type privacyCheckpointCopy struct {
	Sequence int64       `json:"sequence"`
	Copy     ManagedCopy `json:"copy"`
}
type privacyCheckpointDeletion struct {
	Sequence int64           `json:"sequence"`
	TaskID   string          `json:"task_id"`
	Command  DeletionCommand `json:"command"`
}
type privacyCheckpointAttempt struct {
	Sequence int64          `json:"sequence"`
	Attempt  privacyAttempt `json:"attempt"`
}
type privacyCheckpointRetention struct {
	Sequence  int64                  `json:"sequence"`
	Retention privacyRetentionRecord `json:"retention"`
}
type privacyCheckpoint struct {
	SchemaVersion   int                          `json:"schema_version"`
	AuthorityDigest string                       `json:"authority_digest"`
	PhysicalRoot    string                       `json:"physical_root"`
	Generation      int64                        `json:"generation"`
	BaseSequence    int64                        `json:"base_sequence"`
	BaseTail        string                       `json:"base_tail"`
	Sequence        int64                        `json:"sequence"`
	Tail            string                       `json:"tail"`
	Watermark       int64                        `json:"watermark"`
	ScopeDigest     string                       `json:"scope_digest"`
	Scopes          []privacyScope               `json:"scopes"`
	SourceRecords   []privacyCheckpointSource    `json:"source_records"`
	Copies          []privacyCheckpointCopy      `json:"copies"`
	Deletions       []privacyCheckpointDeletion  `json:"deletions"`
	Attempts        []privacyCheckpointAttempt   `json:"attempts"`
	Retentions      []privacyCheckpointRetention `json:"retentions"`
}
type privacyCheckpointAnchor struct {
	SchemaVersion    int    `json:"schema_version"`
	AuthorityDigest  string `json:"authority_digest"`
	Generation       int64  `json:"generation"`
	PreviousAnchor   string `json:"previous_anchor_digest,omitempty"`
	CheckpointDigest string `json:"checkpoint_digest"`
	Sequence         int64  `json:"sequence"`
	Tail             string `json:"tail"`
	Watermark        int64  `json:"watermark"`
	ScopeDigest      string `json:"scope_digest"`
	ScopeCount       int    `json:"scope_count"`
}
type privacyCheckpointIntent struct {
	SchemaVersion    int                     `json:"schema_version"`
	AuthorityDigest  string                  `json:"authority_digest"`
	PreviousAnchor   string                  `json:"previous_anchor_digest,omitempty"`
	Anchor           privacyCheckpointAnchor `json:"anchor"`
	AnchorDigest     string                  `json:"anchor_digest"`
	CheckpointDigest string                  `json:"checkpoint_digest"`
	ScopeDigest      string                  `json:"scope_digest"`
	SourceDigest     string                  `json:"source_digest"`
}

// privacyCheckpointRetirement is the durable proof that authorizes removal of
// exactly one checkpoint's source range. It is published only after the
// checkpoint is current and every registered owner has acknowledged it.
type privacyCheckpointRetirement struct {
	SchemaVersion    int                       `json:"schema_version"`
	AuthorityDigest  string                    `json:"authority_digest"`
	AnchorDigest     string                    `json:"anchor_digest"`
	PreviousAnchor   string                    `json:"previous_anchor_digest,omitempty"`
	CheckpointDigest string                    `json:"checkpoint_digest"`
	SourceDigest     string                    `json:"source_digest"`
	Sequence         int64                     `json:"sequence"`
	Tail             string                    `json:"tail"`
	Watermark        int64                     `json:"watermark"`
	SourceRecords    []privacyCheckpointSource `json:"source_records"`
}

// PrivacyCheckpointStatus describes a pinned projection and receipt-gated source
// retirement. Owner catalogs and full dedup/UNKNOWN state remain retained.
type PrivacyCheckpointStatus struct {
	Supported                     bool   `json:"checkpoint_supported"`
	Pending                       bool   `json:"checkpoint_pending"`
	RetirementEnabled             bool   `json:"retirement_enabled"`
	HistoricalRetirementPending   bool   `json:"historical_retirement_pending"`
	HistoricalRetirementReason    string `json:"historical_retirement_pending_reason,omitempty"`
	HistoricalDataFilesRetired    int    `json:"historical_data_files_retired"`
	HistoricalReceiptFilesRetired int    `json:"historical_receipt_files_retired"`
	Generation                    int64  `json:"generation"`
	Sequence                      int64  `json:"sequence"`
	Tail                          string `json:"tail"`
	Watermark                     int64  `json:"watermark"`
	AnchorDigest                  string `json:"anchor_digest,omitempty"`
	CheckpointDigest              string `json:"checkpoint_digest,omitempty"`
}

func emptyPrivacyView() privacyView {
	return privacyView{
		Copies: []ManagedCopy{}, CopySequences: map[string]int64{},
		Deletions: map[string]DeletionCommand{}, DeletionSequences: map[string]int64{},
		Attempts: []privacyAttempt{}, AttemptSequences: map[string]int64{},
		Retentions: map[string]privacyRetentionRecord{}, RetentionSequences: map[string]int64{},
		RetentionCommands: map[string]privacyRetentionRecord{},
	}
}

func privacyAttemptKey(attempt privacyAttempt) string {
	return attempt.Scope.PhysicalRoot + "\x00" + attempt.ChildTask
}

func privacyCheckpointIntentPath() string {
	return filepath.Join(privacyCheckpointDirectory, "intent.json")
}
func privacyCheckpointCurrentPath() string {
	return filepath.Join(privacyCheckpointDirectory, "current.json")
}
func privacyCheckpointNextPath() string {
	return filepath.Join(privacyCheckpointDirectory, "current.next")
}
func privacyCheckpointDataPath(digest string) string {
	return filepath.Join(privacyCheckpointDirectory, "data", digest+".json")
}
func privacyCheckpointAnchorHistoryPath(digest string) string {
	return filepath.Join(privacyCheckpointDirectory, "anchors", digest+".json")
}
func privacyCheckpointRetirementPath(digest string) string {
	return filepath.Join(privacyCheckpointDirectory, "retired", digest+".json")
}

func readPrivacyCheckpointBase(root *os.Root) (privacyView, *privacyCheckpoint, error) {
	return readPrivacyCheckpointBaseMode(root, false)
}

func readPrivacyCheckpointBaseMode(root *os.Root, allowPending bool) (privacyView, *privacyCheckpoint, error) {
	view := emptyPrivacyView()
	if _, err := root.Lstat(privacyCheckpointIntentPath()); err == nil && !allowPending {
		return view, nil, c.Fail(c.StaleAuthority, "privacy metadata checkpoint recovery is pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return view, nil, err
		}
	}
	if _, err := root.Lstat(privacyCheckpointNextPath()); err == nil && !allowPending {
		return view, nil, c.Fail(c.StoreIntegrityError, "uncommitted privacy checkpoint anchor")
	} else if !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return view, nil, err
		}
	}
	rawAnchor, err := fileguard.ReadRegular(root, privacyCheckpointCurrentPath(), 4096)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
		if !allowPending {
			err = rejectUnanchoredPrivacyCheckpoint(root)
		}
		if err != nil {
			return view, nil, err
		}
		return view, nil, nil
	}
	if err != nil {
		return view, nil, err
	}
	var anchor privacyCheckpointAnchor
	if c.DecodeStrict(rawAnchor, &anchor) != nil || validatePrivacyCheckpointAnchor(anchor) != nil {
		return view, nil, c.Fail(c.StoreIntegrityError, "current privacy checkpoint anchor is invalid")
	}
	canonicalAnchor, err := c.CanonicalV1(anchor)
	if err != nil || string(canonicalAnchor) != string(rawAnchor) {
		return view, nil, c.Fail(c.StoreIntegrityError, "current privacy checkpoint anchor is noncanonical")
	}
	anchorDigest := c.HashBytes(rawAnchor)
	if err = validatePrivacyCheckpointAnchorHistory(root, anchor, rawAnchor); err != nil {
		return view, nil, err
	}
	genesis, err := fileguard.ReadRegular(root, "authority.json", 4096)
	if err != nil {
		return view, nil, err
	}
	var authority PrivacyAuthority
	if c.DecodeStrict(genesis, &authority) != nil || authority.validate() != nil {
		return view, nil, c.Fail(c.StoreIntegrityError, "checkpoint authority genesis is invalid")
	}
	authorityDigest, _ := c.Digest(authority)
	if authorityDigest != anchor.AuthorityDigest {
		return view, nil, c.Fail(c.StaleAuthority, "checkpoint belongs to another privacy authority")
	}
	rawCheckpoint, err := fileguard.ReadRegular(root, privacyCheckpointDataPath(anchor.CheckpointDigest), privacyCheckpointFileLimit)
	if err != nil {
		return view, nil, err
	}
	if c.HashBytes(rawCheckpoint) != anchor.CheckpointDigest {
		return view, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint digest mismatch")
	}
	var checkpoint privacyCheckpoint
	if c.DecodeStrict(rawCheckpoint, &checkpoint) != nil || validatePrivacyCheckpoint(checkpoint, authority) != nil {
		return view, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint contents are invalid")
	}
	canonicalCheckpoint, err := c.CanonicalV1(checkpoint)
	if err != nil || string(canonicalCheckpoint) != string(rawCheckpoint) {
		return view, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint is noncanonical")
	}
	if checkpoint.Generation != anchor.Generation || checkpoint.Sequence != anchor.Sequence || checkpoint.Tail != anchor.Tail ||
		checkpoint.Watermark != anchor.Watermark || checkpoint.ScopeDigest != anchor.ScopeDigest || len(checkpoint.Scopes) != anchor.ScopeCount {
		return view, nil, c.Fail(c.StoreIntegrityError, "checkpoint and current anchor diverged")
	}
	if _, _, err = readPrivacyCheckpointRetirement(root, anchor, anchorDigest, checkpoint); err != nil {
		return view, nil, err
	}
	if err = validatePrivacyCheckpointScopeLineage(root, checkpoint, anchorDigest); err != nil {
		return view, nil, err
	}
	if allowPending {
		intent, _, intentErr := readCheckpointIntent(root)
		if intentErr == nil {
			if anchorDigest != intent.AnchorDigest && anchorDigest != intent.PreviousAnchor {
				return view, nil, c.Fail(c.StaleAuthority, "checkpoint intent does not extend the current anchor")
			}
		} else if !errors.Is(intentErr, os.ErrNotExist) {
			return view, nil, intentErr
		}
	}
	view, err = privacyViewFromCheckpoint(checkpoint, authority)
	if err != nil {
		return view, nil, err
	}
	return view, &checkpoint, nil
}

func rejectUnanchoredPrivacyCheckpoint(root *os.Root) error {
	info, err := root.Lstat(privacyCheckpointDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint directory is invalid")
	}
	dir, err := root.Open(privacyCheckpointDirectory)
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(64)
	if err == io.EOF {
		err = nil
	}
	err = errors.Join(err, dir.Close())
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "data" || name == "anchors" {
			data, openErr := root.Open(filepath.Join(privacyCheckpointDirectory, name))
			if openErr != nil {
				return openErr
			}
			entries, readErr := data.Readdirnames(1)
			if readErr == io.EOF {
				readErr = nil
			}
			readErr = errors.Join(readErr, data.Close())
			if readErr != nil {
				return readErr
			}
			if len(entries) != 0 {
				return c.Fail(c.StoreIntegrityError, "unanchored privacy checkpoint data")
			}
			continue
		}
		return c.Fail(c.StoreIntegrityError, "foreign privacy checkpoint member")
	}
	return nil
}

func validatePrivacyCheckpointAnchor(anchor privacyCheckpointAnchor) error {
	if anchor.SchemaVersion != privacyCheckpointSchema || !c.ValidDigest(anchor.AuthorityDigest) || anchor.Generation < 1 ||
		!c.ValidDigest(anchor.CheckpointDigest) || anchor.Sequence < 0 || anchor.Watermark < 0 || !c.ValidDigest(anchor.ScopeDigest) || anchor.ScopeCount < 1 {
		return c.Fail(c.StoreIntegrityError, "invalid current privacy checkpoint anchor")
	}
	if anchor.Sequence == 0 && anchor.Tail != "" || anchor.Sequence > 0 && !c.ValidDigest(anchor.Tail) {
		return c.Fail(c.StoreIntegrityError, "invalid checkpoint sequence/tail")
	}
	if anchor.Generation == 1 {
		if anchor.PreviousAnchor != "" {
			return c.Fail(c.StoreIntegrityError, "checkpoint genesis has a previous anchor")
		}
	} else if !c.ValidDigest(anchor.PreviousAnchor) {
		return c.Fail(c.StoreIntegrityError, "checkpoint anchor lineage is incomplete")
	}
	return nil
}

func validatePrivacyCheckpointAnchorHistory(root *os.Root, current privacyCheckpointAnchor, rawCurrent []byte) error {
	currentDigest := c.HashBytes(rawCurrent)
	currentHistory, err := fileguard.ReadRegular(root, privacyCheckpointAnchorHistoryPath(currentDigest), 4096)
	if err != nil || string(currentHistory) != string(rawCurrent) {
		return c.Fail(c.StoreIntegrityError, "current privacy checkpoint anchor history is missing or changed")
	}
	next := current
	for depth := int64(1); ; depth++ {
		if depth > privacyCheckpointAnchorLimit {
			return c.Fail(c.BudgetLimitReached, "privacy checkpoint anchor lineage limit reached")
		}
		if next.Generation == 1 {
			return nil
		}
		raw, readErr := fileguard.ReadRegular(root, privacyCheckpointAnchorHistoryPath(next.PreviousAnchor), 4096)
		if readErr != nil {
			return c.Fail(c.StaleAuthority, "previous privacy checkpoint anchor is unavailable")
		}
		if c.HashBytes(raw) != next.PreviousAnchor {
			return c.Fail(c.StoreIntegrityError, "previous privacy checkpoint anchor digest mismatch")
		}
		var prior privacyCheckpointAnchor
		if c.DecodeStrict(raw, &prior) != nil || validatePrivacyCheckpointAnchor(prior) != nil || prior.Generation+1 != next.Generation || prior.AuthorityDigest != current.AuthorityDigest {
			return c.Fail(c.StoreIntegrityError, "privacy checkpoint anchor lineage is invalid")
		}
		canonical, canonicalErr := c.CanonicalV1(prior)
		if canonicalErr != nil || string(canonical) != string(raw) {
			return c.Fail(c.StoreIntegrityError, "previous privacy checkpoint anchor is noncanonical")
		}
		next = prior
	}
}

func currentPrivacyCheckpointAnchor(root *os.Root) (*privacyCheckpointAnchor, string, error) {
	raw, err := fileguard.ReadRegular(root, privacyCheckpointCurrentPath(), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var anchor privacyCheckpointAnchor
	if c.DecodeStrict(raw, &anchor) != nil || validatePrivacyCheckpointAnchor(anchor) != nil {
		return nil, "", c.Fail(c.StoreIntegrityError, "current privacy checkpoint anchor is invalid")
	}
	canonical, err := c.CanonicalV1(anchor)
	if err != nil || string(canonical) != string(raw) {
		return nil, "", c.Fail(c.StoreIntegrityError, "current privacy checkpoint anchor is noncanonical")
	}
	return &anchor, c.HashBytes(raw), nil
}

func validatePrivacyCheckpoint(checkpoint privacyCheckpoint, authority PrivacyAuthority) error {
	if checkpoint.SchemaVersion != privacyCheckpointSchema || checkpoint.Generation < 1 || checkpoint.Sequence < 0 || checkpoint.BaseSequence < 0 || checkpoint.BaseSequence > checkpoint.Sequence || checkpoint.Sequence > privacyCheckpointAnchorLimit*privacyRecordLimit || checkpoint.Watermark < 0 ||
		checkpoint.AuthorityDigest == "" || !c.ValidDigest(checkpoint.ScopeDigest) || !c.ValidDigest(checkpoint.PhysicalRoot) ||
		checkpoint.AuthorityDigest != mustDigest(authority) || checkpoint.PhysicalRoot != authority.PhysicalRoot || len(checkpoint.Scopes) == 0 ||
		len(checkpoint.Scopes) > privacyCatalogLimit || len(checkpoint.SourceRecords) > privacyRecordLimit ||
		len(checkpoint.Copies) > privacyRecordLimit || len(checkpoint.Deletions) > privacyRecordLimit || len(checkpoint.Attempts) > privacyRecordLimit ||
		len(checkpoint.Retentions) > privacyRecordLimit {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint binding or quota invalid")
	}
	if (checkpoint.Sequence == 0 && checkpoint.Tail != "") || (checkpoint.Sequence > 0 && !c.ValidDigest(checkpoint.Tail)) {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint tail invalid")
	}
	if checkpoint.BaseSequence == 0 && checkpoint.BaseTail != "" || checkpoint.BaseSequence > 0 && !c.ValidDigest(checkpoint.BaseTail) {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint base anchor invalid")
	}
	if int64(len(checkpoint.SourceRecords)) != checkpoint.Sequence-checkpoint.BaseSequence {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint source range is incomplete")
	}
	scopeDigest, err := c.Digest(checkpoint.Scopes)
	if err != nil || scopeDigest != checkpoint.ScopeDigest {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint owner lineage digest mismatch")
	}
	previous := checkpoint.BaseSequence
	for _, source := range checkpoint.SourceRecords {
		if source.Sequence != previous+1 || source.Sequence > checkpoint.Sequence || !c.ValidDigest(source.Digest) || source.Size < 1 || source.Size > 16<<20 {
			return c.Fail(c.StoreIntegrityError, "privacy checkpoint source inventory invalid")
		}
		previous = source.Sequence
	}
	if checkpoint.Sequence == checkpoint.BaseSequence && checkpoint.Tail != checkpoint.BaseTail || checkpoint.Sequence > checkpoint.BaseSequence && checkpoint.SourceRecords[len(checkpoint.SourceRecords)-1].Digest != checkpoint.Tail {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint tail differs from source inventory")
	}
	return nil
}

func mustDigest(value any) string {
	digest, _ := c.Digest(value)
	return digest
}

func validatePrivacyCheckpointScopeLineage(root *os.Root, checkpoint privacyCheckpoint, anchorDigest string) error {
	scopes, _, err := readPrivacyScopes(root)
	if err != nil {
		return err
	}
	current := map[string]privacyScope{}
	for _, scope := range scopes {
		current[scopeName(scope)] = scope
	}
	base := map[string]bool{}
	for _, scope := range checkpoint.Scopes {
		name := scopeName(scope)
		if actual, ok := current[name]; !ok || actual != scope {
			return c.Fail(c.StaleAuthority, "checkpoint owner scope was removed or rebound")
		}
		base[name] = true
	}
	for name, scope := range current {
		if base[name] {
			continue
		}
		if scope.CheckpointAnchor != anchorDigest || !c.ValidDigest(scope.RestoreBinding) {
			return c.Fail(c.StaleAuthority, "unbound owner scope appeared after the current checkpoint")
		}
	}
	return nil
}

func privacyViewFromCheckpoint(checkpoint privacyCheckpoint, authority PrivacyAuthority) (privacyView, error) {
	view := emptyPrivacyView()
	type entry struct {
		sequence int64
		typeName string
		index    int
	}
	entries := make([]entry, 0, len(checkpoint.Copies)+len(checkpoint.Deletions)+len(checkpoint.Attempts)+len(checkpoint.Retentions))
	for i, item := range checkpoint.Copies {
		entries = append(entries, entry{item.Sequence, "MANAGED_COPY", i})
	}
	for i, item := range checkpoint.Deletions {
		entries = append(entries, entry{item.Sequence, "DELETE_INTENT", i})
	}
	for i, item := range checkpoint.Attempts {
		entries = append(entries, entry{item.Sequence, "ATTEMPT_LINEAGE", i})
	}
	for i, item := range checkpoint.Retentions {
		entries = append(entries, entry{item.Sequence, "RETENTION_POLICY", i})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sequence < entries[j].sequence })
	for i := 1; i < len(entries); i++ {
		if entries[i-1].sequence == entries[i].sequence {
			return view, c.Fail(c.StoreIntegrityError, "checkpoint assigns one sequence to multiple facts")
		}
	}
	retentionAuthority, _ := c.Digest(authority)
	reservedIDs := map[[32]byte]bool{}
	retentionRevisions := map[string]int64{}
	copyPaths := map[string]bool{}
	attempts := map[string]bool{}
	for _, item := range entries {
		if item.sequence < 1 || item.sequence > checkpoint.Sequence {
			return view, c.Fail(c.StoreIntegrityError, "checkpoint fact sequence out of range")
		}
		switch item.typeName {
		case "MANAGED_COPY":
			copy := checkpoint.Copies[item.index].Copy
			if validateManagedCopy(copy) != nil || copyPaths[copy.Directory] {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint managed copy invalid or duplicated")
			}
			for _, task := range copy.Tasks {
				if _, deleted := view.Deletions[task]; deleted {
					return view, c.Fail(c.StoreIntegrityError, "checkpoint copy was published after deletion")
				}
			}
			copyPaths[copy.Directory] = true
			view.Copies = append(view.Copies, copy)
			view.CopySequences[copy.Directory] = item.sequence
		case "DELETE_INTENT":
			entry := checkpoint.Deletions[item.index]
			command := entry.Command
			if entry.TaskID != command.Plan.TaskID || validateDeletionCommand(command) != nil || retentionDeletionCollision(view, command) {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint deletion command invalid")
			}
			for _, id := range deletionCommandIDs(command) {
				key := sha256.Sum256([]byte(id))
				if reservedIDs[key] {
					return view, c.Fail(c.StoreIntegrityError, "checkpoint deletion dedup namespace collision")
				}
				reservedIDs[key] = true
			}
			if command.Expiry != nil {
				policy, exists := view.Retentions[command.Plan.TaskID]
				expected, _ := c.Digest(policy.Policy)
				actual, _ := c.Digest(command.Expiry.Policy)
				if !exists || expected != actual {
					return view, c.Fail(c.StoreIntegrityError, "checkpoint expiry lost current retention consent")
				}
			}
			if _, exists := view.Deletions[entry.TaskID]; exists || command.Plan.Watermark != view.Watermark+1 {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint deletion watermark or identity gap")
			}
			view.Watermark++
			view.Deletions[entry.TaskID] = command
			view.DeletionSequences[entry.TaskID] = item.sequence
		case "ATTEMPT_LINEAGE":
			attempt := checkpoint.Attempts[item.index].Attempt
			if attempt.validate() != nil {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint attempt lineage invalid")
			}
			key := privacyAttemptKey(attempt)
			if attempts[key] {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint attempt lineage duplicated")
			}
			if _, deleted := view.Deletions[attempt.ParentTask]; deleted {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint attempt follows parent deletion")
			}
			if _, deleted := view.Deletions[attempt.ChildTask]; deleted {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint attempt follows child deletion")
			}
			attempts[key] = true
			view.Attempts = append(view.Attempts, attempt)
			view.AttemptSequences[key] = item.sequence
		case "RETENTION_POLICY":
			retention := checkpoint.Retentions[item.index].Retention
			if retention.validate() != nil || retention.Policy.AuthorityDigest != retentionAuthority {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint retention authority invalid")
			}
			if _, deleted := view.Deletions[retention.Policy.TaskID]; deleted {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint retention follows deletion")
			}
			if _, exists := view.RetentionCommands[retention.CommandID]; exists || retention.Policy.Revision != retentionRevisions[retention.Policy.TaskID]+1 {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint retention revision or command gap")
			}
			if reservedIDs[sha256.Sum256([]byte(retention.CommandID))] {
				return view, c.Fail(c.StoreIntegrityError, "checkpoint retention collides with deletion namespace")
			}
			retentionRevisions[retention.Policy.TaskID] = retention.Policy.Revision
			view.Retentions[retention.Policy.TaskID] = retention
			view.RetentionCommands[retention.CommandID] = retention
			view.RetentionSequences[retention.CommandID] = item.sequence
		}
	}
	if int64(len(view.Deletions)) != checkpoint.Watermark || checkpoint.Watermark != view.Watermark || len(entries) == 0 && checkpoint.Sequence != 0 {
		return view, c.Fail(c.StoreIntegrityError, "checkpoint sequence or deletion ledger incomplete")
	}
	sort.Slice(view.Copies, func(i, j int) bool { return view.Copies[i].Directory < view.Copies[j].Directory })
	sort.Slice(view.Attempts, func(i, j int) bool { return privacyAttemptKey(view.Attempts[i]) < privacyAttemptKey(view.Attempts[j]) })
	view.Sequence, view.Tail, view.Watermark = checkpoint.Sequence, checkpoint.Tail, checkpoint.Watermark
	view.RecordCount = 0
	return view, nil
}

func privacyCheckpointPin(anchor privacyCheckpointAnchor, anchorDigest, authorityDigest string, scope privacyScope, phase, restoreBinding string) store.PrivacyCheckpointPin {
	return store.PrivacyCheckpointPin{
		SchemaVersion: 1, Phase: phase, AuthorityDigest: authorityDigest,
		AnchorDigest: anchorDigest, PreviousAnchor: anchor.PreviousAnchor,
		Generation: anchor.Generation, Sequence: anchor.Sequence, Tail: anchor.Tail,
		Watermark: anchor.Watermark, InstanceID: scope.InstanceID,
		PhysicalRoot: scope.PhysicalRoot, RestoreBinding: restoreBinding,
	}
}

func checkpointSourceDigest(records []privacyCheckpointSource) string {
	digest, _ := c.Digest(records)
	return digest
}

func validatePrivacyCheckpointRetirement(receipt privacyCheckpointRetirement, anchor privacyCheckpointAnchor, anchorDigest string, checkpoint privacyCheckpoint) error {
	if receipt.SchemaVersion != privacyCheckpointSchema || receipt.AuthorityDigest != anchor.AuthorityDigest ||
		receipt.AnchorDigest != anchorDigest || receipt.PreviousAnchor != anchor.PreviousAnchor ||
		receipt.CheckpointDigest != anchor.CheckpointDigest || receipt.SourceDigest != checkpointSourceDigest(checkpoint.SourceRecords) ||
		receipt.Sequence != anchor.Sequence || receipt.Tail != anchor.Tail || receipt.Watermark != anchor.Watermark ||
		len(receipt.SourceRecords) != len(checkpoint.SourceRecords) {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint retirement receipt binding mismatch")
	}
	for i := range receipt.SourceRecords {
		if receipt.SourceRecords[i] != checkpoint.SourceRecords[i] {
			return c.Fail(c.StoreIntegrityError, "privacy checkpoint retirement inventory changed")
		}
	}
	return nil
}

func readPrivacyCheckpointRetirement(root *os.Root, anchor privacyCheckpointAnchor, anchorDigest string, checkpoint privacyCheckpoint) (*privacyCheckpointRetirement, []byte, error) {
	raw, err := fileguard.ReadRegular(root, privacyCheckpointRetirementPath(anchorDigest), privacyCheckpointFileLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var receipt privacyCheckpointRetirement
	if c.DecodeStrict(raw, &receipt) != nil || validatePrivacyCheckpointRetirement(receipt, anchor, anchorDigest, checkpoint) != nil {
		return nil, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint retirement receipt is invalid")
	}
	canonical, err := c.CanonicalV1(receipt)
	if err != nil || string(canonical) != string(raw) {
		return nil, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint retirement receipt is noncanonical")
	}
	return &receipt, raw, nil
}

func publishPrivacyCheckpointRetirement(root *os.Root, anchor privacyCheckpointAnchor, anchorDigest string, checkpoint privacyCheckpoint) error {
	receipt := privacyCheckpointRetirement{
		SchemaVersion: privacyCheckpointSchema, AuthorityDigest: anchor.AuthorityDigest,
		AnchorDigest: anchorDigest, PreviousAnchor: anchor.PreviousAnchor,
		CheckpointDigest: anchor.CheckpointDigest, SourceDigest: checkpointSourceDigest(checkpoint.SourceRecords),
		Sequence: anchor.Sequence, Tail: anchor.Tail, Watermark: anchor.Watermark,
		SourceRecords: append([]privacyCheckpointSource{}, checkpoint.SourceRecords...),
	}
	if err := validatePrivacyCheckpointRetirement(receipt, anchor, anchorDigest, checkpoint); err != nil {
		return err
	}
	raw, err := c.CanonicalV1(receipt)
	if err != nil || int64(len(raw)) > privacyCheckpointFileLimit {
		return c.Fail(c.BudgetLimitReached, "privacy checkpoint retirement receipt exceeds its bound")
	}
	if err = root.MkdirAll(filepath.Join(privacyCheckpointDirectory, "retired"), 0700); err != nil {
		return err
	}
	if err = publishCheckpointImmutable(root, privacyCheckpointRetirementPath(anchorDigest), raw, privacyCheckpointFileLimit); err != nil {
		return err
	}
	return fileguard.SyncDirectory(root, filepath.Join(privacyCheckpointDirectory, "retired"))
}

// verifyCheckpointSourceFiles binds the exact source range to the immutable
// checkpoint. Entries after the anchor are an append-only tail and are checked
// by readPrivacyMode; anything else in the records directory is unexpected.
func verifyCheckpointSourceFiles(root *os.Root, checkpoint privacyCheckpoint, allowMissing bool) error {
	dir, err := root.Open("records")
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(privacyRecordLimit + 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	err = errors.Join(err, dir.Close())
	if err != nil {
		return err
	}
	if len(names) > privacyRecordLimit {
		return c.Fail(c.BudgetLimitReached, "privacy source inventory exceeds its bound")
	}
	expected := make(map[int64]privacyCheckpointSource, len(checkpoint.SourceRecords))
	for _, source := range checkpoint.SourceRecords {
		expected[source.Sequence] = source
	}
	for _, name := range names {
		sequence, parseErr := strconv.ParseInt(strings.TrimSuffix(name, ".json"), 10, 64)
		if parseErr != nil || name != fmt.Sprintf("%010d.json", sequence) || sequence < 1 {
			return c.Fail(c.StoreIntegrityError, "foreign privacy journal source entry")
		}
		if sequence <= checkpoint.BaseSequence {
			return c.Fail(c.StoreIntegrityError, "retired privacy source reappeared")
		}
		if sequence <= checkpoint.Sequence {
			if _, ok := expected[sequence]; !ok {
				return c.Fail(c.StoreIntegrityError, "unexpected source record inside checkpoint range")
			}
		}
	}
	previous := checkpoint.BaseTail
	missing := false
	for _, source := range checkpoint.SourceRecords {
		name := filepath.Join("records", fmt.Sprintf("%010d.json", source.Sequence))
		info, statErr := root.Lstat(name)
		if errors.Is(statErr, os.ErrNotExist) {
			if !allowMissing {
				return c.Fail(c.StoreIntegrityError, "privacy checkpoint source record is missing without a receipt")
			}
			missing = true
			previous = ""
			continue
		}
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return c.Fail(c.StoreIntegrityError, "privacy checkpoint source is not a regular file")
		}
		raw, readErr := fileguard.ReadRegular(root, name, 16<<20)
		if readErr != nil || int64(len(raw)) != source.Size || c.HashBytes(raw) != source.Digest {
			return c.Fail(c.StoreIntegrityError, "privacy checkpoint source bytes changed")
		}
		var record privacyRecord
		if c.DecodeStrict(raw, &record) != nil || record.SchemaVersion != 1 || record.Sequence != source.Sequence {
			return c.Fail(c.StoreIntegrityError, "privacy checkpoint source record is invalid")
		}
		if !missing && record.Previous != previous {
			return c.Fail(c.StoreIntegrityError, "privacy checkpoint source hash chain changed")
		}
		previous = source.Digest
	}
	if checkpoint.Sequence == checkpoint.BaseSequence {
		if checkpoint.Tail != checkpoint.BaseTail {
			return c.Fail(c.StoreIntegrityError, "empty checkpoint source range changed its tail")
		}
	} else if !missing && previous != checkpoint.Tail {
		return c.Fail(c.StoreIntegrityError, "privacy checkpoint source tail changed")
	}
	return nil
}

func retirePrivacyCheckpointSources(ctx context.Context, root *os.Root, authority PrivacyAuthority, anchor privacyCheckpointAnchor, anchorDigest string, checkpoint privacyCheckpoint) error {
	if err := validateCurrentPrivacyPinsReadOnly(ctx, root, authority); err != nil {
		return err
	}
	view, err := readPrivacyCheckpointRecoveryView(root)
	if err != nil {
		return err
	}
	if view.Sequence < anchor.Sequence || view.Watermark < anchor.Watermark {
		return c.Fail(c.StaleAuthority, "privacy state is behind its checkpoint before retirement")
	}
	receipt, _, err := readPrivacyCheckpointRetirement(root, anchor, anchorDigest, checkpoint)
	if err != nil {
		return err
	}
	if receipt == nil {
		if err = verifyCheckpointSourceFiles(root, checkpoint, false); err != nil {
			return err
		}
		if err = publishPrivacyCheckpointRetirement(root, anchor, anchorDigest, checkpoint); err != nil {
			return err
		}
		receipt, _, err = readPrivacyCheckpointRetirement(root, anchor, anchorDigest, checkpoint)
		if err != nil || receipt == nil {
			return errors.Join(err, c.Fail(c.StoreIntegrityError, "privacy retirement receipt publication did not persist"))
		}
	}
	if err = validateCurrentPrivacyPinsReadOnly(ctx, root, authority); err != nil {
		return err
	}
	if err = verifyCheckpointSourceFiles(root, checkpoint, true); err != nil {
		return err
	}
	for _, source := range checkpoint.SourceRecords {
		name := filepath.Join("records", fmt.Sprintf("%010d.json", source.Sequence))
		info, statErr := root.Lstat(name)
		if errors.Is(statErr, os.ErrNotExist) {
			continue // Exact immutable receipt above authorizes this missing member.
		}
		if statErr != nil {
			return statErr
		}
		if _, err = fileguard.RemoveBoundIdentity(root, name, info, source.Digest, source.Size, 16<<20); err != nil {
			return err
		}
	}
	if err = fileguard.SyncDirectory(root, "records"); err != nil {
		return err
	}
	if _, err = readPrivacyCheckpointRecoveryView(root); err != nil {
		return err
	}
	return nil
}

func readCheckpointIntent(root *os.Root) (privacyCheckpointIntent, []byte, error) {
	var intent privacyCheckpointIntent
	raw, err := fileguard.ReadRegular(root, privacyCheckpointIntentPath(), 1<<20)
	if err != nil {
		return intent, nil, err
	}
	if c.DecodeStrict(raw, &intent) != nil || intent.SchemaVersion != privacyCheckpointSchema || !c.ValidDigest(intent.AuthorityDigest) ||
		intent.AnchorDigest != c.HashBytes(mustCanonical(intent.Anchor)) || intent.CheckpointDigest != intent.Anchor.CheckpointDigest ||
		intent.ScopeDigest != intent.Anchor.ScopeDigest || !c.ValidDigest(intent.SourceDigest) || validatePrivacyCheckpointAnchor(intent.Anchor) != nil ||
		intent.AuthorityDigest != intent.Anchor.AuthorityDigest || intent.PreviousAnchor != intent.Anchor.PreviousAnchor {
		return intent, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint migration intent invalid")
	}
	canonical, err := c.CanonicalV1(intent)
	if err != nil || string(canonical) != string(raw) {
		return intent, nil, c.Fail(c.StoreIntegrityError, "privacy checkpoint migration intent noncanonical")
	}
	return intent, raw, nil
}

func mustCanonical(value any) []byte {
	raw, _ := c.CanonicalV1(value)
	return raw
}

func privacyCheckpointMetadataStatus(anchor *privacyCheckpointAnchor, pending bool) map[string]any {
	result := map[string]any{"checkpoint_supported": false, "checkpoint_pending": pending, "retirement_enabled": false}
	if anchor != nil {
		result["checkpoint_generation"] = anchor.Generation
		result["checkpoint_sequence"] = anchor.Sequence
		result["checkpoint_tail"] = anchor.Tail
		result["checkpoint_watermark"] = anchor.Watermark
		result["checkpoint_anchor_digest"] = c.HashBytes(mustCanonical(anchor))
	}
	return result
}

// CheckpointPrivacyMetadata publishes a lossless current-state projection and
// two-phase owner witnesses, then retires only its exact receipt-bound source
// journal range. Historical checkpoint/catalog metadata remains bounded.
func (s *Session) CheckpointPrivacyMetadata(ctx context.Context) (PrivacyCheckpointStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := PrivacyCheckpointStatus{RetirementEnabled: false}
	if s.privacy == nil || s.Journal == nil {
		return status, c.Fail(c.UnsupportedCapability, "bound privacy authority required")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return status, err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return status, err
	}
	defer lock.Close()
	liveOperation, err := hasLivePrivacyOperationLease(ctx, root, *s.privacy)
	if err != nil {
		return status, err
	}
	if liveOperation {
		return status, c.Fail(c.StoreOwned, "privacy checkpoint is blocked by a live backup or restore operation")
	}
	if err = recoverPrivacyCheckpoint(ctx, root, *s.privacy, s.Journal, s.instance, s.directory); err != nil {
		return status, err
	}
	view, err := readPrivacy(root)
	if err != nil {
		return status, err
	}
	if err = validateCurrentPrivacyPins(ctx, root, *s.privacy, view, s.Journal, s.instance, s.directory); err != nil {
		return status, err
	}
	scopes, _, err := readPrivacyScopes(root)
	if err != nil {
		return status, err
	}
	if len(scopes) == 0 {
		return status, c.Fail(c.StaleAuthority, "checkpoint requires a registered owner scope")
	}
	if privacyCheckpointHasUnfinishedRestore(view, scopes) {
		return status, c.Fail(c.StaleAuthority, "finish or recover the registered restore stage before advancing the privacy checkpoint")
	}
	previous, previousDigest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil {
		return status, err
	}
	generation := int64(1)
	if previous != nil {
		generation = previous.Generation + 1
	}
	if generation > privacyCheckpointAnchorLimit {
		return status, c.Fail(c.BudgetLimitReached, "privacy checkpoint lineage bound reached")
	}
	baseSequence, baseTail := int64(0), ""
	if previous != nil {
		baseSequence, baseTail = previous.Sequence, previous.Tail
	}
	checkpoint, err := buildPrivacyCheckpoint(root, *s.privacy, view, scopes, generation, baseSequence, baseTail)
	if err != nil {
		return status, err
	}
	rawCheckpoint, err := c.CanonicalV1(checkpoint)
	if err != nil || len(rawCheckpoint) < 1 || int64(len(rawCheckpoint)) > privacyCheckpointFileLimit {
		return status, c.Fail(c.BudgetLimitReached, "bounded privacy checkpoint encoding unavailable")
	}
	checkpointDigest := c.HashBytes(rawCheckpoint)
	anchor := privacyCheckpointAnchor{
		SchemaVersion: privacyCheckpointSchema, AuthorityDigest: mustDigest(*s.privacy),
		Generation: generation, PreviousAnchor: previousDigest, CheckpointDigest: checkpointDigest,
		Sequence: view.Sequence, Tail: view.Tail, Watermark: view.Watermark,
		ScopeDigest: checkpoint.ScopeDigest, ScopeCount: len(scopes),
	}
	rawAnchor, err := c.CanonicalV1(anchor)
	if err != nil {
		return status, err
	}
	anchorDigest := c.HashBytes(rawAnchor)
	intent := privacyCheckpointIntent{
		SchemaVersion: privacyCheckpointSchema, AuthorityDigest: anchor.AuthorityDigest,
		PreviousAnchor: anchor.PreviousAnchor, Anchor: anchor, AnchorDigest: anchorDigest,
		CheckpointDigest: checkpointDigest, ScopeDigest: checkpoint.ScopeDigest,
		SourceDigest: checkpointSourceDigest(checkpoint.SourceRecords),
	}
	rawIntent, err := c.CanonicalV1(intent)
	if err != nil {
		return status, err
	}
	retained, err := privacyMetadataBytes(root)
	if err != nil {
		return status, err
	}
	reserved := int64(len(rawCheckpoint)+len(rawAnchor)+len(rawIntent))*2 + 8192
	if retained > privacyMetadataLimit || reserved > privacyMetadataLimit-retained {
		return status, c.Fail(c.BudgetLimitReached, "checkpoint would exceed the bounded privacy metadata allocation")
	}
	if err = admitPrivacyDisk(root, reserved, false); err != nil {
		return status, err
	}
	if err = root.MkdirAll(filepath.Join(privacyCheckpointDirectory, "data"), 0700); err != nil {
		return status, err
	}
	if err = root.MkdirAll(filepath.Join(privacyCheckpointDirectory, "anchors"), 0700); err != nil {
		return status, err
	}
	if err = publishCheckpointImmutable(root, privacyCheckpointDataPath(checkpointDigest), rawCheckpoint, privacyCheckpointFileLimit); err != nil {
		return status, err
	}
	if err = publishCheckpointImmutable(root, privacyCheckpointIntentPath(), rawIntent, 1<<20); err != nil {
		return status, err
	}
	if err = recoverPrivacyCheckpoint(ctx, root, *s.privacy, s.Journal, s.instance, s.directory); err != nil {
		return status, err
	}
	view, err = readPrivacy(root)
	if err != nil {
		return status, err
	}
	pruned, err := pruneHistoricalPrivacyCheckpointMetadata(ctx, root, *s.privacy, view)
	if err != nil {
		return status, err
	}
	status = PrivacyCheckpointStatus{
		Supported: true, RetirementEnabled: true, Generation: generation,
		Sequence: view.Sequence, Tail: view.Tail, Watermark: view.Watermark,
		AnchorDigest: anchorDigest, CheckpointDigest: checkpointDigest,
		HistoricalRetirementPending:   pruned.Pending,
		HistoricalRetirementReason:    pruned.Reason,
		HistoricalDataFilesRetired:    pruned.DataFiles,
		HistoricalReceiptFilesRetired: pruned.ReceiptFiles,
	}
	return status, nil
}

func buildPrivacyCheckpoint(root *os.Root, authority PrivacyAuthority, view privacyView, scopes []privacyScope, generation, baseSequence int64, baseTail string) (privacyCheckpoint, error) {
	checkpoint := privacyCheckpoint{
		SchemaVersion: privacyCheckpointSchema, AuthorityDigest: mustDigest(authority),
		PhysicalRoot: authority.PhysicalRoot, Generation: generation, BaseSequence: baseSequence, BaseTail: baseTail, Sequence: view.Sequence,
		Tail: view.Tail, Watermark: view.Watermark, Scopes: append([]privacyScope{}, scopes...),
		SourceRecords: []privacyCheckpointSource{}, Copies: []privacyCheckpointCopy{},
		Deletions: []privacyCheckpointDeletion{}, Attempts: []privacyCheckpointAttempt{},
		Retentions: []privacyCheckpointRetention{},
	}
	checkpoint.ScopeDigest, _ = c.Digest(checkpoint.Scopes)
	if baseSequence < 0 || baseSequence > view.Sequence || view.Sequence > privacyCheckpointAnchorLimit*privacyRecordLimit || int64(view.RecordCount) != view.Sequence-baseSequence || (baseSequence == 0 && baseTail != "") || (baseSequence > 0 && !c.ValidDigest(baseTail)) {
		return checkpoint, c.Fail(c.StoreIntegrityError, "privacy checkpoint source range is not bounded and complete")
	}
	previous := baseTail
	for sequence := baseSequence + 1; sequence <= view.Sequence; sequence++ {
		name := filepath.Join("records", fmt.Sprintf("%010d.json", sequence))
		raw, err := fileguard.ReadRegular(root, name, 16<<20)
		if err != nil {
			return checkpoint, err
		}
		var record privacyRecord
		if c.DecodeStrict(raw, &record) != nil || record.SchemaVersion != 1 || record.Sequence != sequence || record.Previous != previous {
			return checkpoint, c.Fail(c.StoreIntegrityError, "privacy journal changed while checkpointing")
		}
		checkpoint.SourceRecords = append(checkpoint.SourceRecords, privacyCheckpointSource{Sequence: sequence, Digest: c.HashBytes(raw), Size: int64(len(raw))})
		previous = c.HashBytes(raw)
	}
	if previous != view.Tail {
		return checkpoint, c.Fail(c.StaleBase, "privacy journal tail changed while checkpointing")
	}
	for _, copy := range view.Copies {
		sequence := view.CopySequences[copy.Directory]
		if sequence < 1 {
			return checkpoint, c.Fail(c.StoreIntegrityError, "managed copy lost its source sequence")
		}
		checkpoint.Copies = append(checkpoint.Copies, privacyCheckpointCopy{Sequence: sequence, Copy: copy})
	}
	for task, command := range view.Deletions {
		sequence := view.DeletionSequences[task]
		if sequence < 1 {
			return checkpoint, c.Fail(c.StoreIntegrityError, "deletion ledger lost its source sequence")
		}
		checkpoint.Deletions = append(checkpoint.Deletions, privacyCheckpointDeletion{Sequence: sequence, TaskID: task, Command: command})
	}
	for _, attempt := range view.Attempts {
		key := privacyAttemptKey(attempt)
		sequence := view.AttemptSequences[key]
		if sequence < 1 {
			return checkpoint, c.Fail(c.StoreIntegrityError, "attempt lineage lost its source sequence")
		}
		checkpoint.Attempts = append(checkpoint.Attempts, privacyCheckpointAttempt{Sequence: sequence, Attempt: attempt})
	}
	for id, retention := range view.RetentionCommands {
		sequence := view.RetentionSequences[id]
		if sequence < 1 {
			return checkpoint, c.Fail(c.StoreIntegrityError, "retention command lost its source sequence")
		}
		checkpoint.Retentions = append(checkpoint.Retentions, privacyCheckpointRetention{Sequence: sequence, Retention: retention})
	}
	sort.Slice(checkpoint.Copies, func(i, j int) bool { return checkpoint.Copies[i].Sequence < checkpoint.Copies[j].Sequence })
	sort.Slice(checkpoint.Deletions, func(i, j int) bool { return checkpoint.Deletions[i].Sequence < checkpoint.Deletions[j].Sequence })
	sort.Slice(checkpoint.Attempts, func(i, j int) bool { return checkpoint.Attempts[i].Sequence < checkpoint.Attempts[j].Sequence })
	sort.Slice(checkpoint.Retentions, func(i, j int) bool { return checkpoint.Retentions[i].Sequence < checkpoint.Retentions[j].Sequence })
	return checkpoint, validatePrivacyCheckpoint(checkpoint, authority)
}

func publishCheckpointImmutable(root *os.Root, path string, raw []byte, limit int64) error {
	existing, err := fileguard.ReadRegular(root, path, limit)
	if err == nil {
		if string(existing) != string(raw) {
			return c.Fail(c.StoreIntegrityError, "immutable privacy checkpoint object changed")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fileguard.Publish(root, path, raw)
}

func privacyRuntimeForScope(scope privacyScope) error {
	root, err := os.OpenRoot(scope.Directory)
	if err != nil {
		return c.Fail(c.StaleAuthority, "registered owner store root is unavailable")
	}
	defer root.Close()
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil || physical != scope.PhysicalRoot {
		return c.Fail(c.StaleAuthority, "registered owner store physical identity changed")
	}
	raw, err := fileguard.ReadRegular(root, runtimeInstanceFile, 1024)
	if err != nil {
		return err
	}
	var instance RuntimeInstance
	if c.DecodeStrict(raw, &instance) != nil || instance.SchemaVersion != 1 || instance.ID != scope.InstanceID || instance.PhysicalRoot != scope.PhysicalRoot {
		return c.Fail(c.StaleAuthority, "registered owner runtime identity changed")
	}
	canonical, err := c.CanonicalV1(instance)
	if err != nil || string(canonical) != string(raw) {
		return c.Fail(c.StoreIntegrityError, "registered owner runtime metadata is noncanonical")
	}
	return nil
}

func withPrivacyScopeStore(ctx context.Context, authority PrivacyAuthority, scope privacyScope, preferred *store.Store, preferredInstance RuntimeInstance, preferredDirectory string, fn func(*store.Store) error) error {
	if !validPrivacyScope(scope) {
		return c.Fail(c.StoreIntegrityError, "invalid registered owner scope")
	}
	if err := privacyRuntimeForScope(scope); err != nil {
		return err
	}
	journal := preferred
	opened := false
	preferredPath := ""
	if journal != nil {
		var err error
		preferredPath, err = filepath.EvalSymlinks(preferredDirectory)
		if err == nil {
			preferredPath, err = filepath.Abs(preferredPath)
		}
		if err != nil {
			return c.Fail(c.StaleAuthority, "current owner directory unavailable during checkpoint admission")
		}
	}
	if journal == nil || scope.InstanceID != preferredInstance.ID || scope.PhysicalRoot != preferredInstance.PhysicalRoot || filepath.Clean(scope.Directory) != filepath.Clean(preferredPath) {
		var err error
		journal, err = store.Open(ctx, scope.Directory)
		if err != nil {
			return err
		}
		opened = true
	}
	if opened {
		defer journal.Close()
	}
	raw, err := journal.PrivacyAuthority(ctx)
	expected, _ := c.CanonicalV1(authority)
	if err != nil || string(raw) != string(expected) {
		return c.Fail(c.StaleAuthority, "registered owner store lost its write-once privacy authority")
	}
	return fn(journal)
}

func expectedPrivacyCheckpointPin(anchor privacyCheckpointAnchor, anchorDigest, authorityDigest string, scope privacyScope, phase string) store.PrivacyCheckpointPin {
	return privacyCheckpointPin(anchor, anchorDigest, authorityDigest, scope, phase, scope.RestoreBinding)
}

func validateCurrentPrivacyPins(ctx context.Context, root *os.Root, authority PrivacyAuthority, view privacyView, preferred *store.Store, preferredInstance RuntimeInstance, preferredDirectory string) error {
	anchor, anchorDigest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil {
		return err
	}
	scopes, _, err := readPrivacyScopes(root)
	if err != nil {
		return err
	}
	if anchor == nil {
		for _, scope := range scopes {
			err = withPrivacyScopeStore(ctx, authority, scope, preferred, preferredInstance, preferredDirectory, func(journal *store.Store) error {
				pin, pinErr := journal.PrivacyCheckpointPin(ctx)
				if pinErr != nil {
					return pinErr
				}
				if pin != nil {
					return c.Fail(c.StaleAuthority, "owner store has a checkpoint pin but the external current anchor is missing")
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	authorityDigest, _ := c.Digest(authority)
	if anchor.Sequence > view.Sequence || anchor.Watermark > view.Watermark {
		return c.Fail(c.StaleAuthority, "privacy journal is behind its current external checkpoint")
	}
	for _, scope := range scopes {
		expected := expectedPrivacyCheckpointPin(*anchor, anchorDigest, authorityDigest, scope, "ACTIVE")
		err = withPrivacyScopeStore(ctx, authority, scope, preferred, preferredInstance, preferredDirectory, func(journal *store.Store) error {
			pin, pinErr := journal.PrivacyCheckpointPin(ctx)
			if pinErr != nil {
				return pinErr
			}
			if pin == nil || *pin != expected {
				return c.Fail(c.StaleAuthority, "owner store checkpoint pin is stale, missing, or from another physical root")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// validateCurrentPrivacyPinsReadOnly makes the current external anchor depend
// on every registered owner's independent SQLite witness during ordinary
// replay. This detects an anchor rollback/replacement without taking the
// owners' exclusive writer leases.
func validateCurrentPrivacyPinsReadOnly(ctx context.Context, root *os.Root, authority PrivacyAuthority) error {
	anchor, anchorDigest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil {
		return err
	}
	if _, err = root.Lstat("owners"); errors.Is(err, os.ErrNotExist) {
		if anchor != nil {
			return c.Fail(c.StaleAuthority, "current checkpoint has no owner catalog")
		}
		return nil
	} else if err != nil {
		return err
	}
	scopes, _, err := readPrivacyScopes(root)
	if err != nil {
		return err
	}
	authorityRaw, err := c.CanonicalV1(authority)
	if err != nil {
		return err
	}
	authorityDigest, _ := c.Digest(authority)
	for _, scope := range scopes {
		if anchor == nil {
			if _, lookupErr := os.Lstat(scope.Directory); errors.Is(lookupErr, os.ErrNotExist) {
				// Pre-checkpoint catalogs retain historical restore-stage spellings.
				// Absence cannot grant retirement authority; checkpoint publication
				// still requires every effective owner root and its ACTIVE pin.
				continue
			} else if lookupErr != nil {
				return lookupErr
			}
		}
		if err = privacyRuntimeForScope(scope); err != nil {
			return err
		}
		ownerAuthority, pin, readErr := store.ReadPrivacyCheckpointOwnerAt(ctx, scope.Directory)
		if readErr != nil {
			return readErr
		}
		if string(ownerAuthority) != string(authorityRaw) {
			return c.Fail(c.StaleAuthority, "registered owner store authority differs from the external root")
		}
		if anchor == nil {
			if pin != nil || scope.CheckpointAnchor != "" {
				return c.Fail(c.StaleAuthority, "owner checkpoint witness exists without a current external anchor")
			}
			continue
		}
		expected := expectedPrivacyCheckpointPin(*anchor, anchorDigest, authorityDigest, scope, "ACTIVE")
		if pin == nil || *pin != expected {
			return c.Fail(c.StaleAuthority, "registered owner does not pin the current external checkpoint")
		}
	}
	return nil
}

func checkpointScopeTransition(ctx context.Context, authority PrivacyAuthority, scopes []privacyScope, previous *privacyCheckpointAnchor, previousDigest string, next privacyCheckpointAnchor, nextDigest, phase string, anchorPublished bool, preferred *store.Store, preferredInstance RuntimeInstance, preferredDirectory string) error {
	authorityDigest, _ := c.Digest(authority)
	for _, scope := range scopes {
		err := withPrivacyScopeStore(ctx, authority, scope, preferred, preferredInstance, preferredDirectory, func(journal *store.Store) error {
			actual, err := journal.PrivacyCheckpointPin(ctx)
			if err != nil {
				return err
			}
			prepared := expectedPrivacyCheckpointPin(next, nextDigest, authorityDigest, scope, "PREPARED")
			active := expectedPrivacyCheckpointPin(next, nextDigest, authorityDigest, scope, "ACTIVE")
			if phase == "PREPARED" {
				if actual != nil && *actual == prepared {
					return nil
				}
				if actual != nil && *actual == active {
					if !anchorPublished {
						return c.Fail(c.StaleAuthority, "owner store activated a checkpoint before current anchor publication")
					}
					return nil
				}
				if previous == nil {
					if actual != nil {
						return c.Fail(c.StaleAuthority, "initial checkpoint cannot replace an existing owner pin")
					}
				} else {
					old := expectedPrivacyCheckpointPin(*previous, previousDigest, authorityDigest, scope, "ACTIVE")
					if actual == nil || *actual != old {
						return c.Fail(c.StaleAuthority, "owner store is not pinned to the previous current anchor")
					}
				}
				return journal.CompareAndSwapPrivacyCheckpointPin(ctx, actual, &prepared)
			}
			if phase != "ACTIVE" {
				return c.Fail(c.InvalidArgument, "invalid checkpoint pin phase")
			}
			if actual != nil && *actual == active {
				return nil
			}
			if actual == nil || *actual != prepared {
				return c.Fail(c.StaleAuthority, "owner store was not prepared for current checkpoint activation")
			}
			return journal.CompareAndSwapPrivacyCheckpointPin(ctx, actual, &active)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func recoverPrivacyCheckpoint(ctx context.Context, root *os.Root, authority PrivacyAuthority, preferred *store.Store, preferredInstance RuntimeInstance, preferredDirectory string) error {
	intent, rawIntent, err := readCheckpointIntent(root)
	if errors.Is(err, os.ErrNotExist) {
		if _, nextErr := root.Lstat(privacyCheckpointNextPath()); nextErr == nil {
			return c.Fail(c.StoreIntegrityError, "uncommitted privacy checkpoint anchor without recovery intent")
		} else if !errors.Is(nextErr, os.ErrNotExist) {
			return nextErr
		}
		anchor, anchorDigest, anchorErr := currentPrivacyCheckpointAnchor(root)
		if anchorErr != nil || anchor == nil {
			return anchorErr
		}
		_, checkpoint, baseErr := readPrivacyCheckpointBaseMode(root, true)
		if baseErr != nil || checkpoint == nil {
			return errors.Join(baseErr, c.Fail(c.StoreIntegrityError, "current checkpoint projection is unavailable for retirement recovery"))
		}
		return retirePrivacyCheckpointSources(ctx, root, authority, *anchor, anchorDigest, *checkpoint)
	}
	if err != nil {
		return err
	}
	authorityDigest, _ := c.Digest(authority)
	if intent.AuthorityDigest != authorityDigest {
		return c.Fail(c.StaleAuthority, "checkpoint recovery intent belongs to another privacy authority")
	}
	rawCheckpoint, err := fileguard.ReadRegular(root, privacyCheckpointDataPath(intent.CheckpointDigest), privacyCheckpointFileLimit)
	if err != nil || c.HashBytes(rawCheckpoint) != intent.CheckpointDigest {
		return c.Fail(c.StoreIntegrityError, "checkpoint recovery projection is missing or changed")
	}
	var checkpoint privacyCheckpoint
	if c.DecodeStrict(rawCheckpoint, &checkpoint) != nil || validatePrivacyCheckpoint(checkpoint, authority) != nil {
		return c.Fail(c.StoreIntegrityError, "checkpoint recovery projection is invalid")
	}
	canonicalCheckpoint, err := c.CanonicalV1(checkpoint)
	if err != nil || string(canonicalCheckpoint) != string(rawCheckpoint) || checkpointSourceDigest(checkpoint.SourceRecords) != intent.SourceDigest {
		return c.Fail(c.StoreIntegrityError, "checkpoint recovery projection digest mismatch")
	}
	if checkpoint.Generation != intent.Anchor.Generation || checkpoint.Sequence != intent.Anchor.Sequence || checkpoint.Tail != intent.Anchor.Tail || checkpoint.Watermark != intent.Anchor.Watermark || checkpoint.ScopeDigest != intent.ScopeDigest {
		return c.Fail(c.StoreIntegrityError, "checkpoint recovery target differs from its anchor")
	}
	scopes, scopeDigest, err := readPrivacyScopes(root)
	if err != nil || scopeDigest != intent.ScopeDigest || len(scopes) != len(checkpoint.Scopes) {
		return c.Fail(c.StaleAuthority, "checkpoint recovery owner catalog changed")
	}
	for i := range scopes {
		if scopes[i] != checkpoint.Scopes[i] {
			return c.Fail(c.StaleAuthority, "checkpoint recovery owner catalog changed")
		}
	}
	current, currentDigest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil {
		return err
	}
	anchorPublished := current != nil && currentDigest == intent.AnchorDigest
	if anchorPublished {
		if current.Generation != intent.Anchor.Generation || current.CheckpointDigest != intent.Anchor.CheckpointDigest {
			return c.Fail(c.StaleAuthority, "published checkpoint anchor diverges from recovery intent")
		}
	} else if intent.Anchor.Generation == 1 {
		if current != nil || intent.PreviousAnchor != "" {
			return c.Fail(c.StaleAuthority, "checkpoint genesis conflicts with a current anchor")
		}
	} else if current == nil || currentDigest != intent.PreviousAnchor || current.Generation+1 != intent.Anchor.Generation {
		return c.Fail(c.StaleAuthority, "checkpoint recovery intent does not extend the current anchor")
	}
	baseSequence, baseTail := int64(0), ""
	if current != nil && !anchorPublished {
		baseSequence, baseTail = current.Sequence, current.Tail
	} else if anchorPublished && intent.Anchor.Generation > 1 {
		previousRaw, previousErr := fileguard.ReadRegular(root, privacyCheckpointAnchorHistoryPath(intent.PreviousAnchor), 4096)
		var previous privacyCheckpointAnchor
		if previousErr != nil || c.HashBytes(previousRaw) != intent.PreviousAnchor || c.DecodeStrict(previousRaw, &previous) != nil || validatePrivacyCheckpointAnchor(previous) != nil || previous.Generation+1 != intent.Anchor.Generation || previous.AuthorityDigest != authorityDigest {
			return c.Fail(c.StaleAuthority, "checkpoint recovery lost its prior anchor witness")
		}
		baseSequence, baseTail = previous.Sequence, previous.Tail
	}
	if checkpoint.BaseSequence != baseSequence || checkpoint.BaseTail != baseTail {
		return c.Fail(c.StaleAuthority, "checkpoint recovery base differs from its pinned prior anchor")
	}
	if rawNext, readErr := fileguard.ReadRegular(root, privacyCheckpointNextPath(), 4096); readErr == nil {
		if string(rawNext) != string(mustCanonical(intent.Anchor)) || anchorPublished {
			return c.Fail(c.StoreIntegrityError, "prepared current anchor differs from the recovery intent")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	view, err := readPrivacyCheckpointRecoveryView(root)
	if err != nil {
		return err
	}
	if view.Sequence != intent.Anchor.Sequence || view.Tail != intent.Anchor.Tail || view.Watermark != intent.Anchor.Watermark {
		return c.Fail(c.StaleAuthority, "privacy journal changed during checkpoint recovery")
	}
	if anchorPublished {
		receipt, _, receiptErr := readPrivacyCheckpointRetirement(root, intent.Anchor, intent.AnchorDigest, checkpoint)
		if receiptErr != nil {
			return receiptErr
		}
		if err = verifyCheckpointSourceFiles(root, checkpoint, receipt != nil); err != nil {
			return err
		}
	} else {
		expectedCheckpoint, buildErr := buildPrivacyCheckpoint(root, authority, view, scopes, intent.Anchor.Generation, baseSequence, baseTail)
		if buildErr != nil {
			return buildErr
		}
		expectedRaw, encodeErr := c.CanonicalV1(expectedCheckpoint)
		if encodeErr != nil || string(expectedRaw) != string(rawCheckpoint) {
			return c.Fail(c.StaleAuthority, "checkpoint projection does not match the current privacy journal")
		}
	}
	if err = checkpointScopeTransition(ctx, authority, scopes, current, currentDigest, intent.Anchor, intent.AnchorDigest, "PREPARED", anchorPublished, preferred, preferredInstance, preferredDirectory); err != nil {
		return err
	}
	if !anchorPublished {
		rawAnchor := mustCanonical(intent.Anchor)
		if err = root.MkdirAll(filepath.Join(privacyCheckpointDirectory, "anchors"), 0700); err != nil {
			return err
		}
		if err = publishCheckpointImmutable(root, privacyCheckpointAnchorHistoryPath(intent.AnchorDigest), rawAnchor, 4096); err != nil {
			return err
		}
		if err = publishCheckpointImmutable(root, privacyCheckpointNextPath(), rawAnchor, 4096); err != nil {
			return err
		}
		if err = root.Rename(privacyCheckpointNextPath(), privacyCheckpointCurrentPath()); err != nil {
			return err
		}
		if err = fileguard.SyncDirectory(root, privacyCheckpointDirectory); err != nil {
			return err
		}
	} else {
		if err = publishCheckpointImmutable(root, privacyCheckpointAnchorHistoryPath(intent.AnchorDigest), mustCanonical(intent.Anchor), 4096); err != nil {
			return err
		}
	}
	if err = checkpointScopeTransition(ctx, authority, scopes, current, currentDigest, intent.Anchor, intent.AnchorDigest, "ACTIVE", true, preferred, preferredInstance, preferredDirectory); err != nil {
		return err
	}
	if err = validateCurrentPrivacyPinsReadOnly(ctx, root, authority); err != nil {
		return err
	}
	if err = retirePrivacyCheckpointSources(ctx, root, authority, intent.Anchor, intent.AnchorDigest, checkpoint); err != nil {
		return err
	}
	if _, err = fileguard.RemoveBound(root, privacyCheckpointIntentPath(), c.HashBytes(rawIntent), int64(len(rawIntent)), 1<<20); err != nil {
		return err
	}
	return fileguard.SyncDirectory(root, privacyCheckpointDirectory)
}
