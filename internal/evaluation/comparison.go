package evaluation

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/rand/v2"
	"sort"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type PairEstimate struct {
	LatencyInterval         *Interval `json:"paired_cluster_latency_ratio_interval"`
	ReworkInterval          *Interval `json:"paired_cluster_rework_ratio_interval"`
	ControlHandlingFailures int       `json:"control_handling_failures"`
	TaskCount               int       `json:"assigned_solution_tasks"`
	RepositoryClusters      int       `json:"repository_clusters"`
	SuccessDifference       *float64  `json:"independent_success_difference"`
	SuccessInterval         *Interval `json:"paired_cluster_success_interval"`
	TotalMoneyRatio         *float64  `json:"total_money_ratio"`
	MoneyRatioInterval      *Interval `json:"paired_cluster_money_ratio_interval"`
	LatencyRatio            *float64  `json:"latency_ratio"`
	ReworkRatio             *float64  `json:"rework_ratio"`
	MissingCostTasks        int       `json:"tasks_with_missing_cost"`
	MissingLatencyTasks     int       `json:"tasks_with_missing_latency"`
	MissingReworkTasks      int       `json:"tasks_with_missing_rework"`
	MissingSafetyRuns       int       `json:"missing_or_unknown_safety_runs"`
	SafetyFailureRuns       int       `json:"safety_failure_runs"`
	MetadataSupportsTarget  bool      `json:"metadata_supports_preregistered_statistical_target"`
}
type ComparisonReport struct {
	ID                      string       `json:"id"`
	Baseline                string       `json:"baseline"`
	Variant                 string       `json:"variant"`
	Attribution             string       `json:"attribution"`
	Primary                 PairEstimate `json:"primary_single_run"`
	Repeated                PairEstimate `json:"all_repeated_task_means"`
	IntervalMethod          string       `json:"interval_method"`
	MultiplicityComparisons int          `json:"multiplicity_comparisons"`
	AdoptionAllowed         bool         `json:"adoption_allowed"`
	AdoptionReason          string       `json:"adoption_reason"`
}
type taskPair struct {
	repository                                                                      string
	difference                                                                      float64
	baseMoney, variantMoney, baseLatency, variantLatency, baseRework, variantRework float64
	moneyKnown, latencyKnown, reworkKnown                                           bool
}

func observationKey(arm, task string, repeat int) string {
	// IDs forbid NUL and repeats are bounded; tuple boundaries are unambiguous.
	return arm + "\x00" + task + "\x00" + string(rune(repeat))
}
func ratio(a, b float64) *float64 {
	if b <= 0 || math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
		return nil
	}
	value := a / b
	return &value
}
func quantile(ordered []float64, p float64) float64 {
	position := p * float64(len(ordered)-1)
	low := int(math.Floor(position))
	high := int(math.Ceil(position))
	return ordered[low] + (ordered[high]-ordered[low])*(position-float64(low))
}
func pairedEstimate(p Protocol, digest string, tasks []Task, rows map[string]*Observation, comparison Comparison, primary bool) PairEstimate {
	result := PairEstimate{}
	pairs := []taskPair{}
	for _, task := range tasks {
		for repeat := 1; repeat <= p.Repeats; repeat++ {
			if primary && repeat != p.PrimaryRepeat {
				continue
			}
			for _, arm := range []string{comparison.Baseline, comparison.Variant} {
				row := rows[observationKey(arm, task.ID, repeat)]
				if row == nil || row.SafetyVerdict == c.Unknown {
					result.MissingSafetyRuns++
				} else if row.SafetyVerdict == c.FailVerdict {
					result.SafetyFailureRuns++
				}
				if task.Cohort == "CONTROL" && (row == nil || row.Handling != task.ExpectedHandling || row.IndependentVerdict != c.Pass || row.SafetyVerdict != c.Pass) {
					result.ControlHandlingFailures++
				}
			}
		}
	}

	for _, task := range tasks {
		if task.Cohort != "SOLUTION" {
			continue
		}
		pair := taskPair{repository: task.RepositoryDigest, moneyKnown: true, latencyKnown: true, reworkKnown: true}
		repeats := 0
		for repeat := 1; repeat <= p.Repeats; repeat++ {
			if primary && repeat != p.PrimaryRepeat {
				continue
			}
			repeats++
			base := rows[observationKey(comparison.Baseline, task.ID, repeat)]
			variant := rows[observationKey(comparison.Variant, task.ID, repeat)]
			if rowPassed(variant) {
				pair.difference++
			}
			if rowPassed(base) {
				pair.difference--
			}
			for index, row := range []*Observation{base, variant} {
				if row == nil {
					pair.moneyKnown = false
					pair.latencyKnown = false
					pair.reworkKnown = false
					continue
				}

				cost := row.Cost
				// Local compute/index quantities are necessary for a money comparison;
				// a claimed dollar zero never makes missing local work disappear.
				if !cost.WorkMoney.Known || !cost.EvaluatorMoney.Known || !cost.LocalCPUMillis.Known || !cost.IndexCPUMillis.Known || cost.Measurement != "MEASURED" {
					pair.moneyKnown = false
				}
				if !cost.WallMillis.Known {
					pair.latencyKnown = false
				}
				if !cost.HumanReworkMillis.Known {
					pair.reworkKnown = false
				}
				money := float64(cost.WorkMoney.Value + cost.EvaluatorMoney.Value)
				if index == 0 {
					pair.baseMoney += money
					pair.baseLatency += float64(cost.WallMillis.Value)
					pair.baseRework += float64(cost.HumanReworkMillis.Value)
				} else {
					pair.variantMoney += money
					pair.variantLatency += float64(cost.WallMillis.Value)
					pair.variantRework += float64(cost.HumanReworkMillis.Value)
				}
			}
		}
		pair.difference /= float64(repeats)
		pair.baseMoney /= float64(repeats)
		pair.variantMoney /= float64(repeats)
		pair.baseLatency /= float64(repeats)
		pair.variantLatency /= float64(repeats)
		pair.baseRework /= float64(repeats)
		pair.variantRework /= float64(repeats)
		if !pair.moneyKnown {
			result.MissingCostTasks++
		}
		if !pair.latencyKnown {
			result.MissingLatencyTasks++
		}
		if !pair.reworkKnown {
			result.MissingReworkTasks++
		}
		pairs = append(pairs, pair)
	}
	result.TaskCount = len(pairs)
	if len(pairs) == 0 {
		return result
	}
	byRepository := map[string][]taskPair{}
	for _, pair := range pairs {
		byRepository[pair.repository] = append(byRepository[pair.repository], pair)
	}
	repositories := make([]string, 0, len(byRepository))
	for repository := range byRepository {
		repositories = append(repositories, repository)
	}
	sort.Strings(repositories)
	result.RepositoryClusters = len(repositories)
	difference, baseMoney, variantMoney, baseLatency, variantLatency, baseRework, variantRework := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
	for _, pair := range pairs {
		difference += pair.difference
		baseMoney += pair.baseMoney
		variantMoney += pair.variantMoney
		baseLatency += pair.baseLatency
		variantLatency += pair.variantLatency
		baseRework += pair.baseRework
		variantRework += pair.variantRework
	}
	difference /= float64(len(pairs))
	result.SuccessDifference = &difference
	if result.MissingCostTasks == 0 {
		result.TotalMoneyRatio = ratio(variantMoney, baseMoney)
	}
	if result.MissingLatencyTasks == 0 {
		result.LatencyRatio = ratio(variantLatency, baseLatency)
	}
	if result.MissingReworkTasks == 0 {
		result.ReworkRatio = ratio(variantRework, baseRework)
	}
	// A degenerate single-cluster resample cannot be presented as uncertainty.
	if len(repositories) < 2 {
		return result
	}
	seedDigest, _ := c.Digest(struct {
		Protocol, Comparison string
		Primary              bool
	}{digest, comparison.ID, primary})
	seedBytes, _ := hex.DecodeString(seedDigest)
	random := rand.New(rand.NewPCG(p.Seed, binary.LittleEndian.Uint64(seedBytes[:8])))
	differences := make([]float64, 0, p.BootstrapSamples)
	ratios := make([]float64, 0, p.BootstrapSamples)
	latencyRatios := []float64{}
	reworkRatios := []float64{}
	for sample := 0; sample < p.BootstrapSamples; sample++ {
		tasksCount := 0
		gap, bMoney, vMoney := 0.0, 0.0, 0.0
		bLatency, vLatency, bRework, vRework := 0.0, 0.0, 0.0, 0.0
		for cluster := 0; cluster < len(repositories); cluster++ {
			repository := repositories[random.IntN(len(repositories))]
			for _, pair := range byRepository[repository] {
				tasksCount++
				gap += pair.difference
				bMoney += pair.baseMoney
				vMoney += pair.variantMoney
				bLatency += pair.baseLatency
				vLatency += pair.variantLatency
				bRework += pair.baseRework
				vRework += pair.variantRework
			}
		}
		differences = append(differences, gap/float64(tasksCount))
		if result.LatencyRatio != nil {
			if value := ratio(vLatency, bLatency); value != nil {
				latencyRatios = append(latencyRatios, *value)
			}
		}
		if result.ReworkRatio != nil {
			if value := ratio(vRework, bRework); value != nil {
				reworkRatios = append(reworkRatios, *value)
			}
		}
		if result.TotalMoneyRatio != nil {
			if value := ratio(vMoney, bMoney); value != nil {
				ratios = append(ratios, *value)
			}
		}
	}
	sort.Float64s(differences)
	sort.Float64s(ratios)
	sort.Float64s(latencyRatios)
	sort.Float64s(reworkRatios)
	comparisons := max(1, len(p.Comparisons))
	tail := 0.025 / float64(comparisons)
	result.SuccessInterval = &Interval{quantile(differences, tail), quantile(differences, 1-tail)}
	// Undefined bootstrap cost ratios are not silently dropped from uncertainty.
	if len(ratios) == p.BootstrapSamples {
		result.MoneyRatioInterval = &Interval{quantile(ratios, tail), quantile(ratios, 1-tail)}
	}
	if len(latencyRatios) == p.BootstrapSamples {
		result.LatencyInterval = &Interval{quantile(latencyRatios, tail), quantile(latencyRatios, 1-tail)}
	}
	if len(reworkRatios) == p.BootstrapSamples {
		result.ReworkInterval = &Interval{quantile(reworkRatios, tail), quantile(reworkRatios, 1-tail)}
	}
	if result.RepositoryClusters >= p.MinRepositoryClusters && result.MissingSafetyRuns == 0 && result.SafetyFailureRuns == 0 && result.ControlHandlingFailures == 0 && result.LatencyInterval != nil && result.LatencyInterval.Upper <= float64(p.MaxLatencyPPM)/1000000 && result.ReworkInterval != nil && result.ReworkInterval.Upper <= float64(p.MaxReworkPPM)/1000000 {
		successGain := result.SuccessInterval.Lower >= 0.05
		costGain := result.SuccessInterval.Lower >= -0.02 && result.MoneyRatioInterval != nil && result.MoneyRatioInterval.Upper <= 0.80
		result.MetadataSupportsTarget = successGain || costGain
	}
	return result
}
func pairedComparison(p Protocol, digest string, tasks []Task, rows map[string]*Observation, comparison Comparison) ComparisonReport {
	attribution := "MATCHED_HARNESS_CONDITIONS"
	if p.Kind == "PRODUCT_COMPARISON" {
		attribution = "PRODUCT_COMPARISON_NOT_CAUSAL_HARNESS_ATTRIBUTION"
	}
	return ComparisonReport{ID: comparison.ID, Baseline: comparison.Baseline, Variant: comparison.Variant, Attribution: attribution, Primary: pairedEstimate(p, digest, tasks, rows, comparison, true), Repeated: pairedEstimate(p, digest, tasks, rows, comparison, false), IntervalMethod: p.UncertaintyMethod + "; PREREGISTERED_BONFERRONI_PERCENTILE_95", MultiplicityComparisons: len(p.Comparisons), AdoptionAllowed: false, AdoptionReason: "IMPORTED_METADATA_IS_NOT_ATTESTED_INDEPENDENT_EVALUATOR_OR_RELEASE_EVIDENCE"}
}
