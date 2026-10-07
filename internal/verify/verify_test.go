package verify

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func fixture() (c.TaskSpec, c.Binding, c.VerificationReceipt, Guards) {
	h := c.HashBytes([]byte("fixture"))
	s := c.TaskSpec{SchemaVersion: 1, TaskID: "t", Version: 1, Goal: "fix", DeliveryPolicy: "CANDIDATE_ONLY", ProtectedOrigin: h,
		Inputs:       []c.InputSource{{ID: "i", PayloadRef: "ctx://i", Digest: h, ByteLength: 3, Integrity: c.Intact}},
		Requirements: []c.Requirement{{ID: "r", Required: true, Source: c.SourceSpan{InputID: "i", End: 3}, Risk: "CRITICAL", VerificationMethod: "independent"}}}
	b := c.Binding{TaskID: "t", SpecVersion: 1, CandidateDigest: h, CheckSetDigest: h, EnvironmentDigest: h, PolicyDigest: h}
	r := c.VerificationReceipt{ID: "receipt", CheckKind: "TEST", Binding: b, RequirementIDs: []string{"r"}, Verdict: c.Pass, Integrity: c.Intact, PolicyCompatible: true, SourceReadOnly: true, ProtectedResultChannel: true, ProcessTreeQuiescent: true, DiscoveryExpected: true, ExecutedTests: 1, TrustedCompletion: true, IndependentObserver: true}
	g := Guards{RawIntentAvailable: true, GoalCoverageReviewed: true, ManifestCoherent: true, ManifestIntact: true, ProtectedOriginIntact: true, SourceEnforcement: true, ProcessTreeQuiescent: true, NoRelevantUnknownEffects: true, NoStalePreconditions: true, NoPolicyViolation: true, RequiredGatesPass: true, NoPendingInput: true}
	return s, b, r, g
}
func TestVerifiedRequiresEveryBindingAndGuard(t *testing.T) {
	s, b, r, g := fixture()
	if a := Assess(s, b, []c.VerificationReceipt{r}, g); a.Quality != c.Verified {
		t.Fatal(a)
	}
	mutations := map[string]func(*c.VerificationReceipt){
		"zero tests":         func(r *c.VerificationReceipt) { r.ExecutedTests = 0 },
		"unknown check kind": func(r *c.VerificationReceipt) { r.CheckKind = "MODEL_SAYS_PASS" },
		"wrong candidate":    func(r *c.VerificationReceipt) { r.Binding.CandidateDigest = c.HashBytes([]byte("new")) },
		"new environment":    func(r *c.VerificationReceipt) { r.Binding.EnvironmentDigest = c.HashBytes([]byte("lockfile")) },
		"check weakening":    func(r *c.VerificationReceipt) { r.Binding.CheckSetDigest = c.HashBytes([]byte("weaker")) },
		"no readonly":        func(r *c.VerificationReceipt) { r.SourceReadOnly = false },
		"forged channel":     func(r *c.VerificationReceipt) { r.ProtectedResultChannel = false },
		"live child":         func(r *c.VerificationReceipt) { r.ProcessTreeQuiescent = false },
		"discovery unknown":  func(r *c.VerificationReceipt) { r.DiscoveryExpected = false },
		"LLM judge only":     func(r *c.VerificationReceipt) { r.IndependentObserver = false },
		"missing artifact":   func(r *c.VerificationReceipt) { r.Integrity = c.Missing },
		"policy changed":     func(r *c.VerificationReceipt) { r.PolicyCompatible = false },
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := r
			mutation(&copy)
			if Assess(s, b, []c.VerificationReceipt{copy}, g).Quality == c.Verified {
				t.Fatal("false VERIFIED")
			}
		})
	}
	guards := map[string]func(*Guards){
		"missing raw":          func(g *Guards) { g.RawIntentAvailable = false },
		"goal omission":        func(g *Guards) { g.GoalCoverageReviewed = false },
		"inconsistent":         func(g *Guards) { g.ManifestCoherent = false },
		"corrupt":              func(g *Guards) { g.ManifestIntact = false },
		"origin":               func(g *Guards) { g.ProtectedOriginIntact = false },
		"mutation during test": func(g *Guards) { g.SourceEnforcement = false },
		"writer":               func(g *Guards) { g.ProcessTreeQuiescent = false },
		"unknown effect":       func(g *Guards) { g.NoRelevantUnknownEffects = false },
		"stale":                func(g *Guards) { g.NoStalePreconditions = false },
		"policy":               func(g *Guards) { g.NoPolicyViolation = false },
		"regression":           func(g *Guards) { g.RequiredGatesPass = false },
		"steering":             func(g *Guards) { g.NoPendingInput = false },
	}
	for name, mutation := range guards {
		t.Run(name, func(t *testing.T) {
			copy := g
			mutation(&copy)
			if Assess(s, b, []c.VerificationReceipt{r}, copy).Quality == c.Verified {
				t.Fatal("false VERIFIED")
			}
		})
	}
}
func TestNoBestRunSelectionOrEmptyIntent(t *testing.T) {
	s, b, r, g := fixture()
	failed := r
	failed.ID = "failed-attempt"
	failed.Verdict = c.FailVerdict
	if Assess(s, b, []c.VerificationReceipt{failed, r}, g).Quality != c.QualityFailed {
		t.Fatal("best rerun selected")
	}
	s.Requirements = nil
	if Assess(s, b, []c.VerificationReceipt{r}, g).Quality == c.Verified {
		t.Fatal("empty requirement success")
	}
	s, b, r, g = fixture()
	s.Inputs[0].Integrity = c.Missing
	if Assess(s, b, []c.VerificationReceipt{r}, g).Quality == c.Verified {
		t.Fatal("deleted raw intent success")
	}
}
func TestQualityAndDeliveryIndependent(t *testing.T) {
	s, b, r, g := fixture()
	if Assess(s, b, []c.VerificationReceipt{r}, g).Quality != c.Verified {
		t.Fatal("fixture")
	}
	d := DeliveryFacts{ReportDurable: true, ArtifactsAccessible: true, ContentBindingValid: true, CandidatePublished: true, Conflict: true}
	if AssessFulfillment("LIVE_APPLY_REQUIRED", d) != c.Conflicted {
		t.Fatal("live conflict hidden")
	}
	d.Conflict = false
	if AssessFulfillment("LIVE_APPLY_REQUIRED", d) == c.Satisfied {
		t.Fatal("candidate publication implied live apply")
	}
	if AssessFulfillment("CANDIDATE_ONLY", d) != c.Satisfied {
		t.Fatal("candidate delivery rejected")
	}
}

func TestUnknownGuardIsDifferentFromProvenFailure(t *testing.T) {
	s, b, r, g := fixture()
	g.NoPolicyViolation = false
	if Assess(s, b, []c.VerificationReceipt{r}, g).Quality != c.Partial {
		t.Fatal("unknown policy mislabeled as proved failure")
	}
	g.PolicyViolation = true
	if Assess(s, b, []c.VerificationReceipt{r}, g).Quality != c.QualityFailed {
		t.Fatal("proved violation hidden")
	}
	g.PolicyViolation = false
	g.NoPolicyViolation = true
	g.RequiredGateFailed = true
	if Assess(s, b, []c.VerificationReceipt{r}, g).Quality != c.QualityFailed {
		t.Fatal("required gate failure hidden")
	}
}
