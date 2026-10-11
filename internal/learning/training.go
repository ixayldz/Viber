package learning

import (
	"context"
	"os"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/retrieval"
	"github.com/ixayldz/Viber/internal/workspace"
)

const TrainingProtocol = "SOURCE_BOUND_LEXICAL_GRID_V1; BM25_TRIGRAM_1_2_4_8; MRR_MICROS; BASELINE_FIRST_TIES"

type TrainingScore struct {
	Weights   retrieval.Weights `json:"weights"`
	MRRMicros int64             `json:"mrr_micros"`
}

type TrainingReceipt struct {
	SchemaVersion         int               `json:"schema_version"`
	DatasetDigest         string            `json:"dataset_digest"`
	SourceDigest          string            `json:"source_snapshot_digest"`
	ProtocolDigest        string            `json:"protocol_digest"`
	LabelProvenanceDigest string            `json:"label_provenance_digest"`
	TrainerDigest         string            `json:"trainer_executable_digest"`
	RetrievalVersion      string            `json:"retrieval_version"`
	TrainingProtocol      string            `json:"training_protocol"`
	QueryDigests          []string          `json:"query_digests"`
	Scores                []TrainingScore   `json:"grid_scores"`
	Selected              retrieval.Weights `json:"selected_weights"`
	Trust                 string            `json:"trust"`
}

type TrainingResult struct {
	Definition       Definition      `json:"definition"`
	DefinitionDigest string          `json:"definition_digest"`
	Receipt          TrainingReceipt `json:"receipt"`
	ReceiptDigest    string          `json:"training_receipt_digest"`
}

// queryDigest normalizes superficial spelling differences conservatively.
// Labels are never used for split assignment or weight selection on HOLDOUT.
func queryDigest(item Case) string {
	return c.HashBytes([]byte(strings.ToLower(strings.Join(strings.Fields(item.Query), " "))))
}

func validateDataset(dataset Dataset, split, reviewedDigest, sourceDigest, protocolDigest string) (string, []string, error) {
	digest, _ := c.Digest(dataset)
	if dataset.SchemaVersion != 1 || dataset.Split != split || digest != reviewedDigest || dataset.SourceDigest != sourceDigest || dataset.ProtocolDigest != protocolDigest || !c.ValidDigest(sourceDigest) || !c.ValidDigest(protocolDigest) || !c.ValidDigest(dataset.LabelProvenanceDigest) || len(dataset.Documents) < 1 || len(dataset.Documents) > 256 || len(dataset.Cases) < 1 || len(dataset.Cases) > 256 {
		return "", nil, c.Fail(c.PolicyDenied, "exact separately reviewed source-bound dataset required")
	}
	captured := workspace.Capture{Snapshot: dataset.Snapshot, Contents: map[string][]byte{}}
	paths := map[string]bool{}
	sourceBytes := 0
	for _, doc := range dataset.Documents {
		sourceBytes += len(doc.Bytes)
		if sourceBytes > 4<<20 || !policy.SafePath(doc.Path) || paths[doc.Path] || doc.Digest != c.HashBytes(doc.Bytes) {
			return "", nil, c.Fail(c.InvalidArgument, "ranking dataset source bound exceeded or bytes changed")
		}
		paths[doc.Path] = true
		captured.Contents[doc.Path] = doc.Bytes
	}
	if captured.Snapshot.Digest != sourceDigest || workspace.VerifyCapture(captured) != nil {
		return "", nil, c.Fail(c.StoreIntegrityError, "ranking bytes are not the declared exact source snapshot")
	}
	seen := map[string]bool{}
	queries := map[string]bool{}
	digests := []string{}
	for _, item := range dataset.Cases {
		key := queryDigest(item)
		if !validID(item.ID) || seen[item.ID] || queries[key] || len(item.RelevantPaths) < 1 || len(item.RelevantPaths) > 256 || len(item.Query) > 256 || strings.TrimSpace(item.Query) == "" || !retrieval.ValidIntent(item.Intent) {
			return "", nil, c.Fail(c.InvalidArgument, "invalid ranking case identity, query or labels")
		}
		seen[item.ID], queries[key] = true, true
		digests = append(digests, key)
		labels := map[string]bool{}
		for _, path := range item.RelevantPaths {
			if !paths[path] || labels[path] {
				return "", nil, c.Fail(c.InvalidArgument, "ranking labels require unique actual source paths")
			}
			labels[path] = true
		}
	}
	sort.Strings(digests)
	return digest, digests, nil
}

func searchScore(ctx context.Context, index *retrieval.Index, binding retrieval.Binding, item Case, weights retrieval.Weights) (int64, error) {
	labels := map[string]bool{}
	for _, path := range item.RelevantPaths {
		labels[path] = true
	}
	offset := 0
	for {
		page, err := index.SearchWeighted(ctx, binding, item.Query, item.Intent, offset, 128, weights)
		if err != nil {
			return 0, err
		}
		for rank, hit := range page.Hits {
			if labels[hit.Path] {
				return 1000000 / int64(offset+rank+1), nil
			}
		}
		offset += len(page.Hits)
		if offset >= page.Total {
			return 0, nil
		}
		if len(page.Hits) == 0 {
			return 0, c.Fail(c.StoreIntegrityError, "ranking search pagination made no progress")
		}
	}
}

// Train performs the complete bounded grid on a frozen DEV split. It changes
// only two advisory lexical weights; no policy, verifier or execution behavior
// can be learned through this API. Source bytes remain in the caller's input.
func (r *Registry) Train(ctx context.Context, id string, version int, license string, dataset Dataset, reviewedDigest string) (TrainingResult, error) {
	var result TrainingResult
	digest, queries, err := validateDataset(dataset, "DEV", reviewedDigest, dataset.SourceDigest, dataset.ProtocolDigest)
	if err != nil {
		return result, err
	}
	definition := Definition{SchemaVersion: 1, ID: id, Version: version, License: license, SourceDigest: dataset.SourceDigest, ProtocolDigest: dataset.ProtocolDigest, TrainingDigest: digest, Weights: retrieval.Weights{BM25: 1, Trigram: 1}}
	if err = definition.Validate(); err != nil {
		return result, err
	}
	status, err := r.Status()
	if err != nil {
		return result, err
	}
	if status.Events >= MaxEvents {
		return result, c.Fail(c.BudgetLimitReached, "ranking lifecycle capacity reached")
	}
	if err = r.write("training-started-"+digest+".json", struct{ Dataset, Protocol string }{digest, TrainingProtocol}); err != nil {
		return result, err
	}
	binding := retrieval.Binding{Candidate: dataset.SourceDigest, Policy: dataset.ProtocolDigest}
	index, err := retrieval.New(ctx, binding, dataset.Documents)
	if err != nil {
		return result, err
	}
	defer index.Close()
	executable, err := os.Executable()
	if err != nil {
		return result, err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return result, err
	}
	receipt := TrainingReceipt{SchemaVersion: 1, DatasetDigest: digest, SourceDigest: dataset.SourceDigest, ProtocolDigest: dataset.ProtocolDigest, LabelProvenanceDigest: dataset.LabelProvenanceDigest, TrainerDigest: c.HashBytes(binary), RetrievalVersion: retrieval.Version, TrainingProtocol: TrainingProtocol, QueryDigests: queries, Trust: "OPERATOR_REVIEWED_DEV_LABELS; ADVISORY_ONLY; NO_HOLDOUT_ACCESS"}
	best := int64(-1)
	for _, bm := range []int64{1, 2, 4, 8} {
		for _, trigram := range []int64{1, 2, 4, 8} {
			weights := retrieval.Weights{BM25: bm, Trigram: trigram}
			var score int64
			for _, item := range dataset.Cases {
				if err = ctx.Err(); err != nil {
					return result, err
				}
				value, e := searchScore(ctx, index, binding, item, weights)
				if e != nil {
					return result, e
				}
				score += value
			}
			score /= int64(len(dataset.Cases))
			receipt.Scores = append(receipt.Scores, TrainingScore{weights, score})
			if score > best {
				best, receipt.Selected = score, weights
			}
		}
	}
	definition.Weights = receipt.Selected
	if err = r.write("training-"+digest+".json", receipt); err != nil {
		return result, err
	}
	definitionDigest, _ := c.Digest(definition)
	receiptDigest, _ := c.Digest(receipt)
	return TrainingResult{definition, definitionDigest, receipt, receiptDigest}, nil
}

func (r *Registry) training(definition Definition, holdoutQueries []string) (TrainingReceipt, string, error) {
	var receipt TrainingReceipt
	if err := r.read("training-"+definition.TrainingDigest+".json", &receipt); err != nil {
		return receipt, "", c.Fail(c.PolicyDenied, "actual DEV training receipt required before heldout evaluation")
	}
	if receipt.SchemaVersion != 1 || receipt.DatasetDigest != definition.TrainingDigest || receipt.SourceDigest != definition.SourceDigest || receipt.ProtocolDigest != definition.ProtocolDigest || receipt.Selected != definition.Weights || receipt.TrainingProtocol != TrainingProtocol || receipt.RetrievalVersion != retrieval.Version || !c.ValidDigest(receipt.TrainerDigest) || !c.ValidDigest(receipt.LabelProvenanceDigest) || len(receipt.QueryDigests) < 1 || len(receipt.QueryDigests) > 256 || len(receipt.Scores) != 16 {
		return receipt, "", c.Fail(c.StoreIntegrityError, "training receipt bindings changed")
	}
	seen := map[string]bool{}
	for _, query := range receipt.QueryDigests {
		if !c.ValidDigest(query) || seen[query] {
			return receipt, "", c.Fail(c.StoreIntegrityError, "training query inventory invalid")
		}
		seen[query] = true
	}
	for _, query := range holdoutQueries {
		if seen[query] {
			return receipt, "", c.Fail(c.PolicyDenied, "DEV and HOLDOUT queries overlap")
		}
	}
	best := int64(-1)
	selected := retrieval.Weights{}
	for i, score := range receipt.Scores {
		grid := []int64{1, 2, 4, 8}
		if score.Weights != (retrieval.Weights{BM25: grid[i/4], Trigram: grid[i%4]}) || score.MRRMicros < 0 || score.MRRMicros > 1000000 {
			return receipt, "", c.Fail(c.StoreIntegrityError, "training grid inventory invalid")
		}
		if score.MRRMicros > best {
			best, selected = score.MRRMicros, score.Weights
		}
	}
	if selected != receipt.Selected {
		return receipt, "", c.Fail(c.StoreIntegrityError, "selected weights disagree with actual training grid")
	}
	digest, _ := c.Digest(receipt)
	return receipt, digest, nil
}
