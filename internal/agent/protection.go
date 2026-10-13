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
	if doc.Protection.SchemaVersion != 1 || doc.Protection.TaskID != doc.TaskID || doc.Protection.InitialSpecVersion != 1 || doc.Spec.Version < doc.Protection.InitialSpecVersion || doc.Protection.BaselineDigest != doc.Baseline.SnapshotDigest {
		return c.Fail(c.StoreIntegrityError, "protected check origin binding mismatch")
	}
	if !c.ValidDigest(doc.Spec.ProtectedOrigin) || !c.ValidDigest(doc.Protection.InputDigest) {
		return c.Fail(c.StoreIntegrityError, "protected check origin digest is invalid")
	}
	originDigest, err := c.Digest(*doc.Protection)
	if err != nil || originDigest != doc.Spec.ProtectedOrigin {
		return c.Fail(c.StoreIntegrityError, "protected check origin digest binding mismatch")
	}
	if err = doc.Protection.Plan.Validate(); err != nil {
		return c.Fail(c.StoreIntegrityError, "protected check plan is invalid")
	}
	requirements := make(map[string]bool, len(doc.Spec.Requirements))
	for _, requirement := range doc.Spec.Requirements {
		requirements[requirement.ID] = true
	}
	for _, check := range doc.Protection.Plan.Checks {
		for _, requirementID := range check.RequirementIDs {
			if !requirements[requirementID] {
				return c.Fail(c.StoreIntegrityError, "protected check criterion missing from current spec")
			}
		}
	}
	found := false
	for _, input := range doc.Spec.Inputs {
		if input.ID == "initial" && input.Digest == doc.Protection.InputDigest {
			found = true
		}
	}
	if !found {
		return c.Fail(c.StoreIntegrityError, "protected origin raw intent binding missing")
	}
	// Retained protected references are validated as digest-bound manifests so
	// a missing, purged or policy-denied blob cannot make an otherwise intact
	// task unloadable. Byte-level origin re-derivation and protected-closure
	// guarding run at their point of use with fresh hydration: check dispatch,
	// frozen checks, observed evidence, candidate publication, delivery
	// reverification and verified finals.
	baseline, err := s.Archive.ReadManifest(doc.Baseline)
	if err != nil {
		return err
	}
	candidate, err := s.Archive.ReadManifest(doc.Candidate)
	if err != nil {
		return err
	}
	if baseline.TaskID != doc.TaskID || candidate.TaskID != doc.TaskID ||
		doc.Protection.SourceRoot != baseline.Snapshot.Root || candidate.Snapshot.Root != baseline.Snapshot.Root {
		return c.Fail(c.StoreIntegrityError, "protected check origin source identity mismatch")
	}
	return nil
}

// validateProtectionBytes re-derives the protected check origin from the
// retained baseline bytes and guards the current candidate against protected
// closure changes with full hydration. Verification-quality authority gates
// call it at their point of use.
func (s *Session) validateProtectionBytes(doc Document) error {
	if doc.Protection == nil {
		return s.validateProtection(doc)
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
