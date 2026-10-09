package agent

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/retrieval"
	"strings"
	"testing"
	"time"
)

func TestOwnerSearchReusesIndexButRechecksPolicyAndBytes(t *testing.T) {
	capture, layers, state := capturedPages(t, map[string][]byte{"src/a.go": []byte("Needle"), "secret.go": []byte("Needle private")})
	state.TaskID = "cache-task"
	cache, err := retrieval.NewCache(8<<20, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	search := func() (retrieval.Result, error) {
		args, _ := json.Marshal(map[string]any{"query": "Needle", "mode": "ranked", "intent": "IDENTIFIER", "limit": 8})
		value, err := nativeReadPageWithCache(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: args}, cache)
		if err != nil {
			return retrieval.Result{}, err
		}
		encoded, _ := json.Marshal(value)
		var page struct {
			Retrieval retrieval.Result `json:"retrieval"`
		}
		err = json.Unmarshal(encoded, &page)
		return page.Retrieval, err
	}
	first, err := search()
	if err != nil || first.Cache == nil || first.Cache.Hit {
		t.Fatal("first owner build", err)
	}
	second, err := search()
	if err != nil || second.Cache == nil || !second.Cache.Hit {
		t.Fatal("owner rebuilt warm index", err)
	}
	layers[0].Paths = []string{"src/**"}
	restricted, err := search()
	if err != nil || restricted.Cache.Hit || restricted.Manifest.Documents != 1 {
		t.Fatal("policy cache leak", err)
	}
	for _, hit := range restricted.Hits {
		if hit.Path == "secret.go" {
			t.Fatal("revoked source returned")
		}
	}
	capture.Contents["src/a.go"] = []byte("mutated")
	_, err = search()
	var failure *c.Error
	if !errors.As(err, &failure) || failure.Code != c.StoreIntegrityError {
		t.Fatal("cache bypassed source integrity", err)
	}
}

func TestRankedSearchFiltersPolicyBeforeIndexAndBindsCursor(t *testing.T) {
	capture, layers, state := capturedPages(t, map[string][]byte{
		"src/a.go": []byte("SecretIdentifier"), "src/b.go": []byte("SecretIdentifier"),
		"private/hidden.go": []byte("SecretIdentifier protected payload"), "private/.env": []byte("private")})
	layers[0].Paths = []string{"src/**"}
	args := map[string]any{"query": "SecretIdentifier", "mode": "ranked", "intent": "IDENTIFIER", "limit": 1}
	raw := pageResult(t, capture, layers, state, "fs_search", args)
	var page struct {
		Retrieval retrieval.Result `json:"retrieval"`
		Coverage  pageCoverage     `json:"coverage"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if page.Retrieval.Manifest.Documents != 2 || len(page.Retrieval.Hits) != 1 || !page.Coverage.HasMore || strings.Contains(string(raw), "private/") {
		t.Fatal("unauthorized index/metadata", string(raw))
	}
	args["cursor"] = page.Coverage.Next
	next := pageResult(t, capture, layers, state, "fs_search", args)
	if !strings.Contains(string(next), "src/b.go") {
		t.Fatal("pagination repeated", string(next))
	}
	for _, change := range []struct {
		key   string
		value any
	}{{"mode", "literal"}, {"intent", "ERROR"}, {"query", "changed"}} {
		altered := map[string]any{}
		for k, v := range args {
			altered[k] = v
		}
		altered[change.key] = change.value
		if change.key == "mode" {
			delete(altered, "intent")
		}
		encoded, _ := json.Marshal(altered)
		_, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: encoded})
		var failure *c.Error
		if !errors.As(err, &failure) || failure.Code != c.StaleBase {
			t.Fatal("cross-scope cursor accepted", change, err)
		}
	}
	layers[0].DeniedPaths = []string{"src/b.go"}
	encoded, _ := json.Marshal(args)
	if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: encoded}); err == nil {
		t.Fatal("old policy cursor accepted")
	}
	delete(args, "cursor")
	capture.Contents["src/a.go"] = []byte("tampered")
	encoded, _ = json.Marshal(args)
	if _, err := nativeReadPage(context.Background(), capture, layers, state, model.Call{Name: "fs_search", Arguments: encoded}); err == nil {
		t.Fatal("changed source accepted")
	}
}
func TestCheckLogSelectionRemainsUntrustedAndReceiptBound(t *testing.T) {
	s, options := checkFixture(t, "golang@sha256:"+strings.Repeat("a", 64), []Turn{{Text: "done", UsageKnown: true}})
	state, doc := syntheticCheck(t, s, options)
	result, err := s.readCheckOutput(context.Background(), doc, checkOutputQuery{RunID: "unit-run", Stream: "stdout", Limit: 6, Intent: "ERROR"}, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), doc.CheckRuns[0].Digest) || !strings.Contains(string(raw), "UNTRUSTED_LOG_HINT_NOT_VERIFICATION") || !strings.Contains(string(raw), doc.Candidate.SnapshotDigest) {
		t.Fatal("missing receipt binding", string(raw))
	}
	summaries, err := s.CheckSummaries(doc, state)
	if err != nil || summaries[0].Verification != "UNKNOWN" {
		t.Fatal("log became verification", err)
	}
	if _, err = s.readCheckOutput(context.Background(), doc, checkOutputQuery{RunID: "unit-run", Stream: "stdout", Offset: 1, Limit: 6, Intent: "ERROR"}, false); err == nil {
		t.Fatal("ambiguous intent offset")
	}
}
