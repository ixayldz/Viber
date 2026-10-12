package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func memoryWords(text string, count int) ([]string, string) {
	words := []string{}
	for len(words) < count {
		text = strings.TrimSpace(text)
		if text == "" {
			break
		}
		at := strings.IndexAny(text, " \t\r\n")
		if at < 0 {
			words = append(words, text)
			text = ""
			break
		}
		words = append(words, text[:at])
		text = text[at:]
	}
	return words, strings.TrimSpace(text)
}

func (u uiSession) memoryCommand(ctx context.Context, arg string) (string, bool, error) {
	words, rest := memoryWords(arg, 1)
	action := "search"
	if len(words) > 0 {
		action = words[0]
	}
	if action == "search" {
		raw, err := u.call(ctx, "project-memory-search", projectMemorySearchRequest{Query: rest, Limit: 20})
		if err != nil {
			return "", false, err
		}
		var result agent.ProjectMemorySearchResult
		if err = c.DecodeStrict(raw, &result); err != nil {
			return "", false, err
		}
		var text strings.Builder
		fmt.Fprintf(&text, "Project memory: %d entries. Advisory user statements; independent verification is separate.\n", result.Total)
		for _, item := range result.Items {
			body := strconv.Quote(item.Text)
			if item.Redacted {
				body = "[content hidden by current policy]"
			}
			fmt.Fprintf(&text, "%s %s/%s: %s\n  task %s | ID %s | digest %s\n", item.Status, item.Kind, item.Key, body, item.Source.TaskID, item.ID, item.Digest)
		}
		if result.HasMore {
			text.WriteString("More entries: narrow /memory search QUERY, or use CLI page flags.\n")
		}
		return text.String(), false, nil
	}
	id, err := resolveCommandID("")
	if err != nil {
		return "", false, err
	}
	request := projectMemoryMutationRequest{CommandID: id}
	switch action {
	case "record":
		parts, body := memoryWords(rest, 2)
		if len(parts) != 2 || body == "" {
			return "", false, c.Fail(c.InvalidArgument, "/memory record FACT|DECISION|CONVENTION KEY TEXT")
		}
		request.Kind, request.Key, request.Text = strings.ToUpper(parts[0]), parts[1], body
	case "correct":
		parts, body := memoryWords(rest, 4)
		if len(parts) != 4 || body == "" {
			return "", false, c.Fail(c.InvalidArgument, "/memory correct ID DIGEST FACT|DECISION|CONVENTION KEY TEXT; open the origin task")
		}
		request.TargetID, request.TargetDigest, request.Kind, request.Key, request.Text = parts[0], parts[1], strings.ToUpper(parts[2]), parts[3], body
	case "retire":
		parts, body := memoryWords(rest, 2)
		if len(parts) != 2 || body != "" {
			return "", false, c.Fail(c.InvalidArgument, "/memory retire ID DIGEST; open the origin task")
		}
		request.TargetID, request.TargetDigest = parts[0], parts[1]
	default:
		return "", false, c.Fail(c.InvalidArgument, "/memory [search QUERY|record KIND KEY TEXT|correct ID DIGEST KIND KEY TEXT|retire ID DIGEST]")
	}
	raw, err := u.call(ctx, "project-memory-"+action, request)
	if err != nil {
		return "", false, err
	}
	var result agent.ProjectMemoryMutationReceipt
	if err = c.DecodeStrict(raw, &result); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("Memory %s: %s\nID %s\nDigest %s\n/memory to review status and provenance.", result.Operation, result.Status, result.ItemID, result.EventDigest), false, nil
}
