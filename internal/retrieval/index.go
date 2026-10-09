// Package retrieval indexes only immutable, authorized source bytes. Rankings
// are untrusted hints; exact source bindings and spans remain the authority.
package retrieval

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"modernc.org/sqlite"
)

const Version = "SOURCE_FTS5_BM25_TRIGRAM_RRF_V1"
const MaxBytes = 32 << 20
const MaxChunks = 32768

type Binding struct {
	Candidate string `json:"candidate"`
	Policy    string `json:"policy"`
}
type Document struct {
	Path, Digest string
	Bytes        []byte
}
type chunk struct {
	path, digest     string
	start, end, line int
	text             string
}
type Manifest struct {
	Version          string  `json:"version"`
	Binding          Binding `json:"binding"`
	SourceSet        string  `json:"source_set"`
	Documents        int     `json:"documents"`
	IndexedDocuments int     `json:"indexed_documents"`
	SkippedBinary    int     `json:"skipped_binary"`
	Chunks           int     `json:"chunks"`
	SourceBytes      int64   `json:"source_bytes"`
	SQLiteBytes      int64   `json:"sqlite_allocated_bytes"`
	BuildMicros      int64   `json:"build_micros"`
}
type Index struct {
	db       *sql.DB
	chunks   []chunk
	sources  map[string]Document
	manifest Manifest
}

// New builds an ephemeral private in-memory index. No repository SQLite, WAL,
// extension, SQL or tokenizer configuration is ever opened or executed.
func New(ctx context.Context, binding Binding, documents []Document) (*Index, error) {
	if !c.ValidDigest(binding.Candidate) || !c.ValidDigest(binding.Policy) {
		return nil, c.Fail(c.InvalidArgument, "bounded source-bound index required")
	}
	if len(documents) > 10000 {
		return nil, c.Fail(c.UnsupportedCapability, "index document bound exceeded")
	}
	ordered := append([]Document{}, documents...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	index := &Index{sources: map[string]Document{}, manifest: Manifest{Version: Version, Binding: binding, Documents: len(ordered)}}
	started := time.Now()
	refs := []struct{ Path, Digest string }{}
	for _, doc := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, boundedBuildError(ctx, err)
		}
		if int64(len(doc.Bytes)) > MaxBytes-index.manifest.SourceBytes {
			return nil, c.Fail(c.UnsupportedCapability, "index source byte bound exceeded")
		}
		if len(doc.Path) > 4096 {
			return nil, c.Fail(c.UnsupportedCapability, "index source path bound exceeded")
		}
		doc.Bytes = bytes.Clone(doc.Bytes)
		if !policy.SafePath(doc.Path) || !c.ValidDigest(doc.Digest) || c.HashBytes(doc.Bytes) != doc.Digest {
			return nil, c.Fail(c.StoreIntegrityError, "index source identity mismatch")
		}
		if _, seen := index.sources[doc.Path]; seen {
			return nil, c.Fail(c.InvalidArgument, "duplicate source path")
		}
		index.manifest.SourceBytes += int64(len(doc.Bytes))
		if index.manifest.SourceBytes > MaxBytes {
			return nil, c.Fail(c.UnsupportedCapability, "index source byte bound exceeded")
		}
		index.sources[doc.Path] = doc
		refs = append(refs, struct{ Path, Digest string }{doc.Path, doc.Digest})
		if !utf8.Valid(doc.Bytes) || bytes.IndexByte(doc.Bytes, 0) >= 0 {
			index.manifest.SkippedBinary++
			continue
		}
		index.manifest.IndexedDocuments++
		if len(doc.Bytes) == 0 {
			index.chunks = append(index.chunks, chunk{path: doc.Path, digest: doc.Digest, line: 1})
		}
		for start, line := 0, 1; start < len(doc.Bytes); {
			end := min(start+4096, len(doc.Bytes))
			for end < len(doc.Bytes) && !utf8.RuneStart(doc.Bytes[end]) {
				end--
			}
			if end < len(doc.Bytes) {
				if newline := bytes.LastIndexByte(doc.Bytes[start:end], '\n'); newline >= 1024 {
					end = start + newline + 1
				}
			}
			index.chunks = append(index.chunks, chunk{doc.Path, doc.Digest, start, end, line, string(doc.Bytes[start:end])})
			if len(index.chunks) > MaxChunks {
				return nil, c.Fail(c.UnsupportedCapability, "index chunk bound exceeded")
			}
			if end == len(doc.Bytes) {
				break
			}
			next := end - 512
			for next < end && !utf8.RuneStart(doc.Bytes[next]) {
				next++
			}
			line += bytes.Count(doc.Bytes[start:next], []byte("\n"))
			start = next
		}
	}
	var err error
	index.manifest.SourceSet, err = c.Digest(refs)
	if err != nil {
		return nil, boundedBuildError(ctx, err)
	}
	index.db, err = sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, boundedBuildError(ctx, err)
	}
	index.db.SetMaxOpenConns(1)
	success := false
	defer func() {
		if !success {
			index.db.Close()
		}
	}()
	for _, statement := range []string{
		"PRAGMA temp_store=MEMORY", "PRAGMA max_page_count=32768",
		"CREATE VIRTUAL TABLE lexical USING fts5(path, body, tokenize='unicode61')",
		"CREATE VIRTUAL TABLE substrings USING fts5(path, body, tokenize='trigram case_sensitive 1')",
	} {
		if _, err = index.db.ExecContext(ctx, statement); err != nil {
			return nil, c.Fail(c.UnsupportedCapability, "bounded FTS5/trigram index unavailable")
		}
	}
	tx, err := index.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, boundedBuildError(ctx, err)
	}
	defer tx.Rollback()
	for n, item := range index.chunks {
		for _, table := range []string{"lexical", "substrings"} {
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+"(rowid,path,body) VALUES(?,?,?)", n+1, item.path, item.text); err != nil {
				return nil, boundedBuildError(ctx, err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, boundedBuildError(ctx, err)
	}
	var pages, size int64
	if err = index.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return nil, boundedBuildError(ctx, err)
	}
	if err = index.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return nil, boundedBuildError(ctx, err)
	}
	index.manifest.SQLiteBytes = pages * size
	index.manifest.Chunks = len(index.chunks)
	index.manifest.BuildMicros = time.Since(started).Microseconds()
	success = true
	return index, nil
}
func (index *Index) Close() error       { return index.db.Close() }
func (index *Index) Manifest() Manifest { return index.manifest }
func boundedBuildError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var sqliteError *sqlite.Error
	if errors.As(err, &sqliteError) && (sqliteError.Code()&255 == 7 || sqliteError.Code()&255 == 13) {
		return c.Fail(c.UnsupportedCapability, "bounded SQLite memory/page capacity exhausted; use literal search")
	}
	return err
}
func phrase(query string) string { return `"` + strings.ReplaceAll(query, `"`, `""`) + `"` }
func terms(query string) string {
	words := strings.FieldsFunc(query, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r >= 128 || r == '_')
	})
	parts := []string{}
	for _, word := range words {
		if len(parts) == 16 {
			break
		}
		parts = append(parts, phrase(word))
	}
	return strings.Join(parts, " OR ")
}
func (index *Index) rows(ctx context.Context, table, query string) ([]int, error) {
	if query == "" {
		return nil, nil
	}
	rows, err := index.db.QueryContext(ctx, fmt.Sprintf("SELECT rowid FROM %s WHERE %s MATCH ? ORDER BY bm25(%s, 2.0, 1.0), rowid LIMIT 512", table, table, table), query)
	if err != nil {
		return nil, boundedBuildError(ctx, err)
	}
	defer rows.Close()
	ids := []int{}
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			return nil, boundedBuildError(ctx, err)
		}
		if id < 1 || id > len(index.chunks) {
			return nil, c.Fail(c.StoreIntegrityError, "invalid index reference")
		}
		ids = append(ids, id-1)
	}
	return ids, rows.Err()
}
