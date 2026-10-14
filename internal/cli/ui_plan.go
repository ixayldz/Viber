package cli

import (
	"fmt"
	"strings"

	"github.com/ixayldz/Viber/internal/owner"
)

func renderWorkPlan(view owner.View) string {
	if view.Plan == nil {
		return "No work plan has been proposed yet. Ask the model to read your PRD and propose an implementation plan, then use /resume.\nUse /requests to review proposed changes and /diff to inspect their contents."
	}
	statuses := make(map[string]string, len(view.Plan.Progress))
	for _, progress := range view.Plan.Progress {
		statuses[progress.NodeID] = progress.Status
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Work plan: %s\n", view.Plan.Definition.ID)
	for index, node := range view.Plan.Definition.Nodes {
		status := statuses[node.ID]
		if status == "" {
			status = "PENDING"
		}
		fmt.Fprintf(&out, "%d. [%s] %s\n   %s\n", index+1, status, node.ID, node.Goal)
		if len(node.RequiredChecks) > 0 {
			fmt.Fprintf(&out, "   Required checks: %s\n", strings.Join(node.RequiredChecks, ", "))
		}
	}
	out.WriteString("\nIMPLEMENTED means a work product was recorded. See /verification for check results.")
	return out.String()
}
