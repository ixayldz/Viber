package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func TestCompiledContextRetainsIntentPolicyProtocolAndRestoresItsManifest(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{UsageKnown: true, Text: "done", InputTokens: 2, OutputTokens: 1}}, false)
	defer s.Close()
	if _, err := s.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Context == nil || doc.Context.Profile != offlineContextProfile || len(doc.Context.Manifest.Omitted) != 0 || doc.Context.Manifest.OutputReserve != 512 {
		t.Fatal("compiled context missing or silently omitted data")
	}
	raw, err := s.Archive.GetBytes(options.TaskID, doc.Context.RequestDigest)
	if err != nil {
		t.Fatal(err)
	}
	var request model.Request
	if err = c.DecodeStrict(raw, &request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.Instructions, doc.Spec.Goal) || !strings.Contains(request.Instructions, "restriction_layers") || !strings.Contains(request.Instructions, "UNRESOLVED_GOAL_COVERAGE") || request.Messages[0].Text != string(options.Prompt) || len(request.Tools) != len(Tools()) {
		t.Fatal("required intent/policy/protocol dropped")
	}
	if doc.Context.Manifest.InputTokens < int64(len(raw)) {
		t.Fatal("non-conservative byte estimate")
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(context.Background(), backup, target); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	_, actual, err := restored.Load(context.Background(), options.TaskID)
	if err != nil || actual.Context == nil || actual.Context.RequestDigest != doc.Context.RequestDigest {
		t.Fatal("context audit did not restore", err)
	}
}
func TestMandatoryContextOverflowDoesNotInvokeOrReserveTheFixture(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{UsageKnown: true, Text: "must not execute"}}, true)
	defer s.Close()
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Messages = append(doc.Messages, model.Message{Role: "assistant", Text: strings.Repeat("large history\r\n", 40000)})
	if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, after, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Execution != c.WaitingResource || !strings.Contains(after.Blocker, "CONTEXT_TOO_SMALL") || after.FixtureCursor != 0 || after.Budget.Steps != 0 || after.Budget.ReservedInput != 0 || after.Budget.ReservedOutput != 0 || after.Context != nil || after.Pending != nil {
		t.Fatal("oversized mandatory context dispatched or silently truncated", state, after.Budget)
	}
}
func TestContextRequestAndManifestCorruptionFailClosed(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing-request", false: "manifest-mismatch"}[missing], func(t *testing.T) {
			s, options, _ := startFixture(t, []Turn{{UsageKnown: true, Text: "done"}}, false)
			defer s.Close()
			if _, err := s.Run(context.Background(), options.TaskID); err != nil {
				t.Fatal(err)
			}
			state, doc, err := s.Load(context.Background(), options.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				digest := doc.Context.RequestDigest
				if err = os.Remove(filepath.Join(s.directory, "artifacts", "tasks", options.TaskID, "blobs", digest[:2], digest)); err != nil {
					t.Fatal(err)
				}
			} else {
				doc.Context.Manifest.InputTokens++
				if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err = s.Load(context.Background(), options.TaskID); err == nil {
				t.Fatal("invalid context became authoritative")
			}
			if _, err = s.Backup(context.Background(), filepath.Join(t.TempDir(), "backup")); err == nil {
				t.Fatal("corrupt context was backed up")
			}
		})
	}
}
func TestContextCompilerRejectsUnpairedToolProtocolBeforeAdmission(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{UsageKnown: true, Text: "done"}}, true)
	defer s.Close()
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Messages = append(doc.Messages, model.Message{Role: "assistant", Calls: []model.Call{{ID: "call", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}}, model.Message{Role: "user", Text: "unexpected input"})
	if _, _, _, err = compileOfflineRequest(doc, state, s.layers(state, doc)); err == nil {
		t.Fatal("unpaired protocol compiled")
	}
}
