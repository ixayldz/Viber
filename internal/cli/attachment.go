package cli

import (
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
	"github.com/ixayldz/Viber/internal/store"
	"io"
	"os"
	"os/signal"
	"time"
	"unicode/utf8"
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
		result = owner.AttachmentPage{SchemaVersion: 1, EphemeralReplay: "NOT_RETAINED"}
		result.History, err = s.Journal.History(ctx, task, query.After, query.Limit)
		if err != nil {
			return result, err
		}
		result.State, err = s.State(ctx, task)
		if err != nil {
			return result, err
		}
		first, firstErr := s.Journal.History(ctx, task, 0, 1)
		if firstErr != nil {
			return result, firstErr
		}
		if len(first.Records) > 0 {
			result.EarliestTaskSeq = first.Records[0].Event.TaskSeq
		} else {
			result.EarliestTaskSeq = result.State.TaskSeq + 1
		}
		if query.After < result.EarliestTaskSeq-1 {
			result.ResyncRequired = true
			result.History = store.HistoryPage{SchemaVersion: 1, TaskID: task, After: query.After, Next: query.After, Head: first.Head, HasMore: true, Records: []store.JournalRecord{}}
		}
		result.CurrentStateRef = owner.CurrentStateRef{TaskID: result.State.TaskID, TaskSeq: result.State.TaskSeq, DocumentDigest: result.State.DocumentDigest}
	}
	if result.SchemaVersion != 1 || result.History.TaskID != task || result.State.TaskID != task || result.CurrentStateRef.TaskID != task || result.CurrentStateRef.TaskSeq != result.State.TaskSeq || result.CurrentStateRef.DocumentDigest != result.State.DocumentDigest || result.EarliestTaskSeq < 1 || result.History.After != query.After || result.History.Next < query.After || result.History.Next > result.History.Head || result.State.TaskSeq < result.History.Head {
		return result, c.Fail(c.StoreIntegrityError, "attachment cursor or snapshot binding invalid")
	}
	if result.ResyncRequired {
		if query.After >= result.EarliestTaskSeq-1 || len(result.History.Records) != 0 || result.History.Next != query.After {
			return result, c.Fail(c.StoreIntegrityError, "attachment retention gap is inconsistent")
		}
		return result, nil
	}
	if query.After < result.EarliestTaskSeq-1 {
		return result, c.Fail(c.StoreIntegrityError, "attachment silently crossed a retention gap")
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

func readProviderDeltas(ctx context.Context, directory, task string, query owner.StreamPageQuery) (owner.StreamPage, error) {
	var result owner.StreamPage
	id, err := ipc.NewID()
	if err != nil {
		return result, err
	}
	raw, routed, err := ownerCall(ctx, directory, task, "stream-page", id, query)
	if err != nil {
		return result, err
	}
	if routed {
		if err = c.DecodeStrict(raw, &result); err != nil {
			return result, err
		}
	} else {
		session, openErr := agent.OpenExisting(ctx, directory)
		if openErr != nil {
			return result, openErr
		}
		defer session.Close()
		state, stateErr := session.State(ctx, task)
		if stateErr != nil {
			return result, stateErr
		}
		result = owner.StreamPage{SchemaVersion: 1, TaskID: task, Replay: "NOT_RETAINED", After: query.After, Next: query.After, CurrentState: owner.CurrentStateRef{TaskID: task, TaskSeq: state.TaskSeq, DocumentDigest: state.DocumentDigest}, Deltas: []owner.ProviderDelta{}}
	}
	if result.SchemaVersion != 1 || result.TaskID != task || result.After != query.After || result.Next < query.After || result.Next > result.CurrentDeltaSeq || result.CurrentState.TaskID != task || result.CurrentState.TaskSeq < 0 || result.EarliestDeltaSeq < 0 || result.Replay != "AVAILABLE" && result.Replay != "NOT_RETAINED" {
		return result, c.Fail(c.StoreIntegrityError, "provider stream cursor or state reference invalid")
	}
	if result.Replay == "NOT_RETAINED" {
		if len(result.Deltas) != 0 || result.ResyncRequired {
			return result, c.Fail(c.StoreIntegrityError, "unretained provider stream returned replay records")
		}
		return result, nil
	}
	if result.ResyncRequired {
		if query.After >= result.EarliestDeltaSeq-1 || len(result.Deltas) != 0 || result.Next != query.After {
			return result, c.Fail(c.StoreIntegrityError, "provider stream retention gap is inconsistent")
		}
		return result, nil
	}
	if query.After < result.EarliestDeltaSeq-1 {
		return result, c.Fail(c.StoreIntegrityError, "provider stream silently crossed a retention gap")
	}
	for i, delta := range result.Deltas {
		if delta.Sequence != query.After+int64(i)+1 || delta.DurableTaskSeq < 0 || delta.DurableTaskSeq > result.CurrentState.TaskSeq || len(delta.Text) == 0 || len(delta.Text) > 4096 || !utf8.ValidString(delta.Text) {
			return result, c.Fail(c.StoreIntegrityError, "provider stream delta is invalid")
		}
	}
	if len(result.Deltas) > query.Limit || len(result.Deltas) > 0 && result.Next != result.Deltas[len(result.Deltas)-1].Sequence || len(result.Deltas) == 0 && result.Next != query.After {
		return result, c.Fail(c.StoreIntegrityError, "provider stream page cursor differs from deltas")
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
	providerDeltas := f.Bool("provider-deltas", false, "include ephemeral visible provider text deltas")
	providerAfter := f.Int64("provider-after", 0, "exclusive ephemeral provider delta cursor")
	resync := f.Bool("resync", false, "explicitly resume at the current durable/provider head after a retention gap")
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
	if task == "" || *directory == "" || *after < 0 || *providerAfter < 0 || *limit < 1 || *limit > 256 || *poll < 100 || *poll > 5000 || action == "detach" && (*after != 0 || *follow || *providerDeltas || *providerAfter != 0 || *resync) || action == "attach" && *commandID != "" {
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
	providerUnavailableReported := false
	for {
		page, err := readAttachment(ctx, *directory, task, owner.Page{After: *after, Limit: *limit})
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if page.ResyncRequired {
			if !*resync {
				gap := struct {
					SchemaVersion int                   `json:"schema_version"`
					Kind          string                `json:"kind"`
					From          int64                 `json:"after_task_seq"`
					Earliest      int64                 `json:"earliest_available_task_seq"`
					Current       owner.CurrentStateRef `json:"current_state_ref"`
				}{1, "attachment_gap", *after, page.EarliestTaskSeq, page.CurrentStateRef}
				_ = jsonWrite(out, gap)
				return 3
			}
			resynced := map[string]any{"schema_version": 1, "kind": "attachment_resynced", "from_task_seq": *after, "cursor": page.State.TaskSeq, "current_state_ref": page.CurrentStateRef}
			if err = jsonWrite(out, resynced); err != nil {
				return 4
			}
			*after = page.State.TaskSeq
			first = true
			continue
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
		if *providerDeltas && !providerUnavailableReported {
			stream, streamErr := readProviderDeltas(ctx, *directory, task, owner.StreamPageQuery{After: *providerAfter, Limit: *limit})
			if streamErr != nil {
				return report(out, errout, streamErr, *jsonMode)
			}
			if stream.Replay == "NOT_RETAINED" {
				if err = jsonWrite(out, map[string]any{"schema_version": 1, "kind": "provider_stream_unavailable", "reason": "NOT_RETAINED", "current_state_ref": stream.CurrentState}); err != nil {
					return 4
				}
				providerUnavailableReported = true
			} else if stream.ResyncRequired {
				if !*resync {
					if err = jsonWrite(out, map[string]any{"schema_version": 1, "kind": "provider_stream_gap", "after_delta_seq": *providerAfter, "earliest_available_delta_seq": stream.EarliestDeltaSeq, "current_delta_seq": stream.CurrentDeltaSeq, "current_state_ref": stream.CurrentState}); err != nil {
						return 4
					}
					return 3
				}
				if err = jsonWrite(out, map[string]any{"schema_version": 1, "kind": "provider_stream_resynced", "from_delta_seq": *providerAfter, "cursor": stream.CurrentDeltaSeq, "current_state_ref": stream.CurrentState}); err != nil {
					return 4
				}
				*providerAfter = stream.CurrentDeltaSeq
			} else {
				for _, delta := range stream.Deltas {
					envelope := map[string]any{"schema_version": 1, "kind": "provider_text_delta", "delta_seq": delta.Sequence, "durable_task_seq": delta.DurableTaskSeq, "text": delta.Text, "invocation_id": stream.InvocationID}
					if err = jsonWrite(out, envelope); err != nil {
						return 4
					}
				}
				*providerAfter = stream.Next
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
