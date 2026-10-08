// Package outline emits source-bound lexical hints. It is deliberately not a
// semantic resolver: unsupported syntax and hidden declarations remain UNKNOWN.
package outline

import (
	"bytes"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

type Symbol struct {
	ID    string `json:"symbol_id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Start int    `json:"name_byte_start"`
	End   int    `json:"name_byte_end"`
	Line  int    `json:"line"`
}
type Result struct {
	SchemaVersion int      `json:"schema_version"`
	Path          string   `json:"path"`
	SourceDigest  string   `json:"source_digest"`
	Language      string   `json:"language"`
	Extractor     string   `json:"extractor"`
	Coverage      string   `json:"semantic_coverage"`
	Symbols       []Symbol `json:"symbols"`
}

var pythonDecl = regexp.MustCompile(`(?m)^[ \t]*(?:async[ \t]+)?(def|class)[ \t]+([a-zA-Z_][a-zA-Z_0-9]*)`)
var scriptDecl = regexp.MustCompile(`(?m)^[ \t]*(?:(?:export|default|declare|abstract|async)[ \t]+)*(function|class|interface|type|enum|const|let|var)[ \t]+([a-zA-Z_$][a-zA-Z_$0-9]*)`)

// mask keeps byte positions and newlines. It suppresses strings and comments;
// template substitutions, regex literals, decorators and dynamic declarations
// do not gain semantic resolution authority.
func mask(raw []byte, python bool) []byte {
	out := append([]byte(nil), raw...)
	blank := func(start, end int) {
		for k := start; k < end; k++ {
			if raw[k] != '\n' && raw[k] != '\r' {
				out[k] = ' '
			}
		}
	}
	for i := 0; i < len(raw); {
		start := i
		if python && raw[i] == '#' || !python && i+1 < len(raw) && raw[i] == '/' && raw[i+1] == '/' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			blank(start, i)
			continue
		}
		if !python && i+1 < len(raw) && raw[i] == '/' && raw[i+1] == '*' {
			i += 2
			for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
				i++
			}
			if i+1 < len(raw) {
				i += 2
			} else {
				i = len(raw)
			}
			blank(start, i)
			continue
		}
		q := raw[i]
		if q == '\'' || q == '"' || !python && q == 96 {
			triple := python && i+2 < len(raw) && raw[i+1] == q && raw[i+2] == q
			width := 1
			if triple {
				width = 3
			}
			i += width
			for i < len(raw) {
				if raw[i] == '\\' {
					i = min(i+2, len(raw))
					continue
				}
				if raw[i] == q && (!triple || i+2 < len(raw) && raw[i+1] == q && raw[i+2] == q) {
					i += width
					break
				}
				i++
			}
			blank(start, i)
			continue
		}
		i++
	}
	return out
}
func Extract(name string, raw []byte) (Result, error) {
	result := Result{SchemaVersion: 1, Path: name, SourceDigest: c.HashBytes(raw), Extractor: "LEXICAL_V1", Coverage: "UNKNOWN_SEMANTIC_RESOLUTION", Symbols: []Symbol{}}
	if !policy.SafePath(name) || len(raw) > 2<<20 || !utf8.Valid(raw) {
		return result, c.Fail(c.UnsupportedCapability, "outline requires captured UTF-8 source within 2 MiB")
	}
	var re *regexp.Regexp
	switch strings.ToLower(path.Ext(name)) {
	case ".py":
		result.Language = "python"
		re = pythonDecl
	case ".js", ".jsx", ".mjs", ".cjs":
		result.Language = "javascript"
		re = scriptDecl
	case ".ts", ".tsx", ".mts", ".cts":
		result.Language = "typescript"
		re = scriptDecl
	default:
		result.Language = "unsupported"
		return result, nil
	}
	matches := re.FindAllSubmatchIndex(mask(raw, result.Language == "python"), 4097)
	if len(matches) > 4096 {
		return result, c.Fail(c.BudgetLimitReached, "outline symbol quota exceeded")
	}
	for _, m := range matches {
		kind := string(raw[m[2]:m[3]])
		start, end := m[4], m[5]
		symbolName := string(raw[start:end])
		id, _ := c.Digest(struct {
			Path, Source, Kind, Name string
			Start, End               int
		}{name, result.SourceDigest, kind, symbolName, start, end})
		result.Symbols = append(result.Symbols, Symbol{ID: id, Name: symbolName, Kind: kind, Start: start, End: end, Line: bytes.Count(raw[:start], []byte{'\n'}) + 1})
	}
	return result, nil
}
