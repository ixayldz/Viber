package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

type projectMemorySearchRequest struct {
	Query  string `json:"query,omitempty"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type projectMemorySuggestionRequest struct {
	Limit int `json:"limit"`
}

type projectMemoryMutationRequest struct {
	CommandID    string `json:"command_id"`
	Kind         string `json:"kind,omitempty"`
	Key          string `json:"key,omitempty"`
	Text         string `json:"text,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	TargetDigest string `json:"target_digest,omitempty"`
}

// A copy request names one source entry by exact task, ID, and digest. The
// destination task is carried by the owner envelope, so the operation cannot
// turn into cross-task search or implicit memory discovery.
type projectMemoryCopyRequest struct {
	CommandID  string `json:"command_id"`
	SourceTask string `json:"source_task"`
	ItemID     string `json:"item_id"`
	ItemDigest string `json:"item_digest"`
}

func formatProjectMemoryCopyReceipt(receipt agent.ProjectMemoryCopyReceipt) string {
	return fmt.Sprintf("Project memory copy %s · %s → %s\nSource item: %s (%s)\nCopy pin: %s (%s) · advisory only: %t\nThe receipt records the copy operation at that time. While the target retains its pin, source deletion is blocked. Check current deletion/retention status to learn whether that pin remains; this receipt does not prove that external backups, exports or physical media were erased.\n",
		receipt.Status, receipt.SourceTask, receipt.TargetTask, receipt.SourceItemID, receipt.SourceItemDigest,
		receipt.CopyID, receipt.CopyDigest, receipt.AdvisoryOnly)
}

func formatProjectMemorySuggestions(result agent.ProjectMemorySuggestionResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Project memory suggestions: %d matching entries · read-only proposal\n", result.Total)
	fmt.Fprintf(&out, "Target: %s @ sequence %d · project %s · baseline %s · candidate %s\n", strconv.QuoteToASCII(result.TargetSource.TaskID), result.TargetSource.TaskSeq, result.TargetSource.ProjectDigest, result.TargetSource.BaselineDigest, result.TargetSource.CandidateDigest)
	fmt.Fprintf(&out, "Retained policy: %s · current effective policy: %s\n", result.TargetSource.PolicyDigest, result.TargetEffectivePolicyDigest)
	fmt.Fprintf(&out, "Not suggested: %d conflicted · %d stale/noncurrent · %d current-policy mismatch or unavailable\n", result.ExcludedConflicted, result.ExcludedNonCurrent, result.ExcludedPolicyMismatch)
	if result.CopyPermitted {
		out.WriteString("Current target policy permits a user-selected copy; copy rechecks source and target policy before recording.\n")
	} else {
		out.WriteString("Current target policy does not permit context edits; these recommendations are read-only and cannot be copied now.\n")
	}
	out.WriteString("Suggestions use retained goal/plan and untrusted model summary only for lexical relevance. CURRENT means the origin snapshots match the target; HISTORICAL means the assertion remains current at its origin task but its snapshots differ from the target. Both are advisory and do not establish target facts or verification. The model does not search cross-task memory during inference.\n")
	if len(result.Suggestions) == 0 {
		out.WriteString("No eligible, conflict-free cross-task assertions matched. No memory was copied.\n")
		return out.String()
	}
	for index, suggestion := range result.Suggestions {
		item := suggestion.Item
		currentness := "HISTORICAL"
		if suggestion.SourceCurrentness == "CURRENT_FOR_TARGET_SOURCE" {
			currentness = "CURRENT"
		}
		fmt.Fprintf(&out, "%d. %s_%s_FOR_TARGET_SOURCE · USER_ASSERTED %s/%s · historical origin task %s @ sequence %d · source document %s\n", index+1, currentness, item.Kind, item.Kind, item.Key, strconv.QuoteToASCII(item.Source.TaskID), item.Source.TaskSeq, item.Source.DocumentDigest)
		fmt.Fprintf(&out, "   Text: %s\n   Item ID: %s · digest: %s\n", strconv.QuoteToASCII(item.Text), item.ID, item.Digest)
		fmt.Fprintf(&out, "   Source baseline/candidate: %s / %s · target baseline/candidate: %s / %s\n", item.Source.BaselineDigest, item.Source.CandidateDigest, result.TargetSource.BaselineDigest, result.TargetSource.CandidateDigest)
		fmt.Fprintf(&out, "   Source retained policy: %s · source effective policy: %s\n", item.Source.PolicyDigest, suggestion.SourceEffectivePolicyDigest)
		fmt.Fprintf(&out, "   Match signals: %s · terms: %s\n", strings.Join(suggestion.MatchedSignals, ", "), quotedTerms(suggestion.MatchedTerms))
	}
	if result.HasMore {
		out.WriteString("More matches are available with a larger --limit.\n")
	}
	return out.String()
}

func quotedTerms(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.QuoteToASCII(value)
	}
	return strings.Join(quoted, ", ")
}

func runProjectMemory(args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "project-memory suggest|search|record|correct|retire|copy required"), false)
	}
	action, args := args[0], args[1:]
	if action != "suggest" && action != "search" && action != "record" && action != "correct" && action != "retire" && action != "copy" {
		return report(out, errout, c.Fail(c.InvalidArgument, "project-memory suggest|search|record|correct|retire|copy required"), false)
	}
	f := flags("project-memory", errout)
	storePath := f.String("store", "", "existing private task store")
	task := f.String("task", "", "task whose current project and source bind the operation")
	fromTask := f.String("from", "", "exact source task for an explicit memory copy")
	toTask := f.String("to", "", "target task for an explicit memory copy")
	commandID := f.String("command-id", "", "stable user command ID for record/correct/retire/copy")
	kind := f.String("kind", "", "FACT, DECISION or CONVENTION")
	key := f.String("key", "", "stable lowercase key for lookup and conflict reporting")
	text := f.String("text", "", "explicit user-authored advisory content")
	targetID := f.String("id", "", "exact memory entry ID for correction, retirement or copy")
	targetDigest := f.String("digest", "", "exact memory entry digest for correction, retirement or copy")
	query := f.String("query", "", "case-insensitive substring filter")
	offset := f.Int("offset", 0, "result offset")
	limit := f.Int("limit", 50, "result page size; suggestions are limited to 50")
	jsonMode := f.Bool("json", false, "structured local output")
	if err := f.Parse(args); err != nil {
		return 4
	}
	limitProvided := false
	f.Visit(func(value *flag.Flag) {
		if value.Name == "limit" {
			limitProvided = true
		}
	})
	if action == "suggest" && !limitProvided {
		*limit = 10
	}
	if f.NArg() != 0 || *storePath == "" || *offset < 0 || *limit < 0 || *limit > 200 {
		return report(out, errout, c.Fail(c.InvalidArgument, "existing store and bounded project-memory page required"), *jsonMode)
	}
	if action == "copy" {
		if *task != "" || *fromTask == "" || *toTask == "" || *fromTask == *toTask || *commandID == "" || *targetID == "" || !c.ValidDigest(*targetDigest) || *kind != "" || *key != "" || *text != "" || *query != "" || *offset != 0 || *limit != 50 {
			return report(out, errout, c.Fail(c.InvalidArgument, "copy needs --from source task, distinct --to target task, exact --id/--digest, and --command-id; it does not accept search or mutation content"), *jsonMode)
		}
	} else if *task == "" || *fromTask != "" || *toTask != "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "this operation needs --task and does not accept --from/--to"), *jsonMode)
	}
	if action == "suggest" {
		if *limit > 50 || *commandID != "" || *kind != "" || *key != "" || *text != "" || *targetID != "" || *targetDigest != "" || *query != "" || *offset != 0 {
			return report(out, errout, c.Fail(c.InvalidArgument, "suggest accepts only store, task, bounded limit and output format flags"), *jsonMode)
		}
	} else if action == "search" {
		if *commandID != "" || *kind != "" || *key != "" || *text != "" || *targetID != "" || *targetDigest != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "search accepts only store, task, query and page flags"), *jsonMode)
		}
	} else if action != "copy" {
		if *commandID == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "stable --command-id required for project-memory mutations"), *jsonMode)
		}
		if action == "record" && (*targetID != "" || *targetDigest != "") || action == "retire" && (*kind != "" || *key != "" || *text != "") {
			return report(out, errout, c.Fail(c.InvalidArgument, "mutation flags do not match the selected action"), *jsonMode)
		}
		if action != "retire" && (*kind == "" || *key == "" || *text == "") || action != "record" && (*targetID == "" || *targetDigest == "") {
			return report(out, errout, c.Fail(c.InvalidArgument, "record needs kind/key/text; correct needs target ID/digest and replacement; retire needs target ID/digest"), *jsonMode)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	requestID, err := resolveCommandID(*commandID)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var payload any
	ownerCommand := "project-memory-" + action
	ownerTask := *task
	if action == "suggest" {
		payload = projectMemorySuggestionRequest{Limit: *limit}
	} else if action == "search" {
		payload = projectMemorySearchRequest{Query: *query, Offset: *offset, Limit: *limit}
	} else if action == "copy" {
		payload = projectMemoryCopyRequest{CommandID: *commandID, SourceTask: *fromTask, ItemID: *targetID, ItemDigest: *targetDigest}
		ownerTask = *toTask
	} else {
		payload = projectMemoryMutationRequest{CommandID: *commandID, Kind: strings.ToUpper(strings.TrimSpace(*kind)), Key: strings.ToLower(strings.TrimSpace(*key)), Text: *text, TargetID: *targetID, TargetDigest: *targetDigest}
	}
	raw, routed, err := ownerCall(ctx, *storePath, ownerTask, ownerCommand, requestID, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result any
	if routed {
		if action == "suggest" {
			var suggestions agent.ProjectMemorySuggestionResult
			if err = c.DecodeStrict([]byte(raw), &suggestions); err != nil {
				return report(out, errout, err, *jsonMode)
			}
			result = suggestions
		} else if action == "copy" {
			var receipt agent.ProjectMemoryCopyReceipt
			if err = c.DecodeStrict([]byte(raw), &receipt); err != nil {
				return report(out, errout, err, *jsonMode)
			}
			result = receipt
		} else {
			result = raw
		}
	} else {
		session, openErr := agent.OpenExisting(ctx, *storePath)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		switch action {
		case "suggest":
			result, err = session.SuggestProjectMemory(ctx, *task, *limit)
		case "search":
			result, err = session.SearchProjectMemory(ctx, *task, *query, *offset, *limit)
		case "record":
			result, err = session.RecordProjectMemory(ctx, *task, *commandID, *kind, *key, *text)
		case "correct":
			result, err = session.CorrectProjectMemory(ctx, *task, *commandID, *targetID, *targetDigest, *kind, *key, *text)
		case "retire":
			result, err = session.RetireProjectMemory(ctx, *task, *commandID, *targetID, *targetDigest)
		case "copy":
			result, err = session.CopyProjectMemory(ctx, *fromTask, *toTask, *commandID, *targetID, *targetDigest)
		}
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if routed {
			_, err = fmt.Fprintln(out, string(raw))
		} else {
			err = jsonWrite(out, result)
		}
		if err != nil {
			return 4
		}
		return 0
	}
	if routed && action != "copy" && action != "suggest" {
		_, err = fmt.Fprintln(out, string(raw))
		if err != nil {
			return 4
		}
		return 0
	}
	switch value := result.(type) {
	case agent.ProjectMemorySearchResult:
		_, err = fmt.Fprintf(out, "Project memory %s · %d entries · more %t\n", value.ProjectDigest, value.Total, value.HasMore)
		for _, item := range value.Items {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(out, "%s %s/%s · %s · %s · %s\n", item.Status, item.Kind, item.Key, item.ID, item.Digest, strconv.QuoteToASCII(item.Text))
		}
	case agent.ProjectMemoryMutationReceipt:
		_, err = fmt.Fprintf(out, "Project memory %s · %s · %s · %s\n", strings.ToLower(value.Operation), value.Status, value.ItemID, value.EventDigest)
	case agent.ProjectMemoryCopyReceipt:
		_, err = fmt.Fprint(out, formatProjectMemoryCopyReceipt(value))
	case agent.ProjectMemorySuggestionResult:
		_, err = fmt.Fprint(out, formatProjectMemorySuggestions(value))
		if err == nil {
			for _, suggestion := range value.Suggestions {
				if !value.CopyPermitted {
					break
				}
				item := suggestion.Item
				_, err = fmt.Fprintf(out, "Explicit copy: viber project-memory copy --store <PATH> --from %s --to %s --id %s --digest %s --command-id <new-id>\n", item.Source.TaskID, value.TargetSource.TaskID, item.ID, item.Digest)
				if err != nil {
					break
				}
			}
		}
	default:
		err = fmt.Errorf("unexpected project memory response %T", result)
	}
	if err != nil {
		return 4
	}
	return 0
}
