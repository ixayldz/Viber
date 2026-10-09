package evaluation

import (
	"math"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func protocolFixture(tasks int, repeats int) Protocol {
	hash := c.HashBytes([]byte("common-fixed-condition"))
	conditions := Conditions{Provider: "fixture", ModelProfileDigest: hash, DecodingDigest: hash, ToolCapabilitiesDigest: hash, EnvironmentDigest: hash, BudgetDigest: hash, PrivacyDigest: hash, SafetyBoundaryDigest: hash, AdapterDigest: hash, PromptDigest: hash, KernelDigest: hash, DependencyDigest: hash, PricingDigest: hash, Currency: "USD"}
	p := Protocol{SchemaVersion: 1, Version: ProtocolVersion, ID: "fixed-study", Kind: "HARNESS_ABLATION", DatasetDigest: hash, EvaluatorDigest: c.HashBytes([]byte("independent")), Repeats: repeats, PrimaryRepeat: 1, Seed: 17, BootstrapSamples: 1000, UncertaintyMethod: "REPOSITORY_CLUSTER_BOOTSTRAP_PCG_V1", Confidence: "0.95", StoppingRule: "FIXED_ASSIGNMENT_NO_BEST_RUN", HoldoutRule: "REPOSITORY_DISJOINT_HISTORY_CUTOFF", MinRepositoryClusters: 5, MaxLatencyPPM: 1100000, MaxReworkPPM: 1100000, Arms: []Arm{{"base", hash, conditions}, {"variant", c.HashBytes([]byte("feature")), conditions}}, Comparisons: []Comparison{{"comparison", "base", "variant"}}}
	for i := 0; i < tasks; i++ {
		id := string(rune('a' + i))
		p.Tasks = append(p.Tasks, Task{ID: id, RepositoryDigest: c.HashBytes([]byte("repo-" + id)), Split: SplitHoldout, Cohort: "SOLUTION", RequirementsDigest: hash, InitialSnapshot: hash, AcceptanceDigest: hash, CheckSetDigest: hash, StartUTC: "2026-10-01T00:00:00Z", HistoryCutoffUTC: "2026-09-30T23:59:59Z"})
	}
	return p
}
func observed(p Protocol, arm, task string, repeat int, verdict c.Verdict) Observation {
	digest, _ := p.Digest()
	hash := c.HashBytes([]byte("retained-source"))
	known := Quantity{true, 10}
	return Observation{SchemaVersion: 1, ProtocolDigest: digest, AssignmentID: AssignmentID(digest, arm, task, repeat), Arm: arm, Task: task, Repeat: repeat, CandidateDigest: hash, SourceArtifactDigest: hash, EvaluatorDigest: p.EvaluatorDigest, EvaluationReceiptDigest: hash, IndependentVerdict: verdict, SafetyVerdict: c.Pass, Claim: &Claim{Execution: c.Terminated, Outcome: c.Finished, Quality: c.Verified, Fulfillment: c.Satisfied}, Outcome: "FINISHED", Attempts: 1, Cost: Cost{WorkMoney: known, EvaluatorMoney: known, InputTokens: known, OutputTokens: known, LocalCPUMillis: known, IndexCPUMillis: known, WallMillis: known, HumanReworkMillis: known, Measurement: "MEASURED"}}
}
func findArm(t *testing.T, r Report, id string) ArmReport {
	t.Helper()
	for _, a := range r.Splits[0].Arms {
		if a.ID == id {
			return a
		}
	}
	t.Fatal("arm absent")
	return ArmReport{}
}
func TestImportedMetricsKeepAllAssignedFailuresControlsRetriesAndUnknownCost(t *testing.T) {
	p := protocolFixture(3, 2)
	p.Tasks[2].Cohort = "CONTROL"
	p.Tasks[2].ExpectedHandling = "SAFE_BLOCK"
	rows := []Observation{
		observed(p, "variant", "a", 1, c.Pass), observed(p, "variant", "a", 2, c.FailVerdict),
		observed(p, "variant", "b", 1, c.Unknown), observed(p, "variant", "c", 1, c.Pass), observed(p, "variant", "c", 2, c.Pass),
	}
	rows[1].Attempts = 3
	rows[1].Cost.WorkMoney.Value = 30
	rows[2].Outcome = "UNSUPPORTED"
	rows[2].Claim = nil
	rows[2].Cost.WorkMoney = Quantity{Known: false, Value: 7}
	for _, index := range []int{3, 4} {
		rows[index].Handling = "SAFE_BLOCK"
		rows[index].Outcome = "BLOCKED"
		rows[index].Claim = &Claim{Execution: c.Blocked, Quality: c.Unverified, Fulfillment: c.FulfillmentPending}
	}
	report, err := ImportedReport(p, rows)
	if err != nil {
		t.Fatal(err)
	}
	m := findArm(t, report, "variant")
	if m.Repeated.Assigned != 6 || m.Repeated.Observed != 5 || m.Repeated.Missing != 1 || m.Repeated.Attempts != 7 || m.Repeated.IndependentSuccess.Numerator != 1 || m.Repeated.IndependentSuccess.Denominator != 4 || m.Repeated.StrictSuccess.Numerator != 1 || m.Repeated.SafeHandling.Numerator != 2 || m.Repeated.SafeHandling.Denominator != 2 || m.Repeated.Unsupported != 1 || m.Repeated.UnknownEvaluator != 2 || m.Repeated.WorkMoney.PointTotal != nil || m.Repeated.WorkMoney.LowerBound != 67 || m.Repeated.WorkMoney.Missing != 2 || m.Repeated.SolutionCostPerIndependentSuccess != nil {
		t.Fatal("denominator/cost/handling math wrong", m)
	}
	if m.Primary.IndependentSuccess.Denominator != 2 || *m.Primary.IndependentSuccess.Value != 0.5 || *m.Repeated.IndependentSuccess.Value != 0.25 {
		t.Fatal("primary and repeated denominator mixed")
	}
	if report.ReleaseEvidence || report.EvidenceMode != "IMPORTED_METADATA_UNATTESTED" || report.Splits[0].Comparisons[0].AdoptionAllowed {
		t.Fatal("metadata granted authority")
	}
}
func TestEvalNoBestRunSelectionAndSelfVerifiedFailureIsCounted(t *testing.T) {
	p := protocolFixture(1, 3)
	rows := []Observation{observed(p, "variant", "a", 1, c.FailVerdict), observed(p, "variant", "a", 2, c.Pass), observed(p, "variant", "a", 3, c.FailVerdict)}
	report, err := ImportedReport(p, rows)
	if err != nil {
		t.Fatal(err)
	}
	m := findArm(t, report, "variant")
	if *m.Primary.IndependentSuccess.Value != 0 || math.Abs(*m.Repeated.IndependentSuccess.Value-1.0/3) > 1e-12 || *m.Repeated.FalseVerified.Value != 2.0/3 || *m.Repeated.FalseSuccess.Value != 2.0/3 || *m.Repeated.SolutionCostPerIndependentSuccess != 60 {
		t.Fatal("best-run or false-verdict accounting", m)
	}
	rows = append(rows, rows[1])
	if _, err = ImportedReport(p, rows); err == nil {
		t.Fatal("duplicated successful run accepted")
	}
	empty, err := ImportedReport(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	zero := findArm(t, empty, "variant").Repeated
	if zero.SolutionCostPerIndependentSuccess != nil || zero.FalseVerified.Value != nil || zero.FalseSuccess.Value != nil || zero.Assigned != 3 || zero.Missing != 3 {
		t.Fatal("undefined rates became zero")
	}
}
func TestEvaluatorUnknownDoesNotBecomeZeroFalseVerifiedRisk(t *testing.T) {
	p := protocolFixture(1, 1)
	row := observed(p, "variant", "a", 1, c.Unknown)
	report, err := ImportedReport(p, []Observation{row})
	if err != nil {
		t.Fatal(err)
	}
	m := findArm(t, report, "variant").Repeated
	if m.FalseVerified.Denominator != 1 || m.UnknownVerifiedClaims != 1 || m.FalseVerified.Value != nil || m.FalseSuccess.Value != nil || *m.IndependentSuccess.Value != 0 {
		t.Fatal("unknown treated as correct or risk-free", m)
	}
}
func TestPreregistrationRejectsLeakageMismatchPostHocAndInvalidNumbers(t *testing.T) {
	for _, mode := range []string{"budget", "model", "privacy", "safety", "repo-leak", "future-history", "paired-without-checkpoint", "invalid-threshold", "stopping", "cost-negative"} {
		t.Run(mode, func(t *testing.T) {
			p := protocolFixture(2, 2)
			switch mode {
			case "budget":
				p.Arms[1].Conditions.BudgetDigest = c.HashBytes([]byte("larger"))
			case "model":
				p.Arms[1].Conditions.ModelProfileDigest = c.HashBytes([]byte("different"))
			case "privacy":
				p.Arms[1].Conditions.PrivacyDigest = c.HashBytes([]byte("egress"))
			case "safety":
				p.Arms[1].Conditions.SafetyBoundaryDigest = c.HashBytes([]byte("weaker"))
			case "repo-leak":
				p.Tasks[1].RepositoryDigest = p.Tasks[0].RepositoryDigest
				p.Tasks[1].Split = SplitDevelopment
			case "future-history":
				p.Tasks[0].HistoryCutoffUTC = "2026-10-02T00:00:00Z"
			case "paired-without-checkpoint":
				p.Kind = "PAIRED_CONTINUATION"
			case "invalid-threshold":
				p.MaxLatencyPPM = 0
			case "stopping":
				p.StoppingRule = "STOP_WHEN_ONE_RUN_PASSES"
			case "cost-negative":
				row := observed(p, "variant", "a", 1, c.Pass)
				row.Cost.WorkMoney.Value = -1
				if _, err := ImportedReport(p, []Observation{row}); err == nil {
					t.Fatal("negative cost")
				}
				return
			}
			if p.Validate() == nil {
				t.Fatal("invalid preregistration accepted")
			}
		})
	}
	p := protocolFixture(1, 1)
	row := observed(p, "variant", "a", 1, c.Pass)
	row.ProtocolDigest = c.HashBytes([]byte("posthoc"))
	if _, err := ImportedReport(p, []Observation{row}); err == nil {
		t.Fatal("posthoc protocol accepted")
	}
}
func TestPairedRepoUncertaintyIsDeterministicAndCannotAuthorizeImportedEvidence(t *testing.T) {
	p := protocolFixture(6, 3)
	rows := []Observation{}
	for _, task := range p.Tasks {
		for repeat := 1; repeat <= p.Repeats; repeat++ {
			rows = append(rows, observed(p, "base", task.ID, repeat, c.FailVerdict), observed(p, "variant", task.ID, repeat, c.Pass))
		}
	}
	report, err := ImportedReport(p, rows)
	if err != nil {
		t.Fatal(err)
	}
	estimate := report.Splits[0].Comparisons[0].Repeated
	if estimate.SuccessInterval == nil || estimate.SuccessInterval.Lower != 1 || estimate.RepositoryClusters != 6 || !estimate.MetadataSupportsTarget || report.Splits[0].Comparisons[0].AdoptionAllowed {
		t.Fatal(estimate)
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	reordered, err := ImportedReport(p, rows)
	if err != nil || !reflect.DeepEqual(reordered, report) {
		t.Fatal("row order changed metrics")
	}
	// A safety failure in the control cohort must block target support.
	p.Tasks[5].Cohort = "CONTROL"
	p.Tasks[5].ExpectedHandling = "SAFE_BLOCK"
	rows = nil
	for _, task := range p.Tasks {
		for repeat := 1; repeat <= p.Repeats; repeat++ {
			for _, arm := range []string{"base", "variant"} {
				row := observed(p, arm, task.ID, repeat, c.Pass)
				if task.Cohort == "CONTROL" {
					row.Handling = "SAFE_BLOCK"
					row.Outcome = "BLOCKED"
					row.Claim = nil
					row.SafetyVerdict = c.FailVerdict
				}
				rows = append(rows, row)
			}
		}
	}
	bad, err := ImportedReport(p, rows)
	if err != nil {
		t.Fatal(err)
	}
	comparison := bad.Splits[0].Comparisons[0].Repeated
	if comparison.MetadataSupportsTarget || comparison.SafetyFailureRuns != 6 {
		t.Fatal("control safety failure ignored", comparison)
	}
}
