package evaluation

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/kernel"
	"math"
)

type Quantity struct {
	Known bool  `json:"known"`
	Value int64 `json:"value"`
}
type Cost struct {
	WorkMoney         Quantity `json:"work_money_micros"`
	EvaluatorMoney    Quantity `json:"evaluator_money_micros"`
	InputTokens       Quantity `json:"input_tokens"`
	OutputTokens      Quantity `json:"output_tokens"`
	LocalCPUMillis    Quantity `json:"local_cpu_millis"`
	IndexCPUMillis    Quantity `json:"index_cpu_millis"`
	WallMillis        Quantity `json:"wall_millis"`
	HumanReworkMillis Quantity `json:"human_rework_millis"`
	Measurement       string   `json:"measurement"`
}
type Claim struct {
	Execution    c.ExecutionState `json:"execution"`
	Outcome      c.Outcome        `json:"outcome"`
	Quality      c.Quality        `json:"quality"`
	Fulfillment  c.Fulfillment    `json:"fulfillment"`
	OpenRequired int              `json:"open_required"`
	InputBarrier bool             `json:"input_barrier"`
}

func (claim Claim) Strict() bool {
	return kernel.StrictSuccess(c.TaskState{Execution: claim.Execution, Outcome: claim.Outcome, Quality: claim.Quality, Fulfillment: claim.Fulfillment, OpenRequiredObligations: claim.OpenRequired, InputBarrier: claim.InputBarrier})
}

type Observation struct {
	SchemaVersion           int       `json:"schema_version"`
	ProtocolDigest          string    `json:"protocol_digest"`
	AssignmentID            string    `json:"assignment_id"`
	Arm                     string    `json:"arm"`
	Task                    string    `json:"task"`
	Repeat                  int       `json:"repeat"`
	CandidateDigest         string    `json:"candidate_digest"`
	SourceArtifactDigest    string    `json:"source_artifact_digest"`
	EvaluatorDigest         string    `json:"evaluator_digest"`
	EvaluationReceiptDigest string    `json:"evaluation_receipt_digest,omitempty"`
	IndependentVerdict      c.Verdict `json:"independent_verdict"`
	SafetyVerdict           c.Verdict `json:"safety_verdict"`
	Claim                   *Claim    `json:"kernel_claim,omitempty"`
	Outcome                 string    `json:"outcome"`
	Handling                string    `json:"handling,omitempty"`
	Attempts                int       `json:"attempts"`
	Cost                    Cost      `json:"cost"`
}

func validClaim(claim Claim) bool {
	if claim.OpenRequired < 0 || claim.OpenRequired > 10000 {
		return false
	}
	switch claim.Execution {
	case c.Created, c.Scoping, c.Ready, c.Running, c.Verifying, c.Delivering, c.WaitingUser, c.WaitingResource, c.Blocked, c.Pausing, c.Paused, c.Recovering, c.Terminated:
	default:
		return false
	}
	switch claim.Quality {
	case c.Unverified, c.Partial, c.Verified, c.AcceptedWithWaiver, c.QualityFailed:
	default:
		return false
	}
	switch claim.Fulfillment {
	case c.FulfillmentPending, c.Satisfied, c.Conflicted, c.FulfillmentUnknown, c.FulfillmentFailed:
	default:
		return false
	}
	switch claim.Outcome {
	case "", c.Finished, c.Cancelled, c.BudgetExhausted, c.Failed:
	default:
		return false
	}
	return true
}
func validVerdict(verdict c.Verdict) bool {
	return verdict == c.Pass || verdict == c.FailVerdict || verdict == c.Unknown
}
func (row Observation) Validate(p Protocol, digest string) error {
	var task *Task
	var arm *Arm
	for i := range p.Tasks {
		if p.Tasks[i].ID == row.Task {
			task = &p.Tasks[i]
		}
	}
	for i := range p.Arms {
		if p.Arms[i].ID == row.Arm {
			arm = &p.Arms[i]
		}
	}
	if row.SchemaVersion != 1 || row.ProtocolDigest != digest || task == nil || arm == nil || row.Repeat < 1 || row.Repeat > p.Repeats || row.AssignmentID != AssignmentID(digest, row.Arm, row.Task, row.Repeat) || !c.ValidDigest(row.CandidateDigest) || !c.ValidDigest(row.SourceArtifactDigest) || row.EvaluatorDigest != p.EvaluatorDigest || !validVerdict(row.IndependentVerdict) || !validVerdict(row.SafetyVerdict) || row.Attempts < 1 || row.Attempts > 1024 || row.Claim != nil && !validClaim(*row.Claim) {
		return c.Fail(c.StoreIntegrityError, "measurement differs from preregistered assignment")
	}
	if row.IndependentVerdict != c.Unknown && !c.ValidDigest(row.EvaluationReceiptDigest) || row.EvaluationReceiptDigest != "" && !c.ValidDigest(row.EvaluationReceiptDigest) {
		return c.Fail(c.StoreIntegrityError, "evaluator claim lacks a candidate-bound receipt reference")
	}
	switch row.Outcome {
	case "FINISHED", "ERROR", "TIMEOUT", "UNSUPPORTED", "BLOCKED", "CANCELLED":
	default:
		return c.Fail(c.InvalidArgument, "invalid assigned-run outcome")
	}
	if row.Handling != "" && !safeHandling(row.Handling) || task.Cohort == "SOLUTION" && row.Handling != "" {
		return c.Fail(c.InvalidArgument, "solution and safe-handling cohorts cannot be mixed")
	}
	for _, q := range []Quantity{row.Cost.WorkMoney, row.Cost.EvaluatorMoney, row.Cost.InputTokens, row.Cost.OutputTokens, row.Cost.LocalCPUMillis, row.Cost.IndexCPUMillis, row.Cost.WallMillis, row.Cost.HumanReworkMillis} {
		if q.Value < 0 || q.Value > 1<<42 {
			return c.Fail(c.InvalidArgument, "invalid cost quantity; unknown is never an implicit zero")
		}
	}
	if row.Cost.Measurement != "MEASURED" && row.Cost.Measurement != "CONSERVATIVE_LEDGER" && row.Cost.Measurement != "UNKNOWN" {
		return c.Fail(c.InvalidArgument, "explicit measurement basis required")
	}
	return nil
}

type Rate struct {
	Numerator           int       `json:"numerator"`
	Denominator         int       `json:"denominator"`
	Value               *float64  `json:"value"`
	DescriptiveWilson95 *Interval `json:"descriptive_wilson95"`
}
type Interval struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

func rate(n, d int) Rate {
	result := Rate{Numerator: n, Denominator: d}
	if d == 0 {
		return result
	}
	value := float64(n) / float64(d)
	result.Value = &value
	z := 1.959963984540054
	denominator := 1 + z*z/float64(d)
	center := (value + z*z/(2*float64(d))) / denominator
	half := z * math.Sqrt(value*(1-value)/float64(d)+z*z/(4*float64(d*d))) / denominator
	result.DescriptiveWilson95 = &Interval{math.Max(0, center-half), math.Min(1, center+half)}
	return result
}
