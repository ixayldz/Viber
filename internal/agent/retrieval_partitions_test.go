package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/retrieval"
	"github.com/ixayldz/Viber/internal/testutil"
)

func TestOwnerLazyPartitionsExposePartialCoverageAndBindCursorAndPolicy(t *testing.T) {
	files := map[string][]byte{"private/hidden.txt": []byte("TargetLast protected secret")}
	for n := 0; n < 600; n++ {
		body := "NeedleCommon captured source\n"
		if n == 599 {
			body += "TargetLast independent declaration\n"
		}
		files[fmt.Sprintf("src/%04d.txt", n)] = []byte(body)
	}
	capture, layers, state := capturedPages(t, files)
	layers[0].Paths = []string{"src/**"}
	cache, err := retrieval.NewCache(16<<20, 4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	type page struct {
		Retrieval retrieval.Result `json:"retrieval"`
		Coverage  pageCoverage     `json:"coverage"`
	}
	search := func(query string, ordinal int, cursor string) (page, error) {
		raw, _ := json.Marshal(map[string]any{"query": query, "mode": "ranked", "intent": "IDENTIFIER", "partition": ordinal, "limit": 1, "cursor": cursor})
		value, err := nativeReadPageWithCache(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: raw}, cache)
		if err != nil {
			return page{}, err
		}
		encoded, _ := json.Marshal(value)
		if bytes.Contains(encoded, []byte("private/")) || bytes.Contains(encoded, []byte("protected secret")) {
			t.Fatal("unauthorized partition metadata leaked", string(encoded))
		}
		var result page
		err = json.Unmarshal(encoded, &result)
		return result, err
	}
	first, err := search("TargetLast", 0, "")
	if err != nil || len(first.Retrieval.Hits) != 0 || first.Retrieval.Partition == nil || len(first.Retrieval.Partition.Partitions) != 3 || first.Retrieval.Partition.AuthorizedDocuments != 600 || first.Retrieval.Manifest.Documents != 256 || first.Retrieval.Cache.Hit {
		t.Fatal("first useful query built all source indexes or hid incomplete coverage", first, err)
	}
	if !strings.Contains(first.Retrieval.Partition.Scope, "NO_REPOSITORY_ABSENCE_PROOF") || first.Retrieval.Partition.Next == nil || *first.Retrieval.Partition.Next != 1 {
		t.Fatal("empty first partition implied repository absence", first.Retrieval.Partition)
	}
	warm, err := search("TargetLast", 0, "")
	if err != nil || !warm.Retrieval.Cache.Hit {
		t.Fatal("same lazy partition rebuilt", err)
	}
	last, err := search("TargetLast", 2, "")
	if err != nil || len(last.Retrieval.Hits) != 1 || last.Retrieval.Hits[0].Path != "src/0599.txt" || last.Retrieval.Manifest.Documents != 88 || last.Retrieval.Partition.Next != nil {
		t.Fatal("last partition lost exact source", last, err)
	}
	hit := last.Retrieval.Hits[0]
	if !bytes.Equal(hit.Bytes, capture.Contents[hit.Path][hit.Start:hit.End]) || hit.FileDigest != c.HashBytes(capture.Contents[hit.Path]) {
		t.Fatal("partition hydration changed source span")
	}
	cursorPage, err := search("NeedleCommon", 0, "")
	if err != nil || !cursorPage.Coverage.HasMore {
		t.Fatal(cursorPage, err)
	}
	if _, err = search("NeedleCommon", 1, cursorPage.Coverage.Next); err == nil {
		t.Fatal("ranked cursor crossed source partition")
	}
	layers[0].Paths = []string{"src/0000.txt"}
	if _, err = search("NeedleCommon", 0, cursorPage.Coverage.Next); err == nil {
		t.Fatal("partition cursor survived policy restriction")
	}
	layers[0].Paths = []string{"src/**"}
	capture.Contents["src/0599.txt"] = []byte("mutated file outside selected first partition")
	_, err = search("NeedleCommon", 0, "")
	var failure *c.Error
	if !errors.As(err, &failure) || failure.Code != c.StoreIntegrityError {
		t.Fatal("unselected stale source bypassed whole-plan integrity", err)
	}
}

func TestRankedPartitionArgumentsRejectNullUnboundedAndOtherToolUse(t *testing.T) {
	capture, layers, state := capturedPages(t, map[string][]byte{"a.txt": []byte("Needle")})
	for _, raw := range []string{
		`{"query":"Needle","mode":"ranked","partition":null}`,
		`{"query":"Needle","mode":"ranked","partition":-1}`,
		`{"query":"Needle","mode":"ranked","partition":64}`,
		`{"query":"Needle","mode":"ranked","partition":"0"}`,
		`{"query":"Needle","mode":"literal","partition":0}`,
		`{"query":"Needle","mode":"ranked","partition":1}`,
	} {
		if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal("invalid partition accepted", raw)
		}
	}
	if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt","partition":0}`)}); err == nil {
		t.Fatal("partition parameter used by another tool")
	}
}

func TestPartitionCapacityFallbackExplicitlyLabelsFullLiteralScope(t *testing.T) {
	files := map[string][]byte{}
	for n := 0; n < 300; n++ {
		files[fmt.Sprintf("src/%04d.txt", n)] = []byte("Needle captured source\n")
	}
	capture, layers, state := capturedPages(t, files)
	cache, err := retrieval.NewCache(1, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	raw := json.RawMessage(`{"query":"Needle","mode":"ranked","partition":1,"limit":1}`)
	value, err := nativeReadPageWithCache(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: raw}, cache)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	var result struct {
		Fallback  bool                         `json:"fallback_used"`
		Scope     string                       `json:"fallback_scope"`
		Requested *retrieval.PartitionEvidence `json:"requested_partition"`
		Literal   struct {
			Matches []struct {
				Path string `json:"path"`
			} `json:"matches"`
		} `json:"literal"`
	}
	if err = json.Unmarshal(encoded, &result); err != nil || !result.Fallback || result.Scope != "FULL_CAPTURED_POLICY_SCOPE_LITERAL_PAGE; NOT_PARTITION_RANKING" || result.Requested == nil || result.Requested.Selected.Ordinal != 1 || len(result.Literal.Matches) != 1 || result.Literal.Matches[0].Path != "src/0000.txt" {
		t.Fatal("capacity fallback masqueraded as selected partition", string(encoded), err)
	}
}

func BenchmarkOwnerLazyPartition(b *testing.B) {
	files := map[string][]byte{}
	for n := 0; n < 600; n++ {
		files[fmt.Sprintf("src/%04d.txt", n)] = []byte("NeedleCommon captured declaration\n")
	}
	capture, layers, state := capturedPages(b, files)
	cache, err := retrieval.NewCache(16<<20, 4, time.Minute)
	if err != nil {
		b.Fatal(err)
	}
	defer cache.Close()
	call := model.Call{Name: "fs_search", Arguments: json.RawMessage(`{"query":"NeedleCommon","mode":"ranked","partition":0,"limit":5}`)}
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
