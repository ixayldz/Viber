package retrieval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	c "github.com/ixayldz/Viber/internal/contracts"
	"strings"
	"testing"
	"unicode/utf8"
)

func fixtureBinding() Binding {
	return Binding{c.HashBytes([]byte("candidate")), c.HashBytes([]byte("policy"))}
}
func document(path, text string) Document {
	raw := []byte(text)
	return Document{path, c.HashBytes(raw), raw}
}
func openIndex(t *testing.T, docs []Document) *Index {
	t.Helper()
	index, err := New(context.Background(), fixtureBinding(), docs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { index.Close() })
	return index
}
func TestSourceBoundFTSAndLiteralPriority(t *testing.T) {
	docs := []Document{document("src/exact.go", "RetryHTTPClient closes transport // Retry HTTP Client\n"), document("src/tokens.go", "retry http client retries transport\n"), document("src/binary.dat", string([]byte{0, 255}))}
	index := openIndex(t, docs)
	for _, query := range []string{"RetryHTTPClient", "HTTPCl", "\" OR body:*", "é", "x"} {
		result, err := index.Search(context.Background(), fixtureBinding(), query, "IDENTIFIER", 0, 10)
		if err != nil {
			t.Fatal(query, err)
		}
		for _, hit := range result.Hits {
			raw := index.sources[hit.Path].Bytes
			if !bytes.Equal(hit.Bytes, raw[hit.Start:hit.End]) || hit.FileDigest != c.HashBytes(raw) {
				t.Fatal("false source span")
			}
			if hit.Path == "src/binary.dat" {
				t.Fatal("binary indexed")
			}
		}
		if query == "RetryHTTPClient" || query == "HTTPCl" {
			if len(result.Hits) != 1 || !result.Hits[0].Exact || result.Hits[0].Path != "src/exact.go" {
				t.Fatal("identifier/substring missed", result)
			}
		}
	}
	result, err := index.Search(context.Background(), fixtureBinding(), "retry http client", "FEATURE", 0, 10)
	if err != nil || len(result.Hits) < 2 || result.Hits[0].Path != "src/tokens.go" || !result.Hits[0].Exact {
		t.Fatal("literal did not outrank token hints", result, err)
	}
	if index.Manifest().SkippedBinary != 1 || index.Manifest().SQLiteBytes == 0 {
		t.Fatal("false coverage/cost")
	}
}
func TestSourceOwnershipStaleAndChunkBoundary(t *testing.T) {
	query := strings.Repeat("é", 128)
	doc := document("src/unicode.go", strings.Repeat("a", 3960)+query+"\r\nnext")
	index := openIndex(t, []Document{doc})
	doc.Bytes[0] = 'b'
	result, err := index.Search(context.Background(), fixtureBinding(), query, "IDENTIFIER", 0, 1)
	if err != nil || len(result.Hits) != 1 || !bytes.Contains(result.Hits[0].Bytes, []byte(query)) {
		t.Fatal("boundary/immutable capture failure", err)
	}
	result.Hits[0].Bytes[0] = 'z'
	again, err := index.Search(context.Background(), fixtureBinding(), query, "IDENTIFIER", 0, 1)
	if err != nil || c.HashBytes(index.sources[doc.Path].Bytes) != doc.Digest || bytes.Equal(again.Hits[0].Bytes, result.Hits[0].Bytes) {
		t.Fatal("result mutated index")
	}
	for _, binding := range []Binding{{c.HashBytes([]byte("other")), fixtureBinding().Policy}, {fixtureBinding().Candidate, c.HashBytes([]byte("other"))}} {
		_, err = index.Search(context.Background(), binding, query, "IDENTIFIER", 0, 1)
		var failure *c.Error
		if !errors.As(err, &failure) || failure.Code != c.StaleBase {
			t.Fatal("stale binding accepted", err)
		}
	}
	corrupted := document("src/bad.go", "before")
	corrupted.Bytes = []byte("after")
	if _, err = New(context.Background(), fixtureBinding(), []Document{corrupted}); err == nil {
		t.Fatal("changed source accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = New(canceled, fixtureBinding(), []Document{doc}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = index.Search(canceled, fixtureBinding(), "unicode", "IDENTIFIER", 0, 1); err == nil {
		t.Fatal("cancel ignored")
	}
}

// Relevance labels are declared independently of the ranker. This synthetic
// fixture measures retrieval only, never model coding success or adoption.
func effectivenessCorpus() ([]Document, []struct{ query, intent, want string }) {
	docs := []Document{
		document("src/auth.go", "func RefreshAccessToken() {} // refresh access token expiration"),
		document("src/store.go", "database is locked: retry transaction after sqlite busy"),
		document("tests/auth_test.go", "--- FAIL: TestExpiredSession\nassert expired session rejected"),
		document("src/client.go", "RetryHTTPClient transport closes idle connections"),
		document("docs/security.md", "rate limiting uses token bucket capacity for login"),
		document("src/utf8.go", "🙂 TürkçeKimlik doğrulama")}
	for n := 0; n < 60; n++ {
		docs = append(docs, document(fmt.Sprintf("noise/file%03d.txt", n), strings.Repeat("unrelated rendering geometry pixel shader\n", 32)))
	}
	return docs, []struct{ query, intent, want string }{
		{"RefreshAccessToken", "IDENTIFIER", "src/auth.go"}, {"database is locked", "ERROR", "src/store.go"},
		{"TestExpiredSession", "ERROR", "tests/auth_test.go"}, {"HTTPCl", "IDENTIFIER", "src/client.go"},
		{"token bucket capacity", "FEATURE", "docs/security.md"}, {"TürkçeKimlik", "REFACTOR", "src/utf8.go"}}
}
func TestContextEffectivenessRelevanceAndExactSources(t *testing.T) {
	docs, cases := effectivenessCorpus()
	index := openIndex(t, docs)
	found := 0
	for _, item := range cases {
		result, err := index.Search(context.Background(), fixtureBinding(), item.query, item.intent, 0, 3)
		if err != nil {
			t.Fatal(err)
		}
		hit := false
		for _, candidate := range result.Hits {
			if candidate.Path == item.want {
				hit = true
			}
			raw := index.sources[candidate.Path].Bytes
			if !bytes.Equal(candidate.Bytes, raw[candidate.Start:candidate.End]) {
				t.Fatal("invalid hydration")
			}
		}
		if hit {
			found++
		} else {
			t.Errorf("missed label %s -> %s", item.query, item.want)
		}
	}
	t.Logf("synthetic recall@3=%d/%d; exact source validity checked for every result; NOT coding success", found, len(cases))
}
func BenchmarkSourceRetrieval(b *testing.B) {
	docs, cases := effectivenessCorpus()
	b.Run("cold_index_and_query", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			index, err := New(context.Background(), fixtureBinding(), docs)
			if err != nil {
				b.Fatal(err)
			}
			if _, err = index.Search(context.Background(), fixtureBinding(), cases[0].query, cases[0].intent, 0, 3); err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(index.Manifest().SQLiteBytes), "sqlite-bytes")
			index.Close()
		}
	})
	b.Run("warm_query_only", func(b *testing.B) {
		index, err := New(context.Background(), fixtureBinding(), docs)
		if err != nil {
			b.Fatal(err)
		}
		defer index.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item := cases[i%len(cases)]
			if _, err = index.Search(context.Background(), fixtureBinding(), item.query, item.intent, 0, 3); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestRankedPoolTruncationAndInvalidBounds(t *testing.T) {
	docs := []Document{}
	for n := 0; n < 1100; n++ {
		docs = append(docs, document(fmt.Sprintf("src/%04d.txt", n), "same identifier"))
	}
	index := openIndex(t, docs)
	result, err := index.Search(context.Background(), fixtureBinding(), "same", "IDENTIFIER", 1023, 1)
	if err != nil || !result.PoolTruncated || result.Total != 1024 || len(result.Hits) != 1 {
		t.Fatal("false pool completeness", result, err)
	}
	if _, err = index.Search(context.Background(), fixtureBinding(), "same", "IDENTIFIER", 1025, 1); err == nil {
		t.Fatal("out of pool offset accepted")
	}
	oversized := Document{Path: "src/large", Digest: c.HashBytes(nil), Bytes: make([]byte, MaxBytes+1)}
	_, err = New(context.Background(), fixtureBinding(), []Document{oversized})
	var failure *c.Error
	if !errors.As(err, &failure) || failure.Code != c.UnsupportedCapability {
		t.Fatal("source quota not typed for literal fallback", err)
	}
}
func FuzzBoundLiteralQuerySourceValidity(f *testing.F) {
	for _, query := range []string{"Identifier", "\" OR body:*", "HTTPCl", "🙂", "...", "a\nb"} {
		f.Add(query)
	}
	index, err := New(context.Background(), fixtureBinding(), []Document{document("src/source.go", "Identifier RetryHTTPClient 🙂")})
	if err != nil {
		f.Fatal(err)
	}
	defer index.Close()
	f.Fuzz(func(t *testing.T, query string) {
		if query == "" || len(query) > 256 || !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
			t.Skip()
		}
		result, err := index.Search(context.Background(), fixtureBinding(), query, "IDENTIFIER", 0, 3)
		if err != nil {
			t.Fatal("literal query interpreted as syntax", err)
		}
		for _, hit := range result.Hits {
			raw := index.sources[hit.Path].Bytes
			if !bytes.Equal(hit.Bytes, raw[hit.Start:hit.End]) || c.HashBytes(raw) != hit.FileDigest {
				t.Fatal("invalid source binding")
			}
		}
	})
}
