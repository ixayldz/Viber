package intelligence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
)

func testCapture(t *testing.T) workspace.Capture {
	t.Helper()
	data := map[string][]byte{"target.go": []byte("package target\nfunc Changed() {}\n"), "related.go": []byte("package related\nfunc Helper() {}\n")}
	entries := []c.Entry{}
	for path, raw := range data {
		entries = append(entries, c.Entry{Path: path, Hash: c.HashBytes(raw), Size: int64(len(raw)), Mode: 0644})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	state := c.GitState{GitDir: "C:/repo/.git", CommonDir: "C:/repo/.git", HeadObject: strings.Repeat("a", 40), ObjectFormat: "sha1", Entries: []c.GitIndexEntry{}}
	state.IgnoreSourcesDigest, _ = c.Digest(map[string][]byte{})
	snapshot := c.Snapshot{Git: &state, SchemaVersion: c.SchemaVersion, Root: "C:/repo", Consistency: "BEST_EFFORT",
		Entries: entries, Exclusions: []string{}, Directories: []string{}}
	snapshot.Digest, _ = c.Digest(snapshot)
	capture := workspace.Capture{Snapshot: snapshot, Contents: data, IgnoreSources: map[string][]byte{}}
	if err := workspace.VerifyCapture(capture); err != nil {
		t.Fatalf("invalid test source: %v", err)
	}
	return capture
}

func testGitHistory(t *testing.T, source workspace.Capture, commits []workspace.HistoryCommit, cutoff time.Time) workspace.GitHistory {
	t.Helper()
	stateDigest, err := c.Digest(*source.Snapshot.Git)
	if err != nil {
		t.Fatal(err)
	}
	history := workspace.GitHistory{SchemaVersion: c.SchemaVersion, SourceSnapshotDigest: source.Snapshot.Digest,
		GitStateDigest: stateDigest, HeadObject: source.Snapshot.Git.HeadObject, ObjectFormat: "sha1",
		CutoffUTC: cutoff.UTC().Format(time.RFC3339), Consistency: "BEST_EFFORT",
		Scope: "CAPTURED_HEAD_FIRST_PARENT_COMMITS_AT_OR_BEFORE_CUTOFF", Coverage: "PARTIAL_OR_UNKNOWN",
		FirstParentDiffsOnly: true, Unknowns: []string{"PACKED_GIT_OBJECTS_NOT_PARSED_HISTORY_UNKNOWN"}, Commits: commits}
	history.ReportDigest, _ = c.Digest(history)
	return history
}

func TestCoChangeRanksOnlyBoundHistoryAndMarksPartialCoverage(t *testing.T) {
	source := testCapture(t)
	cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	commits := []workspace.HistoryCommit{
		{ObjectID: strings.Repeat("a", 40), CommitterUTC: "2026-03-01T00:00:00Z", Changes: []workspace.HistoryChange{{Path: "related.go", Kind: "MODIFIED"}, {Path: "target.go", Kind: "MODIFIED"}}},
		{ObjectID: strings.Repeat("b", 40), CommitterUTC: "2026-02-01T00:00:00Z", Changes: []workspace.HistoryChange{{Path: "related.go", Kind: "MODIFIED"}, {Path: "target.go", Kind: "MODIFIED"}}},
	}
	history := testGitHistory(t, source, commits, cutoff)
	report, err := RankCoChanges(history, source, "target.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if report.HistoryCoverage != "PARTIAL_OR_UNKNOWN" || report.Interpretation != "HISTORICAL_CO_CHANGE_NOT_CAUSATION" ||
		len(report.Results) != 1 || report.Results[0].Path != "related.go" || report.Results[0].Occurrences != 2 {
		t.Fatalf("unexpected co-change ranking: %#v", report)
	}
	if err := ValidateCoChanges(report, history, source); err != nil {
		t.Fatal(err)
	}
	forged := report
	forged.Results = append([]CoChange(nil), report.Results...)
	forged.Results[0].EvidenceCommits = append([]string(nil), report.Results[0].EvidenceCommits...)
	forged.Results[0].EvidenceCommits[0] = strings.Repeat("c", 40)
	forged.ReportDigest = ""
	forged.ReportDigest, _ = c.Digest(forged)
	if err := ValidateCoChanges(forged, history, source); err == nil {
		t.Fatal("forged co-change evidence admitted")
	}
}

func TestLocalEmbeddingReranksExactPoolAndRejectsUntrustedSourceBoundary(t *testing.T) {
	source := testCapture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "test-runtime"})
		case "/api/embed":
			var request struct {
				Model    string   `json:"model"`
				Input    []string `json:"input"`
				Truncate bool     `json:"truncate"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.Model != "local-embed:1" || request.Truncate || len(request.Input) != 3 {
				t.Errorf("unexpected embed request: %#v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"model": request.Model, "embeddings": [][]float64{{1, 0}, {0, 1}, {1, 0}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	spans := []SourceSpan{{Path: "related.go", Start: 0, End: len(source.Contents["related.go"])}, {Path: "target.go", Start: 0, End: len(source.Contents["target.go"])}}
	report, err := RankCapturedSpans(context.Background(), source, "target", spans, LocalEmbeddingOptions{
		Endpoint: server.URL, Model: "local-embed:1", ModelRevision: "sha256:operator-declared", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ranked[0].Path != "target.go" || report.Manifest.EgressAttested ||
		report.Manifest.EgressPosture != "LOOPBACK_ONLY_DECLARED_NOT_ATTESTED" ||
		report.Manifest.RuntimeVersion != "test-runtime" || report.Manifest.Dimensions != 2 {
		t.Fatalf("unexpected local ranking: %#v", report)
	}
	if err := ValidateSemanticRanking(report, source, spans); err != nil {
		t.Fatal(err)
	}
	forged := report
	forged.Ranked = append([]RankedSpan(nil), report.Ranked...)
	forged.Ranked[0].Path = "newly-added.go"
	if err := ValidateSemanticRanking(forged, source, spans); err == nil {
		t.Fatal("reranker widened its candidate pool")
	}
}

func TestLocalEmbeddingRejectsRemoteEndpointAndMalformedVectors(t *testing.T) {
	source := testCapture(t)
	spans := []SourceSpan{{Path: "target.go", Start: 0, End: 8}}
	_, err := RankCapturedSpans(context.Background(), source, "query", spans, LocalEmbeddingOptions{Endpoint: "http://example.invalid:11434", Model: "local:1", ModelRevision: "v1"})
	if err == nil {
		t.Fatal("remote embedding endpoint admitted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_, _ = w.Write([]byte("{\"version\":\"test\"}"))
			return
		}
		_, _ = w.Write([]byte("{\"model\":\"local:1\",\"embeddings\":[[1,0],[0,0]]}"))
	}))
	defer server.Close()
	_, err = RankCapturedSpans(context.Background(), source, "query", spans, LocalEmbeddingOptions{Endpoint: server.URL, Model: "local:1", ModelRevision: "v1"})
	if err == nil {
		t.Fatal("zero vector accepted")
	}
}

func TestBlueprintIsSourceBoundAndNeverAnAcceptanceReceipt(t *testing.T) {
	source := testCapture(t)
	fileDigest := c.HashBytes(source.Contents["target.go"])
	manifest, err := BuildBlueprint(source,
		[]BlueprintDecision{{Area: "storage", Choice: "use existing store", Reason: "source shows existing owner", Evidence: []BlueprintEvidence{{Path: "target.go", FileDigest: fileDigest}}}},
		[]BlueprintDependency{{Ecosystem: "go", Name: "example.test/lib", Version: "v1.2.3", Source: "operator declaration"}},
		[]string{"internal/generated.go"}, []string{"protected:unit", "protected:integration"})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.AcceptanceState != "EXPERIMENTAL_NOT_ACCEPTANCE" || manifest.Dependencies[0].Resolution != "DECLARED_NOT_RESOLVED" {
		t.Fatalf("blueprint overclaimed status: %#v", manifest)
	}
	if err := ValidateBlueprint(manifest, source); err != nil {
		t.Fatal(err)
	}
	forged := manifest
	forged.AcceptanceState = "VERIFIED"
	forged.ManifestDigest = ""
	forged.ManifestDigest, _ = c.Digest(forged)
	if err := ValidateBlueprint(forged, source); err == nil {
		t.Fatal("blueprint claimed verification acceptance")
	}
	stale := manifest
	stale.SourceSnapshotDigest = c.HashBytes([]byte("different source snapshot"))
	stale.ManifestDigest = ""
	stale.ManifestDigest, _ = c.Digest(stale)
	if err := ValidateBlueprint(stale, source); err == nil {
		t.Fatal("stale blueprint admitted")
	}
}
