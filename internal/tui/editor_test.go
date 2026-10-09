package tui

import (
	"strings"
	"testing"
)

func feed(e *Editor, text string) []Input {
	results := []Input{}
	for _, b := range []byte(text) {
		r := e.Feed(b)
		if r.Kind == "byte" {
			e.InsertUTF8([]byte{b})
		} else if r.Kind != "" {
			results = append(results, r)
		}
	}
	return results
}
func TestPasteNeverExecutesNewlinesOrInterrupt(t *testing.T) {
	e := Editor{}
	actions := feed(&e, "\x1b[200~/cancel\n/quit\x03\x1b[201~")
	if len(actions) != 0 || string(e.Text) != "/cancel\n/quit" || e.Paste {
		t.Fatal(e, actions)
	}
	actions = feed(&e, "\r")
	if len(actions) != 1 || actions[0].Kind != "submit" || actions[0].Text != "/cancel\n/quit" {
		t.Fatal(actions)
	}
}
func TestKeyboardEditingHistoryAndUnknownSequence(t *testing.T) {
	e := Editor{}
	feed(&e, "ac\x1b[Db\x1b[C\r")
	if len(e.History) != 1 || e.History[0] != "abc" {
		t.Fatal(e)
	}
	feed(&e, "\x1b[A\x1b[F\x7f")
	if string(e.Text) != "ab" {
		t.Fatal(e)
	}
	feed(&e, "\x1b[3~x")
	if string(e.Text) != "abx" {
		t.Fatal("unknown CSI swallowed input", e)
	}
}
func TestRenderingEscapesOSCAndBoundsViewport(t *testing.T) {
	s := Screen{Task: "\x1b]52;c;secret\a", Details: strings.Repeat("details\n", 10000)}
	raw := Render(s, Editor{Text: []rune("input\x1b[2J")}, 40, 9)
	if strings.ContainsAny(raw, "\x1b\a") || len(strings.Split(strings.TrimSpace(raw), "\n")) > 9 || !strings.Contains(raw, "\\\\x1b") && !strings.Contains(raw, "\\x1b") {
		t.Fatal("unsafe/unbounded render", raw)
	}
}
