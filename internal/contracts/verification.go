package contracts

// VerificationGuards are populated by the trusted kernel from archived evidence,
// protected runner facts and current task state. They are never model tool args.
type VerificationGuards struct {
	RawIntentAvailable       bool `json:"raw_intent_available"`
	GoalCoverageReviewed     bool `json:"goal_coverage_reviewed"`
	ManifestCoherent         bool `json:"manifest_coherent"`
	ManifestIntact           bool `json:"manifest_intact"`
	ProtectedOriginIntact    bool `json:"protected_origin_intact"`
	SourceEnforcement        bool `json:"source_enforcement"`
	ProcessTreeQuiescent     bool `json:"process_tree_quiescent"`
	NoRelevantUnknownEffects bool `json:"no_relevant_unknown_effects"`
	NoStalePreconditions     bool `json:"no_stale_preconditions"`
	NoPolicyViolation        bool `json:"no_policy_violation"`
	PolicyViolation          bool `json:"policy_violation"`
	RequiredGateFailed       bool `json:"required_gate_failed"`
	RequiredGatesPass        bool `json:"required_gates_pass"`
	NoPendingInput           bool `json:"no_pending_input"`
}

const VerificationProjectionGoal = "SOURCE_BOUND_VERIFICATION_PROJECTION_V1"

type VerificationFinalization struct {
	SourceSpecDigest string                `json:"source_spec_digest"`
	Spec             TaskSpec              `json:"spec"`
	Binding          Binding               `json:"binding"`
	Receipts         []VerificationReceipt `json:"receipts"`
	Guards           VerificationGuards    `json:"guards"`
	PolicyEpoch      int64                 `json:"policy_epoch"`
	ArtifactDigest   string                `json:"artifact_digest"`
	ReportDigest     string                `json:"report_digest"`
}

func AssessmentProofDigest(proof VerificationFinalization) (string, error) {
	proof.ReportDigest = ""
	return Digest(proof)
}
