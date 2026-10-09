// Package tui supplies a bounded terminal editor and renders escaped data.
// It has no store or host process authority.
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Editor struct {
	Text      []rune
	Cursor    int
	History   []string
	HistoryAt int
	Paste     bool
	sequence  []byte
}
type Input struct {
	Kind string
	Text string
}

func (e *Editor) Feed(b byte) Input {
	if b == 27 {
		e.sequence = []byte{b}
		return Input{}
	}
	if len(e.sequence) > 0 {
		e.sequence = append(e.sequence, b)
		s := string(e.sequence)
		if s == "\x1b[200~" {
			e.Paste = true
			e.sequence = nil
			return Input{}
		}
		if s == "\x1b[201~" {
			e.Paste = false
			e.sequence = nil
			return Input{}
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
			e.HistoryAt = max(0, e.HistoryAt-1)
			e.recall()
			e.sequence = nil
		}
		if s == "\x1b[B" && !e.Paste {
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
		return Input{Kind: "submit", Text: text}
	case 8, 127:
		if e.Cursor > 0 {
			e.Text = append(e.Text[:e.Cursor-1], e.Text[e.Cursor:]...)
			e.Cursor--
		}
	case 9:
		if !e.Paste {
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
}
func (e *Editor) recall() {
	e.Text = nil
	if e.HistoryAt < len(e.History) {
		e.Text = []rune(e.History[e.HistoryAt])
	}
	e.Cursor = len(e.Text)
}
func Safe(s string) string { q := strconv.QuoteToASCII(s); return q[1 : len(q)-1] }

type Screen struct{ Task, State, Candidate, Quality, Fulfillment, Work, Decision, Budget, Notice, Details string }

func Render(s Screen, e Editor, width, height int) string {
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
		for _, line := range strings.Split(s.Details, "\n") {
			lines = append(lines, Safe(line))
		}
	}
	if len(lines) > height-3 {
		lines = lines[:height-3]
	}
	lines = append(lines, "/help  /status /diff /plan /pause /resume /queue /model /context /evidence /quit", "> "+Safe(string(e.Text)))
	for i, line := range lines {
		if len(line) > width {
			lines[i] = line[:width-3] + "..."
		}
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}
