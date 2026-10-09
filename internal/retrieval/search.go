package retrieval

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type Signal struct {
	Kind string `json:"kind"`
	Rank int    `json:"rank"`
}
type Hit struct {
	Path       string   `json:"path"`
	FileDigest string   `json:"file_digest"`
	Start      int      `json:"byte_start"`
	End        int      `json:"byte_end"`
	Line       int      `json:"line"`
	Bytes      []byte   `json:"exact_bytes"`
	Exact      bool     `json:"literal_match"`
	RankScore  int64    `json:"rrf_score_micros"`
	Signals    []Signal `json:"signals"`
}
type Result struct {
	SpanPolicy    string         `json:"span_policy"`
	Cache         *CacheEvidence `json:"cache,omitempty"`
	SchemaVersion int            `json:"schema_version"`
	Trust         string         `json:"trust"`
	Manifest      Manifest       `json:"index"`
	Query         string         `json:"query"`
	Intent        string         `json:"intent"`
	Hits          []Hit          `json:"hits"`
	Total         int            `json:"ranked_pool_total"`
	PoolTruncated bool           `json:"ranked_pool_truncated"`
	QueryMicros   int64          `json:"query_micros"`
	Fallback      bool           `json:"fallback_used"`
}

func ValidIntent(intent string) bool {
	return intent == "IDENTIFIER" || intent == "ERROR" || intent == "FEATURE" || intent == "REFACTOR"
}
func (index *Index) Search(ctx context.Context, binding Binding, query, intent string, offset, limit int) (Result, error) {
	result := Result{SchemaVersion: 1, Trust: "UNTRUSTED_SOURCE_BOUND_RANKING_HINT", Manifest: index.manifest, Query: query, Intent: intent, Hits: []Hit{}, SpanPolicy: "MAX_3_NONOVERLAPPING_CHUNKS_PER_FILE; BODY_LITERAL_BEFORE_PATH"}
	if binding != index.manifest.Binding {
		return result, c.Fail(c.StaleBase, "index snapshot or policy changed")
	}
	if query == "" || len(query) > 256 || !utf8.ValidString(query) || strings.ContainsRune(query, 0) || !ValidIntent(intent) || offset < 0 || limit < 1 || limit > 128 {
		return result, c.Fail(c.InvalidArgument, "bounded literal retrieval query, intent and page required")
	}
	started := time.Now()
	lexicalQuery := terms(query)
	if intent == "IDENTIFIER" {
		lexicalQuery = phrase(query)
	}
	lexical, err := index.rows(ctx, "lexical", lexicalQuery)
	if err != nil {
		return result, err
	}
	trigram := []int{}
	if utf8.RuneCountInString(query) >= 3 {
		trigram, err = index.rows(ctx, "substrings", phrase(query))
		if err != nil {
			return result, err
		}
	}
	result.PoolTruncated = len(lexical) == 512 || len(trigram) == 512
	type candidate struct {
		id        int
		score     int64
		exact     bool
		bodyExact bool
		signals   []Signal
	}
	pool := map[int]*candidate{}
	add := func(id int, kind string, rank int, exact bool) {
		current := pool[id]
		if current == nil {
			current = &candidate{id: id, signals: []Signal{}}
			pool[id] = current
		}
		current.exact = current.exact || exact
		if kind == "BODY_LITERAL" {
			current.bodyExact = true
		}
		if kind != "BODY_LITERAL" && kind != "PATH_LITERAL" {
			current.score += 1000000 / int64(60+rank)
		}
		current.signals = append(current.signals, Signal{kind, rank})
	}
	// Body literals precede path-only hints. A path contributes once, so its
	// length or chunk count cannot flood the ranked pool.
	rank := 0
	pathSeen := map[string]bool{}
	for id, chunk := range index.chunks {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if strings.Contains(chunk.text, query) {
			rank++
			add(id, "BODY_LITERAL", rank, true)
		} else if !pathSeen[chunk.path] && strings.Contains(chunk.path, query) {
			rank++
			add(id, "PATH_LITERAL", rank, true)
		}
		pathSeen[chunk.path] = true
	}
	for rank, id := range lexical {
		add(id, "BM25", rank+1, false)
	}
	for rank, id := range trigram {
		add(id, "TRIGRAM", rank+1, false)
	}
	ordered := make([]*candidate, 0, len(pool))
	for _, item := range pool {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.bodyExact != b.bodyExact {
			return a.bodyExact
		}
		if a.exact != b.exact {
			return a.exact
		}
		if a.score != b.score {
			return a.score > b.score
		}
		ca, cb := index.chunks[a.id], index.chunks[b.id]
		if ca.path != cb.path {
			return ca.path < cb.path
		}
		return ca.start < cb.start
	})
	unique := []*candidate{}
	seen := map[string][]chunk{}
	for _, item := range ordered {
		current := index.chunks[item.id]
		prior := seen[current.path]
		if len(prior) >= 3 {
			result.PoolTruncated = true
			continue
		}
		overlap := false
		for _, other := range prior {
			if current.start < other.end && other.start < current.end {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		seen[current.path] = append(prior, current)
		unique = append(unique, item)
	}
	if len(unique) > 1024 {
		unique = unique[:1024]
		result.PoolTruncated = true
	}
	result.Total = len(unique)
	if offset > len(unique) {
		return result, c.Fail(c.StaleBase, "retrieval cursor beyond ranked pool")
	}
	for _, item := range unique[offset:min(offset+limit, len(unique))] {
		chunk := index.chunks[item.id]
		source := index.sources[chunk.path]
		if c.HashBytes(source.Bytes) != chunk.digest || chunk.start < 0 || chunk.end > len(source.Bytes) || string(source.Bytes[chunk.start:chunk.end]) != chunk.text {
			return result, c.Fail(c.StoreIntegrityError, "index hydration disagrees with exact source")
		}
		result.Hits = append(result.Hits, Hit{chunk.path, chunk.digest, chunk.start, chunk.end, chunk.line, bytes.Clone(source.Bytes[chunk.start:chunk.end]), item.exact, item.score, append([]Signal{}, item.signals...)})
	}
	result.QueryMicros = time.Since(started).Microseconds()
	return result, nil
}
