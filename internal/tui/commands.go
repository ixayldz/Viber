package tui

import "strings"

// Command is presentation only. Execution remains in the owner-backed CLI.
type Command struct {
	Name, Arguments, Description string
}

var slashCommands = []Command{
	{"/help", "", "Show commands and keyboard shortcuts"},
	{"/status", "", "See task progress and pending decisions"},
	{"/diff", "", "Review proposed file changes"},
	{"/export", "[NEW_DIRECTORY]", "Save the patch and changed files; choose a folder automatically if omitted"},
	{"/preview", "[NEW_DIRECTORY]", "Preview merging changes with your current project files"},
	{"/plan", "", "See the current work plan"},
	{"/requests", "[json]", "Review proposals waiting for your decision"},
	{"/approve", "[REQUEST_ID]", "Approve a reviewed proposal; omit the ID when only one is pending"},
	{"/reject", "[REQUEST_ID]", "Reject a proposal; omit the ID when only one is pending"},
	{"/respond", "REQUEST_ID approve|reject", "Approve or reject a reviewed request"},
	{"/resume", "", "Continue the task in the background"},
	{"/pause", "", "Pause work and preserve the task"},
	{"/cancel", "", "Cancel work; keep history and source files"},
	{"/model", "[MODEL_ID]", "See or change the task's model"},
	{"/read", "PATH", "Read a captured project file"},
	{"/context", "", "See retained context and source pins"},
	{"/compact", "[KEEP_RECENT]", "Compact conversation while retaining task obligations"},
	{"/clear", "", "Clear displayed output; retain task history"},
	{"/memory", "[suggest|list|search QUERY|record KIND KEY TEXT|correct ID DIGEST KIND KEY TEXT|retire ID DIGEST|copy SOURCE_TASK ITEM_ID DIGEST]", "Suggest current source-bound project memory; copy only an explicitly selected entry"},
	{"/workers", "[inventory,requirements|inventory,search QUERY]", "Run 2–4 budgeted read-only source analyzers"},
	{"/why", "", "Explain what was included in model context"},
	{"/evidence", "", "Inspect independent check evidence"},
	{"/verification", "", "See the current verification verdict"},
	{"/budget", "", "See token and resource budgets"},
	{"/runtime", "", "See the model and execution profile"},
	{"/queue", "[add TEXT|remove ID|activate ID]", "Queue steering for this task"},
	{"/goal", "[add CHILD_ID TEXT|cancel ID|activate ID|recover ID]", "Manage a separate child goal"},
	{"/revise", "[FRESH_FIXTURE]", "Review a pending scope revision"},
	{"/capacity", "[replenish|retire-operations]", "Inspect disk capacity and supported maintenance"},
	{"/retention", "[keep REV|expires UTC REV CONSENT|run-due]", "Manage explicit content retention"},
	{"/trace", "", "Read recent task events"},
	{"/quit", "", "Detach the interface; background work continues"},
	{"/exit", "", "Detach the interface; background work continues"},
	{"/detach", "", "Detach the interface; background work continues"},
}

func CommandHelp() string {
	lines := []string{"Commands — type / to browse; Up/Down selects, Tab completes, Esc closes."}
	for _, command := range slashCommands {
		usage := command.Name
		if command.Arguments != "" {
			usage += " " + command.Arguments
		}
		lines = append(lines, usage+" — "+command.Description)
	}
	return strings.Join(lines, "\n")
}

func (e Editor) CommandMatches() []Command {
	if e.Paste || e.MenuDismissed || e.Cursor != len(e.Text) {
		return nil
	}
	text := string(e.Text)
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \t\r\n") {
		return nil
	}
	matches := []Command{}
	for _, command := range slashCommands {
		if strings.HasPrefix(command.Name, text) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (e *Editor) CompleteCommand() (Command, bool) {
	matches := e.CommandMatches()
	if len(matches) == 0 {
		return Command{}, false
	}
	command := matches[e.MenuIndex%len(matches)]
	text := command.Name
	if command.Arguments != "" {
		text += " "
	}
	e.Text, e.Cursor = []rune(text), len([]rune(text))
	e.MenuIndex, e.MenuDismissed = 0, true
	return command, true
}

func (e *Editor) FlushEscape() bool {
	if string(e.sequence) != "\x1b" {
		return false
	}
	e.sequence = nil
	e.MenuDismissed = true
	return true
}
