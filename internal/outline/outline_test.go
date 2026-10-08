package outline

import (
	"strings"
	"testing"
)

func TestLexicalOutlineSuppressesCommentsStringsAndBindsExactByteSpans(t *testing.T) {
	cases := []struct {
		name, source string
		want         []string
	}{
		{"sample.py", "# def fake():\r\ntext = '''\r\ndef hidden():\r\n'''\r\n# é UTF-8\r\nasync def visible(x):\r\n    class Inner:\r\n        pass\r\n", []string{"visible", "Inner"}},
		{"sample.ts", "/*\nexport class Fake {}\n*/\nconst text = __BACKTICK__\nfunction hidden() {}\n__BACKTICK__;\n// interface Nope {}\nexport async function visible() {}\nexport interface Shape {}\n", []string{"text", "visible", "Shape"}},
		{"sample.jsx", "export default class Component {}\nconst arrow = () => 1;\n", []string{"Component", "arrow"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.source = strings.ReplaceAll(tc.source, "__BACKTICK__", string(rune(96)))
			result, err := Extract(tc.name, []byte(tc.source))
			if err != nil || len(result.Symbols) != len(tc.want) || result.Coverage != "UNKNOWN_SEMANTIC_RESOLUTION" {
				t.Fatal(result, err)
			}
			for i, symbol := range result.Symbols {
				if symbol.Name != tc.want[i] || tc.source[symbol.Start:symbol.End] != symbol.Name || symbol.ID == "" || symbol.Line < 1 {
					t.Fatal(symbol)
				}
			}
			changed, err := Extract(tc.name, []byte(" "+tc.source))
			if err != nil || changed.SourceDigest == result.SourceDigest || changed.Symbols[0].ID == result.Symbols[0].ID {
				t.Fatal("stale identity", err)
			}
		})
	}
}
func TestUnsupportedAndHostileInputsCannotClaimResolution(t *testing.T) {
	result, err := Extract("other.rs", []byte("fn declaration(){}"))
	if err != nil || result.Language != "unsupported" || len(result.Symbols) != 0 {
		t.Fatal(result, err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{{"../unsafe.py", []byte("def x(): pass")}, {"invalid.ts", []byte{0xff}}, {"large.py", []byte(strings.Repeat("x", 2<<20+1))}, {"many.py", []byte(strings.Repeat("def x(): pass\n", 4097))}} {
		if _, err = Extract(tc.name, tc.raw); err == nil {
			t.Fatal("hostile input accepted", tc.name)
		}
	}
}
