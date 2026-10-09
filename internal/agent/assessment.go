package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/verify"
)

// Nonfinal evidence can expose FAIL/PARTIAL; it cannot issue VERIFIED before
// the separate frozen candidate/report transaction.
func (s *Session) recordObservedAssessment(ctx context.Context, state c.TaskState, doc Document) (c.TaskState, error) {
	report, err := s.deriveVerification(doc, state)
	if err != nil {
		return state, err
	}
	report.Guards.ManifestCoherent = false
	assessment := verify.Assess(doc.Spec, report.Binding, report.Receipts, report.Guards)
	proof := &c.VerificationFinalization{SourceSpecDigest: report.SpecDigest, Spec: verificationSpecProjection(doc.Spec), Binding: report.Binding, Receipts: report.Receipts, Guards: report.Guards, PolicyEpoch: state.PolicyEpoch}
	proof.ReportDigest, err = c.AssessmentProofDigest(*proof)
	if err != nil {
		return state, err
	}
	return s.record(ctx, state, doc, "VerificationAssessed", c.EventPayload{SnapshotDigest: doc.Candidate.SnapshotDigest, Verification: proof, Quality: assessment.Quality, Reason: proof.ReportDigest})
}
