// Package intelligence provides source-bound, non-authoritative retrieval hints.
package intelligence

import (
	"sort"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

const HistoryRankerVersion = "FIRST_PARENT_COCHANGE_RANK_V1"

type CoChange struct {
	Path            string   `json:"path"`
	Occurrences     int      `json:"occurrences"`
	LastSeenUTC     string   `json:"last_seen_utc"`
	EvidenceCommits []string `json:"evidence_commits"`
}
type CoChangeReport struct {
	SchemaVersion        int        `json:"schema_version"`
	RankerVersion        string     `json:"ranker_version"`
	SourceSnapshotDigest string     `json:"source_snapshot_digest"`
	HistoryDigest        string     `json:"history_digest"`
	TargetPath           string     `json:"target_path"`
	TargetFileDigest     string     `json:"target_file_digest"`
	CutoffUTC            string     `json:"cutoff_utc"`
	HistoryCoverage      string     `json:"history_coverage"`
	Interpretation       string     `json:"interpretation"`
	Total                int        `json:"total"`
	Truncated            bool       `json:"truncated"`
	Results              []CoChange `json:"results"`
	ReportDigest         string     `json:"report_digest"`
}

// RankCoChanges ranks exact path co-occurrence in source-bound first-parent
// history. It is a retrieval hint, never a causal or verification claim.
func RankCoChanges(history workspace.GitHistory, source workspace.Capture, targetPath string, limit int) (CoChangeReport, error) {
	out := CoChangeReport{SchemaVersion: c.SchemaVersion, RankerVersion: HistoryRankerVersion,
		SourceSnapshotDigest: source.Snapshot.Digest, HistoryDigest: history.ReportDigest,
		TargetPath: targetPath, CutoffUTC: history.CutoffUTC, HistoryCoverage: history.Coverage,
		Interpretation: "HISTORICAL_CO_CHANGE_NOT_CAUSATION", Results: []CoChange{}}
	if err := workspace.ValidateGitHistory(history, source); err != nil {
		return out, err
	}
	if !policy.SafePath(targetPath) || limit < 1 || limit > 100 {
		return out, c.Fail(c.InvalidArgument, "safe target path and bounded result limit required")
	}
	var target *c.Entry
	for i := range source.Snapshot.Entries {
		if source.Snapshot.Entries[i].Path == targetPath {
			target = &source.Snapshot.Entries[i]
			break
		}
	}
	if target == nil {
		return out, c.Fail(c.InvalidArgument, "target path is outside the captured source")
	}
	out.TargetFileDigest = target.Hash
	type aggregate struct {
		count   int
		last    time.Time
		commits []string
	}
	counts := map[string]*aggregate{}
	for _, commit := range history.Commits {
		containsTarget := false
		for _, change := range commit.Changes {
			if change.Path == targetPath {
				containsTarget = true
				break
			}
		}
		if !containsTarget {
			continue
		}
		at, _ := time.Parse(time.RFC3339, commit.CommitterUTC)
		perCommit := map[string]bool{}
		for _, change := range commit.Changes {
			if change.Path == targetPath || perCommit[change.Path] {
				continue
			}
			perCommit[change.Path] = true
			item := counts[change.Path]
			if item == nil {
				item = &aggregate{commits: []string{}}
				counts[change.Path] = item
			}
			item.count++
			if at.After(item.last) {
				item.last = at
			}
			item.commits = append(item.commits, commit.ObjectID)
		}
	}
	all := make([]CoChange, 0, len(counts))
	for path, item := range counts {
		sort.Strings(item.commits)
		all = append(all, CoChange{Path: path, Occurrences: item.count, LastSeenUTC: item.last.UTC().Format(time.RFC3339), EvidenceCommits: item.commits})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Occurrences != all[j].Occurrences {
			return all[i].Occurrences > all[j].Occurrences
		}
		if all[i].LastSeenUTC != all[j].LastSeenUTC {
			return all[i].LastSeenUTC > all[j].LastSeenUTC
		}
		return all[i].Path < all[j].Path
	})
	out.Total = len(all)
	if len(all) > limit {
		out.Truncated = true
		all = all[:limit]
	}
	out.Results = all
	digest, err := c.Digest(out)
	if err != nil {
		return CoChangeReport{}, err
	}
	out.ReportDigest = digest
	return out, nil
}

func ValidateCoChanges(report CoChangeReport, history workspace.GitHistory, source workspace.Capture) error {
	if err := workspace.ValidateGitHistory(history, source); err != nil {
		return err
	}
	if report.SchemaVersion != c.SchemaVersion || report.RankerVersion != HistoryRankerVersion ||
		report.SourceSnapshotDigest != source.Snapshot.Digest || report.HistoryDigest != history.ReportDigest ||
		report.CutoffUTC != history.CutoffUTC || report.HistoryCoverage != history.Coverage ||
		report.Interpretation != "HISTORICAL_CO_CHANGE_NOT_CAUSATION" || !policy.SafePath(report.TargetPath) ||
		report.Total < len(report.Results) || len(report.Results) > 100 {
		return c.Fail(c.StoreIntegrityError, "co-change report is not bound to this source history")
	}
	foundTarget := false
	for _, entry := range source.Snapshot.Entries {
		if entry.Path == report.TargetPath && entry.Hash == report.TargetFileDigest {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		return c.Fail(c.StoreIntegrityError, "co-change target changed")
	}
	seen := map[string]bool{}
	for _, result := range report.Results {
		if !policy.SafePath(result.Path) || result.Path == report.TargetPath || seen[result.Path] ||
			result.Occurrences < 1 || len(result.EvidenceCommits) != result.Occurrences {
			return c.Fail(c.InvalidArgument, "invalid co-change result")
		}
		seen[result.Path] = true
		for _, commitID := range result.EvidenceCommits {
			found := false
			for _, commit := range history.Commits {
				if commit.ObjectID != commitID {
					continue
				}
				hasTarget, hasPath := false, false
				for _, change := range commit.Changes {
					if change.Path == report.TargetPath {
						hasTarget = true
					}
					if change.Path == result.Path {
						hasPath = true
					}
				}
				found = hasTarget && hasPath
				break
			}
			if !found {
				return c.Fail(c.StoreIntegrityError, "co-change evidence is outside the captured history")
			}
		}
	}
	expected := report.ReportDigest
	report.ReportDigest = ""
	actual, err := c.Digest(report)
	if err != nil {
		return err
	}
	if actual != expected {
		return c.Fail(c.StoreIntegrityError, "co-change report digest mismatch")
	}
	limit := len(report.Results)
	if limit == 0 {
		limit = 1
	}
	rebuilt, err := RankCoChanges(history, source, report.TargetPath, limit)
	if err != nil {
		return err
	}
	if rebuilt.ReportDigest != expected {
		return c.Fail(c.StoreIntegrityError, "co-change ranking does not match captured history")
	}
	return nil
}
