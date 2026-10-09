package agent

import (
	"bytes"
	"context"
	"encoding/json"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/retrieval"
	"github.com/ixayldz/Viber/internal/testutil"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Labels name known declarations in the real repository; they are not derived
// from search output. This is retrieval acceptance, not a coding-task cohort.
func repositorySources(t testing.TB) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	// Only production Go sources under internal, never developer credentials,
	// generated output or host paths. The source tree is test input only.
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fs.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" || bytes.HasSuffix([]byte(path), []byte("_test.go")) {
			return nil
		}
		relative, err := filepath.Rel("..", path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 2<<20 {
			t.Fatal("unexpected repository source")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = raw
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
func TestRealRepositoryOwnerRetrievalRecallSpansAndWarmInvalidation(t *testing.T) {
	files := repositorySources(t)
	// These two labels are selected independently but filenames may move as the
	// implementation develops; require the declared source to contain the query.
	labels := []struct{ query, path string }{
		{"bindEnginePin", "agent/engine_binding.go"}, {"CaptureDirectory", "workspace/snapshot.go"},
		{"RemoveOrphan", "artifact/gc.go"}, {"replayObserveLocked", "store/store.go"},
		{"rankedReadPage", "agent/retrieval.go"}, {"vaultEnvironment", "auth/vault_unix.go"},
	}
	for _, label := range labels {
		if !bytes.Contains(files[label.path], []byte(label.query)) {
			t.Fatalf("invalid predeclared label %s", label.query)
		}
	}
	capture, layers, state := capturedPages(t, files)
	cache, err := retrieval.NewCache(128<<20, 4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cold, warm := []int64{}, []int64{}
	recall, valid := 0, 0
	for n, label := range labels {
		args, _ := json.Marshal(map[string]any{"query": label.query, "mode": "ranked", "intent": "IDENTIFIER", "limit": 5})
		for attempt := 0; attempt < 2; attempt++ {
			started := time.Now()
			value, err := nativeReadPageWithCache(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: args}, cache)
			elapsed := time.Since(started).Microseconds()
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(value)
			var page struct {
				Retrieval retrieval.Result `json:"retrieval"`
			}
			if err = json.Unmarshal(encoded, &page); err != nil {
				t.Fatal(err)
			}
			if page.Retrieval.Cache == nil || page.Retrieval.Cache.Hit != (n > 0 || attempt > 0) {
				t.Fatal("owner cache evidence", string(encoded))
			}
			if n == 0 && attempt == 0 {
				cold = append(cold, elapsed)
			} else {
				warm = append(warm, elapsed)
			}
			found := false
			for _, hit := range page.Retrieval.Hits {
				source := files[hit.Path]
				if hit.Start < 0 || hit.End > len(source) || !bytes.Equal(hit.Bytes, source[hit.Start:hit.End]) || hit.FileDigest != c.HashBytes(source) {
					t.Fatal("incorrect real source span")
				}
				valid++
				if hit.Path == label.path && bytes.Contains(hit.Bytes, []byte(label.query)) {
					found = true
				}
			}
			if attempt == 0 && found {
				recall++
			}
		}
	}
	if recall != len(labels) {
		t.Fatalf("real repository recall@5=%d/%d", recall, len(labels))
	}
	peak, err := testutil.PeakResidentBytes()
	if err != nil || peak <= 0 {
		t.Fatal("OS process resident measurement unavailable", err)
	}
	sort.Slice(warm, func(i, j int) bool { return warm[i] < warm[j] })
	t.Logf("real repository files=%d recall@5=%d/%d exact spans=%d cold_us=%v warm_median_us=%d warm_p95_us=%d; NOT independent coding success or hardware reference acceptance", len(files), recall, len(labels), valid, cold, warm[len(warm)/2], warm[len(warm)-1])
	t.Logf("OS process-wide peak resident bytes=%d; includes test runner and capture setup; NOT cache-attributable RSS or a hard quota", peak)
	// An actually changed source must fail integrity before a warm cache hit.
	capture.Contents[labels[0].path] = []byte("changed source")
	args, _ := json.Marshal(map[string]any{"query": labels[0].query, "mode": "ranked", "intent": "IDENTIFIER", "limit": 5})
	if _, err = nativeReadPageWithCache(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: args}, cache); err == nil {
		t.Fatal("real repository stale bytes reused")
	}
}
func BenchmarkOwnerRankedSearch(b *testing.B) {
	files := repositorySources(b)
	capture, layers, state := capturedPages(b, files)
	cache, err := retrieval.NewCache(128<<20, 4, time.Minute)
	if err != nil {
		b.Fatal(err)
	}
	defer cache.Close()
	args, _ := json.Marshal(map[string]any{"query": "bindEnginePin", "mode": "ranked", "intent": "IDENTIFIER", "limit": 5})
	call := model.Call{Name: "fs_search", Arguments: args}
	if _, err = nativeReadPageWithCache(context.Background(), capture, layers, state, call, cache); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if _, err = nativeReadPageWithCache(context.Background(), capture, layers, state, call, cache); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	peak, err := testutil.PeakResidentBytes()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(peak), "process-peak-resident-bytes")
}
