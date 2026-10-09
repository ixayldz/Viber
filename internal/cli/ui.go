package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
	"github.com/ixayldz/Viber/internal/tui"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

var uiInput io.Reader = os.Stdin

func runUI(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("ui", errout)
	directory := f.String("store", "", "existing store")
	line := f.Bool("line", false, "accessible line mode without terminal controls")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" || *directory == "" || f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "ui requires task/store"), false)
	}
	ctx := context.Background()
	if _, err := startBackgroundOwner(ctx, *directory); err != nil {
		return report(out, errout, err, false)
	}
	u := uiSession{task, *directory}
	view, err := u.status(ctx)
	if err != nil {
		return report(out, errout, err, false)
	}
	terminal := false
	restore := func() {}
	if !*line && uiInput == os.Stdin {
		restore, terminal, err = tui.Terminal(os.Stdin)
		if err != nil {
			return report(out, errout, err, false)
		}
	}
	defer restore()
	if !terminal {
		return runUILines(u, uiInput, out, errout)
	}
	fmt.Fprint(out, "\x1b[?1049h\x1b[?2004h\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\x1b[?2004l\x1b[?1049l")
	return runUILive(u, view, uiInput, out)
}
func runUILines(u uiSession, input io.Reader, out, errout io.Writer) int {
	fmt.Fprintln(out, "Viber connected. /help lists commands. /quit detaches the UI.")
	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 4096), 65537)
	for scan.Scan() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		result, quit, err := u.command(ctx, scan.Text())
		cancel()
		if err != nil {
			fmt.Fprintln(errout, tui.Safe(err.Error()))
		} else {
			fmt.Fprintln(out, tui.Safe(result))
		}
		if quit {
			return 0
		}
	}
	if err := scan.Err(); err != nil {
		return report(out, errout, c.Fail(c.InvalidArgument, "UI input exceeded bounded UTF-8 line contract"), false)
	}
	return 0
}
func screen(view owner.View, notice, details string) tui.Screen {
	work := "awaiting command"
	if view.Plan != nil {
		raw, _ := json.Marshal(view.Plan)
		work = string(raw)
	}
	decision := view.Blocker
	if view.State.InputBarrier {
		decision = "raw steering awaits explicit scope revision"
	}
	if view.State.OpenRequiredObligations > 0 {
		decision += fmt.Sprintf(" | %d required obligations", view.State.OpenRequiredObligations)
	}
	budget := fmt.Sprintf("input %d + reserved %d / %d; output %d + reserved %d / %d", view.Budget.UsedInput, view.Budget.ReservedInput, view.Budget.MaxInputTokens, view.Budget.UsedOutput, view.Budget.ReservedOutput, view.Budget.MaxOutputTokens)
	return tui.Screen{Task: view.State.TaskID, State: string(view.State.Execution), Candidate: view.State.CandidateDigest, Quality: string(view.State.Quality), Fulfillment: string(view.State.Fulfillment), Work: work, Decision: decision, Budget: budget, Notice: notice, Details: details}
}

type uiResult struct {
	view *owner.View
	text string
	quit bool
	err  error
}

func runUILive(u uiSession, view owner.View, input io.Reader, out io.Writer) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keys := make(chan byte, 1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 256)
		for {
			n, err := input.Read(buffer)
			for _, b := range buffer[:n] {
				select {
				case keys <- b:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	results := make(chan uiResult, 2)
	editor := tui.Editor{}
	utf := []byte{}
	notice := "Connected; /quit detaches."
	details := ""
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	busy, polling := false, false
	interrupts := 0
	draw := func() {
		w, h := tui.Size()
		fmt.Fprint(out, "\x1b[H\x1b[2J"+tui.Render(screen(view, notice, details), editor, w, h))
	}
	dispatch := func(text string) {
		busy = true
		go func() {
			callCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			defer stop()
			result, quit, err := u.command(callCtx, text)
			select {
			case results <- uiResult{text: result, quit: quit, err: err}:
			case <-ctx.Done():
			}
		}()
	}
	draw()
	for {
		select {
		case <-done:
			return 0
		case b := <-keys:
			action := editor.Feed(b)
			switch action.Kind {
			case "quit":
				return 0
			case "interrupt":
				if busy {
					notice = "Control is settling; current task state remains visible."
					break
				}
				interrupts++
				if interrupts == 1 {
					dispatch("/pause")
				} else {
					dispatch("/cancel")
				}
			case "submit":
				if busy {
					editor.Text = []rune(action.Text)
					editor.Cursor = len(editor.Text)
					notice = "Previous command is pending; prompt retained."
					break
				}
				interrupts = 0
				dispatch(action.Text)
			case "complete":
				if busy {
					break
				}
				token := action.Text
				i := strings.LastIndex(token, "@")
				if i < 0 {
					notice = "Use @ followed by a captured file name."
					break
				}
				query := strings.Trim(token[i+1:], "\"")
				busy = true
				go func() {
					callCtx, stop := context.WithTimeout(ctx, 10*time.Second)
					defer stop()
					raw, err := u.call(callCtx, "source-list", agent.Observation{Kind: "source-list", Query: query, Limit: 32})
					select {
					case results <- uiResult{text: string(raw), err: err}:
					case <-ctx.Done():
					}
				}()
			case "byte":
				utf = append(utf, b)
				if utf8.FullRune(utf) {
					editor.InsertUTF8(utf)
					utf = nil
				}
			}
			draw()
		case result := <-results:
			if result.view != nil {
				if result.err == nil {
					view = *result.view
				}
				polling = false
			} else {
				busy = false
				details = result.text
				if len(details) > 32768 {
					details = details[:32768]
				}
				notice = "Command settled."
			}
			if result.err != nil {
				notice = result.err.Error()
			}
			if result.quit {
				return 0
			}
			draw()
		case <-ticker.C:
			if !polling {
				polling = true
				go func() {
					callCtx, stop := context.WithTimeout(ctx, 10*time.Second)
					defer stop()
					v, err := u.status(callCtx)
					select {
					case results <- uiResult{view: &v, err: err}:
					case <-ctx.Done():
					}
				}()
			}
			draw()
		}
	}
}
