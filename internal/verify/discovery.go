package verify

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/discovery"
	"github.com/ixayldz/Viber/internal/workspace"
)

func HasTestChecks(plan CheckPlan) bool {
	for _, check := range plan.Checks {
		if check.Kind == "TEST" {
			return true
		}
	}
	return false
}

// RequireDiscoveredClosure ensures TEST check scopes include every currently
// observed protected test/config/manifest/support file. The discovery graph is
// only partial source evidence; caller-visible coverage stays partial and this
// check does not choose tests, resolve runtime packages, or grant verification.
func RequireDiscoveredClosure(plan CheckPlan, report discovery.Report, capture workspace.Capture) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := discovery.Validate(report, capture); err != nil {
		return err
	}
	return requireDiscoveredClosure(plan, report)
}

func requireDiscoveredClosure(plan CheckPlan, report discovery.Report) error {
	testChecks := 0
	for _, check := range plan.Checks {
		if check.Kind != "TEST" {
			continue
		}
		testChecks++
		for _, target := range discovery.AddDiscoveredClosureFiles(&report) {
			covered := false
			for _, scope := range check.Closure {
				if foldedContains(scope, target) {
					covered = true
					break
				}
			}
			if !covered {
				return c.Fail(c.PolicyDenied, "test check closure omits a source-discovered test/config dependency")
			}
		}
	}
	if testChecks == 0 {
		return c.Fail(c.InvalidArgument, "source-discovered closure requires a TEST check")
	}
	return nil
}

// DiscoveredClosureMissing returns paths the TEST scopes would omit. It is a
// display helper; admission must still call RequireDiscoveredClosure.
func DiscoveredClosureMissing(check CheckDefinition, report discovery.Report) []string {
	if check.Kind != "TEST" {
		return nil
	}
	missing := []string{}
	for _, target := range discovery.AddDiscoveredClosureFiles(&report) {
		covered := false
		for _, scope := range check.Closure {
			if foldedContains(scope, target) {
				covered = true
				break
			}
		}
		if !covered {
			missing = append(missing, target)
		}
	}
	return missing
}
