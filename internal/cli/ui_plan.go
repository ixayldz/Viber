package cli

import (
	"fmt"
	"strings"

	"github.com/ixayldz/Viber/internal/owner"
)

func renderWorkPlan(view owner.View) string {
	var out strings.Builder
	out.WriteString(renderPRDCoverage(view.PRDCoverage))
	if view.Plan == nil {
		return out.String() + "No work plan has been proposed yet. Use /resume to continue.\nUse /requests to review proposed changes and /diff to inspect the current candidate."
	}
	statuses := make(map[string]string, len(view.Plan.Progress))
	for _, progress := range view.Plan.Progress {
		statuses[progress.NodeID] = progress.Status
	}
	complete, total, currentID, currentGoal, currentStatus := planProgress(view)
	fmt.Fprintf(&out, "Plan: %s · %d/%d work products recorded\n", safeDisplayText(view.Plan.Definition.ID, 128), complete, total)
	if currentID != "" {
		fmt.Fprintf(&out, "Current step: %s [%s] · %s\n", safeDisplayText(currentID, 128), safeDisplayText(currentStatus, 32), safeDisplayText(currentGoal, 240))
	}
	for index, node := range view.Plan.Definition.Nodes {
		status := statuses[node.ID]
		if status == "" {
			status = "PENDING"
		}
		fmt.Fprintf(&out, "%d. [%s] %s\n   %s\n", index+1, safeDisplayText(status, 32), safeDisplayText(node.ID, 128), safeDisplayText(node.Goal, 240))
		if len(node.RequiredChecks) > 0 {
			checks := make([]string, 0, len(node.RequiredChecks))
			for _, id := range node.RequiredChecks {
				checks = append(checks, safeDisplayText(id, 128))
			}
			fmt.Fprintf(&out, "   Required checks: %s\n", strings.Join(checks, ", "))
		}
	}
	out.WriteString("\nSource byte coverage, structural plan coverage, and check verification are separate signals. IMPLEMENTED records a work product; it does not mean PRD completion or passing checks. See /status and /verification for current verification.")
	return out.String()
}
