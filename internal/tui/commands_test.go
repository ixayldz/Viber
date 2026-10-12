package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCommandPaletteCompletesWithoutExecutingAndIgnoresCRLF(t *testing.T) {
	e := Editor{}
	feed(&e, "/ca")
	actions := feed(&e, "\r\n")
	if len(actions) != 1 || actions[0].Kind != "suggest" || string(e.Text) != "/cancel" || len(e.History) != 0 {
		t.Fatal("completion must not execute cancellation, including CRLF terminals", e, actions)
	}
	actions = feed(&e, "\r\n")
	if len(actions) != 1 || actions[0].Kind != "submit" || actions[0].Text != "/cancel" {
		t.Fatal("a separate explicit Enter should submit exactly once", actions)
	}
	feed(&e, "/resp")
	actions = feed(&e, "\t")
	if len(actions) != 1 || actions[0].Kind != "suggest" || string(e.Text) != "/respond " || strings.Contains(string(e.Text), "REQUEST_ID") {
		t.Fatal("usage placeholders must not become an executable command", e, actions)
	}
}

func TestCommandPaletteNavigationAndEscapePreserveDraft(t *testing.T) {
	e := Editor{History: []string{"old prompt"}, HistoryAt: 1}
	feed(&e, "/\x1b[B")
	if e.MenuIndex != 1 || string(e.Text) != "/" || e.HistoryAt != 1 {
		t.Fatal("menu navigation should not recall history", e)
	}
	feed(&e, "\x1b")
	if !e.FlushEscape() || string(e.Text) != "/" || len(e.CommandMatches()) != 0 {
		t.Fatal("Escape should close the menu and preserve input", e)
	}
	feed(&e, "d")
	if len(e.CommandMatches()) == 0 || string(e.Text) != "/d" {
		t.Fatal("typing should reopen a filtered menu", e)
	}
	if len(feed(&e, "\x1b[200~\n/cancel\x03\x1b[201~")) != 0 || len(e.CommandMatches()) != 0 {
		t.Fatal("pasted commands/control bytes must not activate the menu or execute")
	}
}

func TestUnicodePromptViewportAndPaletteStayBounded(t *testing.T) {
	e := Editor{}
	e.InsertUTF8([]byte(strings.Repeat("Türkçe界", 30)))
	for _, size := range [][2]int{{20, 8}, {40, 9}, {120, 30}} {
		raw := Render(Screen{Notice: "Kaynak: \x1b]52;c;secret\a"}, e, size[0], size[1])
		if !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x1b\a") {
			t.Fatal("Unicode should remain intact and control data escaped", raw)
		}
		lines := strings.Split(strings.TrimSuffix(raw, "\r\n"), "\r\n")
		row, column := CursorPosition(Screen{Notice: "Kaynak: \x1b]52;c;secret\a"}, e, size[0], size[1])
		if len(lines) >= size[1] || row != len(lines) || column < 3 || column > size[0] {
			t.Fatal("cursor/viewport outside terminal", row, column, size)
		}
		for _, line := range lines {
			if cells(line) > size[0] {
				t.Fatal("terminal row exceeded viewport", line)
			}
		}
	}
	menu := Editor{Text: []rune("/"), Cursor: 1}
	if raw := Render(Screen{Details: strings.Repeat("detail\n", 100)}, menu, 40, 8); !strings.Contains(raw, "/help") {
		t.Fatal("palette must remain visible in a small terminal", raw)
	}
}

func TestOutputPagingPreservesDraftAndNeverExecutesPaste(t *testing.T) {
	e := Editor{}
	feed(&e, "unfinished prompt")
	for _, key := range []struct{ sequence, direction string }{{"\x1b[5~", "up"}, {"\x1b[6~", "down"}} {
		actions := feed(&e, key.sequence)
		if len(actions) != 1 || actions[0].Kind != "scroll" || actions[0].Text != key.direction || string(e.Text) != "unfinished prompt" {
			t.Fatal("paging should preserve the prompt", actions, e)
		}
	}
	if actions := feed(&e, "\x1b[200~\x1b[5~\x1b[6~\x1b[201~"); len(actions) != 0 {
		t.Fatal("pasted control sequences must not page or submit", actions)
	}
	body := "first result\nsecond result\nthird result"
	for _, page := range []struct {
		offset   int
		expected string
	}{{-100, "first result"}, {0, "first result"}, {1, "second result"}, {100, "third result"}} {
		raw := Render(Screen{Details: body, ScrollOffset: page.offset}, Editor{}, 120, 30)
		if !strings.Contains(raw, page.expected) || strings.Contains(raw, "\x1b") {
			t.Fatal("bounded output paging failed", page.offset, raw)
		}
	}
}
