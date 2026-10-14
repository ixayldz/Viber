package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/owner"
	"github.com/ixayldz/Viber/internal/plan"
	"github.com/ixayldz/Viber/internal/tui"
)

func TestUIStatusSeparatesReadPlanAndVerificationCoverage(t *testing.T) {
	view := owner.View{
		Goal: "Implement a safe status view",
		State: c.TaskState{
			TaskID: "status-task", Execution: c.WaitingUser, Quality: c.Unverified,
			Fulfillment: c.FulfillmentPending,
		},
		PRDCoverage: &c.PRDCoverage{
			Status: "PLAN_COMPLETE", SectionStatus: "REVIEW_REQUIRED",
			ReadStatus: "PARTIAL", ReadBytes: 40, ReadTotalBytes: 100,
			RequiredRequirementIDs: []string{"r1", "r2", "r3"},
			CoveredRequirementIDs:  []string{"r1", "r2", "r3"},
			CoveredSectionIDs:      []string{"overview"},
		},
		Plan: &plan.State{
			Definition: plan.Definition{ID: "plan-1", Nodes: []plan.Node{
				{ID: "step-a", Goal: "Inspect existing checks"},
				{ID: "step-b", Goal: "Implement the status display", RequiredChecks: []string{"unit"}},
			}},
			Progress: []plan.Progress{
				{NodeID: "step-a", Status: "IMPLEMENTED"},
				{NodeID: "step-b", Status: "RUNNING"},
			},
		},
		Checks: []agent.CheckSummary{
			{CheckID: "unit", Current: true, Outcome: "EXIT_ZERO", Verification: "UNKNOWN"},
			{CheckID: "observer", Current: false, Outcome: "EXIT_ZERO", Verification: "PASS"},
		},
	}
	count := 2
	status := renderTaskStatusWithChangeCount(view, &count)
	for _, want := range []string{
		"Goal: Implement a safe status view",
		"PRD source read: 40/100 bytes (PARTIAL)",
		"section extraction: REVIEW_REQUIRED",
		"PRD plan coverage: 3/3 required requirement IDs",
		"PLAN_COMPLETE (planning only)",
		"Current plan step: step-b [RUNNING]",
		"Verification quality: UNVERIFIED",
		"execution EXIT_ZERO · goal verification UNKNOWN · current candidate",
		"execution EXIT_ZERO · goal verification PASS · stale",
		"Candidate changes: 2 files in the isolated candidate",
		"Next action:",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}
	if strings.Contains(strings.ToLower(status), "prd complete") {
		t.Fatalf("review-required source was rendered as complete:\n%s", status)
	}

	planText := renderWorkPlan(view)
	for _, want := range []string{"40/100 bytes", "3/3 required requirement IDs", "Current step: step-b", "Source byte coverage, structural plan coverage, and check verification are separate signals"} {
		if !strings.Contains(planText, want) {
			t.Fatalf("work plan missing %q:\n%s", want, planText)
		}
	}
}

func TestUIStatusAndReviewTextEscapeTerminalControls(t *testing.T) {
	view := owner.View{
		Goal:   "Build this\x1b[2J\nspoofed status",
		State:  c.TaskState{TaskID: "hostile-ui", Execution: c.WaitingUser, Quality: c.Unverified},
		Checks: []agent.CheckSummary{{CheckID: "unit", Outcome: "NOT_RUN", Verification: "UNKNOWN"}},
	}
	status := renderTaskStatus(view)
	frame := tui.Render(tui.Screen{Details: status}, tui.Editor{}, 100, 30)
	if strings.ContainsRune(frame, '\x1b') || !strings.Contains(frame, `\x1b[2J\nspoofed status`) {
		t.Fatalf("task goal control text was not escaped in terminal output: %q", frame)
	}

	action, _ := json.Marshal(struct {
		ReadSet []c.ReadCondition `json:"read_set"`
		Changes []c.Change        `json:"changes"`
	}{ReadSet: []c.ReadCondition{}, Changes: []c.Change{{Path: "new.go", After: []byte("safe\n\x1b[2J")}}})
	requests := []agent.UserRequest{{
		ID: "request-1", Kind: "CANDIDATE_CHANGE", Status: "PENDING",
		Action: &model.Call{ID: "proposal-1", Name: "candidate_propose", Arguments: action},
	}}
	review := renderRequests(requests, view)
	if strings.ContainsRune(review, '\x1b') || !strings.Contains(review, "new.go") || !strings.Contains(review, `\x1b[2J`) || !strings.Contains(review, "isolated candidate") || !strings.Contains(review, "no VERIFIED quality verdict") {
		t.Fatalf("proposal review omitted safe candidate/verification context: %q", review)
	}
}

func TestUIDiffShowsPerFileStatsAndTruncation(t *testing.T) {
	diff := owner.DiffText{
		Changes: []owner.DiffChange{
			{Path: "app.go", Before: "old", After: "new"},
			{Path: "image.bin", Before: "before", After: "after"},
			{Path: "tail.txt", Before: "before", After: "after"},
		},
		Patch: strings.Join([]string{
			`diff --git "a/app.go" "b/app.go"`,
			`--- "a/app.go"`,
			`+++ "b/app.go"`,
			`@@ -1 +1,2 @@`,
			`-old`,
			`+new`,
			`+extra`,
			"+hostile\x1b[2J",
			`diff --git "a/image.bin" "b/image.bin"`,
			`Binary files a/image.bin and b/image.bin differ`,
		}, "\n"),
		PatchComplete: false,
		Truncated:     true,
	}
	text := renderDiff(diff)
	for _, want := range []string{
		"app.go · +3 / -1 lines (visible portion; patch truncated)",
		"image.bin · binary content; line counts unavailable",
		"tail.txt · line counts unavailable (patch truncated before this file)",
		"bounded text patch is truncated",
		"/export saves a review package",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("diff summary missing %q:\n%s", want, text)
		}
	}
	if strings.ContainsRune(text, '\x1b') || !strings.Contains(text, `+hostile\x1b[2J`) {
		t.Fatalf("candidate diff text was not escaped for terminal output: %q", text)
	}
}
