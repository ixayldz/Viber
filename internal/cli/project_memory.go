package cli

import (
	"context"
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

type projectMemoryMutationRequest struct {
	CommandID    string `json:"command_id"`
	Kind         string `json:"kind,omitempty"`
	Key          string `json:"key,omitempty"`
	Text         string `json:"text,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	TargetDigest string `json:"target_digest,omitempty"`
}

func runProjectMemory(args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "project-memory search|record|correct|retire required"), false)
	}
	action, args := args[0], args[1:]
	if action != "search" && action != "record" && action != "correct" && action != "retire" {
		return report(out, errout, c.Fail(c.InvalidArgument, "project-memory search|record|correct|retire required"), false)
	}
	f := flags("project-memory", errout)
	storePath := f.String("store", "", "existing private task store")
	task := f.String("task", "", "task whose current project and source bind the operation")
	commandID := f.String("command-id", "", "stable user command ID for record/correct/retire")
	kind := f.String("kind", "", "FACT, DECISION or CONVENTION")
	key := f.String("key", "", "stable lowercase key for lookup and conflict reporting")
	text := f.String("text", "", "explicit user-authored advisory content")
	targetID := f.String("id", "", "exact memory entry ID for correction or retirement")
	targetDigest := f.String("digest", "", "exact memory entry digest for correction or retirement")
	query := f.String("query", "", "case-insensitive substring filter")
	offset := f.Int("offset", 0, "result offset")
	limit := f.Int("limit", 50, "result page size, at most 200")
	jsonMode := f.Bool("json", false, "structured local output")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *storePath == "" || *task == "" || *offset < 0 || *limit < 0 || *limit > 200 {
		return report(out, errout, c.Fail(c.InvalidArgument, "existing store, task and bounded project-memory page required"), *jsonMode)
	}
	if action == "search" {
		if *commandID != "" || *kind != "" || *key != "" || *text != "" || *targetID != "" || *targetDigest != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "search accepts only store, task, query and page flags"), *jsonMode)
		}
	} else {
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
	if action == "search" {
		payload = projectMemorySearchRequest{Query: *query, Offset: *offset, Limit: *limit}
	} else {
		payload = projectMemoryMutationRequest{CommandID: *commandID, Kind: strings.ToUpper(strings.TrimSpace(*kind)), Key: strings.ToLower(strings.TrimSpace(*key)), Text: *text, TargetID: *targetID, TargetDigest: *targetDigest}
	}
	raw, routed, err := ownerCall(ctx, *storePath, *task, ownerCommand, requestID, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result any
	if routed {
		result = raw
	} else {
		session, openErr := agent.OpenExisting(ctx, *storePath)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		switch action {
		case "search":
			result, err = session.SearchProjectMemory(ctx, *task, *query, *offset, *limit)
		case "record":
			result, err = session.RecordProjectMemory(ctx, *task, *commandID, *kind, *key, *text)
		case "correct":
			result, err = session.CorrectProjectMemory(ctx, *task, *commandID, *targetID, *targetDigest, *kind, *key, *text)
		case "retire":
			result, err = session.RetireProjectMemory(ctx, *task, *commandID, *targetID, *targetDigest)
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
	if routed {
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
			_, err = fmt.Fprintf(out, "%s %s/%s · %s · %s · %s\n", item.Status, item.Kind, item.Key, item.ID, item.Digest, strconv.Quote(item.Text))
		}
	case agent.ProjectMemoryMutationReceipt:
		_, err = fmt.Fprintf(out, "Project memory %s · %s · %s · %s\n", strings.ToLower(value.Operation), value.Status, value.ItemID, value.EventDigest)
	default:
		err = fmt.Errorf("unexpected project memory response %T", result)
	}
	if err != nil {
		return 4
	}
	return 0
}
