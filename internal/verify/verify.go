// Package verify computes conservative quality from trusted runtime facts.
// Models cannot write these facts or receipts. OS enforcement and provenance
// validation must precede this pure assessment; it cannot establish them itself.
package verify

import (
	c "github.com/ixayldz/Viber/internal/contracts"
)

type Guards struct {
	RawIntentAvailable       bool
	GoalCoverageReviewed     bool
	ManifestCoherent         bool
	ManifestIntact           bool
	ProtectedOriginIntact    bool
	SourceEnforcement        bool
	ProcessTreeQuiescent     bool
	NoRelevantUnknownEffects bool
	NoStalePreconditions     bool
	NoPolicyViolation        bool
	PolicyViolation          bool
	RequiredGateFailed       bool
	RequiredGatesPass        bool
	NoPendingInput           bool
}
type Assessment struct {
	Quality  c.Quality            `json:"quality_verdict"`
	Criteria map[string]c.Verdict `json:"criteria"`
	Reasons  []string             `json:"reasons"`
}

func admissible(r c.VerificationReceipt, b c.Binding, critical bool) bool {
	switch r.CheckKind {
	case "TEST":
		if r.ExecutedTests <= 0 {
			return false
		}
	case "STATIC", "BEHAVIOR", "HUMAN":
		if r.ExecutedTests < 0 {
			return false
		}
	default:
		return false
	}
	return r.ID != "" && r.Binding == b && r.Integrity == c.Intact && r.PolicyCompatible &&
		r.SourceReadOnly && r.ProtectedResultChannel && r.ProcessTreeQuiescent &&
		r.TrustedCompletion && r.DiscoveryExpected && (!critical || r.IndependentObserver)
}
func validBinding(b c.Binding, s c.TaskSpec) bool {
	return b.TaskID == s.TaskID && b.SpecVersion == s.Version && c.ValidDigest(b.CandidateDigest) &&
		c.ValidDigest(b.CheckSetDigest) && c.ValidDigest(b.EnvironmentDigest) && c.ValidDigest(b.PolicyDigest)
}
func Assess(spec c.TaskSpec, binding c.Binding, receipts []c.VerificationReceipt, g Guards) Assessment {
	a := Assessment{Quality: c.Unverified, Criteria: map[string]c.Verdict{}, Reasons: []string{}}
	if err := spec.Validate(); err != nil {
		a.Reasons = append(a.Reasons, err.Error())
		return a
	}
	if !validBinding(binding, spec) {
		a.Reasons = append(a.Reasons, "invalid current evidence binding")
		return a
	}
	have, allPass, allResolved, anyWaiver := false, true, true, false
	anyFail := false
	receiptIDs := map[string]bool{}
	for _, r := range receipts {
		if r.ID != "" && receiptIDs[r.ID] {
			a.Reasons = append(a.Reasons, "duplicate receipt ID")
			return a
		}
		receiptIDs[r.ID] = true
	}
	for _, req := range spec.Requirements {
		if !req.Required {
			continue
		}
		verdict := c.Unknown
		for _, r := range receipts {
			matches := false
			for _, id := range r.RequirementIDs {
				if id == req.ID {
					matches = true
				}
			}
			if !matches || !admissible(r, binding, req.Risk == "CRITICAL") {
				continue
			}
			switch r.Verdict {
			case c.FailVerdict:
				verdict = c.FailVerdict
				have = true
			case c.Pass:
				if verdict != c.FailVerdict {
					verdict = c.Pass
				}
				have = true
			case c.Waived:
				if r.WaiverAuthorized && req.Risk != "CRITICAL" && verdict != c.FailVerdict && verdict != c.Pass {
					verdict = c.Waived
					have = true
				}
			case c.Unknown, c.Pending: // No positive evidence.
			default:
				a.Reasons = append(a.Reasons, "unknown receipt verdict")
			}
		}
		a.Criteria[req.ID] = verdict
		if verdict == c.FailVerdict {
			anyFail = true
			a.Reasons = append(a.Reasons, "required criterion failed")
		}
		if verdict != c.Pass {
			allPass = false
		}
		if verdict != c.Pass && verdict != c.Waived {
			allResolved = false
		}
		if verdict == c.Waived {
			anyWaiver = true
		}
	}
	guards := []struct {
		name string
		ok   bool
	}{
		{"raw intent", g.RawIntentAvailable}, {"goal coverage", g.GoalCoverageReviewed},
		{"coherent frozen manifest", g.ManifestCoherent}, {"manifest integrity", g.ManifestIntact},
		{"protected origin", g.ProtectedOriginIntact}, {"read-only source/check enforcement", g.SourceEnforcement},
		{"process quiescence", g.ProcessTreeQuiescent}, {"unknown effects", g.NoRelevantUnknownEffects},
		{"stale preconditions", g.NoStalePreconditions}, {"policy", g.NoPolicyViolation},
		{"required gates", g.RequiredGatesPass}, {"input barrier", g.NoPendingInput},
	}
	guardOK := true
	for _, guard := range guards {
		if !guard.ok {
			guardOK = false
			a.Reasons = append(a.Reasons, "unresolved "+guard.name)
		}
	}
	if g.PolicyViolation || g.RequiredGateFailed || anyFail {
		a.Quality = c.QualityFailed
		return a
	}
	if guardOK && allPass {
		a.Quality = c.Verified
	} else if guardOK && allResolved && anyWaiver {
		a.Quality = c.AcceptedWithWaiver
	} else if have {
		a.Quality = c.Partial
	}
	return a
}

type DeliveryFacts struct {
	ReportDurable                 bool
	ArtifactsAccessible           bool
	ContentBindingValid           bool
	CandidatePublished            bool
	RequiredLiveDeliveryConfirmed bool
	ApprovalPending               bool
	Conflict                      bool
	UnknownEffect                 bool
	PolicyViolation               bool
}

func AssessFulfillment(policy string, d DeliveryFacts) c.Fulfillment {
	if d.PolicyViolation {
		return c.FulfillmentFailed
	}
	if d.Conflict {
		return c.Conflicted
	}
	if d.UnknownEffect {
		return c.FulfillmentUnknown
	}
	if d.ApprovalPending || !d.ReportDurable || !d.ArtifactsAccessible || !d.ContentBindingValid {
		return c.FulfillmentPending
	}
	switch policy {
	case "CANDIDATE_ONLY":
		if d.CandidatePublished {
			return c.Satisfied
		}
	case "LIVE_APPLY_REQUIRED":
		if d.RequiredLiveDeliveryConfirmed {
			return c.Satisfied
		}
	}
	return c.FulfillmentPending
}
