package cli

import (
	"fmt"
	"strings"

	"github.com/ixayldz/Viber/internal/owner"
)

func renderWorkPlan(view owner.View) string {
	var out strings.Builder
	if view.PRDCoverage != nil {
		fmt.Fprintf(&out, "PRD: %s · read %s (%d/%d bytes)\nSections: %s · %d/%d source criteria in the plan\n", view.PRDCoverage.Status, view.PRDCoverage.ReadStatus, view.PRDCoverage.ReadBytes, view.PRDCoverage.ReadTotalBytes, view.PRDCoverage.SectionStatus, len(view.PRDCoverage.CoveredRequirementIDs), len(view.PRDCoverage.RequiredRequirementIDs))
	}
	if view.Plan == nil {
		return out.String() + "No work plan has been proposed yet. The model must read the PRD before proposing its plan. Use /resume to continue.\nUse /requests to review proposed changes and /diff to inspect their contents."
	}
	statuses := make(map[string]string, len(view.Plan.Progress))
	for _, progress := range view.Plan.Progress {
		statuses[progress.NodeID] = progress.Status
	}
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
	out.WriteString("\nSource section coverage records reading and planning, not semantic completion. IMPLEMENTED means a work product was recorded. See /verification for check results.")
	return out.String()
}
