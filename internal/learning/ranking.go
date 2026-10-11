// Package learning implements explicitly enabled data-only advisory ranking.
// It has no policy, tool, verification or state-transition authority.
package learning

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/retrieval"
)

const Approval = "I_REVIEWED_HELDOUT_LABELS_AND_APPROVE_ADVISORY_RANKING_ONLY"
const MaxEvents = 256

func validID(value string) bool {
	if len(value) < 1 || len(value) > 80 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

type Definition struct {
	SchemaVersion  int               `json:"schema_version"`
	ID             string            `json:"id"`
	Version        int               `json:"version"`
	SourceDigest   string            `json:"source_snapshot_digest"`
	TrainingDigest string            `json:"training_dataset_digest"`
	ProtocolDigest string            `json:"protocol_digest"`
	Weights        retrieval.Weights `json:"weights"`
	License        string            `json:"license"`
}

func (d Definition) Validate() error {
	if d.SchemaVersion != 1 || !validID(d.ID) || d.Version < 1 || d.Version > 1000000 || !c.ValidDigest(d.SourceDigest) || !c.ValidDigest(d.TrainingDigest) || !c.ValidDigest(d.ProtocolDigest) || len(d.License) < 1 || len(d.License) > 80 || strings.ContainsAny(d.License, "\x00\r\n") {
		return c.Fail(c.InvalidArgument, "invalid scoped ranking definition")
	}
	return d.Weights.Validate()
}

type Case struct {
	ID            string   `json:"id"`
	Query         string   `json:"query"`
	Intent        string   `json:"intent"`
	RelevantPaths []string `json:"reviewed_relevant_paths"`
}
type Dataset struct {
	Snapshot              c.Snapshot           `json:"snapshot"`
	SchemaVersion         int                  `json:"schema_version"`
	Split                 string               `json:"split"`
	SourceDigest          string               `json:"source_snapshot_digest"`
	ProtocolDigest        string               `json:"protocol_digest"`
	Documents             []retrieval.Document `json:"documents"`
	Cases                 []Case               `json:"cases"`
	LabelProvenanceDigest string               `json:"label_provenance_digest"`
}
type Evaluation struct {
	SchemaVersion         int      `json:"schema_version"`
	DefinitionDigest      string   `json:"definition_digest"`
	DatasetDigest         string   `json:"dataset_digest"`
	ProtocolDigest        string   `json:"protocol_digest"`
	SourceDigest          string   `json:"source_snapshot_digest"`
	LabelProvenanceDigest string   `json:"label_provenance_digest"`
	EvaluatorDigest       string   `json:"evaluator_executable_digest"`
	RetrievalVersion      string   `json:"retrieval_version"`
	Cases                 int      `json:"cases"`
	BaselineMRRMicros     int64    `json:"baseline_mrr_micros"`
	CandidateMRRMicros    int64    `json:"candidate_mrr_micros"`
	Regressions           int      `json:"regressed_cases"`
	BaselineP95Micros     int64    `json:"baseline_p95_micros"`
	CandidateP95Micros    int64    `json:"candidate_p95_micros"`
	Eligible              bool     `json:"eligible_for_operator_review"`
	Trust                 string   `json:"trust"`
	TrainingReceiptDigest string   `json:"training_receipt_digest"`
	QueryDigests          []string `json:"query_digests"`
}
type Event struct {
	SchemaVersion    int    `json:"schema_version"`
	Sequence         int    `json:"sequence"`
	Previous         string `json:"previous_digest"`
	Action           string `json:"action"`
	DefinitionDigest string `json:"definition_digest"`
	EvaluationDigest string `json:"evaluation_digest"`
	Mode             string `json:"mode"`
	Approval         string `json:"approval,omitempty"`
}
type Status struct {
	Events           int    `json:"events"`
	HeadDigest       string `json:"head_digest"`
	Mode             string `json:"mode"`
	DefinitionDigest string `json:"definition_digest"`
	EvaluationDigest string `json:"evaluation_digest"`
}
type Binding struct {
	Directory        string     `json:"directory"`
	PhysicalRoot     string     `json:"physical_root"`
	Definition       Definition `json:"definition"`
	DefinitionDigest string     `json:"definition_digest"`
}

func (b Binding) Validate() error {
	digest, err := c.Digest(b.Definition)
	if err != nil || b.Definition.Validate() != nil || !filepath.IsAbs(b.Directory) || b.PhysicalRoot == "" || digest != b.DefinitionDigest {
		return c.Fail(c.StoreIntegrityError, "invalid retained ranking binding")
	}
	return nil
}

type Registry struct {
	root      *os.Root
	lock      *os.File
	directory string
}

func Open(directory string, create bool) (*Registry, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if create {
		if err = os.Mkdir(absolute, 0700); err != nil {
			return nil, err
		}
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*Registry, error) { root.Close(); return nil, e }
	identity, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return fail(err)
	}
	marker := struct {
		SchemaVersion int    `json:"schema_version"`
		PhysicalRoot  string `json:"physical_root"`
	}{1, identity}
	if !create {
		raw, e := fileguard.ReadRegular(root, "registry.json", 4096)
		if e != nil {
			return fail(c.Fail(c.StaleAuthority, "ranking registry marker unavailable"))
		}
		var saved struct {
			SchemaVersion int    `json:"schema_version"`
			PhysicalRoot  string `json:"physical_root"`
		}
		if c.DecodeStrict(raw, &saved) != nil || saved != marker {
			return fail(c.Fail(c.StaleAuthority, "ranking registry physical marker changed"))
		}
	}
	if err = fileguard.Private(root); err != nil {
		return fail(err)
	}
	lock, err := fileguard.Lock(root, "registry.lock")
	if err != nil {
		return fail(err)
	}
	registry := &Registry{root: root, lock: lock, directory: absolute}
	if create {
		if err = registry.write("registry.json", marker); err != nil {
			registry.Close()
			return nil, err
		}
	}
	if _, err = registry.Status(); err != nil {
		registry.Close()
		return nil, err
	}
	return registry, nil
}
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.lock.Close()
	return r.root.Close()
}
func (r *Registry) identity() (string, error) { return fileguard.DirectoryIdentity(r.root) }
func (r *Registry) write(name string, v any) error {
	raw, err := c.CanonicalV1(v)
	if err != nil {
		return err
	}
	return fileguard.Publish(r.root, name, raw)
}
func (r *Registry) read(name string, v any) error {
	raw, err := fileguard.ReadRegular(r.root, name, 1<<20)
	if err != nil {
		return err
	}
	return c.DecodeStrict(raw, v)
}
func (r *Registry) Status() (Status, error) {
	status := Status{Mode: "OFF"}
	directory, err := r.root.Open(".")
	if err != nil {
		return status, err
	}
	entries, err := directory.ReadDir(MaxEvents*4 + 2)
	directory.Close()
	if err != nil {
		return status, err
	}
	names := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "event-") {
			names = append(names, name)
		} else if name != "registry.lock" && name != "registry.json" && !(strings.HasPrefix(name, "definition-") || strings.HasPrefix(name, "evaluation-") || strings.HasPrefix(name, "started-") || strings.HasPrefix(name, "training-")) {
			return status, c.Fail(c.StoreIntegrityError, "unknown ranking registry member")
		}
	}
	if len(names) > MaxEvents || len(entries) > MaxEvents*4+2 {
		return status, c.Fail(c.BudgetLimitReached, "ranking registry lifecycle bound reached")
	}
	sort.Strings(names)
	for i, name := range names {
		if name != fmt.Sprintf("event-%06d.json", i+1) {
			return status, c.Fail(c.StoreIntegrityError, "ranking lifecycle sequence gap")
		}
		var event Event
		if err = r.read(name, &event); err != nil {
			return status, err
		}
		if event.SchemaVersion != 1 || event.Sequence != i+1 || event.Previous != status.HeadDigest {
			return status, c.Fail(c.StoreIntegrityError, "ranking lifecycle hash-chain mismatch")
		}
		switch event.Action {
		case "EVALUATED":
			if event.Mode != "OFF" || !c.ValidDigest(event.DefinitionDigest) || !c.ValidDigest(event.EvaluationDigest) {
				return status, c.Fail(c.StoreIntegrityError, "invalid ranking evaluation event")
			}
		case "SELECT":
			if event.Approval != Approval || event.Mode != "SHADOW" && event.Mode != "CANARY" && event.Mode != "ACTIVE" {
				return status, c.Fail(c.PolicyDenied, "ranking activation lacks explicit bounded approval")
			}
			def, eval, e := r.pair(event.DefinitionDigest, event.EvaluationDigest)
			if e != nil {
				return status, e
			}
			if !eval.Eligible || eval.SourceDigest != def.SourceDigest {
				return status, c.Fail(c.PolicyDenied, "ranking selection lacks eligible exact evaluation")
			}
			if event.Mode != "SHADOW" && (status.DefinitionDigest != event.DefinitionDigest || status.EvaluationDigest != event.EvaluationDigest || status.Mode != "SHADOW" && status.Mode != "CANARY") {
				return status, c.Fail(c.PolicyDenied, "ranking promotion must pass explicit shadow selection first")
			}
			status.Mode = event.Mode
			status.DefinitionDigest = event.DefinitionDigest
			status.EvaluationDigest = event.EvaluationDigest
		case "DISABLE":
			if event.Mode != "OFF" || event.DefinitionDigest != "" || event.EvaluationDigest != "" {
				return status, c.Fail(c.StoreIntegrityError, "invalid ranking kill switch")
			}
			status.Mode = "OFF"
			status.DefinitionDigest = ""
			status.EvaluationDigest = ""
		default:
			return status, c.Fail(c.StoreIntegrityError, "unknown ranking lifecycle action")
		}
		status.Events = i + 1
		status.HeadDigest, err = c.Digest(event)
		if err != nil {
			return status, err
		}
	}
	return status, nil
}
func (r *Registry) pair(definitionDigest, evaluationDigest string) (Definition, Evaluation, error) {
	var definition Definition
	var evaluation Evaluation
	if !c.ValidDigest(definitionDigest) || !c.ValidDigest(evaluationDigest) {
		return definition, evaluation, c.Fail(c.InvalidArgument, "exact ranking receipt digests required")
	}
	if err := r.read("definition-"+definitionDigest+".json", &definition); err != nil {
		return definition, evaluation, err
	}
	digest, _ := c.Digest(definition)
	if digest != definitionDigest || definition.Validate() != nil {
		return definition, evaluation, c.Fail(c.StoreIntegrityError, "ranking definition changed")
	}
	if err := r.read("evaluation-"+evaluationDigest+".json", &evaluation); err != nil {
		return definition, evaluation, err
	}
	digest, _ = c.Digest(evaluation)
	if digest != evaluationDigest || evaluation.SchemaVersion != 1 || evaluation.DefinitionDigest != definitionDigest || evaluation.ProtocolDigest != definition.ProtocolDigest || evaluation.SourceDigest != definition.SourceDigest || evaluation.DatasetDigest == definition.TrainingDigest || evaluation.RetrievalVersion != retrieval.Version || !c.ValidDigest(evaluation.EvaluatorDigest) || !c.ValidDigest(evaluation.LabelProvenanceDigest) || !c.ValidDigest(evaluation.DatasetDigest) || evaluation.Cases < 1 || evaluation.Cases > 256 || evaluation.BaselineMRRMicros < 0 || evaluation.BaselineMRRMicros > 1000000 || evaluation.CandidateMRRMicros < 0 || evaluation.CandidateMRRMicros > 1000000 || evaluation.Eligible != (evaluation.Cases >= 16 && evaluation.Regressions == 0 && evaluation.CandidateMRRMicros >= evaluation.BaselineMRRMicros) {
		return definition, evaluation, c.Fail(c.StoreIntegrityError, "ranking evaluation binding or eligibility changed")
	}
	if len(evaluation.QueryDigests) != evaluation.Cases {
		return definition, evaluation, c.Fail(c.StoreIntegrityError, "heldout query inventory changed")
	}
	_, trainingDigest, err := r.training(definition, evaluation.QueryDigests)
	if err != nil {
		return definition, evaluation, err
	}
	if trainingDigest != evaluation.TrainingReceiptDigest {
		return definition, evaluation, c.Fail(c.StoreIntegrityError, "heldout training binding changed")
	}
	return definition, evaluation, nil
}
func (r *Registry) append(action, definitionDigest, evaluationDigest, mode, approval string) (Status, error) {
	status, err := r.Status()
	if err != nil {
		return status, err
	}
	if status.Events >= MaxEvents {
		return status, c.Fail(c.BudgetLimitReached, "ranking lifecycle capacity reached")
	}
	event := Event{1, status.Events + 1, status.HeadDigest, action, definitionDigest, evaluationDigest, mode, approval}
	if err = r.write(fmt.Sprintf("event-%06d.json", event.Sequence), event); err != nil {
		return status, err
	}
	return r.Status()
}
func (r *Registry) Select(definitionDigest, evaluationDigest, mode, approval string) (Status, error) {
	status, err := r.Status()
	if err != nil {
		return status, err
	}
	_, eval, err := r.pair(definitionDigest, evaluationDigest)
	if err != nil {
		return status, err
	}
	if approval != Approval || !eval.Eligible || mode != "SHADOW" && mode != "CANARY" && mode != "ACTIVE" || mode != "SHADOW" && (status.Mode != "SHADOW" && status.Mode != "CANARY" || status.DefinitionDigest != definitionDigest || status.EvaluationDigest != evaluationDigest) {
		return status, c.Fail(c.PolicyDenied, "reviewed heldout evaluation and ordered shadow/canary promotion required")
	}
	return r.append("SELECT", definitionDigest, evaluationDigest, mode, approval)
}
func (r *Registry) Disable() (Status, error) { return r.append("DISABLE", "", "", "OFF", "") }
func (r *Registry) Bind() (Binding, error) {
	status, err := r.Status()
	if err != nil {
		return Binding{}, err
	}
	if status.Mode == "OFF" {
		return Binding{}, c.Fail(c.PolicyDenied, "ranking is disabled")
	}
	def, _, err := r.pair(status.DefinitionDigest, status.EvaluationDigest)
	if err != nil {
		return Binding{}, err
	}
	identity, err := r.identity()
	return Binding{r.directory, identity, def, status.DefinitionDigest}, err
}

type Decision struct {
	Mode             string `json:"mode"`
	DefinitionDigest string `json:"definition_digest"`
	LifecycleDigest  string `json:"lifecycle_digest"`
	UseCandidate     bool   `json:"use_candidate"`
	Reason           string `json:"reason"`
}

func (b Binding) Current(sourceDigest, query string) (Decision, error) {
	decision := Decision{Mode: "OFF", DefinitionDigest: b.DefinitionDigest, Reason: "DISABLED_OR_STALE_SOURCE"}
	if err := b.Validate(); err != nil {
		return decision, err
	}
	r, err := Open(b.Directory, false)
	if err != nil {
		return decision, c.Fail(c.StaleAuthority, "ranking registry unavailable")
	}
	defer r.Close()
	identity, err := r.identity()
	if err != nil || identity != b.PhysicalRoot {
		return decision, c.Fail(c.StaleAuthority, "ranking registry physical identity changed")
	}
	status, err := r.Status()
	if err != nil {
		return decision, err
	}
	decision.LifecycleDigest = status.HeadDigest
	if sourceDigest != b.Definition.SourceDigest || status.DefinitionDigest != b.DefinitionDigest || status.Mode == "OFF" {
		return decision, nil
	}
	decision.Mode = status.Mode
	decision.Reason = "ADVISORY_ONLY_EXACT_SOURCE"
	bucket := c.HashBytes([]byte(query))
	decision.UseCandidate = status.Mode == "ACTIVE" || status.Mode == "CANARY" && strings.ContainsRune("0123", rune(bucket[0]))
	return decision, nil
}
func (r *Registry) Evaluate(ctx context.Context, definition Definition, dataset Dataset, reviewedDigest string) (Evaluation, error) {
	var result Evaluation
	if err := definition.Validate(); err != nil {
		return result, err
	}
	datasetDigest, queryDigests, err := validateDataset(dataset, "HOLDOUT", reviewedDigest, definition.SourceDigest, definition.ProtocolDigest)
	if err != nil {
		return result, err
	}
	if datasetDigest == definition.TrainingDigest {
		return result, c.Fail(c.PolicyDenied, "training and holdout must be separate frozen datasets")
	}
	_, trainingDigest, err := r.training(definition, queryDigests)
	if err != nil {
		return result, err
	}
	status, err := r.Status()
	if err != nil {
		return result, err
	}
	if status.Events >= MaxEvents {
		return result, c.Fail(c.BudgetLimitReached, "ranking lifecycle capacity reached")
	}
	definitionDigest, _ := c.Digest(definition)
	// Single-use immutable STARTED prevents reruns selectively replacing failures.
	startName := "started-" + definitionDigest + "-" + datasetDigest + ".json"
	if err = r.write(startName, struct{ Definition, Dataset string }{definitionDigest, datasetDigest}); err != nil {
		return result, err
	}
	binding := retrieval.Binding{Candidate: dataset.SourceDigest, Policy: definition.ProtocolDigest}
	index, err := retrieval.New(ctx, binding, dataset.Documents)
	if err != nil {
		return result, err
	}
	defer index.Close()
	exe, err := os.Executable()
	if err != nil {
		return result, err
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		return result, err
	}
	result = Evaluation{SchemaVersion: 1, DefinitionDigest: definitionDigest, DatasetDigest: datasetDigest, ProtocolDigest: definition.ProtocolDigest, SourceDigest: definition.SourceDigest, LabelProvenanceDigest: dataset.LabelProvenanceDigest, EvaluatorDigest: c.HashBytes(binary), RetrievalVersion: retrieval.Version, Cases: len(dataset.Cases), Trust: "OPERATOR_REVIEWED_LABELS; NOT_INDEPENDENT_PRODUCT_QUALITY_OR_REFERENCE_PERFORMANCE", TrainingReceiptDigest: trainingDigest, QueryDigests: queryDigests}
	baselineTimes, candidateTimes := []int64{}, []int64{}
	for _, item := range dataset.Cases {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		search := func(weights retrieval.Weights) (int64, int64, error) {
			began := time.Now()
			hits := []retrieval.Hit{}
			offset := 0
			for {
				page, e := index.SearchWeighted(ctx, binding, item.Query, item.Intent, offset, 128, weights)
				if e != nil {
					return 0, 0, e
				}
				hits = append(hits, page.Hits...)
				offset += len(page.Hits)
				if offset >= page.Total {
					break
				}
			}
			relevant := map[string]bool{}
			for _, path := range item.RelevantPaths {
				relevant[path] = true
			}
			score := int64(0)
			for rank, hit := range hits {
				if relevant[hit.Path] {
					score = 1000000 / int64(rank+1)
					break
				}
			}
			return score, time.Since(began).Microseconds(), nil
		}
		baseline, latency, e := search(retrieval.Weights{BM25: 1, Trigram: 1})
		if e != nil {
			return result, e
		}
		baselineTimes = append(baselineTimes, latency)
		candidate, latency, e := search(definition.Weights)
		if e != nil {
			return result, e
		}
		candidateTimes = append(candidateTimes, latency)
		result.BaselineMRRMicros += baseline
		result.CandidateMRRMicros += candidate
		if candidate < baseline {
			result.Regressions++
		}
	}
	result.BaselineMRRMicros /= int64(result.Cases)
	result.CandidateMRRMicros /= int64(result.Cases)
	p95 := func(samples []int64) int64 {
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return samples[(95*len(samples)+99)/100-1]
	}
	result.BaselineP95Micros = p95(baselineTimes)
	result.CandidateP95Micros = p95(candidateTimes)
	result.Eligible = result.Cases >= 16 && result.Regressions == 0 && result.CandidateMRRMicros >= result.BaselineMRRMicros
	if err = r.write("definition-"+definitionDigest+".json", definition); err != nil {
		return result, err
	}
	evaluationDigest, _ := c.Digest(result)
	if err = r.write("evaluation-"+evaluationDigest+".json", result); err != nil {
		return result, err
	}
	_, err = r.append("EVALUATED", definitionDigest, evaluationDigest, "OFF", "")
	return result, err
}
