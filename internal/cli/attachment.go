package cli

import (
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
	"io"
	"os"
	"os/signal"
	"time"
)

func runDetachedTask(ctx context.Context, task, directory, id string, out, errout io.Writer, jsonMode bool) int {
	if _, err := startBackgroundOwner(ctx, directory); err != nil {
		return report(out, errout, err, jsonMode)
	}
	raw, routed, err := ownerCall(ctx, directory, task, "detach", id, nil)
	if err != nil {
		return report(out, errout, err, jsonMode)
	}
	if !routed {
		return report(out, errout, c.Fail(c.UnknownOutcome, "detached owner disappeared before admission; inspect task and invocation ID"), jsonMode)
	}
	if err = jsonWrite(out, json.RawMessage(raw)); err != nil {
		return 4
	}
	return 0
}
func readAttachment(ctx context.Context, directory, task string, query owner.Page) (owner.AttachmentPage, error) {
	var result owner.AttachmentPage
	id, err := ipc.NewID()
	if err != nil {
		return result, err
	}
	raw, routed, err := ownerCall(ctx, directory, task, "attach-page", id, query)
	if err != nil {
		return result, err
	}
	if routed {
		if err = c.DecodeStrict(raw, &result); err != nil {
			return result, err
		}
	} else {
		s, err := agent.OpenExisting(ctx, directory)
		if err != nil {
			return result, err
		}
		defer s.Close()
		result = owner.AttachmentPage{SchemaVersion: 1, EarliestTaskSeq: 1, EphemeralReplay: "NOT_RETAINED"}
		result.History, err = s.Journal.History(ctx, task, query.After, query.Limit)
		if err != nil {
			return result, err
		}
		result.State, err = s.State(ctx, task)
		if err != nil {
			return result, err
		}
	}
	if result.SchemaVersion != 1 || result.History.TaskID != task || result.State.TaskID != task || result.History.After != query.After || result.History.Next < query.After || result.History.Next > result.History.Head || result.State.TaskSeq < result.History.Head || result.ResyncRequired || result.EarliestTaskSeq != 1 {
		return result, c.Fail(c.StoreIntegrityError, "attachment cursor or snapshot binding invalid")
	}
	for i, record := range result.History.Records {
		if record.Event.TaskID != task || record.Event.TaskSeq != query.After+int64(i)+1 {
			return result, c.Fail(c.StoreIntegrityError, "attachment event sequence is not contiguous")
		}
	}
	if len(result.History.Records) > query.Limit || len(result.History.Records) > 0 && result.History.Next != result.History.Records[len(result.History.Records)-1].Event.TaskSeq {
		return result, c.Fail(c.StoreIntegrityError, "attachment page cursor differs from retained events")
	}
	return result, nil
}
func runAttachment(action string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(action, errout)
	directory := f.String("store", "", "existing store")
	after := f.Int64("after", 0, "exclusive durable task event cursor")
	limit := f.Int("limit", 64, "event page size 1..256")
	follow := f.Bool("follow", false, "poll without tying task lifetime to this client")
	poll := f.Int("poll-ms", 500, "bounded poll interval 100..5000ms")
	commandID := f.String("command-id", "", "stable detached invocation ID")
	jsonMode := f.Bool("json", false, "versioned JSONL pages")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task required"), *jsonMode)
	}
	if task == "" || *directory == "" || *after < 0 || *limit < 1 || *limit > 256 || *poll < 100 || *poll > 5000 || action == "detach" && (*after != 0 || *follow) || action == "attach" && *commandID != "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task/store and valid command-specific cursor flags required"), *jsonMode)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if action == "detach" {
		id, err := resolveCommandID(*commandID)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		return runDetachedTask(ctx, task, *directory, id, out, errout, *jsonMode)
	}
	first := true
	previousActive := false
	previousStateSeq := int64(-1)
	for {
		page, err := readAttachment(ctx, *directory, task, owner.Page{After: *after, Limit: *limit})
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if first || len(page.History.Records) > 0 || page.Active != previousActive || page.State.TaskSeq != previousStateSeq {
			envelope := struct {
				SchemaVersion int                  `json:"schema_version"`
				Kind          string               `json:"kind"`
				Cursor        int64                `json:"cursor"`
				Data          owner.AttachmentPage `json:"data"`
			}{1, "attachment_page", page.History.Next, page}
			if err = jsonWrite(out, envelope); err != nil {
				return 4
			}
		}
		first = false
		previousActive = page.Active
		previousStateSeq = page.State.TaskSeq
		*after = page.History.Next
		if !*follow || !page.Active && !page.History.HasMore {
			return 0
		}
		if page.History.HasMore {
			continue
		}
		timer := time.NewTimer(time.Duration(*poll) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0
		case <-timer.C:
		}
	}
}
