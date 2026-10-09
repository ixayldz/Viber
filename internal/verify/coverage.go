package verify

import (
	c "github.com/ixayldz/Viber/internal/contracts"
)

const CoverageAcknowledgement = "RAW_GOALS_AND_EXECUTION_DEPENDENCIES_REVIEWED_V1"

type GoalMapping struct {
	RequirementID string   `json:"requirement_id"`
	CheckIDs      []string `json:"check_ids"`
}
type GoalReview struct {
	SchemaVersion    int           `json:"schema_version"`
	InputDigests     []string      `json:"input_digests"`
	Coverage         []GoalMapping `json:"coverage"`
	DependencyChecks []string      `json:"dependency_checks"`
	Acknowledgement  string        `json:"acknowledgement"`
}
type ReviewedCoverage struct {
	SchemaVersion  int        `json:"schema_version"`
	Actor          string     `json:"actor"`
	Spec           c.TaskSpec `json:"reviewed_spec"`
	CheckSetDigest string     `json:"check_set_digest"`
	Review         GoalReview `json:"review"`
}

func ReviewCoverage(spec c.TaskSpec, plan CheckPlan, checkSetDigest string, review GoalReview) (ReviewedCoverage, error) {
	record := ReviewedCoverage{SchemaVersion: 1, Actor: "USER", Spec: spec, CheckSetDigest: checkSetDigest, Review: review}
	if err := ValidateCoverage(record, plan, checkSetDigest); err != nil {
		return ReviewedCoverage{}, err
	}
	// Own all arrays: callers cannot edit admitted expectation/coverage by alias.
	raw, err := c.CanonicalV1(record)
	if err != nil {
		return ReviewedCoverage{}, err
	}
	var owned ReviewedCoverage
	err = c.DecodeStrict(raw, &owned)
	return owned, err
}
func ValidateCoverage(record ReviewedCoverage, plan CheckPlan, checkSetDigest string) error {
	spec, review := record.Spec, record.Review
	if record.SchemaVersion != 1 || record.Actor != "USER" || spec.Validate() != nil || plan.Validate() != nil || !c.ValidDigest(checkSetDigest) || record.CheckSetDigest != checkSetDigest || review.SchemaVersion != 1 || review.Acknowledgement != CoverageAcknowledgement || len(review.InputDigests) != len(spec.Inputs) || len(review.Coverage) > 128 || len(review.DependencyChecks) != len(plan.Checks) {
		return c.Fail(c.PolicyDenied, "explicit source-bound goal and dependency review required")
	}
	for i, input := range spec.Inputs {
		if review.InputDigests[i] != input.Digest {
			return c.Fail(c.StaleRequest, "goal review raw input changed")
		}
	}
	checks := map[string]CheckDefinition{}
	for _, check := range plan.Checks {
		checks[check.ID] = check
	}
	dependencies := map[string]bool{}
	for _, id := range review.DependencyChecks {
		if _, ok := checks[id]; !ok || dependencies[id] {
			return c.Fail(c.PolicyDenied, "dependency review must cover every protected check once")
		}
		dependencies[id] = true
	}
	requirements := map[string]c.Requirement{}
	for _, req := range spec.Requirements {
		requirements[req.ID] = req
	}
	mapped := map[string]bool{}
	for _, mapping := range review.Coverage {
		req, ok := requirements[mapping.RequirementID]
		if !ok || mapped[req.ID] || len(mapping.CheckIDs) == 0 || !uniqueStrings(mapping.CheckIDs, 128, true) {
			return c.Fail(c.PolicyDenied, "invalid or duplicate goal mapping")
		}
		mapped[req.ID] = true
		for _, id := range mapping.CheckIDs {
			check, ok := checks[id]
			covered := false
			for _, reqID := range check.RequirementIDs {
				covered = covered || reqID == req.ID
			}
			if !ok || !covered {
				return c.Fail(c.PolicyDenied, "review check does not cover required raw goal")
			}
		}
	}
	for _, req := range spec.Requirements {
		if req.Required && !mapped[req.ID] {
			return c.Fail(c.PolicyDenied, "required raw goal missing from review")
		}
	}
	return nil
}
func CoverageCurrent(record *ReviewedCoverage, spec c.TaskSpec, checkSetDigest string) bool {
	if record == nil || record.CheckSetDigest != checkSetDigest {
		return false
	}
	a, err := c.Digest(record.Spec)
	b, err2 := c.Digest(spec)
	return err == nil && err2 == nil && a == b
}
