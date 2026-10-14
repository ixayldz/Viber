package agent

import (
	"context"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const projectMemorySuggestionProfile = "RETAINED_GOAL_PLAN_UNTRUSTED_SUMMARY_TOKEN_OVERLAP_V1"

// ProjectMemorySuggestion is a read-only proposal to copy one explicit user
// assertion. The source item remains bound to its origin task; it is not
// admitted to this task's context until CopyProjectMemory records a receipt.
type ProjectMemorySuggestion struct {
	Item                        ProjectMemoryItem `json:"item"`
	SourceCurrentness           string            `json:"source_currentness"`
	SourceEffectivePolicyDigest string            `json:"source_effective_policy_digest"`
	MatchedSignals              []string          `json:"matched_signals"`
	MatchedTerms                []string          `json:"matched_terms"`
}

// ProjectMemorySuggestionResult binds the proposals to the exact target task
// snapshots and current effective policy used when they were ranked.
type ProjectMemorySuggestionResult struct {
	TargetSource                ProjectMemorySource       `json:"target_source"`
	TargetEffectivePolicyDigest string                    `json:"target_effective_policy_digest"`
	Profile                     string                    `json:"profile"`
	Suggestions                 []ProjectMemorySuggestion `json:"suggestions"`
	CopyPermitted               bool                      `json:"copy_permitted"`
	Total                       int                       `json:"total"`
	ExcludedConflicted          int                       `json:"excluded_conflicted"`
	ExcludedNonCurrent          int                       `json:"excluded_noncurrent"`
	ExcludedPolicyMismatch      int                       `json:"excluded_policy_mismatch"`
	Limit                       int                       `json:"limit"`
	HasMore                     bool                      `json:"has_more"`
	SchemaVersion               int                       `json:"schema_version"`
}

type projectMemorySuggestionSignal struct {
	name   string
	weight int
	terms  map[string]struct{}
}

type rankedProjectMemorySuggestion struct {
	suggestion ProjectMemorySuggestion
	score      int
}

type projectMemorySuggestionSnapshotKey struct {
	ProjectDigest   string
	BaselineDigest  string
	CandidateDigest string
	PolicyDigest    string
	PolicyEpoch     int64
}

type projectMemorySuggestionConflictKey struct {
	Snapshot projectMemorySuggestionSnapshotKey
	Kind     string
	Key      string
}

func projectMemorySuggestionSnapshot(source ProjectMemorySource) projectMemorySuggestionSnapshotKey {
	return projectMemorySuggestionSnapshotKey{
		ProjectDigest: source.ProjectDigest, BaselineDigest: source.BaselineDigest,
		CandidateDigest: source.CandidateDigest, PolicyDigest: source.PolicyDigest,
		PolicyEpoch: source.PolicyEpoch,
	}
}

func projectMemorySuggestionKey(event ProjectMemoryEvent) projectMemorySuggestionConflictKey {
	return projectMemorySuggestionConflictKey{
		Snapshot: projectMemorySuggestionSnapshot(event.Source), Kind: event.Kind, Key: event.Key,
	}
}

func projectMemorySuggestionEventCurrent(event ProjectMemoryEvent, source projectMemoryCurrent, currentPolicyDigest string) bool {
	return (event.Action == "ASSERT" || event.Action == "CORRECT") && event.EvidenceClass == "USER_ASSERTED" &&
		source.Readable && source.State.Deletion == nil && source.Project == event.Source.ProjectDigest &&
		source.State.BaselineDigest == event.Source.BaselineDigest && source.State.CandidateDigest == event.Source.CandidateDigest &&
		source.State.PolicyEpoch == event.Source.PolicyEpoch && currentPolicyDigest == event.Source.PolicyDigest
}

func projectMemorySuggestionCopyActive(storedCopy projectMemoryStored, query projectMemorySuggestionSnapshotKey,
	current map[string]projectMemoryCurrent, events map[string]projectMemoryStored, retiredBy, supersededBy map[string]string) (bool, error) {
	copy := storedCopy.Copy
	if copy == nil || projectMemorySuggestionSnapshot(copy.Target) != query {
		return false, nil
	}
	target, exists := current[copy.Target.TaskID]
	if !exists || target.State.Deletion != nil || !target.Readable || target.Project != copy.Target.ProjectDigest ||
		target.State.BaselineDigest != copy.Target.BaselineDigest || target.State.CandidateDigest != copy.Target.CandidateDigest ||
		target.State.PolicyEpoch != copy.Target.PolicyEpoch {
		return false, nil
	}
	origin, exists := current[copy.Origin.TaskID]
	if !exists || origin.State.Deletion != nil || !origin.Readable || origin.Project != copy.Origin.ProjectDigest ||
		origin.State.BaselineDigest != copy.Origin.BaselineDigest || origin.State.CandidateDigest != copy.Origin.CandidateDigest ||
		origin.State.PolicyEpoch != copy.Origin.PolicyEpoch {
		return false, nil
	}
	targetPolicy, targetPolicyErr := projectMemoryEffectivePolicyDigest(target.Doc)
	originPolicy, err := projectMemoryPolicyDigest(origin.Doc)
	if err != nil {
		return false, err
	}
	originEffective, originEffectiveErr := projectMemoryEffectivePolicyDigest(origin.Doc)
	if targetPolicyErr != nil || originEffectiveErr != nil || targetPolicy != copy.TransferPolicyDigest || originEffective != copy.TransferPolicyDigest {
		return false, nil
	}
	if originPolicy != copy.Origin.PolicyDigest {
		return false, nil
	}
	originItem, found := events[copy.SourceItemID]
	if !found || originItem.TaskID != copy.Origin.TaskID || originItem.Digest != copy.SourceItemDigest ||
		originItem.Event.Action != "ASSERT" || originItem.Event.EvidenceClass != "USER_ASSERTED" {
		return false, c.Fail(c.StoreIntegrityError, "project memory copy origin item is missing")
	}
	if retiredBy[copy.SourceItemID] != "" || supersededBy[copy.SourceItemID] != "" ||
		!projectMemoryAssertionCurrent(originItem.Event, origin, originPolicy) ||
		projectMemoryAssertionConflicted(originItem.Event, events, current, retiredBy, supersededBy) {
		return false, nil
	}
	return true, nil
}

// SuggestProjectMemory proposes same-project assertions that are current for
// their origin task. An origin snapshot that differs from the target is shown
// as historical advice, never as current target evidence. This runs only when
// explicitly requested; inference continues to use task-local refs only.
func (s *Session) SuggestProjectMemory(ctx context.Context, targetTask string, limit int) (ProjectMemorySuggestionResult, error) {
	result := ProjectMemorySuggestionResult{
		SchemaVersion: 1, Profile: projectMemorySuggestionProfile,
		Suggestions: []ProjectMemorySuggestion{}, Limit: limit,
	}
	if limit < 1 || limit > 50 {
		return result, c.Fail(c.InvalidArgument, "project-memory suggestion limit must be between 1 and 50")
	}
	if s == nil || s.Journal == nil || s.Archive == nil || targetTask == "" {
		return result, c.Fail(c.InvalidArgument, "target task and existing session required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, doc, err := s.Load(ctx, targetTask)
	if err != nil {
		return result, err
	}
	if err = currentOperatorEffect(doc, "context.read"); err != nil {
		return result, err
	}
	result.CopyPermitted = currentOperatorEffect(doc, "context.edit") == nil
	targetSource, err := s.projectMemorySource(ctx, state, doc)
	if err != nil {
		return result, err
	}
	targetPolicy, err := projectMemoryEffectivePolicyDigest(doc)
	if err != nil {
		return result, err
	}
	result.TargetSource = targetSource
	result.TargetEffectivePolicyDigest = targetPolicy

	// This is the same bounded, policy-filtered inventory used by explicit
	// search. The suggestion operation is deliberately outside inference.
	stored, current, err := s.projectMemoryInventory(ctx, targetSource.ProjectDigest)
	if err != nil {
		return result, err
	}
	events, retiredBy, supersededBy, err := projectMemoryEventLineage(stored)
	if err != nil {
		return result, err
	}
	signals := projectMemorySuggestionSignals(doc)
	ranked := make([]rankedProjectMemorySuggestion, 0)
	retainedPolicies := map[string]string{}
	effectivePolicies := map[string]string{}
	active := make(map[string]projectMemoryStored, len(events))
	conflictContents := map[projectMemorySuggestionConflictKey]map[string]struct{}{}
	for id, storedItem := range events {
		event := storedItem.Event
		if (event.Action != "ASSERT" && event.Action != "CORRECT") || event.EvidenceClass != "USER_ASSERTED" {
			continue
		}
		if retiredBy[id] != "" || supersededBy[id] != "" {
			result.ExcludedNonCurrent++
			continue
		}
		source, exists := current[event.Source.TaskID]
		if !exists || source.State.Deletion != nil || !source.Readable || source.Project != targetSource.ProjectDigest ||
			source.State.BaselineDigest != event.Source.BaselineDigest || source.State.CandidateDigest != event.Source.CandidateDigest ||
			source.State.PolicyEpoch != event.Source.PolicyEpoch || !c.ValidDigest(storedItem.Digest) {
			result.ExcludedNonCurrent++
			continue
		}
		retainedPolicy, exists := retainedPolicies[event.Source.TaskID]
		if !exists {
			retainedPolicy, err = projectMemoryPolicyDigest(source.Doc)
			if err != nil {
				return result, err
			}
			retainedPolicies[event.Source.TaskID] = retainedPolicy
		}
		if !projectMemorySuggestionEventCurrent(event, source, retainedPolicy) {
			result.ExcludedNonCurrent++
			continue
		}
		active[id] = storedItem
		conflictKey := projectMemorySuggestionKey(event)
		if conflictContents[conflictKey] == nil {
			conflictContents[conflictKey] = map[string]struct{}{}
		}
		conflictContents[conflictKey][event.ContentDigest] = struct{}{}
	}
	candidateSnapshots := make(map[projectMemorySuggestionSnapshotKey]bool)
	for key := range conflictContents {
		candidateSnapshots[key.Snapshot] = true
	}
	for _, storedCopy := range stored {
		if storedCopy.Copy == nil || !candidateSnapshots[projectMemorySuggestionSnapshot(storedCopy.Copy.Target)] {
			continue
		}
		copy := storedCopy.Copy
		copyActive, copyErr := projectMemorySuggestionCopyActive(storedCopy, projectMemorySuggestionSnapshot(copy.Target), current, events, retiredBy, supersededBy)
		if copyErr != nil {
			return result, copyErr
		}
		if !copyActive {
			continue
		}
		conflictKey := projectMemorySuggestionConflictKey{
			Snapshot: projectMemorySuggestionSnapshot(copy.Target), Kind: copy.Kind, Key: copy.Key,
		}
		if conflictContents[conflictKey] == nil {
			conflictContents[conflictKey] = map[string]struct{}{}
		}
		conflictContents[conflictKey][copy.ContentDigest] = struct{}{}
	}
	for _, storedItem := range active {
		event := storedItem.Event
		if event.Source.TaskID == targetTask {
			continue
		}
		if event.Source.PolicyDigest != targetSource.PolicyDigest || event.Source.PolicyEpoch != targetSource.PolicyEpoch {
			result.ExcludedPolicyMismatch++
			continue
		}
		source, exists := current[event.Source.TaskID]
		if !exists {
			result.ExcludedNonCurrent++
			continue
		}
		sourcePolicy, exists := effectivePolicies[event.Source.TaskID]
		if !exists {
			var policyErr error
			sourcePolicy, policyErr = projectMemoryEffectivePolicyDigest(source.Doc)
			if policyErr != nil {
				result.ExcludedPolicyMismatch++
				continue
			}
			effectivePolicies[event.Source.TaskID] = sourcePolicy
		}
		if sourcePolicy != targetPolicy {
			// Unavailable or mismatched current policy is not presented as fresh.
			result.ExcludedPolicyMismatch++
			continue
		}
		if len(conflictContents[projectMemorySuggestionKey(event)]) > 1 {
			result.ExcludedConflicted++
			continue
		}
		currentness := "HISTORICAL_FOR_TARGET_SOURCE"
		if event.Source.BaselineDigest == targetSource.BaselineDigest && event.Source.CandidateDigest == targetSource.CandidateDigest {
			currentness = "CURRENT_FOR_TARGET_SOURCE"
		}
		item := ProjectMemoryItem{
			Source: event.Source, Digest: storedItem.Digest, ContentDigest: event.ContentDigest,
			ID: event.ID, Kind: event.Kind, Key: event.Key, Text: event.Text, Status: "ACTIVE",
			EvidenceClass: event.EvidenceClass, AdvisoryOnly: true, SchemaVersion: 1,
		}
		matchedSignals, matchedTerms, score := projectMemorySuggestionMatches(signals, item.Key+" "+item.Text)
		if score == 0 {
			continue
		}
		ranked = append(ranked, rankedProjectMemorySuggestion{
			score: score,
			suggestion: ProjectMemorySuggestion{
				Item: item, SourceCurrentness: currentness, SourceEffectivePolicyDigest: sourcePolicy,
				MatchedSignals: matchedSignals, MatchedTerms: matchedTerms,
			},
		})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		left, right := ranked[i].suggestion.Item, ranked[j].suggestion.Item
		if left.Source.TaskID != right.Source.TaskID {
			return left.Source.TaskID < right.Source.TaskID
		}
		if left.Source.TaskSeq != right.Source.TaskSeq {
			return left.Source.TaskSeq < right.Source.TaskSeq
		}
		return left.ID < right.ID
	})
	result.Total = len(ranked)
	if len(ranked) > limit {
		result.HasMore = true
		ranked = ranked[:limit]
	}
	for _, item := range ranked {
		result.Suggestions = append(result.Suggestions, item.suggestion)
	}
	return result, nil
}

func projectMemorySuggestionSignals(doc Document) []projectMemorySuggestionSignal {
	signals := []projectMemorySuggestionSignal{
		{name: "RETAINED_USER_GOAL", weight: 4, terms: projectMemorySuggestionTerms(doc.Spec.Goal, 96)},
	}
	if doc.Plan != nil {
		var planText strings.Builder
		for _, node := range doc.Plan.Definition.Nodes {
			if !appendProjectMemorySuggestionText(&planText, node.Goal, 8192) ||
				!appendProjectMemorySuggestionText(&planText, node.InputContract, 8192) ||
				!appendProjectMemorySuggestionText(&planText, node.OutputContract, 8192) {
				break
			}
		}
		signals = append(signals, projectMemorySuggestionSignal{name: "RETAINED_PLAN_UNTRUSTED", weight: 2, terms: projectMemorySuggestionTerms(planText.String(), 128)})
	}
	if doc.FinalSummary != "" {
		signals = append(signals, projectMemorySuggestionSignal{name: "UNTRUSTED_MODEL_SUMMARY", weight: 1, terms: projectMemorySuggestionTerms(doc.FinalSummary, 48)})
	}
	return signals
}

func appendProjectMemorySuggestionText(out *strings.Builder, text string, maximum int) bool {
	if out.Len() >= maximum {
		return false
	}
	if out.Len() > 0 {
		out.WriteByte(' ')
	}
	remaining := maximum - out.Len()
	if len(text) > remaining {
		text = text[:remaining]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	out.WriteString(text)
	return out.Len() < maximum
}

func projectMemorySuggestionTerms(text string, maximum int) map[string]struct{} {
	const maxInputBytes = 8192
	if len(text) > maxInputBytes {
		text = text[:maxInputBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	terms := make(map[string]struct{}, maximum)
	var token []rune
	add := func() {
		if len(token) >= 2 && len(terms) < maximum {
			value := string(token)
			if !projectMemorySuggestionStopWords[value] {
				terms[value] = struct{}{}
			}
		}
		token = token[:0]
	}
	for _, char := range text {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			token = append(token, unicode.ToLower(char))
		} else if len(token) > 0 {
			add()
			if len(terms) >= maximum {
				break
			}
		}
	}
	if len(terms) < maximum && len(token) > 0 {
		add()
	}
	return terms
}

var projectMemorySuggestionStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true,
	"but": true, "by": true, "for": true, "from": true, "in": true, "into": true, "is": true,
	"it": true, "of": true, "on": true, "or": true, "the": true, "this": true, "to": true,
	"was": true, "with": true, "you": true, "your": true, "bir": true, "bu": true, "da": true,
	"de": true, "ile": true, "için": true, "ve": true, "olan": true, "olarak": true,
}

func projectMemorySuggestionMatches(signals []projectMemorySuggestionSignal, text string) ([]string, []string, int) {
	itemTerms := projectMemorySuggestionTerms(text, 256)
	matchedSignals := []string{}
	matched := map[string]bool{}
	score := 0
	for _, signal := range signals {
		signalMatched := false
		for term := range signal.terms {
			if _, exists := itemTerms[term]; !exists {
				continue
			}
			signalMatched = true
			matched[term] = true
			score += signal.weight
		}
		if signalMatched {
			matchedSignals = append(matchedSignals, signal.name)
		}
	}
	matchedTerms := make([]string, 0, len(matched))
	for term := range matched {
		matchedTerms = append(matchedTerms, term)
	}
	sort.Strings(matchedTerms)
	return matchedSignals, matchedTerms, score
}
