package cli

import (
	"bufio"
	"bytes"
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
	fmt.Fprintln(out, "Viber connected. / shows commands. /requests reviews proposals; /approve then /resume continues. /quit detaches the UI.")
	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 4096), 65537)
	for scan.Scan() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		result, quit, err := u.command(ctx, scan.Text())
		cancel()
		if result != "" {
			for _, line := range strings.Split(result, "\n") {
				fmt.Fprintln(out, tui.Safe(line))
			}
		}
		if err != nil {
			fmt.Fprintln(errout, tui.Safe(err.Error()))
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
	if details == "" && view.Summary != "" {
		details = "Assistant message (untrusted; see quality/checks separately):\n" + view.Summary
	}
	work := "awaiting command"
	if view.Plan != nil {
		if node := view.Plan.Active(); node != nil {
			work = node.Goal
		} else {
			work = fmt.Sprintf("%d planned steps; /plan for details", len(view.Plan.Progress))
		}
	}
	decision := taskNextAction(view.State, view.Detached)
	if view.Blocker != "" {
		decision = view.Blocker + " | " + decision
	}
	if view.State.InputBarrier {
		decision = "raw steering awaits explicit scope revision"
	}
	if view.State.OpenRequiredObligations > 0 {
		decision += fmt.Sprintf(" | %d required obligations", view.State.OpenRequiredObligations)
	}
	budget := fmt.Sprintf("steps %d/%d; calls %d/%d; input %d (+%d)/%d; output %d (+%d)/%d; active %d/%dms", view.Budget.Steps, view.Budget.MaxSteps, view.Budget.ToolCalls, view.Budget.MaxToolCalls, view.Budget.UsedInput, view.Budget.ReservedInput, view.Budget.MaxInputTokens, view.Budget.UsedOutput, view.Budget.ReservedOutput, view.Budget.MaxOutputTokens, view.Budget.ActiveMillis, view.Budget.MaxActiveMillis)
	return tui.Screen{Task: view.State.TaskID, State: string(view.State.Execution), Candidate: view.State.CandidateDigest, Quality: string(view.State.Quality), Fulfillment: string(view.State.Fulfillment), Work: work, Decision: decision, Budget: budget, Notice: notice, Details: details}
}

type uiResult struct {
	view   *owner.View
	stream *owner.StreamPage
	text   string
	quit   bool
	err    error
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
	notice := "Connected. Type / to browse commands; @file selects captured source."
	details := ""
	showStream := true
	streamText, streamInvocation := "", ""
	var streamCursor int64
	var shownRequestSequence int64 = -1
	scrollOffset := 0
	clearedSummary := ""
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	busy, polling := false, false
	interrupts := 0
	lastFrame := ""
	lastWidth, lastHeight, lastRow, lastColumn := 0, 0, 0, 0
	draw := func() {
		w, h := tui.Size()
		s := screen(view, notice, details)
		if details == "" && view.Summary == clearedSummary {
			s.Details = ""
		}
		s.ScrollOffset = scrollOffset
		row, column := tui.CursorPosition(s, editor, w, h)
		frame := tui.Render(s, editor, w, h)
		if frame == lastFrame && w == lastWidth && h == lastHeight && row == lastRow && column == lastColumn {
			return
		}
		lastFrame, lastWidth, lastHeight, lastRow, lastColumn = frame, w, h, row, column
		fmt.Fprint(out, "\x1b[?25l\x1b[H\x1b[2J"+frame)
		fmt.Fprintf(out, "\x1b[%d;%dH\x1b[?25h", row, column)
	}
	dispatch := func(text string) {
		showStream = strings.TrimSpace(text) == "/resume"
		if strings.TrimSpace(text) == "/clear" {
			details, clearedSummary, scrollOffset = "", view.Summary, 0
			notice = "Display cleared. Task history and source files are retained."
			return
		}
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
			case "scroll":
				_, height := tui.Size()
				step := max(1, height-10)
				body := screen(view, notice, details).Details
				if action.Text == "up" {
					scrollOffset = max(0, scrollOffset-step)
				} else {
					scrollOffset = min(strings.Count(body, "\n"), scrollOffset+step)
				}
			case "suggest":
				notice = "Complete the command, then press Enter to run. | " + action.Text
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
				if strings.TrimSpace(action.Text) == "" {
					break
				}
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
					if showStream && view.State.Execution != c.Terminated && result.view.State.Execution == c.Terminated {
						details = renderTaskStatus(*result.view)
						if result.view.Summary != "" {
							details += "\n\nAssistant summary (untrusted):\n" + result.view.Summary
						}
						scrollOffset = 0
					}
					view = *result.view
					if result.text != "" {
						details, scrollOffset = result.text, 0
						showStream = false
						shownRequestSequence = view.State.TaskSeq
					}
					if result.stream != nil {
						page := result.stream
						if page.InvocationID != streamInvocation || page.ResyncRequired {
							streamText = ""
							streamInvocation = page.InvocationID
							if page.ResyncRequired {
								streamText = "[Earlier live output is unavailable; task history and saved state remain authoritative.]\n"
							}
						}
						streamCursor = page.Next
						for _, delta := range page.Deltas {
							streamText += delta.Text
						}
						if len(streamText) > 16000 {
							streamText = "[Earlier live output clipped.]\n" + streamText[len(streamText)-16000:]
						}
						if showStream && streamText != "" && view.State.Execution != c.Terminated {
							details = "Assistant live output (untrusted; not check evidence):\n" + streamText
						}
					}
				}
				polling = false
			} else {
				busy = false
				details = result.text
				scrollOffset = 0
				if len(details) <= 32768 && json.Valid([]byte(details)) {
					var pretty bytes.Buffer
					if json.Indent(&pretty, []byte(details), "", "  ") == nil {
						details = pretty.String()
					}
				}
				if len(details) > 32768 {
					details = details[:32768] + "\n[Output clipped; use the CLI JSON command for the full bounded response.]"
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
			editor.FlushEscape()
			if !polling {
				polling = true
				previousRequestSequence := shownRequestSequence
				previousStreamCursor := streamCursor
				previousStreamInvocation := streamInvocation
				go func() {
					callCtx, stop := context.WithTimeout(ctx, 10*time.Second)
					defer stop()
					v, err := u.status(callCtx)
					var streamPage *owner.StreamPage
					if err == nil && (v.State.Execution == c.Running || v.State.Execution == c.Verifying) {
						if page, streamErr := u.liveStreamPage(callCtx, previousStreamInvocation, previousStreamCursor); streamErr == nil {
							streamPage = &page
						}
					}
					proposal := ""
					if err == nil && v.State.Execution == c.WaitingUser && previousRequestSequence != v.State.TaskSeq {
						// Show a newly pending proposal once. Polling must not
						// continually replace a user's /plan or /diff view.
						if raw, requestErr := u.call(callCtx, "requests", nil); requestErr == nil {
							var requests []agent.UserRequest
							if c.DecodeStrict(raw, &requests) == nil {
								proposal = renderRequests(requests)
								if len(proposal) > 32768 {
									proposal = proposal[:32768] + "\n[Preview clipped; use /requests json or the CLI requests command.]"
								}
							}
						}
					}
					select {
					case results <- uiResult{view: &v, stream: streamPage, text: proposal, err: err}:
					case <-ctx.Done():
					}
				}()
			}
			draw()
		}
	}
}
