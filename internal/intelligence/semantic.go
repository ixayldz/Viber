package intelligence

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

const LocalSemanticVersion = "OLLAMA_EMBED_COSINE_RERANK_V1"
const maxSemanticCandidates = 64
const maxSemanticSpanBytes = 4096
const maxSemanticInputBytes = 128 << 10
const maxSemanticResponseBytes = 2 << 20

type SourceSpan struct {
	Path  string `json:"path"`
	Start int    `json:"byte_start"`
	End   int    `json:"byte_end"`
}
type RankedSpan struct {
	SourceSpan
	FileDigest   string `json:"file_digest"`
	SpanDigest   string `json:"span_digest"`
	Rank         int    `json:"rank"`
	CosineMicros int64  `json:"cosine_micros"`
}
type EmbeddingManifest struct {
	AdapterVersion        string `json:"adapter_version"`
	Runtime               string `json:"runtime"`
	RuntimeVersion        string `json:"runtime_version"`
	Endpoint              string `json:"endpoint"`
	Model                 string `json:"model"`
	DeclaredModelRevision string `json:"declared_model_revision"`
	SourceSnapshotDigest  string `json:"source_snapshot_digest"`
	InputSetDigest        string `json:"input_set_digest"`
	QueryDigest           string `json:"query_digest"`
	Dimensions            int    `json:"dimensions"`
	EgressPosture         string `json:"egress_posture"`
	EgressAttested        bool   `json:"egress_attested"`
}
type SemanticRanking struct {
	SchemaVersion int               `json:"schema_version"`
	Trust         string            `json:"trust"`
	Manifest      EmbeddingManifest `json:"embedding_manifest"`
	PoolDigest    string            `json:"candidate_pool_digest"`
	Original      []RankedSpan      `json:"original_candidates"`
	Ranked        []RankedSpan      `json:"ranked_candidates"`
	ReportDigest  string            `json:"report_digest"`
}
type LocalEmbeddingOptions struct {
	Endpoint      string
	Model         string
	ModelRevision string
	Timeout       time.Duration
}
type ollamaEmbedRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	Truncate bool     `json:"truncate"`
}
type ollamaEmbedResponse struct {
	Model              string      `json:"model"`
	Embeddings         [][]float64 `json:"embeddings"`
	TotalDuration      int64       `json:"total_duration,omitempty"`
	LoadDuration       int64       `json:"load_duration,omitempty"`
	PromptEvalCount    int64       `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64       `json:"prompt_eval_duration,omitempty"`
}
type ollamaVersionResponse struct {
	Version string `json:"version"`
}

// RankCapturedSpans calls a literal loopback Ollama origin and reranks only
// exact spans in the supplied pool. It never adds source paths or check authority.
func RankCapturedSpans(ctx context.Context, source workspace.Capture, query string, candidates []SourceSpan, options LocalEmbeddingOptions) (SemanticRanking, error) {
	out := SemanticRanking{SchemaVersion: c.SchemaVersion, Trust: "UNTRUSTED_LOCAL_EMBEDDING_ORDERING_HINT", Original: []RankedSpan{}, Ranked: []RankedSpan{}}
	if err := workspace.VerifyCapture(source); err != nil {
		return out, err
	}
	if !utf8.ValidString(query) || strings.TrimSpace(query) == "" || len(query) > 2048 || len(candidates) == 0 || len(candidates) > maxSemanticCandidates {
		return out, c.Fail(c.InvalidArgument, "bounded query and exact candidate pool required")
	}
	endpoint, parsed, err := validateLocalEndpoint(options.Endpoint)
	if err != nil {
		return out, err
	}
	model, revision := strings.TrimSpace(options.Model), strings.TrimSpace(options.ModelRevision)
	lower := strings.ToLower(model)
	if model == "" || len(model) > 256 || strings.ContainsAny(model, "\r\n\x00") ||
		strings.Contains(lower, ":cloud") || strings.HasSuffix(lower, "-cloud") ||
		revision == "" || len(revision) > 256 || strings.ContainsAny(revision, "\r\n\x00") {
		return out, c.Fail(c.InvalidArgument, "local model and declared revision required")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	if timeout < time.Second || timeout > 2*time.Minute {
		return out, c.Fail(c.InvalidArgument, "local embedding timeout outside bounds")
	}
	sourceFiles := map[string]c.Entry{}
	for _, entry := range source.Snapshot.Entries {
		sourceFiles[entry.Path] = entry
	}
	refs, inputs := make([]RankedSpan, 0, len(candidates)), []string{query}
	totalBytes, seen := len(query), map[string]bool{}
	for _, candidate := range candidates {
		if !policy.SafePath(candidate.Path) || candidate.Start < 0 || candidate.End <= candidate.Start || candidate.End-candidate.Start > maxSemanticSpanBytes {
			return out, c.Fail(c.InvalidArgument, "candidate span outside bounded exact source range")
		}
		key := spanKey(candidate)
		if seen[key] {
			return out, c.Fail(c.InvalidArgument, "duplicate semantic candidate span")
		}
		seen[key] = true
		raw, exists := source.Contents[candidate.Path]
		entry, manifested := sourceFiles[candidate.Path]
		if !exists || !manifested || candidate.End > len(raw) || entry.Hash != c.HashBytes(raw) ||
			!utf8.Valid(raw[candidate.Start:candidate.End]) || bytes.IndexByte(raw[candidate.Start:candidate.End], 0) >= 0 {
			return out, c.Fail(c.StoreIntegrityError, "semantic candidate disagrees with captured source")
		}
		span := bytes.Clone(raw[candidate.Start:candidate.End])
		totalBytes += len(span)
		if totalBytes > maxSemanticInputBytes {
			return out, c.Fail(c.UnsupportedCapability, "local embedding input byte quota exceeded")
		}
		inputs = append(inputs, string(span))
		refs = append(refs, RankedSpan{SourceSpan: candidate, FileDigest: entry.Hash, SpanDigest: c.HashBytes(span)})
	}
	queryDigest := c.HashBytes([]byte(query))
	poolDigest, err := semanticPoolDigest(source.Snapshot.Digest, queryDigest, refs)
	if err != nil {
		return out, err
	}
	runtimeVersion, err := ollamaVersion(ctx, endpoint, parsed, timeout)
	if err != nil {
		return out, err
	}
	vectors, err := ollamaEmbed(ctx, endpoint, parsed, timeout, model, inputs)
	if err != nil {
		return out, err
	}
	if len(vectors) != len(inputs) {
		return out, c.Fail(c.StoreIntegrityError, "local embedding response count mismatch")
	}
	dimensions := len(vectors[0])
	if dimensions == 0 || dimensions > 4096 {
		return out, c.Fail(c.UnsupportedCapability, "local embedding dimension outside bounds")
	}
	for _, vector := range vectors {
		if len(vector) != dimensions {
			return out, c.Fail(c.StoreIntegrityError, "local embedding dimensions differ")
		}
		norm := 0.0
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1e6 {
				return out, c.Fail(c.StoreIntegrityError, "invalid local embedding vector")
			}
			norm += value * value
		}
		if norm == 0 || math.IsInf(norm, 0) {
			return out, c.Fail(c.StoreIntegrityError, "empty local embedding vector")
		}
	}
	for n := range refs {
		refs[n].CosineMicros = int64(math.Round(cosine(vectors[0], vectors[n+1]) * 1e6))
	}
	ranked := append([]RankedSpan(nil), refs...)
	sortRankedSpans(ranked)
	for n := range ranked {
		ranked[n].Rank = n + 1
	}
	out.Manifest = EmbeddingManifest{AdapterVersion: LocalSemanticVersion, Runtime: "OLLAMA_LOCAL_LOOPBACK",
		RuntimeVersion: runtimeVersion, Endpoint: endpoint, Model: model, DeclaredModelRevision: revision,
		SourceSnapshotDigest: source.Snapshot.Digest, InputSetDigest: poolDigest, QueryDigest: queryDigest,
		Dimensions: dimensions, EgressPosture: "LOOPBACK_ONLY_DECLARED_NOT_ATTESTED", EgressAttested: false}
	out.PoolDigest, out.Original, out.Ranked = poolDigest, refs, ranked
	digest, err := c.Digest(out)
	if err != nil {
		return SemanticRanking{}, err
	}
	out.ReportDigest = digest
	if err := ValidateSemanticRanking(out, source, candidates); err != nil {
		return SemanticRanking{}, err
	}
	return out, nil
}

func semanticPoolDigest(snapshot, query string, refs []RankedSpan) (string, error) {
	type item struct {
		Path                   string
		Start, End             int
		FileDigest, SpanDigest string
	}
	inputs := make([]item, len(refs))
	for n, ref := range refs {
		inputs[n] = item{ref.Path, ref.Start, ref.End, ref.FileDigest, ref.SpanDigest}
	}
	return c.Digest(struct {
		Snapshot, Query string
		Inputs          []item
	}{snapshot, query, inputs})
}

func ValidateSemanticRanking(report SemanticRanking, source workspace.Capture, candidates []SourceSpan) error {
	if err := workspace.VerifyCapture(source); err != nil {
		return err
	}
	if report.SchemaVersion != c.SchemaVersion || report.Trust != "UNTRUSTED_LOCAL_EMBEDDING_ORDERING_HINT" ||
		report.Manifest.AdapterVersion != LocalSemanticVersion || report.Manifest.Runtime != "OLLAMA_LOCAL_LOOPBACK" ||
		report.Manifest.EgressPosture != "LOOPBACK_ONLY_DECLARED_NOT_ATTESTED" || report.Manifest.EgressAttested ||
		report.Manifest.SourceSnapshotDigest != source.Snapshot.Digest || report.Manifest.Model == "" ||
		report.Manifest.DeclaredModelRevision == "" || report.Manifest.Dimensions < 1 || report.Manifest.Dimensions > 4096 ||
		len(report.Ranked) != len(candidates) || len(report.Original) != len(candidates) || len(report.Ranked) > maxSemanticCandidates {
		return c.Fail(c.StoreIntegrityError, "semantic ranking does not match exact source pool")
	}
	endpoint, _, err := validateLocalEndpoint(report.Manifest.Endpoint)
	if err != nil || endpoint != report.Manifest.Endpoint {
		return c.Fail(c.StoreIntegrityError, "semantic manifest endpoint is not loopback")
	}
	sourceFiles := map[string]c.Entry{}
	for _, entry := range source.Snapshot.Entries {
		sourceFiles[entry.Path] = entry
	}
	expected := map[string]RankedSpan{}
	for _, span := range candidates {
		raw, exists := source.Contents[span.Path]
		file, manifested := sourceFiles[span.Path]
		if !policy.SafePath(span.Path) || !exists || !manifested || span.Start < 0 || span.End <= span.Start || span.End > len(raw) ||
			span.End-span.Start > maxSemanticSpanBytes || file.Hash != c.HashBytes(raw) {
			return c.Fail(c.StoreIntegrityError, "semantic candidate source changed")
		}
		text := raw[span.Start:span.End]
		if !utf8.Valid(text) || bytes.IndexByte(text, 0) >= 0 {
			return c.Fail(c.StoreIntegrityError, "semantic candidate is not bounded text")
		}
		expected[spanKey(span)] = RankedSpan{SourceSpan: span, FileDigest: file.Hash, SpanDigest: c.HashBytes(text)}
	}
	if len(expected) != len(candidates) || len(expected) != len(report.Original) {
		return c.Fail(c.StoreIntegrityError, "semantic pool has duplicates or omissions")
	}
	originalSeen := map[string]bool{}
	for _, original := range report.Original {
		wanted, ok := expected[spanKey(original.SourceSpan)]
		if !ok || originalSeen[spanKey(original.SourceSpan)] || original.FileDigest != wanted.FileDigest || original.SpanDigest != wanted.SpanDigest || original.Rank != 0 {
			return c.Fail(c.StoreIntegrityError, "original semantic pool changed")
		}
		originalSeen[spanKey(original.SourceSpan)] = true
	}
	seen := map[string]bool{}
	for n, item := range report.Ranked {
		wanted, ok := expected[spanKey(item.SourceSpan)]
		if !ok || seen[spanKey(item.SourceSpan)] || item.Rank != n+1 ||
			item.FileDigest != wanted.FileDigest || item.SpanDigest != wanted.SpanDigest ||
			item.CosineMicros < -1000001 || item.CosineMicros > 1000001 {
			return c.Fail(c.StoreIntegrityError, "semantic ranking changed the authorized candidate pool")
		}
		seen[spanKey(item.SourceSpan)] = true
	}
	poolDigest, err := semanticPoolDigest(source.Snapshot.Digest, report.Manifest.QueryDigest, report.Original)
	if err != nil || poolDigest != report.PoolDigest || report.PoolDigest != report.Manifest.InputSetDigest {
		return c.Fail(c.StoreIntegrityError, "semantic pool digest mismatch")
	}
	expectedDigest := report.ReportDigest
	report.ReportDigest = ""
	actual, err := c.Digest(report)
	if err != nil {
		return err
	}
	if actual != expectedDigest {
		return c.Fail(c.StoreIntegrityError, "semantic ranking digest mismatch")
	}
	return nil
}

func spanKey(span SourceSpan) string {
	return span.Path + "\x00" + strconv.Itoa(span.Start) + "\x00" + strconv.Itoa(span.End)
}
func sortRankedSpans(items []RankedSpan) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j-1], items[j]
			if a.CosineMicros > b.CosineMicros || a.CosineMicros == b.CosineMicros && (a.Path < b.Path || a.Path == b.Path && a.Start <= b.Start) {
				break
			}
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
}
func cosine(a, b []float64) float64 {
	var dot, aa, bb float64
	for n := range a {
		dot += a[n] * b[n]
		aa += a[n] * a[n]
		bb += b[n] * b[n]
	}
	return dot / math.Sqrt(aa*bb)
}
func validateLocalEndpoint(raw string) (string, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", nil, c.Fail(c.PolicyDenied, "local embedding requires a loopback origin")
	}
	address, err := netip.ParseAddr(u.Hostname())
	if err != nil || !address.IsLoopback() {
		return "", nil, c.Fail(c.PolicyDenied, "local embedding requires a literal loopback address")
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "11434")
	}
	u.Path = ""
	return strings.TrimSuffix(u.String(), "/"), u, nil
}
func localClient(origin *url.URL, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 20 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConns: 1, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 64 << 10}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host != origin.Hostname() || port != origin.Port() {
			return nil, c.Fail(c.PolicyDenied, "local embedding destination changed")
		}
		return dialer.DialContext(ctx, network, address)
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func readOneJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return c.Fail(c.InvalidArgument, "trailing local adapter response data")
	}
	return nil
}
func ollamaVersion(ctx context.Context, endpoint string, origin *url.URL, timeout time.Duration) (string, error) {
	client := localClient(origin, timeout)
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/version", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", c.Fail(c.UnsupportedCapability, "local runtime version unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return "", c.Fail(c.UnsupportedCapability, "local runtime version response exceeded bound")
	}
	var result ollamaVersionResponse
	if err = readOneJSON(raw, &result); err != nil || result.Version == "" || len(result.Version) > 128 {
		return "", c.Fail(c.StoreIntegrityError, "invalid local runtime version response")
	}
	return result.Version, nil
}
func ollamaEmbed(ctx context.Context, endpoint string, origin *url.URL, timeout time.Duration, model string, inputs []string) ([][]float64, error) {
	payload, err := json.Marshal(ollamaEmbedRequest{Model: model, Input: inputs, Truncate: false})
	if err != nil {
		return nil, err
	}
	client := localClient(origin, timeout)
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/embed", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxSemanticResponseBytes+1))
	if err != nil || len(raw) > maxSemanticResponseBytes {
		return nil, c.Fail(c.UnsupportedCapability, "local embedding response exceeded bound")
	}
	if response.StatusCode != http.StatusOK {
		return nil, c.Fail(c.UnsupportedCapability, "local embedding model request failed")
	}
	var result ollamaEmbedResponse
	if err = readOneJSON(raw, &result); err != nil {
		return nil, c.Fail(c.StoreIntegrityError, "invalid local embedding response")
	}
	if result.Model != model || len(result.Embeddings) != len(inputs) {
		return nil, c.Fail(c.StoreIntegrityError, "local embedding model or count mismatch")
	}
	return result.Embeddings, nil
}
