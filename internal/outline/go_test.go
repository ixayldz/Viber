package outline

import (
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestGoASTBindsGroupedGenericUnicodeAndMethodDeclarationsToActualBytes(t *testing.T) {
	source := "package example\r\n" +
		"import _ \"unavailable.example/never-loaded\"\r\n" +
		"//line /foreign/source.go:900\r\n" +
		"type Box[T any] struct { Value T }\r\n" +
		"var (\r\n first, δεύτερο = 1, 2\r\n _ = `func fake() {}`\r\n)\r\n" +
		"const ( Third = 3; Fourth = 4 )\r\n" +
		"// func hidden() {}\r\n" +
		"func Transform[T any](x T) T { type Local struct{}; return x }\r\n" +
		"func (b Box[T]) Read() T { return b.Value }\r\n"
	result, err := Extract("src/outline.go", []byte(source))
	want := []struct{ name, kind string }{{"Box", "type"}, {"first", "var"}, {"δεύτερο", "var"}, {"Third", "const"}, {"Fourth", "const"}, {"Transform", "func"}, {"Read", "method"}}
	if err != nil || result.Language != "go" || result.Extractor != "GO_AST_V1" || result.Coverage != "UNKNOWN_SEMANTIC_RESOLUTION" || result.SyntaxCoverage != "PARSED_PACKAGE_DECLARATIONS_ONLY" || len(result.Symbols) != len(want) {
		t.Fatal(result, err)
	}
	for i, symbol := range result.Symbols {
		if symbol.Name != want[i].name || symbol.Kind != want[i].kind || source[symbol.Start:symbol.End] != symbol.Name || symbol.Line != strings.Count(source[:symbol.Start], "\n")+1 || !c.ValidDigest(symbol.ID) {
			t.Fatal(symbol)
		}
	}
	// A line directive cannot move source spans or attribute host file names.
	if result.Path != "src/outline.go" || result.Symbols[0].Line != 4 {
		t.Fatal("line directive changed original captured binding", result)
	}
	changed, err := Extract("src/outline.go", []byte("\n"+source))
	if err != nil || changed.SourceDigest == result.SourceDigest || changed.Symbols[0].ID == result.Symbols[0].ID {
		t.Fatal("Go AST reused a stale source identity", changed, err)
	}
}

func TestGoASTRejectsMalformedSourceAndParserWorkExhaustion(t *testing.T) {
	cases := []struct {
		name, source string
		code         c.Code
	}{
		{"syntax", "package x; func broken( {", c.UnsupportedCapability},
		{"literal", "package x; var a = \"unterminated", c.UnsupportedCapability},
		{"symbols", "package x\n" + strings.Repeat("var n = 0\n", 4097), c.BudgetLimitReached},
		{"tokens", "package x; var a = []int{" + strings.Repeat("0,", goTokenLimit) + "}", c.BudgetLimitReached},
		{"nesting", "package x; var a = " + strings.Repeat("(", goNestingLimit+1) + "0" + strings.Repeat(")", goNestingLimit+1), c.BudgetLimitReached},
		{"unary", "package x; var a = " + strings.Repeat("!", goNestingLimit+1) + "true", c.BudgetLimitReached},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Extract("captured.go", []byte(test.source))
			if err == nil {
				t.Fatal("invalid or unbounded Go source accepted")
			}
			if typed, ok := err.(*c.Error); !ok || typed.Code != test.code {
				t.Fatal("unexpected rejection", err)
			}
		})
	}
}

func FuzzGoASTSourceBoundDeclarations(f *testing.F) {
	for _, source := range []string{"package example\nfunc Example() {}\n", "package p; type Box[T any] struct{ Value T }; var x = 1", "package p\n//line /foreign.go:99\nvar café = 1\n", "package p; var _ = `func fake() {}`", "package p; func broken( {"} {
		f.Add([]byte(source))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 2<<20 {
			return
		}
		result, err := Extract("captured.go", raw)
		if err != nil {
			return
		}
		if result.SourceDigest != c.HashBytes(raw) || result.Extractor != "GO_AST_V1" || result.Coverage != "UNKNOWN_SEMANTIC_RESOLUTION" || len(result.Symbols) > 4096 {
			t.Fatal("parser manufactured source/resolution coverage", result)
		}
		for _, symbol := range result.Symbols {
			if symbol.Start < 0 || symbol.End < symbol.Start || symbol.End > len(raw) || string(raw[symbol.Start:symbol.End]) != symbol.Name || !c.ValidDigest(symbol.ID) || symbol.Line != strings.Count(string(raw[:symbol.Start]), "\n")+1 {
				t.Fatal("parser manufactured a declaration byte span", symbol)
			}
		}
	})
}
