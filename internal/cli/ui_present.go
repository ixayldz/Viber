package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
	"github.com/ixayldz/Viber/internal/tui"
)

// This is presentation only. Approval still submits all of the immutable
// request bindings to the owner, which revalidates them before recording it.
func (u uiSession) respond(ctx context.Context, requestID, decision string) (string, bool, error) {
	raw, err := u.call(ctx, "requests", nil)
	if err != nil {
		return "", false, err
	}
	var requests []agent.UserRequest
	if err = c.DecodeStrict(raw, &requests); err != nil {
		return "", false, err
	}
	var selected *agent.UserRequest
	for i := range requests {
		request := &requests[i]
		if requestID != "" && request.ID != requestID || request.Status != "PENDING" {
			continue
		}
		if selected != nil {
			return renderRequests(requests), false, c.Fail(c.InvalidArgument, "several proposals are pending; choose an exact ID with /"+decision+" REQUEST_ID")
		}
		selected = request
	}
	if selected == nil {
		return "No current proposal was selected. Use /requests to review pending proposals.", false, c.Fail(c.StaleRequest, "no matching pending request")
	}
	id, err := resolveCommandID("")
	if err != nil {
		return "", false, err
	}
	if _, err = u.call(ctx, "respond", agent.ResponseTemplate(*selected, id, decision)); err != nil {
		return "", false, err
	}
	verb := "Approved"
	if decision == "reject" {
		verb = "Rejected"
	}
	return fmt.Sprintf("%s %s.\nUse /resume to continue with this decision.", verb, selected.ID), false, nil
}

func taskNextAction(state c.TaskState, active bool) string {
	if state.InputBarrier {
		return "Next action: /revise to apply your steering as a scope revision, then /resume."
	}
	if active || state.Execution == c.Running || state.Execution == c.Verifying || state.Execution == c.Delivering || state.Execution == c.Pausing || state.Execution == c.Recovering {
		return "Next action: work is in progress. /plan shows the steps; /pause preserves progress."
	}
	switch state.Execution {
	case c.WaitingUser:
		return "Next action: /requests to review the proposal, /approve or /reject, then /resume."
	case c.WaitingResource, c.Blocked:
		return "Next action: review the blocker and /budget. Resume after resolving it; unknown effects require explicit reconciliation."
	case c.Terminated:
		if state.Outcome == c.Finished {
			return "Next action: /diff to review changes, /preview to check conflicts, /export to save the result."
		}
		return "Next action: task ended. /diff shows retained changes; /status explains the outcome."
	default:
		return "Next action: /resume to continue; /plan and /diff show saved progress."
	}
}

func safeDisplayText(value string, limit int) string {
	if limit > 0 && utf8.RuneCountInString(value) > limit {
		runes := []rune(value)
		value = string(runes[:limit]) + "… [truncated]"
	}
	return tui.Safe(value)
}

func renderPRDCoverage(coverage *c.PRDCoverage) string {
	if coverage == nil {
		return ""
	}
	readStatus := coverage.ReadStatus
	if readStatus == "" {
		readStatus = "UNKNOWN"
	}
	sectionStatus := coverage.SectionStatus
	if sectionStatus == "" {
		sectionStatus = "UNKNOWN"
	}
	planStatus := coverage.Status
	if planStatus == "" {
		planStatus = "UNKNOWN"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "PRD source read: %d/%d bytes (%s); section extraction: %s; covered source sections: %d\n", coverage.ReadBytes, coverage.ReadTotalBytes, safeDisplayText(readStatus, 64), safeDisplayText(sectionStatus, 64), len(coverage.CoveredSectionIDs))
	fmt.Fprintf(&out, "PRD plan coverage: %d/%d required requirement IDs; plan state: %s (planning only)\n", len(coverage.CoveredRequirementIDs), len(coverage.RequiredRequirementIDs), safeDisplayText(planStatus, 64))
	return out.String()
}

func planProgress(view owner.View) (complete, total int, currentID, currentGoal, currentStatus string) {
	if view.Plan == nil {
		return 0, 0, "", "", ""
	}
	total = len(view.Plan.Definition.Nodes)
	statuses := make(map[string]string, len(view.Plan.Progress))
	for _, progress := range view.Plan.Progress {
		statuses[progress.NodeID] = progress.Status
		if progress.Status == "IMPLEMENTED" {
			complete++
		}
	}
	if active := view.Plan.Active(); active != nil {
		return complete, total, active.ID, active.Goal, "RUNNING"
	}
	var nextID string
	var nextGoal string
	for _, node := range view.Plan.Definition.Nodes {
		if statuses[node.ID] != "PENDING" {
			continue
		}
		ready := true
		for _, edge := range view.Plan.Definition.Relations {
			if edge.Kind == "DEPENDS_ON" && edge.From == node.ID && statuses[edge.To] != "IMPLEMENTED" {
				ready = false
				break
			}
		}
		if ready && (nextID == "" || node.ID < nextID) {
			nextID, nextGoal = node.ID, node.Goal
		}
	}
	if nextID != "" {
		return complete, total, nextID, nextGoal, statuses[nextID]
	}
	if complete == total {
		return complete, total, "", "", "NONE_PENDING"
	}
	return complete, total, "", "", "BLOCKED"
}

func renderCheckSummary(checks []agent.CheckSummary, quality c.Quality) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Verification quality: %s\n", safeDisplayText(string(quality), 64))
	if len(checks) == 0 {
		out.WriteString("Checks: no registered check summaries are available; quality remains the separate verdict above.\n")
		return out.String()
	}
	for _, check := range checks {
		freshness := "stale"
		if check.Current {
			freshness = "current candidate"
		}
		fmt.Fprintf(&out, "Check %s: execution %s · goal verification %s · %s\n", safeDisplayText(check.CheckID, 128), safeDisplayText(check.Outcome, 64), safeDisplayText(check.Verification, 64), freshness)
	}
	return out.String()
}

func renderTaskStatus(view owner.View) string {
	return renderTaskStatusDetails(view, nil, false)
}

func renderTaskStatusDetails(view owner.View, changeCount *int, includeChangeCount bool) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Task: %s\nGoal: %s\nState: %s", safeDisplayText(view.State.TaskID, 128), safeDisplayText(view.Goal, 320), safeDisplayText(string(view.State.Execution), 64))
	if view.State.Outcome != "" {
		fmt.Fprintf(&out, " · %s", safeDisplayText(string(view.State.Outcome), 64))
	}
	fmt.Fprintf(&out, "\nTask quality: %s · Goal fulfillment: %s\n", safeDisplayText(string(view.State.Quality), 64), safeDisplayText(string(view.State.Fulfillment), 64))
	out.WriteString(renderPRDCoverage(view.PRDCoverage))
	if view.Plan != nil {
		complete, total, id, goal, status := planProgress(view)
		fmt.Fprintf(&out, "Plan work products: %d/%d recorded", complete, total)
		if id != "" {
			fmt.Fprintf(&out, "\nCurrent plan step: %s [%s] · %s", safeDisplayText(id, 128), safeDisplayText(status, 32), safeDisplayText(goal, 240))
		} else if status == "BLOCKED" {
			out.WriteString("\nCurrent plan step: none schedulable")
		} else {
			out.WriteString("\nCurrent plan step: none pending")
		}
		out.WriteString("\nIMPLEMENTED records a plan work product; it does not establish verification or PRD completion.\n")
	}
	if view.Blocker != "" {
		fmt.Fprintf(&out, "Blocker: %s\n", safeDisplayText(view.Blocker, 320))
	}
	fmt.Fprintf(&out, "Budget: %d/%d turns · %d/%d tool calls\n", view.Budget.Steps, view.Budget.MaxSteps, view.Budget.ToolCalls, view.Budget.MaxToolCalls)
	out.WriteString(renderCheckSummary(view.Checks, view.State.Quality))
	if includeChangeCount {
		if changeCount != nil {
			fmt.Fprintf(&out, "Candidate changes: %d files in the isolated candidate; approval does not write live project files.\n", *changeCount)
		} else {
			out.WriteString("Candidate change count: unavailable from this response; use /diff to inspect it.\n")
		}
	}
	out.WriteString(taskNextAction(view.State, view.Detached))
	return out.String()
}

func renderTaskStatusWithChangeCount(view owner.View, count *int) string {
	return renderTaskStatusDetails(view, count, true)
}

func (u uiSession) renderStatus(ctx context.Context, view owner.View) string {
	raw, err := u.call(ctx, "diff-text", nil)
	if err != nil {
		return renderTaskStatusWithChangeCount(view, nil)
	}
	var diff owner.DiffText
	if err = c.DecodeStrict(raw, &diff); err != nil {
		return renderTaskStatusWithChangeCount(view, nil)
	}
	count := len(diff.Changes)
	return renderTaskStatusWithChangeCount(view, &count)
}

func renderRequests(requests []agent.UserRequest, views ...owner.View) string {
	var out strings.Builder
	out.WriteString("Approval records a proposal for the isolated candidate. It does not write live project files; apply is a separate delivery action.\n")
	if len(views) > 0 {
		out.WriteString(renderCheckSummary(views[0].Checks, views[0].State.Quality))
		if views[0].State.Quality == c.Unverified {
			out.WriteString("The candidate has no VERIFIED quality verdict. A successful process exit alone is not goal verification.\n")
		}
	} else {
		out.WriteString("Current check details are unavailable here; use /status before approving. A proposal is not verification.\n")
	}
	pending := 0
	for _, request := range requests {
		if request.Status != "PENDING" {
			continue
		}
		pending++
		fmt.Fprintf(&out, "%d. %s · %s\n", pending, safeDisplayText(request.ID, 128), safeDisplayText(request.Kind, 64))
		if request.Kind == "LIMITED_UNVERIFIED_DELIVERY" {
			out.WriteString("   Accept a review package without independent verification. This does not mean tests passed.\n")
		}
		if request.Action != nil {
			fmt.Fprintf(&out, "   Proposed tool: %s\n", safeDisplayText(request.Action.Name, 128))
			if request.Action.Name == "candidate_propose" {
				var proposal struct {
					ReadSet []c.ReadCondition `json:"read_set"`
					Changes []c.Change        `json:"changes"`
				}
				if c.DecodeStrict(request.Action.Arguments, &proposal) == nil {
					remaining := 8192
					fmt.Fprintf(&out, "   Pending proposal: %d changed files\n", len(proposal.Changes))
					for _, change := range proposal.Changes {
						kind := "modify"
						if change.Delete {
							kind = "delete"
						} else if change.BeforeDigest == "" {
							kind = "add"
						}
						contentKind := "text"
						if !utf8.Valid(change.After) || bytes.ContainsRune(change.After, 0) {
							contentKind = "binary"
						}
						fmt.Fprintf(&out, "   %s %s (%s; %d proposed bytes)\n", kind, safeDisplayText(change.Path, 512), contentKind, len(change.After))
						if change.Delete || !utf8.Valid(change.After) || bytes.ContainsRune(change.After, 0) {
							continue
						}
						n := min(len(change.After), min(2048, remaining))
						for n > 0 && !utf8.Valid(change.After[:n]) {
							n--
						}
						if n > 0 {
							out.WriteString("   Proposed content (model-authored):\n")
							for _, line := range strings.Split(string(change.After[:n]), "\n") {
								fmt.Fprintf(&out, "     %s\n", safeDisplayText(line, 2048))
							}
							remaining -= n
						}
						if n < len(change.After) {
							out.WriteString("   [Preview truncated; inspect the complete proposal with /requests json or the CLI requests command.]\n")
						}
					}
				}
			}
		}
		fmt.Fprintf(&out, "   /approve %s   or   /reject %s\n", safeDisplayText(request.ID, 128), safeDisplayText(request.ID, 128))
	}
	if pending == 0 {
		return "No proposals are waiting for your decision. Use /status to see the next step."
	}
	if pending == 1 {
		out.WriteString("\nAfter reviewing, /approve or /reject selects this single proposal. Then /resume continues the task.")
	}
	return out.String()
}

type diffFileStats struct {
	Added    int
	Removed  int
	Present  bool
	HasHunks bool
	Binary   bool
}

func patchToken(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if value[0] != '"' {
		if token, rest, found := strings.Cut(value, " "); found {
			return token, strings.TrimSpace(rest)
		}
		return value, ""
	}
	escaped := false
	for index := 1; index < len(value); index++ {
		if escaped {
			escaped = false
			continue
		}
		if value[index] == '\\' {
			escaped = true
			continue
		}
		if value[index] == '"' {
			quoted := value[:index+1]
			token, err := strconv.Unquote(quoted)
			if err != nil {
				return quoted[1 : len(quoted)-1], strings.TrimSpace(value[index+1:])
			}
			return token, strings.TrimSpace(value[index+1:])
		}
	}
	return value, ""
}

func patchPath(value string) string {
	if value == "/dev/null" {
		return ""
	}
	value = strings.TrimPrefix(value, "a/")
	value = strings.TrimPrefix(value, "b/")
	return value
}

func collectDiffStats(diff owner.DiffText) map[string]diffFileStats {
	stats := make(map[string]diffFileStats, len(diff.Changes))
	current := ""
	inHunk := false
	for _, line := range strings.Split(diff.Patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			left, remaining := patchToken(strings.TrimPrefix(line, "diff --git "))
			right, _ := patchToken(remaining)
			path := patchPath(right)
			if path == "" {
				path = patchPath(left)
			}
			current = path
			inHunk = false
			if current != "" {
				stat := stats[current]
				stat.Present = true
				stats[current] = stat
			}
		case !inHunk && strings.HasPrefix(line, "--- "):
			path, _ := patchToken(strings.TrimPrefix(line, "--- "))
			if path = patchPath(path); path != "" {
				current = path
			}
		case !inHunk && strings.HasPrefix(line, "+++ "):
			path, _ := patchToken(strings.TrimPrefix(line, "+++ "))
			if path = patchPath(path); path != "" {
				current = path
			}
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
			if current != "" {
				stat := stats[current]
				stat.Binary = true
				stats[current] = stat
			}
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			inHunk = current != ""
			if inHunk {
				stat := stats[current]
				stat.Present, stat.HasHunks = true, true
				stats[current] = stat
			}
		case inHunk && strings.HasPrefix(line, "+"):
			stat := stats[current]
			stat.Added++
			stats[current] = stat
		case inHunk && strings.HasPrefix(line, "-"):
			stat := stats[current]
			stat.Removed++
			stats[current] = stat
		}
	}
	return stats
}

func renderDiff(diff owner.DiffText) string {
	if len(diff.Changes) == 0 {
		return "No candidate changes have been applied yet. /requests shows pending proposals; /plan shows the work plan."
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d candidate changes · isolated candidate only; live project files are preserved\n", len(diff.Changes))
	stats := collectDiffStats(diff)
	for _, change := range diff.Changes {
		kind := "modified"
		if change.Deleted {
			kind = "deleted"
		} else if change.Before == "" {
			kind = "added"
		}
		fmt.Fprintf(&out, "  %s  %s", kind, safeDisplayText(change.Path, 512))
		if stat, ok := stats[change.Path]; ok {
			switch {
			case stat.Binary:
				out.WriteString(" · binary content; line counts unavailable")
			case stat.HasHunks:
				fmt.Fprintf(&out, " · +%d / -%d lines", stat.Added, stat.Removed)
				if diff.Truncated {
					out.WriteString(" (visible portion; patch truncated)")
				}
			case stat.Present && !diff.Truncated:
				out.WriteString(" · +0 / -0 lines (mode or metadata only)")
			default:
				out.WriteString(" · line counts unavailable (binary or omitted from text patch)")
			}
		} else if diff.Truncated {
			out.WriteString(" · line counts unavailable (patch truncated before this file)")
		} else {
			out.WriteString(" · line counts unavailable (binary or omitted from text patch)")
		}
		out.WriteByte('\n')
	}
	if diff.Patch != "" {
		out.WriteString("\n")
		// The bounded patch contains candidate-authored source text. Escape
		// terminal controls on display while retaining its line structure.
		for index, line := range strings.Split(diff.Patch, "\n") {
			if index > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(tui.Safe(line))
		}
	}
	if diff.Truncated || !diff.PatchComplete {
		if diff.Truncated {
			out.WriteString("\n[The bounded text patch is truncated. /export saves the complete patch and exact before/after files.]\n")
		} else {
			out.WriteString("\n[Some file contents are not represented in the text patch. /export saves exact before/after files.]\n")
		}
	}
	out.WriteString("\nNext: /preview checks for conflicts with your current project; /export saves a review package.")
	return out.String()
}

func (u uiSession) deliveryOutput(kind string) (string, error) {
	id, err := onboardingID()
	if err != nil {
		return "", err
	}
	// The store's parent exists. The owner's delivery gate verifies that the
	// fresh sibling destination is disjoint from both source and managed store.
	return filepath.Join(filepath.Dir(u.directory), "viber-"+kind+"-"+strings.TrimPrefix(id, "task-")), nil
}

func (u uiSession) liveStreamPage(ctx context.Context, invocation string, after int64) (owner.StreamPage, error) {
	read := func(cursor int64) (owner.StreamPage, error) {
		raw, err := u.call(ctx, "stream-page", owner.StreamPageQuery{After: cursor, Limit: 16})
		var page owner.StreamPage
		if err == nil {
			err = c.DecodeStrict(raw, &page)
		}
		return page, err
	}
	page, err := read(after)
	if err != nil {
		// A new invocation may have fewer deltas than the old cursor. Retry
		// only this read-only observation from zero; never retry inference.
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.StaleBase {
			return page, err
		}
		page, err = read(0)
	} else if page.InvocationID != invocation && after != 0 {
		page, err = read(0)
	}
	if err == nil && page.ResyncRequired {
		// The stream is a bounded ephemeral window. Resume at its oldest
		// retained delta and keep the gap explicit in the displayed response.
		page, err = read(max(0, page.EarliestDeltaSeq-1))
		page.ResyncRequired = true
	}
	return page, err
}
