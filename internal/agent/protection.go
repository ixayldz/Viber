package agent

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/verify"
	"github.com/ixayldz/Viber/internal/workspace"
)

type ProtectionInfo struct {
	OriginDigest                string `json:"origin_digest"`
	Provenance                  string `json:"provenance"`
	ClosureCoverage             string `json:"closure_coverage"`
	CheckCount                  int    `json:"check_count"`
	ProtectedScopes             int    `json:"protected_scopes"`
	StrongVerificationAvailable bool   `json:"strong_verification_available"`
}

func CheckProtection(doc Document) *ProtectionInfo {
	if doc.Protection == nil {
		return nil
	}
	return &ProtectionInfo{OriginDigest: doc.Spec.ProtectedOrigin, Provenance: doc.Protection.Provenance, ClosureCoverage: doc.Protection.ClosureCoverage, CheckCount: len(doc.Protection.Plan.Checks), ProtectedScopes: len(doc.Protection.Protected), StrongVerificationAvailable: doc.CheckRuntime != nil && len(doc.CheckRuntime.ObserverSuites) > 0 && verify.CoverageCurrent(doc.GoalCoverage, doc.Spec, checkSetDigest(doc))}
}
func ParseCheckPlan(raw []byte) (verify.CheckPlan, error) {
	plan := verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{}}
	if len(raw) == 0 {
		return plan, nil
	}
	if len(raw) > 1<<20 {
		return plan, c.Fail(c.InvalidArgument, "check plan exceeds 1 MiB")
	}
	if err := c.DecodeStrict(raw, &plan); err != nil {
		return plan, err
	}
	return plan, plan.Validate()
}
func (s *Session) validateProtection(doc Document) error {
	if doc.Protection == nil {
		// Pre-0.5 documents had an opaque marker and can remain UNVERIFIED only.
		// Removing a new protected origin must not downgrade into this legacy path.
		legacy, err := c.Digest(struct {
			Baseline string
			Profile  string
		}{doc.Baseline.SnapshotDigest, "offline-fixture-no-protected-checks-v1"})
		if err != nil {
			return err
		}
		if doc.Spec.ProtectedOrigin != legacy {
			return c.Fail(c.StoreIntegrityError, "protected check origin unavailable")
		}
		return nil
	}
	baseline, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		return err
	}
	if err = verify.ValidateCheckOrigin(doc.Spec, *doc.Protection, baseline); err != nil {
		return err
	}
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return err
	}
	return verify.GuardProtectedCandidate(*doc.Protection, candidate)
}
func guardProtected(doc Document, next workspace.Capture) error {
	if doc.Protection == nil {
		return nil
	}
	return verify.GuardProtectedCandidate(*doc.Protection, next)
}
