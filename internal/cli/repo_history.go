package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/intelligence"
	"github.com/ixayldz/Viber/internal/workspace"
)

type repoHistoryOutput struct {
	History   workspace.GitHistory          `json:"history"`
	CoChanges *intelligence.CoChangeReport  `json:"co_changes,omitempty"`
	Semantic  *intelligence.SemanticRanking `json:"semantic_ranking,omitempty"`
	Notice    string                        `json:"notice"`
}

// runRepoHistory reads only captured local Git objects and current source.
// It never invokes Git or includes commit messages and author identities.
func runRepoHistory(args []string, out, errout io.Writer) int {
	f := flag.NewFlagSet("repo-history", flag.ContinueOnError)
	f.SetOutput(errout)
	root := f.String("root", ".", "source repository root")
	cutoffRaw := f.String("cutoff", "", "RFC3339 latest permitted commit timestamp, UTC normalized")
	allowHistory := f.Bool("allow-local-history", false, "allow local first-parent commit/path metadata retrieval")
	target := f.String("target", "", "current captured path for co-change ranking")
	limit := f.Int("limit", 20, "maximum co-change results (1..100)")
	query := f.String("semantic-query", "", "optional query for local loopback embedding reranking")
	allowEmbedding := f.Bool("allow-local-embedding", false, "allow sending exact candidate spans to the declared local loopback model")
	embeddingEndpoint := f.String("embedding-endpoint", "http://127.0.0.1:11434", "literal loopback Ollama origin")
	embeddingModel := f.String("embedding-model", "", "local embedding model ID")
	embeddingRevision := f.String("embedding-revision", "", "operator-declared model/weight revision")
	jsonMode := f.Bool("json", false, "emit bounded source-bound metadata")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *root == "" || *limit < 1 || *limit > 100 {
		return report(out, errout, c.Fail(c.InvalidArgument, "repository root, no extra args, and bounded result limit required"), *jsonMode)
	}
	if !*allowHistory {
		return report(out, errout, c.Fail(c.PolicyDenied, "repo-history requires explicit --allow-local-history consent"), *jsonMode)
	}
	cutoff, err := time.Parse(time.RFC3339, *cutoffRaw)
	if err != nil || *cutoffRaw == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "--cutoff must be an RFC3339 timestamp"), *jsonMode)
	}
	if *query != "" && !utf8.ValidString(*query) {
		return report(out, errout, c.Fail(c.InvalidArgument, "semantic query must be UTF-8"), *jsonMode)
	}
	if *query != "" && !*allowEmbedding {
		return report(out, errout, c.Fail(c.PolicyDenied, "semantic reranking requires explicit --allow-local-embedding consent"), *jsonMode)
	}
	if *query == "" && (*allowEmbedding || *embeddingModel != "" || *embeddingRevision != "") {
		return report(out, errout, c.Fail(c.InvalidArgument, "embedding options require --semantic-query"), *jsonMode)
	}
	if *query != "" && (*embeddingModel == "" || *embeddingRevision == "") {
		return report(out, errout, c.Fail(c.InvalidArgument, "semantic reranking requires embedding model and declared revision"), *jsonMode)
	}
	limits := workspace.DefaultHistoryLimits()
	source, err := workspace.CaptureRepository(*root, limits.CaptureLimits)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	history, err := workspace.CaptureGitHistory(source, cutoff, limits)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	result := repoHistoryOutput{History: history, Notice: "Local source-bound first-parent history only; no commit messages, author identities or source excerpts are included. Co-change is not causation. Partial coverage is not absence."}
	if *target != "" {
		coChanges, rankErr := intelligence.RankCoChanges(history, source, *target, *limit)
		if rankErr != nil {
			return report(out, errout, rankErr, *jsonMode)
		}
		result.CoChanges = &coChanges
	}
	if *query != "" {
		spans := historySourceSpans(source, history)
		if len(spans) == 0 {
			return report(out, errout, c.Fail(c.UnsupportedCapability, "captured history has no bounded text spans for semantic reranking"), *jsonMode)
		}
		ranking, rankErr := intelligence.RankCapturedSpans(context.Background(), source, *query, spans, intelligence.LocalEmbeddingOptions{
			Endpoint: *embeddingEndpoint, Model: *embeddingModel, ModelRevision: *embeddingRevision, Timeout: 20 * time.Second})
		if rankErr != nil {
			return report(out, errout, rankErr, *jsonMode)
		}
		result.Semantic = &ranking
	}
	if *jsonMode {
		if err := jsonWrite(out, result); err != nil {
			return 4
		}
		return 0
	}
	fmt.Fprintf(out, "Repository history · source %s · HEAD %s · cutoff %s\n", history.SourceSnapshotDigest, history.HeadObject, history.CutoffUTC)
	fmt.Fprintf(out, "First-parent commits %d · coverage %s · report %s\n", len(history.Commits), history.Coverage, history.ReportDigest)
	if result.CoChanges != nil {
		fmt.Fprintf(out, "Co-change candidates for %s: %d\n", result.CoChanges.TargetPath, result.CoChanges.Total)
		for _, item := range result.CoChanges.Results {
			fmt.Fprintf(out, "  %s · %d commits · last %s\n", item.Path, item.Occurrences, item.LastSeenUTC)
		}
	}
	if result.Semantic != nil {
		fmt.Fprintf(out, "Local embedding order: %d exact source spans · %s · model %s (%s)\n", len(result.Semantic.Ranked), result.Semantic.Manifest.RuntimeVersion, result.Semantic.Manifest.Model, result.Semantic.Manifest.DeclaredModelRevision)
		for _, item := range result.Semantic.Ranked {
			fmt.Fprintf(out, "  #%d %s:%d-%d · cosine %d/1000000\n", item.Rank, item.Path, item.Start, item.End, item.CosineMicros)
		}
		fmt.Fprintln(out, "Local endpoint posture is declared loopback-only and not independently attested; ranking is untrusted.")
	}
	fmt.Fprintf(out, "%s\n", result.Notice)
	for _, unknown := range history.Unknowns {
		fmt.Fprintf(out, "Unknown %s\n", unknown)
	}
	return 0
}

func historySourceSpans(source workspace.Capture, history workspace.GitHistory) []intelligence.SourceSpan {
	current := map[string]bool{}
	for _, entry := range source.Snapshot.Entries {
		current[entry.Path] = true
	}
	paths := map[string]bool{}
	for _, commit := range history.Commits {
		for _, change := range commit.Changes {
			if current[change.Path] {
				paths[change.Path] = true
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	spans := []intelligence.SourceSpan{}
	for _, path := range ordered {
		raw := source.Contents[path]
		if len(raw) == 0 || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			continue
		}
		end := min(len(raw), 4096)
		for end < len(raw) && end > 0 && !utf8.RuneStart(raw[end]) {
			end--
		}
		if end <= 0 {
			continue
		}
		spans = append(spans, intelligence.SourceSpan{Path: path, Start: 0, End: end})
		if len(spans) == 64 {
			break
		}
	}
	return spans
}
