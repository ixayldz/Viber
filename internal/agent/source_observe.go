package agent

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"sort"
	"strings"
)

type SourceMatches struct {
	SchemaVersion int       `json:"schema_version"`
	Candidate     string    `json:"candidate"`
	Entries       []c.Entry `json:"entries"`
	Total         int       `json:"total_matches"`
	Truncated     bool      `json:"truncated"`
}
type SourcePage struct {
	SchemaVersion int     `json:"schema_version"`
	Candidate     string  `json:"candidate"`
	Entry         c.Entry `json:"entry"`
	Bytes         []byte  `json:"exact_bytes"`
	Next          int64   `json:"next_offset"`
	Complete      bool    `json:"complete"`
}

func fuzzyMatch(path, query string) bool {
	path = strings.ToLower(path)
	query = strings.ToLower(query)
	if strings.Contains(path, query) {
		return true
	}
	remaining := []rune(query)
	for _, r := range path {
		if len(remaining) > 0 && remaining[0] == r {
			remaining = remaining[1:]
		}
	}
	return len(remaining) == 0
}
func (s *Session) sourceObservation(state c.TaskState, doc Document, args Observation) (any, error) {
	if args.Limit < 1 || args.Limit > 16384 || args.Offset < 0 || len(args.Query) > 256 || strings.ContainsAny(args.Query, "\x00\r\n") ||
		args.RunID != "" || args.Stream != "" || args.HistoryDigest != "" {
		return nil, c.Fail(c.InvalidArgument, "bounded source observation required")
	}
	if args.Candidate != "" && args.Candidate != doc.Candidate.SnapshotDigest {
		return nil, c.Fail(c.StaleBase, "source candidate changed")
	}
	capture, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return nil, err
	}
	admit := func(path string) bool {
		return policy.Admit(s.layers(state, doc), policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Effect: "snapshot.read", Path: path}) == nil
	}
	if args.Kind == "source-list" {
		if args.Path != "" || args.Offset != 0 || args.Limit > 64 {
			return nil, c.Fail(c.InvalidArgument, "source listing limit 1..64 required")
		}
		entries := []c.Entry{}
		for _, entry := range capture.Snapshot.Entries {
			if fuzzyMatch(entry.Path, args.Query) && admit(entry.Path) {
				entries = append(entries, entry)
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			ai, aj := entries[i].Path == args.Query, entries[j].Path == args.Query
			if ai != aj {
				return ai
			}
			return entries[i].Path < entries[j].Path
		})
		total := len(entries)
		entries = entries[:min(total, int(args.Limit))]
		return SourceMatches{1, doc.Candidate.SnapshotDigest, entries, total, total > len(entries)}, nil
	}
	if args.Candidate == "" || args.Query != "" || !policy.SafePath(args.Path) || !admit(args.Path) {
		return nil, c.Fail(c.PolicyDenied, "exact candidate/path read authority required")
	}
	for _, entry := range capture.Snapshot.Entries {
		if entry.Path != args.Path {
			continue
		}
		raw := capture.Contents[entry.Path]
		if args.Offset > int64(len(raw)) {
			return nil, c.Fail(c.InvalidArgument, "source offset exceeds retained bytes")
		}
		end := min(int64(len(raw)), args.Offset+args.Limit)
		return SourcePage{1, doc.Candidate.SnapshotDigest, entry, raw[args.Offset:end], end, end == int64(len(raw))}, nil
	}
	return nil, c.Fail(c.InvalidArgument, "source path absent from authorized candidate")
}
