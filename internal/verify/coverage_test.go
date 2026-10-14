package verify

import (
	"fmt"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestGoalCoverageAllows128PRDSectionsPlusGoalAndSource(t *testing.T) {
	for _, count := range []int{130, 131} {
		t.Run(fmt.Sprintf("%d mappings", count), func(t *testing.T) {
			spec, plan, digest, review := largeCoverageFixture(count)
			record, err := ReviewCoverage(spec, plan, digest, review)
			if count == maxGoalMappings {
				if err != nil || len(record.Review.Coverage) != count {
					t.Fatalf("valid maximum PRD coverage was rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("coverage above the PRD-bound mapping limit was accepted")
			}
		})
	}
}

func largeCoverageFixture(count int) (c.TaskSpec, CheckPlan, string, GoalReview) {
	inputDigest := c.HashBytes([]byte("goal"))
	checkSetDigest := c.HashBytes([]byte("protected checks"))
	requirements := make([]c.Requirement, count)
	for i := range requirements {
		requirements[i] = c.Requirement{
			ID:                 fmt.Sprintf("prd-requirement-%03d", i),
			Source:             c.SourceSpan{InputID: "initial", Start: 0, End: 1},
			Required:           true,
			Risk:               "NORMAL",
			VerificationMethod: "source-bound coverage",
		}
	}
	spec := c.TaskSpec{
		SchemaVersion:   1,
		TaskID:          "coverage-test",
		Version:         1,
		Goal:            "review the complete PRD",
		Inputs:          []c.InputSource{{ID: "initial", PayloadRef: "blob://coverage-test/goal", Digest: inputDigest, ByteLength: 1, Integrity: c.Intact}},
		Requirements:    requirements,
		ProtectedOrigin: c.HashBytes([]byte("origin")),
		DeliveryPolicy:  "CANDIDATE_ONLY",
	}
	firstEnd := min(count, 128)
	firstIDs := make([]string, firstEnd)
	secondIDs := make([]string, count-firstEnd)
	mappings := make([]GoalMapping, count)
	for i, requirement := range requirements {
		if i < firstEnd {
			firstIDs[i] = requirement.ID
			mappings[i] = GoalMapping{RequirementID: requirement.ID, CheckIDs: []string{"prd-check-a"}}
		} else {
			secondIDs[i-firstEnd] = requirement.ID
			mappings[i] = GoalMapping{RequirementID: requirement.ID, CheckIDs: []string{"prd-check-b"}}
		}
	}
	newCheck := func(id string, ids []string) CheckDefinition {
		return CheckDefinition{
			ID:             id,
			Kind:           "STATIC",
			RequirementIDs: ids,
			Argv:           []string{"check"},
			RunnerDigest:   c.HashBytes([]byte("runner")),
			Selection:      "explicit review",
			ExpectedTests:  []string{},
			Closure:        []CheckScope{{Path: "src", Recursive: true}},
		}
	}
	plan := CheckPlan{SchemaVersion: 1, Checks: []CheckDefinition{newCheck("prd-check-a", firstIDs), newCheck("prd-check-b", secondIDs)}}
	review := GoalReview{
		SchemaVersion:    1,
		InputDigests:     []string{inputDigest},
		Coverage:         mappings,
		DependencyChecks: []string{"prd-check-a", "prd-check-b"},
		Acknowledgement:  CoverageAcknowledgement,
	}
	return spec, plan, checkSetDigest, review
}
