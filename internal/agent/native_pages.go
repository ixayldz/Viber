package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

type pageArgs struct {
	Path      string `json:"path,omitempty"`
	Directory string `json:"directory,omitempty"`
	Query     string `json:"query,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	Limit     int64  `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Candidate string `json:"candidate_digest,omitempty"`
}
type pageCursor struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Candidate     string `json:"candidate"`
	Scope         string `json:"scope"`
	Position      int64  `json:"position"`
}

func readToolParameters(name string) json.RawMessage {
	property := "path"
	maximum := 65536
	if name == "fs_list" {
		property = "directory"
		maximum = 256
	}
	if name == "fs_search" {
		property = "query"
		maximum = 128
	}
	offsetSchema := map[string]any{}
	if name == "fs_read" {
		offsetSchema["offset"] = map[string]any{"type": "integer", "minimum": 0}
	}
	properties := map[string]any{property: map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maximum}, "cursor": map[string]any{"type": "string"}, "candidate_digest": map[string]any{"type": "string"}}
	for key, value := range offsetSchema {
		properties[key] = value
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "object", "properties": properties, "required": []string{property}, "additionalProperties": false,
	})
	return raw
}
func parsePage(call model.Call, candidate workspace.Capture, layers []policy.Policy) (pageArgs, pageCursor, error) {
	var args pageArgs
	if err := c.DecodeStrict(call.Arguments, &args); err != nil {
		return args, pageCursor{}, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(call.Arguments, &keys); err != nil {
		return args, pageCursor{}, err
	}
	primary := "path"
	if call.Name == "fs_list" {
		primary = "directory"
	}
	if call.Name == "fs_search" {
		primary = "query"
	}
	for key := range keys {
		if key != primary && key != "limit" && key != "cursor" && key != "candidate_digest" && !(key == "offset" && call.Name == "fs_read") {
			return args, pageCursor{}, c.Fail(c.InvalidArgument, "argument belongs to a different read tool")
		}
	}
	if _, present := keys["limit"]; present && args.Limit < 1 {
		return args, pageCursor{}, c.Fail(c.InvalidArgument, "page limit must be positive")
	}
	if _, present := keys["offset"]; present && args.Cursor != "" {
		return args, pageCursor{}, c.Fail(c.InvalidArgument, "offset and cursor are mutually exclusive")
	}
	if args.Offset < 0 || args.Offset > 0 && !c.ValidDigest(args.Candidate) {
		return args, pageCursor{}, c.Fail(c.InvalidArgument, "random byte offset requires a pinned candidate")
	}
	defaultLimit, maximum := int64(16384), int64(65536)
	if call.Name == "fs_list" {
		defaultLimit, maximum = 64, 256
	}
	if call.Name == "fs_search" {
		defaultLimit, maximum = 32, 128
	}
	if args.Limit == 0 {
		args.Limit = defaultLimit
	}
	if args.Limit < 1 || args.Limit > maximum {
		return args, pageCursor{}, c.Fail(c.InvalidArgument, "page limit outside tool quota")
	}
	if args.Candidate != "" && args.Candidate != candidate.Snapshot.Digest {
		return args, pageCursor{}, c.Fail(c.StaleBase, "page limit or candidate binding invalid")
	}
	policyDigest, err := c.Digest(layers)
	if err != nil {
		return args, pageCursor{}, err
	}
	scope, err := c.Digest(struct {
		Kind, Path, Directory, Query, Policy string
		Limit                                int64
	}{call.Name, args.Path, args.Directory, args.Query, policyDigest, args.Limit})
	if err != nil {
		return args, pageCursor{}, err
	}
	cursor := pageCursor{SchemaVersion: 1, Kind: call.Name, Candidate: candidate.Snapshot.Digest, Scope: scope, Position: args.Offset}
	if args.Cursor == "" {
		return args, cursor, nil
	}
	if len(args.Cursor) > 2048 {
		return args, cursor, c.Fail(c.InvalidArgument, "cursor exceeds quota")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(args.Cursor)
	if err != nil {
		return args, cursor, c.Fail(c.InvalidArgument, "malformed page cursor")
	}
	var supplied pageCursor
	if err = c.DecodeStrict(raw, &supplied); err != nil {
		return args, cursor, err
	}
	if supplied.SchemaVersion != 1 || supplied.Kind != cursor.Kind || supplied.Candidate != cursor.Candidate || supplied.Scope != cursor.Scope || supplied.Position < 0 {
		return args, cursor, c.Fail(c.StaleBase, "page cursor candidate/query/policy binding changed")
	}
	return args, supplied, nil
}
func nextPage(cursor pageCursor, end, total int64) (string, error) {
	if end >= total {
		return "", nil
	}
	cursor.Position = end
	raw, err := c.CanonicalV1(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type pageCoverage struct {
	Start   int64  `json:"page_start"`
	End     int64  `json:"page_end"`
	Total   int64  `json:"total"`
	Next    string `json:"next_cursor"`
	HasMore bool   `json:"has_more"`
}

func coverage(cursor pageCursor, end, total int64) (pageCoverage, error) {
	next, err := nextPage(cursor, end, total)
	return pageCoverage{Start: cursor.Position, End: end, Total: total, Next: next, HasMore: next != ""}, err
}
func nativeReadPage(ctx context.Context, candidate workspace.Capture, layers []policy.Policy, state c.TaskState, call model.Call) (any, error) {
	args, cursor, err := parsePage(call, candidate, layers)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	action := policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "snapshot.read"}
	switch call.Name {
	case "fs_read":
		action.Path = args.Path
		if err = policy.Admit(layers, action); err != nil {
			return nil, err
		}
		raw, ok := candidate.Contents[args.Path]
		if !ok {
			return nil, c.Fail(c.InvalidArgument, "source unavailable/excluded; inspect captured listing coverage")
		}
		total := int64(len(raw))
		if cursor.Position > total {
			return nil, c.Fail(c.StaleBase, "file cursor is beyond exact byte extent")
		}
		end := cursor.Position + args.Limit
		if end > total {
			end = total
		}
		page := raw[cursor.Position:end]
		extent, err := coverage(cursor, end, total)
		if err != nil {
			return nil, err
		}
		var text *string
		if utf8.Valid(page) {
			value := string(page)
			text = &value
		}
		return struct {
			SchemaVersion int             `json:"schema_version"`
			Trust         string          `json:"trust"`
			Candidate     string          `json:"candidate"`
			Path          string          `json:"path"`
			Bytes         []byte          `json:"exact_bytes"`
			Content       *string         `json:"content,omitempty"`
			TextValid     bool            `json:"text_valid_utf8"`
			Coverage      pageCoverage    `json:"coverage"`
			Read          c.ReadCondition `json:"read_condition"`
		}{1, "UNTRUSTED_SOURCE", candidate.Snapshot.Digest, args.Path, append([]byte{}, page...), text, text != nil, extent, c.ReadCondition{Path: args.Path, Kind: "FILE", Digest: c.HashBytes(raw)}}, nil
	case "fs_list":
		if err = policy.Admit(layers, action); err != nil {
			return nil, err
		}
		if args.Directory != "." && !policy.SafePath(args.Directory) || !workspace.ListingAllowed(args.Directory, layers) {
			return nil, c.Fail(c.PolicyDenied, "listing outside captured policy scope")
		}
		prefix := ""
		if args.Directory != "." {
			prefix = args.Directory + "/"
		}
		type item struct {
			Kind  string   `json:"kind"`
			Path  string   `json:"path"`
			Entry *c.Entry `json:"entry,omitempty"`
		}
		items := []item{}
		allowed := func(path string) bool {
			if !strings.HasPrefix(path, prefix) {
				return false
			}
			action.Path = strings.TrimSuffix(path, "/")
			return policy.Admit(layers, action) == nil
		}
		for _, entry := range candidate.Snapshot.Entries {
			if allowed(entry.Path) {
				copy := entry
				items = append(items, item{"FILE", entry.Path, &copy})
			}
		}
		for _, path := range candidate.Snapshot.Directories {
			if allowed(path + "/") {
				items = append(items, item{"DIRECTORY", path + "/", nil})
			}
		}
		for _, path := range candidate.Snapshot.Exclusions {
			if allowed(path) {
				items = append(items, item{"EXCLUDED", path, nil})
			}
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Path != items[j].Path {
				return items[i].Path < items[j].Path
			}
			return items[i].Kind < items[j].Kind
		})
		total := int64(len(items))
		if cursor.Position > total {
			return nil, c.Fail(c.StaleBase, "listing cursor beyond captured extent")
		}
		end := cursor.Position + args.Limit
		if end > total {
			end = total
		}
		extent, err := coverage(cursor, end, total)
		if err != nil {
			return nil, err
		}
		digest, err := workspace.ListingDigest(candidate.Snapshot, args.Directory)
		if err != nil {
			return nil, err
		}
		return struct {
			SchemaVersion int             `json:"schema_version"`
			Trust         string          `json:"trust"`
			Candidate     string          `json:"candidate"`
			Scope         string          `json:"scope"`
			Items         []item          `json:"items"`
			Coverage      pageCoverage    `json:"coverage"`
			Read          c.ReadCondition `json:"read_condition"`
		}{1, "UNTRUSTED_SOURCE", candidate.Snapshot.Digest, "CAPTURED_POLICY_SCOPE_WITH_EXPLICIT_EXCLUSIONS", items[cursor.Position:end], extent, c.ReadCondition{Path: args.Directory, Kind: "LISTING", Digest: digest}}, nil
	case "fs_search":
		if args.Query == "" || len(args.Query) > 256 || !utf8.ValidString(args.Query) {
			return nil, c.Fail(c.InvalidArgument, "bounded UTF-8 literal query required")
		}
		if err = policy.Admit(layers, action); err != nil {
			return nil, err
		}
		type match struct {
			Path            string  `json:"path"`
			Line            int64   `json:"line"`
			FileDigest      string  `json:"file_digest"`
			ByteOffset      int64   `json:"line_byte_offset"`
			ExcerptOffset   int64   `json:"excerpt_byte_offset"`
			MatchOffset     int64   `json:"match_byte_offset"`
			LineBytes       int64   `json:"line_bytes"`
			Excerpt         []byte  `json:"exact_excerpt"`
			Content         *string `json:"content,omitempty"`
			ExcerptComplete bool    `json:"excerpt_complete"`
		}
		matches := []match{}
		total := int64(0)
		paths := make([]string, 0, len(candidate.Contents))
		for path := range candidate.Contents {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			action.Path = path
			if policy.Admit(layers, action) != nil {
				continue
			}
			raw := candidate.Contents[path]
			fileDigest := ""
			offset := 0
			lineNumber := int64(1)
			// Iterate exact-byte lines without materializing an unbounded slice of lines.
			for offset < len(raw) {
				if err = ctx.Err(); err != nil {
					return nil, err
				}
				remaining := raw[offset:]
				length := bytes.IndexByte(remaining, '\n')
				advance := length + 1
				if length < 0 {
					length = len(remaining)
					advance = length
				}
				line := remaining[:length]
				if bytes.Contains(line, []byte(args.Query)) {
					if total >= cursor.Position && int64(len(matches)) < args.Limit {
						if fileDigest == "" {
							fileDigest = c.HashBytes(raw)
						}
						matchOffset := bytes.Index(line, []byte(args.Query))
						start := matchOffset - 512
						if start < 0 || len(line) <= 2048 {
							start = 0
						}
						end := start + 2048
						if end > len(line) {
							end = len(line)
						}
						excerpt := line[start:end]
						var text *string
						if utf8.Valid(excerpt) {
							value := string(excerpt)
							text = &value
						}
						matches = append(matches, match{path, lineNumber, fileDigest, int64(offset), int64(offset + start), int64(offset + matchOffset), int64(len(line)), append([]byte{}, excerpt...), text, len(excerpt) == len(line)})
					}
					total++
				}
				offset += advance
				lineNumber++
			}
		}
		if cursor.Position > total {
			return nil, c.Fail(c.StaleBase, "search cursor beyond captured match extent")
		}
		extent, err := coverage(cursor, cursor.Position+int64(len(matches)), total)
		if err != nil {
			return nil, err
		}
		return struct {
			SchemaVersion     int          `json:"schema_version"`
			Trust             string       `json:"trust"`
			Candidate         string       `json:"candidate"`
			Scope             string       `json:"scope"`
			Matches           []match      `json:"matches"`
			Coverage          pageCoverage `json:"coverage"`
			ScanComplete      bool         `json:"captured_scan_complete"`
			ExcludedLocations int          `json:"excluded_locations"`
		}{1, "UNTRUSTED_SOURCE", candidate.Snapshot.Digest, "CAPTURED_POLICY_SCOPE", matches, extent, true, len(candidate.Snapshot.Exclusions)}, nil
	default:
		return nil, c.Fail(c.UnsupportedCapability, "unknown paged read tool")
	}
}
