// Package evaluation implements preregistered, bounded offline benchmark
// accounting. Imported measurements are not attested evaluator evidence.
package evaluation

import (
	c "github.com/ixayldz/Viber/internal/contracts"

	"regexp"
	"strings"
	"time"
)

const ProtocolVersion = "VIBER_PAIRED_EVAL_V1"
const SplitDevelopment = "DEVELOPMENT"
const SplitValidation = "VALIDATION"
const SplitHoldout = "HOLDOUT"

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

type Task struct {
	ContinuationCheckpoint string `json:"continuation_checkpoint,omitempty"`
	ID                     string `json:"id"`
	RepositoryDigest       string `json:"repository_digest"`
	Split                  string `json:"split"`
	Cohort                 string `json:"cohort"`
	RequirementsDigest     string `json:"requirements_digest"`
	InitialSnapshot        string `json:"initial_snapshot"`
	AcceptanceDigest       string `json:"acceptance_digest"`
	CheckSetDigest         string `json:"check_set_digest"`
	StartUTC               string `json:"start_utc"`
	HistoryCutoffUTC       string `json:"history_cutoff_utc"`
	ExpectedHandling       string `json:"expected_handling,omitempty"`
}
type Conditions struct {
	Provider               string `json:"provider"`
	ModelProfileDigest     string `json:"model_profile_digest"`
	DecodingDigest         string `json:"decoding_digest"`
	ToolCapabilitiesDigest string `json:"tool_capabilities_digest"`
	EnvironmentDigest      string `json:"environment_digest"`
	BudgetDigest           string `json:"budget_digest"`
	PrivacyDigest          string `json:"privacy_digest"`
	SafetyBoundaryDigest   string `json:"safety_boundary_digest"`
	AdapterDigest          string `json:"adapter_digest"`
	PromptDigest           string `json:"prompt_digest"`
	KernelDigest           string `json:"kernel_digest"`
	DependencyDigest       string `json:"dependency_digest"`
	PricingDigest          string `json:"pricing_digest"`
	Currency               string `json:"currency"`
}
type Arm struct {
	ID            string     `json:"id"`
	HarnessDigest string     `json:"harness_digest"`
	Conditions    Conditions `json:"conditions"`
}
type Comparison struct {
	ID       string `json:"id"`
	Baseline string `json:"baseline"`
	Variant  string `json:"variant"`
}
type Protocol struct {
	SchemaVersion         int          `json:"schema_version"`
	Version               string       `json:"version"`
	ID                    string       `json:"id"`
	Kind                  string       `json:"kind"`
	DatasetDigest         string       `json:"dataset_digest"`
	EvaluatorDigest       string       `json:"evaluator_digest"`
	Repeats               int          `json:"repeats"`
	PrimaryRepeat         int          `json:"primary_repeat"`
	Seed                  uint64       `json:"seed"`
	BootstrapSamples      int          `json:"bootstrap_samples"`
	UncertaintyMethod     string       `json:"uncertainty_method"`
	Confidence            string       `json:"confidence"`
	StoppingRule          string       `json:"stopping_rule"`
	HoldoutRule           string       `json:"holdout_rule"`
	MinRepositoryClusters int          `json:"min_repository_clusters"`
	MaxLatencyPPM         int64        `json:"max_latency_ratio_ppm"`
	MaxReworkPPM          int64        `json:"max_rework_ratio_ppm"`
	Tasks                 []Task       `json:"tasks"`
	Arms                  []Arm        `json:"arms"`
	Comparisons           []Comparison `json:"comparisons"`
}

func safeHandling(value string) bool {
	return value == "CLARIFY" || value == "SAFE_BLOCK" || value == "UNKNOWN" || value == "AUTHORIZED_REPAIR"
}
func (conditions Conditions) Validate() error {
	switch conditions.Provider {
	case "fixture", "ollama", "chatgpt", "openai", "anthropic", "external-product":
	default:
		return c.Fail(c.InvalidArgument, "unknown evaluation provider class")
	}
	for _, digest := range []string{conditions.ModelProfileDigest, conditions.DecodingDigest, conditions.ToolCapabilitiesDigest, conditions.EnvironmentDigest, conditions.BudgetDigest, conditions.PrivacyDigest, conditions.SafetyBoundaryDigest, conditions.AdapterDigest, conditions.PromptDigest, conditions.KernelDigest, conditions.DependencyDigest, conditions.PricingDigest} {
		if !c.ValidDigest(digest) {
			return c.Fail(c.InvalidArgument, "evaluation conditions require immutable digests")
		}
	}
	if len(conditions.Currency) != 3 || conditions.Currency != strings.ToUpper(conditions.Currency) {
		return c.Fail(c.InvalidArgument, "explicit evaluation currency required")
	}
	for _, r := range conditions.Currency {
		if r < 'A' || r > 'Z' {
			return c.Fail(c.InvalidArgument, "invalid evaluation currency")
		}
	}
	return nil
}
func (p Protocol) Validate() error {
	if p.Seed > uint64(1<<63-1) || p.SchemaVersion != 1 || p.Version != ProtocolVersion || !identifier.MatchString(p.ID) || p.Kind != "HARNESS_ABLATION" && p.Kind != "PRODUCT_COMPARISON" && p.Kind != "PAIRED_CONTINUATION" || !c.ValidDigest(p.DatasetDigest) || !c.ValidDigest(p.EvaluatorDigest) || p.Repeats < 1 || p.Repeats > 10 || p.PrimaryRepeat < 1 || p.PrimaryRepeat > p.Repeats || p.BootstrapSamples < 1000 || p.BootstrapSamples > 10000 || p.UncertaintyMethod != "REPOSITORY_CLUSTER_BOOTSTRAP_PCG_V1" || p.Confidence != "0.95" || p.StoppingRule != "FIXED_ASSIGNMENT_NO_BEST_RUN" || p.HoldoutRule != "REPOSITORY_DISJOINT_HISTORY_CUTOFF" || p.MinRepositoryClusters < 5 || p.MinRepositoryClusters > 1000 || p.MaxLatencyPPM < 1000000 || p.MaxLatencyPPM > 10000000 || p.MaxReworkPPM < 1000000 || p.MaxReworkPPM > 10000000 || len(p.Tasks) < 1 || len(p.Tasks) > 1000 || len(p.Arms) < 1 || len(p.Arms) > 8 || len(p.Comparisons) > 8 || len(p.Tasks)*len(p.Arms)*p.Repeats > 10000 {
		return c.Fail(c.InvalidArgument, "invalid preregistered evaluation protocol")
	}
	tasks := map[string]bool{}
	repositories := map[string]string{}
	for _, task := range p.Tasks {
		if !identifier.MatchString(task.ID) || tasks[task.ID] || task.Split != SplitDevelopment && task.Split != SplitValidation && task.Split != SplitHoldout || task.Cohort != "SOLUTION" && task.Cohort != "CONTROL" || task.Cohort == "CONTROL" && !safeHandling(task.ExpectedHandling) || task.Cohort == "SOLUTION" && task.ExpectedHandling != "" {
			return c.Fail(c.InvalidArgument, "invalid evaluation task assignment")
		}
		for _, digest := range []string{task.RepositoryDigest, task.RequirementsDigest, task.InitialSnapshot, task.AcceptanceDigest, task.CheckSetDigest} {
			if !c.ValidDigest(digest) {
				return c.Fail(c.InvalidArgument, "evaluation task binding missing")
			}
		}
		if p.Kind == "PAIRED_CONTINUATION" && !c.ValidDigest(task.ContinuationCheckpoint) || p.Kind != "PAIRED_CONTINUATION" && task.ContinuationCheckpoint != "" {
			return c.Fail(c.InvalidArgument, "paired continuation requires its preregistered frozen checkpoint")
		}
		start, err := time.Parse(time.RFC3339, task.StartUTC)
		if err != nil || start.Location() != time.UTC {
			return c.Fail(c.InvalidArgument, "evaluation start must be UTC")
		}
		cutoff, err := time.Parse(time.RFC3339, task.HistoryCutoffUTC)
		if err != nil || cutoff.Location() != time.UTC || cutoff.After(start) {
			return c.Fail(c.PolicyDenied, "history crosses evaluation cutoff")
		}
		if prior := repositories[task.RepositoryDigest]; prior != "" && prior != task.Split {
			return c.Fail(c.PolicyDenied, "repository leaks between development/validation/holdout")
		}
		repositories[task.RepositoryDigest] = task.Split
		tasks[task.ID] = true
	}
	arms := map[string]Arm{}
	for _, arm := range p.Arms {
		if !identifier.MatchString(arm.ID) || !c.ValidDigest(arm.HarnessDigest) || arm.Conditions.Validate() != nil {
			return c.Fail(c.InvalidArgument, "invalid evaluation arm")
		}
		if _, ok := arms[arm.ID]; ok {
			return c.Fail(c.InvalidArgument, "duplicate evaluation arm")
		}
		arms[arm.ID] = arm
	}
	comparisons := map[string]bool{}
	for _, comparison := range p.Comparisons {
		base, bOK := arms[comparison.Baseline]
		variant, vOK := arms[comparison.Variant]
		if !identifier.MatchString(comparison.ID) || comparisons[comparison.ID] || !bOK || !vOK || comparison.Baseline == comparison.Variant {
			return c.Fail(c.InvalidArgument, "invalid preregistered paired comparison")
		}
		a, b := base.Conditions, variant.Conditions
		if a.SafetyBoundaryDigest != b.SafetyBoundaryDigest || a.EnvironmentDigest != b.EnvironmentDigest || a.PrivacyDigest != b.PrivacyDigest || a.Currency != b.Currency {
			return c.Fail(c.PolicyDenied, "safety/evaluator/effect boundaries cannot be ablated")
		}
		if p.Kind != "PRODUCT_COMPARISON" && (a.Provider != b.Provider || a.ModelProfileDigest != b.ModelProfileDigest || a.DecodingDigest != b.DecodingDigest || a.ToolCapabilitiesDigest != b.ToolCapabilitiesDigest || a.BudgetDigest != b.BudgetDigest || a.AdapterDigest != b.AdapterDigest || a.PricingDigest != b.PricingDigest || a.DependencyDigest != b.DependencyDigest) {
			return c.Fail(c.PolicyDenied, "harness attribution requires matched model/tools/environment/budget conditions")
		}
		comparisons[comparison.ID] = true
	}
	return nil
}
func (p Protocol) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	return c.Digest(p)
}
func AssignmentID(protocolDigest, arm, task string, repeat int) string {
	digest, _ := c.Digest(struct {
		Protocol, Arm, Task string
		Repeat              int
	}{protocolDigest, arm, task, repeat})
	return digest
}
