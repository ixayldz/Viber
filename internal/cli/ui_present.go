package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
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
		return "Next: /revise to apply your steering as a scope revision, then /resume."
	}
	if active || state.Execution == c.Running || state.Execution == c.Verifying || state.Execution == c.Delivering || state.Execution == c.Pausing || state.Execution == c.Recovering {
		return "Working. /plan shows the steps; /pause preserves progress."
	}
	switch state.Execution {
	case c.WaitingUser:
		return "Next: /requests to review the proposal, /approve or /reject, then /resume."
	case c.WaitingResource, c.Blocked:
		return "Next: review the blocker and /budget. Resume after resolving it; unknown effects require explicit reconciliation."
	case c.Terminated:
		if state.Outcome == c.Finished {
			return "Next: /diff to review changes, /preview to check conflicts, /export to save the result."
		}
		return "Task ended. /diff shows retained changes; /status explains the outcome."
	default:
		return "Next: /resume to continue; /plan and /diff show saved progress."
	}
}

func renderTaskStatus(view owner.View) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Task: %s\nState: %s", view.State.TaskID, view.State.Execution)
	if view.State.Outcome != "" {
		fmt.Fprintf(&out, " · %s", view.State.Outcome)
	}
	fmt.Fprintf(&out, "\nChecks: %s · Goal: %s\n", view.State.Quality, view.State.Fulfillment)
	if view.PRDCoverage != nil {
		fmt.Fprintf(&out, "PRD: %s · %d/%d required source criteria in the plan\n", view.PRDCoverage.Status, len(view.PRDCoverage.CoveredRequirementIDs), len(view.PRDCoverage.RequiredRequirementIDs))
	}
	if view.Plan != nil {
		complete := 0
		for _, node := range view.Plan.Progress {
			if node.Status == "IMPLEMENTED" {
				complete++
			}
		}
		fmt.Fprintf(&out, "Plan: %d/%d steps recorded", complete, len(view.Plan.Progress))
		if node := view.Plan.Active(); node != nil {
			fmt.Fprintf(&out, " · %s", node.Goal)
		}
		out.WriteByte('\n')
	}
	if view.Blocker != "" {
		fmt.Fprintf(&out, "Blocker: %s\n", view.Blocker)
	}
	fmt.Fprintf(&out, "Budget: %d/%d turns · %d/%d tool calls\n", view.Budget.Steps, view.Budget.MaxSteps, view.Budget.ToolCalls, view.Budget.MaxToolCalls)
	for _, check := range view.Checks {
		fmt.Fprintf(&out, "Check %s: %s (current: %t; verification: %s)\n", check.CheckID, check.Outcome, check.Current, check.Verification)
	}
	if len(view.Checks) == 0 {
		out.WriteString("No check results recorded. An unverified result is a review package.\n")
	}
	out.WriteString(taskNextAction(view.State, view.Detached))
	return out.String()
}

func renderRequests(requests []agent.UserRequest) string {
	var out strings.Builder
	pending := 0
	for _, request := range requests {
		if request.Status != "PENDING" {
			continue
		}
		pending++
		fmt.Fprintf(&out, "%d. %s · %s\n", pending, request.ID, request.Kind)
		if request.Kind == "LIMITED_UNVERIFIED_DELIVERY" {
			out.WriteString("   Accept a review package without independent verification. This does not mean tests passed.\n")
		}
		if request.Action != nil {
			fmt.Fprintf(&out, "   Proposed tool: %s\n", request.Action.Name)
			if request.Action.Name == "candidate_propose" {
				var proposal struct {
					ReadSet []c.ReadCondition `json:"read_set"`
					Changes []c.Change        `json:"changes"`
				}
				if c.DecodeStrict(request.Action.Arguments, &proposal) == nil {
					remaining := 8192
					for _, change := range proposal.Changes {
						kind := "modify"
						if change.Delete {
							kind = "delete"
						} else if change.BeforeDigest == "" {
							kind = "add"
						}
						fmt.Fprintf(&out, "   %s %s (%d proposed bytes)\n", kind, change.Path, len(change.After))
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
								fmt.Fprintf(&out, "     %s\n", line)
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
		fmt.Fprintf(&out, "   /approve %s   or   /reject %s\n", request.ID, request.ID)
	}
	if pending == 0 {
		return "No proposals are waiting for your decision. Use /status to see the next step."
	}
	if pending == 1 {
		out.WriteString("\nAfter reviewing, /approve or /reject selects this single proposal. Then /resume continues the task.")
	}
	return out.String()
}

func renderDiff(diff owner.DiffText) string {
	if len(diff.Changes) == 0 {
		return "No candidate changes have been applied yet. /requests shows pending proposals; /plan shows the work plan."
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d candidate changes · original project files are preserved\n", len(diff.Changes))
	for _, change := range diff.Changes {
		kind := "modified"
		if change.Deleted {
			kind = "deleted"
		} else if change.Before == "" {
			kind = "added"
		}
		fmt.Fprintf(&out, "  %s  %s\n", kind, change.Path)
	}
	if diff.Patch != "" {
		out.WriteString("\n")
		out.WriteString(diff.Patch)
	}
	if diff.Truncated || !diff.PatchComplete {
		out.WriteString("\n[Some changes are omitted from this text view. /export saves the complete patch and exact before/after files.]\n")
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
