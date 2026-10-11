package outline

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// Bound parser work independently of source bytes: a small source can contain
// many tiny AST nodes. Comments, strings and //line directives are never
// declarations or authority over the original byte offsets.
const goTokenLimit = 65536
const goNestingLimit = 256

func extractGo(result Result, raw []byte) (Result, error) {
	result.Language, result.Extractor = "go", "GO_AST_V1"
	result.SyntaxCoverage = "PARSED_PACKAGE_DECLARATIONS_ONLY"
	files := token.NewFileSet()
	var lexical scanner.Scanner
	lexical.Init(files.AddFile(result.Path, -1, len(raw)), raw, nil, 0)
	depth, operators := 0, 0
	for count := 0; ; count++ {
		_, kind, _ := lexical.Scan()
		if kind == token.EOF {
			break
		}
		if count >= goTokenLimit {
			return result, c.Fail(c.BudgetLimitReached, "Go outline parser token quota exceeded")
		}
		switch kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		}
		switch kind {
		case token.ADD, token.SUB, token.NOT, token.XOR, token.MUL, token.AND, token.ARROW:
			operators++
		default:
			operators = 0
		}
		if depth > goNestingLimit || operators > goNestingLimit {
			return result, c.Fail(c.BudgetLimitReached, "Go outline parser nesting quota exceeded")
		}
		if depth < 0 {
			return result, c.Fail(c.UnsupportedCapability, "Go outline source has invalid syntax")
		}
	}
	if lexical.ErrorCount != 0 {
		return result, c.Fail(c.UnsupportedCapability, "Go outline source has invalid syntax")
	}
	// Source is supplied explicitly. ParseFile cannot open result.Path, load
	// imports, execute generators, or resolve identifiers into host packages.
	files = token.NewFileSet()
	// No diagnostic list is exposed. The default parser error cap stops malformed
	// input early instead of allocating an AllErrors list for the whole source.
	parsed, err := parser.ParseFile(files, result.Path, raw, parser.SkipObjectResolution)
	if err != nil {
		return result, c.Fail(c.UnsupportedCapability, "Go outline source has invalid syntax")
	}
	line, scanned := 1, 0
	add := func(name *ast.Ident, kind string) error {
		if name == nil || name.Name == "_" {
			return nil
		}
		if len(result.Symbols) >= 4096 {
			return c.Fail(c.BudgetLimitReached, "outline symbol quota exceeded")
		}
		file := files.File(name.Pos())
		if file == nil {
			return c.Fail(c.StoreIntegrityError, "Go outline declaration has no source file")
		}
		start, end := file.Offset(name.Pos()), file.Offset(name.End())
		if start < scanned || end < start || end > len(raw) || string(raw[start:end]) != name.Name {
			return c.Fail(c.StoreIntegrityError, "Go outline declaration byte binding invalid")
		}
		// Declarations are emitted in source order. Count each prefix byte at
		// most once, rather than rescanning a long file for every symbol.
		line += bytes.Count(raw[scanned:start], []byte{'\n'})
		scanned = start
		id, digestErr := c.Digest(struct {
			Path, Source, Extractor, Kind, Name string
			Start, End                          int
		}{result.Path, result.SourceDigest, result.Extractor, kind, name.Name, start, end})
		if digestErr != nil {
			return digestErr
		}
		result.Symbols = append(result.Symbols, Symbol{ID: id, Name: name.Name, Kind: kind, Start: start, End: end, Line: line})
		return nil
	}
	for _, declaration := range parsed.Decls {
		switch node := declaration.(type) {
		case *ast.FuncDecl:
			kind := "func"
			if node.Recv != nil {
				kind = "method"
			}
			err = add(node.Name, kind)
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					err = add(spec.Name, "type")
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						if err = add(name, node.Tok.String()); err != nil {
							return result, err
						}
					}
				}
				if err != nil {
					return result, err
				}
			}
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
