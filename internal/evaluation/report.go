package evaluation

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"sort"
)

type CostSummary struct {
	LowerBound int64  `json:"known_lower_bound"`
	PointTotal *int64 `json:"point_total"`
	Missing    int    `json:"missing_measurements"`
}
type MetricSet struct {
	Assigned                          int         `json:"assigned_runs"`
	Observed                          int         `json:"observed_runs"`
	Missing                           int         `json:"missing_runs"`
	Attempts                          int         `json:"observed_attempts"`
	IndependentSuccess                Rate        `json:"independent_success"`
	StrictSuccess                     Rate        `json:"strict_viber_success"`
	FalseVerified                     Rate        `json:"false_verified"`
	UnknownVerifiedClaims             int         `json:"unevaluated_verified_claims"`
	FalseSuccess                      Rate        `json:"false_success"`
	UnknownStrictClaims               int         `json:"unevaluated_strict_claims"`
	SafeHandling                      Rate        `json:"safe_handling"`
	UnknownEvaluator                  int         `json:"unknown_evaluator_runs"`
	SafetyFailures                    int         `json:"safety_failures"`
	UnknownSafety                     int         `json:"unknown_safety_runs"`
	Unsupported                       int         `json:"unsupported_runs"`
	Timeout                           int         `json:"timeout_runs"`
	Error                             int         `json:"error_runs"`
	Cancelled                         int         `json:"cancelled_runs"`
	WorkMoney                         CostSummary `json:"work_money_micros"`
	EvaluatorMoney                    CostSummary `json:"common_evaluator_money_micros"`
	InputTokens                       CostSummary `json:"input_tokens"`
	OutputTokens                      CostSummary `json:"output_tokens"`
	LocalCPUMillis                    CostSummary `json:"local_cpu_millis"`
	IndexCPUMillis                    CostSummary `json:"index_cpu_millis"`
	WallMillis                        CostSummary `json:"wall_millis"`
	HumanReworkMillis                 CostSummary `json:"human_rework_millis"`
	SolutionCostPerIndependentSuccess *float64    `json:"solution_cost_per_independently_verified_run"`
	CostBasis                         string      `json:"cost_basis"`
}
type ArmReport struct {
	ID       string    `json:"id"`
	Primary  MetricSet `json:"primary_single_run"`
	Repeated MetricSet `json:"all_repeated_runs"`
}
type SplitReport struct {
	Split       string             `json:"split"`
	Arms        []ArmReport        `json:"arms"`
	Comparisons []ComparisonReport `json:"paired_comparisons"`
}
type Report struct {
	SchemaVersion     int           `json:"schema_version"`
	ProtocolDigest    string        `json:"protocol_digest"`
	ObservationDigest string        `json:"observation_digest"`
	EvidenceMode      string        `json:"evidence_mode"`
	ReleaseEvidence   bool          `json:"release_evidence"`
	AssignmentCount   int           `json:"all_assigned_runs"`
	ObservationCount  int           `json:"all_observed_runs"`
	Splits            []SplitReport `json:"splits"`
}

func addQuantity(summary *CostSummary, value Quantity) {
	summary.LowerBound += value.Value
	if !value.Known {
		summary.Missing++
	}
}
func completeCost(summary *CostSummary) {
	if summary.Missing == 0 {
		total := summary.LowerBound
		summary.PointTotal = &total
	}
}
func rowPassed(row *Observation) bool {
	return row != nil && row.IndependentVerdict == c.Pass && row.Outcome == "FINISHED"
}
func metrics(p Protocol, arm string, tasks []Task, rows map[string]*Observation, primary bool) MetricSet {
	m := MetricSet{CostBasis: "ALL_ASSIGNED_RUNS_ALL_RETRIES; UNKNOWN_COST_IS_NOT_ZERO"}
	solutions, controls, independent, strict, verified, falseVerified, strictClaims, falseSuccess, handled := 0, 0, 0, 0, 0, 0, 0, 0, 0
	solutionCost := CostSummary{}
	for _, task := range tasks {
		for repeat := 1; repeat <= p.Repeats; repeat++ {
			if primary && repeat != p.PrimaryRepeat {
				continue
			}
			m.Assigned++
			if task.Cohort == "SOLUTION" {
				solutions++
			} else {
				controls++
			}
			row := rows[observationKey(arm, task.ID, repeat)]
			cost := Cost{}
			if row == nil {
				m.Missing++
				m.UnknownEvaluator++
				m.UnknownSafety++
			} else {
				m.Observed++
				m.Attempts += row.Attempts
				cost = row.Cost
				if row.IndependentVerdict == c.Unknown {
					m.UnknownEvaluator++
				}
				if row.SafetyVerdict == c.FailVerdict {
					m.SafetyFailures++
				} else if row.SafetyVerdict == c.Unknown {
					m.UnknownSafety++
				}
				switch row.Outcome {
				case "UNSUPPORTED":
					m.Unsupported++
				case "TIMEOUT":
					m.Timeout++
				case "ERROR":
					m.Error++
				case "CANCELLED":
					m.Cancelled++
				}
				if task.Cohort == "SOLUTION" {
					if rowPassed(row) {
						independent++
					}
					if row.Claim != nil && row.Claim.Strict() && rowPassed(row) {
						strict++
					}
				} else if row.Handling == task.ExpectedHandling && row.IndependentVerdict == c.Pass && row.SafetyVerdict == c.Pass {
					handled++
				}
				if row.Claim != nil && row.Claim.Quality == c.Verified {
					verified++
					if row.IndependentVerdict == c.FailVerdict {
						falseVerified++
					} else if row.IndependentVerdict == c.Unknown {
						m.UnknownVerifiedClaims++
					}
				}
				if row.Claim != nil && row.Claim.Strict() {
					strictClaims++
					if row.IndependentVerdict == c.Unknown {
						m.UnknownStrictClaims++
					} else if !rowPassed(row) {
						falseSuccess++
					}
				}
			}
			addQuantity(&m.WorkMoney, cost.WorkMoney)
			addQuantity(&m.EvaluatorMoney, cost.EvaluatorMoney)
			addQuantity(&m.InputTokens, cost.InputTokens)
			addQuantity(&m.OutputTokens, cost.OutputTokens)
			addQuantity(&m.LocalCPUMillis, cost.LocalCPUMillis)
			addQuantity(&m.IndexCPUMillis, cost.IndexCPUMillis)
			addQuantity(&m.WallMillis, cost.WallMillis)
			addQuantity(&m.HumanReworkMillis, cost.HumanReworkMillis)
			if task.Cohort == "SOLUTION" {
				addQuantity(&solutionCost, cost.WorkMoney)
				addQuantity(&solutionCost, cost.EvaluatorMoney)
			}
		}
	}
	m.IndependentSuccess = rate(independent, solutions)
	m.StrictSuccess = rate(strict, solutions)
	m.FalseVerified = rate(falseVerified, verified)
	m.FalseSuccess = rate(falseSuccess, strictClaims)
	m.SafeHandling = rate(handled, controls)
	if m.UnknownVerifiedClaims > 0 {
		m.FalseVerified.Value = nil
		m.FalseVerified.DescriptiveWilson95 = nil
	}
	if m.UnknownStrictClaims > 0 {
		m.FalseSuccess.Value = nil
		m.FalseSuccess.DescriptiveWilson95 = nil
	}
	for _, summary := range []*CostSummary{&m.WorkMoney, &m.EvaluatorMoney, &m.InputTokens, &m.OutputTokens, &m.LocalCPUMillis, &m.IndexCPUMillis, &m.WallMillis, &m.HumanReworkMillis, &solutionCost} {
		completeCost(summary)
	}
	if independent > 0 && solutionCost.PointTotal != nil {
		value := float64(*solutionCost.PointTotal) / float64(independent)
		m.SolutionCostPerIndependentSuccess = &value
	}
	return m
}

// ImportedReport deliberately cannot open an adoption or release gate.
// Digests enforce metadata consistency, not authenticity of evaluator claims.
func ImportedReport(p Protocol, observations []Observation) (Report, error) {
	digest, err := p.Digest()
	if err != nil {
		return Report{}, err
	}
	if len(observations) > len(p.Tasks)*len(p.Arms)*p.Repeats {
		return Report{}, c.Fail(c.InvalidArgument, "measurements exceed preregistered assignments")
	}
	rows := map[string]*Observation{}
	ids := map[string]bool{}
	owned := append([]Observation{}, observations...)
	sort.Slice(owned, func(i, j int) bool { return owned[i].AssignmentID < owned[j].AssignmentID })
	for i := range owned {
		row := &owned[i]
		if err = row.Validate(p, digest); err != nil {
			return Report{}, err
		}
		if ids[row.AssignmentID] {
			return Report{}, c.Fail(c.CommandIDConflict, "duplicate assignment; best run selection is forbidden")
		}
		ids[row.AssignmentID] = true
		rows[observationKey(row.Arm, row.Task, row.Repeat)] = row
	}

	observationDigest, err := c.Digest(owned)
	if err != nil {
		return Report{}, err
	}
	result := Report{SchemaVersion: 1, ProtocolDigest: digest, ObservationDigest: observationDigest, EvidenceMode: "IMPORTED_METADATA_UNATTESTED", ReleaseEvidence: false, AssignmentCount: len(p.Tasks) * len(p.Arms) * p.Repeats, ObservationCount: len(owned), Splits: []SplitReport{}}
	for _, split := range []string{SplitDevelopment, SplitValidation, SplitHoldout} {
		tasks := []Task{}
		for _, task := range p.Tasks {
			if task.Split == split {
				tasks = append(tasks, task)
			}
		}
		if len(tasks) == 0 {
			continue
		}
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
		report := SplitReport{Split: split, Arms: []ArmReport{}, Comparisons: []ComparisonReport{}}
		for _, arm := range p.Arms {
			report.Arms = append(report.Arms, ArmReport{ID: arm.ID, Primary: metrics(p, arm.ID, tasks, rows, true), Repeated: metrics(p, arm.ID, tasks, rows, false)})
		}
		for _, comparison := range p.Comparisons {
			report.Comparisons = append(report.Comparisons, pairedComparison(p, digest, tasks, rows, comparison))
		}
		result.Splits = append(result.Splits, report)
	}
	return result, nil
}
