// Package tui supplies a bounded terminal editor and renders escaped data.
// It has no store or host process authority.
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Editor struct {
	Text          []rune
	Cursor        int
	History       []string
	HistoryAt     int
	Paste         bool
	MenuIndex     int
	MenuDismissed bool
	sequence      []byte
	lastCR        bool
}
type Input struct {
	Kind string
	Text string
}

func (e *Editor) Feed(b byte) Input {
	if b == 10 && e.lastCR && !e.Paste {
		e.lastCR = false
		return Input{}
	}
	e.lastCR = b == 13 && !e.Paste
	if b == 27 {
		e.sequence = []byte{b}
		return Input{}
	}
	if len(e.sequence) > 0 {
		if len(e.sequence) == 1 && b != '[' && b != 'O' {
			e.FlushEscape()
			return e.Feed(b)
		}
		e.sequence = append(e.sequence, b)
		s := string(e.sequence)
		if s == "\x1b[200~" {
			e.Paste = true
			e.MenuDismissed = true
			e.sequence = nil
			return Input{}
		}
		if s == "\x1b[201~" {
			e.Paste = false
			e.sequence = nil
			return Input{}
		}
		if (s == "\x1b[5~" || s == "\x1b[6~") && !e.Paste {
			e.sequence = nil
			if s == "\x1b[5~" {
				return Input{Kind: "scroll", Text: "up"}
			}
			return Input{Kind: "scroll", Text: "down"}
		}
		if s == "\x1b[D" {
			e.Cursor = max(0, e.Cursor-1)
			e.sequence = nil
		}
		if s == "\x1b[C" {
			e.Cursor = min(len(e.Text), e.Cursor+1)
			e.sequence = nil
		}
		if s == "\x1b[H" {
			e.Cursor = 0
			e.sequence = nil
		}
		if s == "\x1b[F" {
			e.Cursor = len(e.Text)
			e.sequence = nil
		}
		if s == "\x1b[A" && !e.Paste {
			if matches := e.CommandMatches(); len(matches) > 0 {
				e.MenuIndex = (e.MenuIndex + len(matches) - 1) % len(matches)
				e.sequence = nil
				return Input{}
			}
			e.HistoryAt = max(0, e.HistoryAt-1)
			e.recall()
			e.sequence = nil
		}
		if s == "\x1b[B" && !e.Paste {
			if matches := e.CommandMatches(); len(matches) > 0 {
				e.MenuIndex = (e.MenuIndex + 1) % len(matches)
				e.sequence = nil
				return Input{}
			}
			e.HistoryAt = min(len(e.History), e.HistoryAt+1)
			e.recall()
			e.sequence = nil
		}
		if len(e.sequence) > 8 || len(e.sequence) >= 3 && b >= 64 && b <= 126 {
			e.sequence = nil
		}
		return Input{}
	}
	switch b {
	case 3:
		if !e.Paste {
			return Input{Kind: "interrupt"}
		}
	case 4:
		if len(e.Text) == 0 && !e.Paste {
			return Input{Kind: "quit"}
		}
	case 13, 10:
		if e.Paste {
			e.insert('\n')
			return Input{}
		}
		if matches := e.CommandMatches(); len(matches) > 0 && matches[e.MenuIndex%len(matches)].Name != string(e.Text) {
			command, _ := e.CompleteCommand()
			return Input{Kind: "suggest", Text: command.Name + " " + command.Arguments + " — " + command.Description}
		}
		text := string(e.Text)
		if text != "" {
			e.History = append(e.History, text)
			if len(e.History) > 32 {
				e.History = e.History[1:]
			}
		}
		e.HistoryAt = len(e.History)
		e.Text = nil
		e.Cursor = 0
		e.MenuDismissed, e.MenuIndex = false, 0
		return Input{Kind: "submit", Text: text}
	case 8, 127:
		if e.Cursor > 0 {
			e.Text = append(e.Text[:e.Cursor-1], e.Text[e.Cursor:]...)
			e.Cursor--
			e.MenuDismissed, e.MenuIndex = false, 0
		}
	case 9:
		if !e.Paste {
			if command, ok := e.CompleteCommand(); ok {
				return Input{Kind: "suggest", Text: command.Name + " " + command.Arguments + " — " + command.Description}
			}
			return Input{Kind: "complete", Text: string(e.Text)}
		}
	default:
		if b >= 32 {
			return Input{Kind: "byte", Text: string([]byte{b})}
		}
	}
	return Input{}
}
func (e *Editor) InsertUTF8(raw []byte) {
	for len(raw) > 0 {
		r, n := utf8.DecodeRune(raw)
		if r != utf8.RuneError || n > 1 {
			e.insert(r)
		}
		raw = raw[n:]
	}
}
func (e *Editor) insert(r rune) {
	if len(e.Text) >= 65536 {
		return
	}
	e.Text = append(e.Text, 0)
	copy(e.Text[e.Cursor+1:], e.Text[e.Cursor:])
	e.Text[e.Cursor] = r
	e.Cursor++
	if !e.Paste {
		e.MenuDismissed, e.MenuIndex = false, 0
	}
}
func (e *Editor) recall() {
	e.Text = nil
	if e.HistoryAt < len(e.History) {
		e.Text = []rune(e.History[e.HistoryAt])
	}
	e.Cursor = len(e.Text)
}
func Safe(s string) string {
	var result strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&result, "\\x%02x", s[0])
		} else if unicode.IsPrint(r) {
			result.WriteRune(r)
		} else {
			quoted := strconv.QuoteRune(r)
			result.WriteString(quoted[1 : len(quoted)-1])
		}
		s = s[size:]
	}
	return result.String()
}

type Screen struct {
	Task, State, Candidate, Quality, Fulfillment, Work, Decision, Budget, Notice, Details string
	ScrollOffset                                                                          int
}

func renderLines(s Screen, e Editor, width, height int) []string {
	width = max(20, min(width, 240))
	height = max(8, min(height, 120))
	lines := []string{
		fmt.Sprintf("Viber | Task %s | %s", Safe(s.Task), Safe(s.State)),
		"Work: " + Safe(s.Work),
		"Candidate: " + Safe(s.Candidate) + " | quality: " + Safe(s.Quality) + " | delivery: " + Safe(s.Fulfillment),
		"Decision: " + Safe(s.Decision),
		"Budget: " + Safe(s.Budget),
		"Notice: " + Safe(s.Notice),
	}
	if s.Details != "" {
		body := strings.Split(s.Details, "\n")
		start := min(max(0, s.ScrollOffset), max(0, len(body)-1))
		for _, line := range body[start:] {
			lines = append(lines, Safe(line))
		}
	}
	menu := []string{}
	if matches := e.CommandMatches(); len(matches) > 0 {
		selected := e.MenuIndex % len(matches)
		count := min(len(matches), max(1, min(6, height-6)))
		start := max(0, selected-count+1)
		menu = append(menu, "Commands | Up/Down select | Tab/Enter fill | Esc close")
		for index := start; index < min(len(matches), start+count); index++ {
			marker := "  "
			if index == selected {
				marker = "> "
			}
			menu = append(menu, marker+matches[index].Name+"  "+matches[index].Description)
		}
	}
	limit := max(0, height-3-len(menu))
	if len(lines) > limit {
		lines = lines[:limit]
	}
	lines = append(lines, menu...)
	prompt, _ := renderPrompt(e, width)
	lines = append(lines, "Type / for commands | PgUp/PgDn scroll | @file | Ctrl+C pause/cancel", prompt)
	for i, line := range lines {
		lines[i] = fitCells(line, width)
	}
	return lines
}

func Render(s Screen, e Editor, width, height int) string {
	return strings.Join(renderLines(s, e, width, height), "\r\n") + "\r\n"
}

func CursorPosition(s Screen, e Editor, width, height int) (int, int) {
	width = max(20, min(width, 240))
	_, column := renderPrompt(e, width)
	return len(renderLines(s, e, width, height)), column
}

func runeCells(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r >= 0x2329 && r <= 0x232a || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f000 && r <= 0x1faff || r >= 0x20000 && r <= 0x3ffff) {
		return 2
	}
	return 1
}

func cells(s string) int {
	count := 0
	for _, r := range s {
		count += runeCells(r)
	}
	return count
}

func fitCells(s string, width int) string {
	if cells(s) <= width {
		return s
	}
	var result strings.Builder
	used := 0
	for _, r := range s {
		if used+runeCells(r) > max(0, width-3) {
			break
		}
		result.WriteRune(r)
		used += runeCells(r)
	}
	return result.String() + "..."
}

func renderPrompt(e Editor, width int) (string, int) {
	position := max(0, min(e.Cursor, len(e.Text)))
	text := []rune(Safe(string(e.Text)))
	before := []rune(Safe(string(e.Text[:position])))
	start, left := 0, ""
	for cells(string(before[start:])) > width-7 && start < len(before) {
		start++
		left = "..."
	}
	visible := left + string(text[start:])
	column := 3 + len(left) + cells(string(before[start:]))
	return "> " + fitCells(visible, width-2), min(width, column)
}
