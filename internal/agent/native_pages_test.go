package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

func capturedPages(t testing.TB, files map[string][]byte) (workspace.Capture, []policy.Policy, c.TaskState) {
	t.Helper()
	root := t.TempDir()
	for name, raw := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	capture, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	layers := []policy.Policy{{SchemaVersion: 1, Epoch: 1, Generation: 1, Effects: []string{"snapshot.read"}, Paths: []string{"**"}}}
	state := c.TaskState{SchemaVersion: 1, TaskID: "task", SpecVersion: 1, Execution: c.Running, PolicyEpoch: 1, KernelGeneration: 1}
	return capture, layers, state
}
func pageResult(t *testing.T, capture workspace.Capture, layers []policy.Policy, state c.TaskState, name string, args any) []byte {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{ID: "read-page", Name: name, Arguments: raw})
	if err != nil {
		t.Fatal(name, err)
	}
	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestPagedFileReassemblesExactBinaryCRLFAndSplitUnicode(t *testing.T) {
	source := bytes.Repeat([]byte("é🙂\xff\x00\r\n"), 9000)
	capture, layers, state := capturedPages(t, map[string][]byte{"source.bin": []byte("base")})
	// Baseline binary capture remains explicitly excluded. Test a valid binary
	// candidate proposal through the real preview contract instead of forging a manifest.
	layers[0].Effects = append(layers[0].Effects, "candidate.write")
	proposal := c.Proposal{SchemaVersion: 1, ID: "binary-candidate", TaskID: state.TaskID, SpecVersion: 1, BaseSnapshot: capture.Snapshot.Digest, PolicyEpoch: 1, KernelGeneration: 1, ReadSet: []c.ReadCondition{{Path: "source.bin", Kind: "FILE", Digest: c.HashBytes([]byte("base"))}}, Changes: []c.Change{{Path: "source.bin", BeforeDigest: c.HashBytes([]byte("base")), After: source}}}
	var err error
	capture, err = workspace.Preview(capture, capture, proposal, layers, state)
	if err != nil {
		t.Fatal(err)
	}
	cursor := ""
	assembled := []byte{}
	for {
		raw := pageResult(t, capture, layers, state, "fs_read", map[string]any{"path": "source.bin", "limit": 16384, "cursor": cursor})
		var page struct {
			Bytes    []byte          `json:"exact_bytes"`
			Content  *string         `json:"content"`
			Valid    bool            `json:"text_valid_utf8"`
			Coverage pageCoverage    `json:"coverage"`
			Read     c.ReadCondition `json:"read_condition"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if page.Coverage.Start != int64(len(assembled)) || page.Coverage.Total != int64(len(source)) || page.Read.Digest != c.HashBytes(source) || len(page.Bytes) > 16384 || page.Valid || page.Content != nil {
			t.Fatal("false coverage or lossy binary text", page.Coverage)
		}
		assembled = append(assembled, page.Bytes...)
		if !page.Coverage.HasMore {
			if page.Coverage.Next != "" {
				t.Fatal("invalid last cursor")
			}
			break
		}
		if page.Coverage.Next == cursor || len(page.Bytes) == 0 {
			t.Fatal("non-progressing cursor")
		}
		cursor = page.Coverage.Next
	}
	if !bytes.Equal(assembled, source) {
		t.Fatal("exact bytes changed across pages")
	}
	unicode, layers, state := capturedPages(t, map[string][]byte{"unicode.txt": []byte("🙂é")})
	raw := pageResult(t, unicode, layers, state, "fs_read", map[string]any{"path": "unicode.txt", "limit": 1})
	var split struct {
		Bytes   []byte  `json:"exact_bytes"`
		Content *string `json:"content"`
		Valid   bool    `json:"text_valid_utf8"`
	}
	json.Unmarshal(raw, &split)
	if split.Valid || split.Content != nil || len(split.Bytes) != 1 {
		t.Fatal("partial rune was replaced")
	}
}
func TestPagedListingIncludesEmptyDirectoriesAndExclusionsWithoutFalseAbsence(t *testing.T) {
	files := map[string][]byte{".env": []byte("private data")}
	for i := 0; i < 520; i++ {
		files[fmt.Sprintf("src/file-%04d.txt", i)] = []byte("value")
	}
	capture, layers, state := capturedPages(t, files)
	if err := os.Mkdir(filepath.Join(capture.Snapshot.Root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	capture, err = workspace.CaptureDirectory(capture.Snapshot.Root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	cursor := ""
	paths := map[string]string{}
	receipt := ""
	for {
		raw := pageResult(t, capture, layers, state, "fs_list", map[string]any{"directory": ".", "limit": 64, "cursor": cursor})
		var page struct {
			Items []struct {
				Kind string `json:"kind"`
				Path string `json:"path"`
			} `json:"items"`
			Coverage pageCoverage    `json:"coverage"`
			Read     c.ReadCondition `json:"read_condition"`
		}
		json.Unmarshal(raw, &page)
		if len(page.Items) > 64 || page.Coverage.Start != int64(len(paths)) {
			t.Fatal("unbounded/overlapping list")
		}
		if receipt != "" && receipt != page.Read.Digest {
			t.Fatal("listing condition changed between pages")
		}
		receipt = page.Read.Digest
		for _, item := range page.Items {
			if _, found := paths[item.Path]; found {
				t.Fatal("duplicate item", item.Path)
			}
			paths[item.Path] = item.Kind
		}
		if !page.Coverage.HasMore {
			if page.Coverage.Total != int64(len(paths)) {
				t.Fatal("missing captured rows")
			}
			break
		}
		cursor = page.Coverage.Next
	}
	if len(paths) != 523 || paths[".env"] != "EXCLUDED" || paths["src/"] != "DIRECTORY" || paths["empty/"] != "DIRECTORY" || paths["src/file-0519.txt"] != "FILE" {
		t.Fatal("coverage lost", len(paths), paths[".env"])
	}
}
func TestPagedSearchFindsAllMatchesAndLongExcerptCanBeHydrated(t *testing.T) {
	long := strings.Repeat("x", 90000) + "needle\r\n"
	source := []byte(long + strings.Repeat("needle\r\n", 70))
	capture, layers, state := capturedPages(t, map[string][]byte{"source.txt": source, ".env": []byte("needle secret")})
	type match struct {
		Offset   int64  `json:"excerpt_byte_offset"`
		Match    int64  `json:"match_byte_offset"`
		Excerpt  []byte `json:"exact_excerpt"`
		Complete bool   `json:"excerpt_complete"`
		Line     int64  `json:"line"`
	}
	cursor := ""
	matches := []match{}
	for {
		raw := pageResult(t, capture, layers, state, "fs_search", map[string]any{"query": "needle", "limit": 16, "cursor": cursor})
		var page struct {
			Matches  []match      `json:"matches"`
			Coverage pageCoverage `json:"coverage"`
			Scan     bool         `json:"captured_scan_complete"`
			Excluded int          `json:"excluded_locations"`
		}
		json.Unmarshal(raw, &page)
		if !page.Scan || page.Excluded != 1 || page.Coverage.Total != 71 {
			t.Fatal("negative-scope/exclusions lost")
		}
		matches = append(matches, page.Matches...)
		if !page.Coverage.HasMore {
			break
		}
		cursor = page.Coverage.Next
	}
	if len(matches) != 71 || matches[0].Complete || matches[0].Match != 90000 || !bytes.Contains(matches[0].Excerpt, []byte("needle")) || len(matches[0].Excerpt) > 2048 {
		t.Fatal("long match location or excerpt lost", len(matches))
	}
	raw := pageResult(t, capture, layers, state, "fs_read", map[string]any{"path": "source.txt", "offset": matches[0].Match, "limit": 6, "candidate_digest": capture.Snapshot.Digest})
	var hydrated struct {
		Bytes    []byte       `json:"exact_bytes"`
		Coverage pageCoverage `json:"coverage"`
	}
	json.Unmarshal(raw, &hydrated)
	if string(hydrated.Bytes) != "needle" || hydrated.Coverage.Start != 90000 {
		t.Fatal("search did not hydrate exact source")
	}
}
func TestPageCursorRejectsChangedCandidatePolicyQueryAndInvalidArguments(t *testing.T) {
	capture, layers, state := capturedPages(t, map[string][]byte{"a.txt": []byte("needle\nneedle\n"), "b.txt": []byte("other")})
	raw := pageResult(t, capture, layers, state, "fs_read", map[string]any{"path": "a.txt", "limit": 2})
	var initial struct {
		Coverage pageCoverage `json:"coverage"`
	}
	json.Unmarshal(raw, &initial)
	for _, args := range []map[string]any{
		{"path": "b.txt", "limit": 2, "cursor": initial.Coverage.Next},
		{"path": "a.txt", "limit": 3, "cursor": initial.Coverage.Next},
		{"path": "a.txt", "limit": 0},
		{"path": "a.txt", "query": ""},
		{"path": "a.txt", "cursor": "invalid!"},
		{"path": "a.txt", "offset": 1},
		{"path": "a.txt", "offset": 0, "cursor": initial.Coverage.Next},
		{"path": "a.txt", "candidate_digest": c.HashBytes([]byte("stale"))},
	} {
		encoded, _ := json.Marshal(args)
		if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_read", Arguments: encoded}); err == nil {
			t.Fatal("bad page accepted", args)
		}
	}
	args, _ := json.Marshal(map[string]any{"path": "a.txt", "limit": 2, "cursor": initial.Coverage.Next})
	searchRaw := pageResult(t, capture, layers, state, "fs_search", map[string]any{"query": "needle", "limit": 1})
	var search struct{ Coverage pageCoverage }
	json.Unmarshal(searchRaw, &search)
	changedQuery, _ := json.Marshal(map[string]any{"query": "other", "limit": 1, "cursor": search.Coverage.Next})
	if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: changedQuery}); err == nil {
		t.Fatal("query cursor was rebound")
	}
	if err := os.WriteFile(filepath.Join(capture.Snapshot.Root, "a.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := workspace.CaptureDirectory(capture.Snapshot.Root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeReadPage(context.Background(), changed, layers, state, model.Call{Name: "fs_read", Arguments: args}); err == nil {
		t.Fatal("changed candidate accepted old cursor")
	}
	layers[0].Epoch = 2
	state.PolicyEpoch = 2
	if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_read", Arguments: args}); err == nil {
		t.Fatal("stale policy cursor accepted")
	}
	state.InputBarrier = true
	if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}); err == nil {
		t.Fatal("input barrier bypassed by paging")
	}
}

func TestNativeOutlinePagesBindExactSourcePolicyAndCandidate(t *testing.T) {
	capture, layers, state := capturedPages(t, map[string][]byte{"src/module.ts": []byte("export function first() {}\r\nexport const second = () => 2;\r\nclass Third {}\r\n"), "other.ts": []byte("function hidden() {}")})
	first := pageResult(t, capture, layers, state, "fs_outline", map[string]any{"path": "src/module.ts", "limit": 1})
	var page struct {
		Coverage pageCoverage    `json:"coverage"`
		Read     c.ReadCondition `json:"read_condition"`
		Outline  struct {
			Symbols []struct {
				Name string `json:"name"`
			} `json:"symbols"`
		} `json:"outline"`
	}
	if err := json.Unmarshal(first, &page); err != nil || len(page.Outline.Symbols) != 1 || page.Outline.Symbols[0].Name != "first" || !page.Coverage.HasMore || page.Read.Digest != c.HashBytes(capture.Contents["src/module.ts"]) {
		t.Fatalf("outline first: %s %v", first, err)
	}
	cursor := page.Coverage.Next
	next := pageResult(t, capture, layers, state, "fs_outline", map[string]any{"path": "src/module.ts", "limit": 1, "cursor": cursor})
	if err := json.Unmarshal(next, &page); err != nil || page.Outline.Symbols[0].Name != "second" {
		t.Fatalf("outline next %s %v", next, err)
	}
	restricted := append([]policy.Policy(nil), layers...)
	restricted[0].Paths = []string{"other.ts"}
	raw, _ := json.Marshal(map[string]any{"path": "src/module.ts", "limit": 1})
	if _, err := nativeReadPage(context.Background(), capture, restricted, state, model.Call{Name: "fs_outline", Arguments: raw}); err == nil {
		t.Fatal("outline leaked disallowed path")
	}
	raw, _ = json.Marshal(map[string]any{"path": "other.ts", "limit": 1, "cursor": cursor})
	if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_outline", Arguments: raw}); err == nil {
		t.Fatal("cross-path outline cursor accepted")
	}
}
