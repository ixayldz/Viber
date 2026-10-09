package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestPrivacyAttemptStageCrashHelper(t *testing.T) {
	directory := os.Getenv("VIBER_PRIVACY_STAGE_HELPER_STORE")
	if directory == "" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	raw, err := os.ReadFile(os.Getenv("VIBER_PRIVACY_STAGE_HELPER_REQUEST"))
	if err != nil {
		t.Fatal(err)
	}
	var request AttemptOptions
	if err = c.DecodeStrict(raw, &request); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	// No Close or deferred cleanup: the first inherited CAS publication is
	// durable, but the task does not have its first journal record.
	s.attemptStageFault = func(int) error { os.Exit(71); return nil }
	if _, err = s.NewAttempt(ctx, request); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash hook was not reached")
}

func TestPrivacyUnpublishedAttemptCrashIsPurgedWithParentAndCannotResurrect(t *testing.T) {
	ctx := context.Background()
	s, options, source := deletionFixture(t)
	sourceBefore, err := os.ReadFile(filepath.Join(source, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.State(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := s.Journal.TokenLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "child complete", UsageKnown: true}}})
	request := AttemptOptions{ParentTask: options.TaskID, NewTask: "unpublished-child", ExpectedParentSequence: parent.TaskSeq, Fixture: fixture, AllowUnverified: true}
	raw, _ := c.CanonicalV1(request)
	requestFile := filepath.Join(t.TempDir(), "attempt.json")
	if err = os.WriteFile(requestFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	directory := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestPrivacyAttemptStageCrashHelper$")
	command.Env = append(os.Environ(), "VIBER_PRIVACY_STAGE_HELPER_STORE="+directory, "VIBER_PRIVACY_STAGE_HELPER_REQUEST="+requestFile)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 71 {
		t.Fatal("abrupt crash did not reach durable copy", err, string(output))
	}
	s, err = OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, published := states[request.NewTask]; published {
		t.Fatal("crash happened after task publication")
	}
	before, err := s.Archive.TaskInventory(ctx, request.NewTask)
	if err != nil || len(before) != 1 {
		t.Fatal("expected one unpublished raw copy", before, err)
	}
	options.TaskID = request.NewTask
	if _, err = s.Create(ctx, options); err == nil {
		t.Fatal("generic Create reused staged attempt identity")
	}
	changed := request
	changed.AllowUnverified = false
	if _, err = s.NewAttempt(ctx, changed); err == nil {
		t.Fatal("changed attempt request adopted staged content")
	}
	plan, err := s.DeletionPreview(ctx, parent.TaskID)
	if err != nil || len(plan.StagedAttempts) != 1 || len(plan.StagedAttempts[0].Objects) != 1 {
		t.Fatal("unpublished derivative absent from deletion manifest", plan.StagedAttempts, err)
	}
	deletion := DeletionCommand{CommandID: "delete-crashed-parent-attempt", Plan: plan}
	for retry := 0; retry < 2; retry++ {
		if result, err := s.DeleteContent(ctx, deletion); err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
			t.Fatal(result, err)
		}
	}
	remaining, err := s.Archive.TaskInventory(ctx, request.NewTask)
	if err != nil || len(remaining) != 0 {
		t.Fatal("unpublished child retained raw content", remaining, err)
	}
	if _, err = s.NewAttempt(ctx, request); err == nil {
		t.Fatal("deleted staged attempt resurrected")
	}
	if _, err = s.Create(ctx, options); err == nil {
		t.Fatal("deleted derived identity reused")
	}
	after, err := s.Journal.TokenLedger(ctx)
	if err != nil || !reflect.DeepEqual(ledger, after) {
		t.Fatal("unpublished attempt charged or reset ledger", ledger, after, err)
	}
	if raw, err = os.ReadFile(filepath.Join(source, "a.txt")); err != nil || !bytes.Equal(raw, sourceBefore) {
		t.Fatal("live source changed", err)
	}
}

func TestPrivacyStagedAttemptStaleAndLateContentCannotProduceFalsePurge(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	parent, err := s.State(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "child", UsageKnown: true}}})
	request := AttemptOptions{ParentTask: options.TaskID, NewTask: "late-staged-child", ExpectedParentSequence: parent.TaskSeq, Fixture: fixture, AllowUnverified: true}
	s.attemptStageFault = func(int) error { return errors.New("staged crash") }
	if _, err = s.NewAttempt(ctx, request); err == nil {
		t.Fatal("fault was not reached")
	}
	s.attemptStageFault = nil
	stale, err := s.DeletionPreview(ctx, parent.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Archive.PutBytes(request.NewTask, []byte("copy changed after preview")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "stale-staged-inventory", Plan: stale}); err == nil {
		t.Fatal("stale staged inventory accepted")
	}
	plan, err := s.DeletionPreview(ctx, parent.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	command := DeletionCommand{CommandID: "late-staged-inventory", Plan: plan}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	view, err := readPrivacy(root)
	if err == nil {
		err = appendPrivacy(root, view, privacyRecord{Type: "DELETE_INTENT", Command: &command})
	}
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := s.Archive.PutBytes(request.NewTask, []byte("foreign payload after authorization"))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := s.DeleteContent(ctx, command); err == nil || result.Status != "PENDING" {
		t.Fatal("late staged content gave false PURGED", result, err)
	}
	remaining, err := s.Archive.TaskInventory(ctx, request.NewTask)
	if err != nil || len(remaining) != 1 || remaining[0].Digest != digest {
		t.Fatal(remaining, err)
	}
	if _, err = s.Archive.RemoveTaskContent(request.NewTask, remaining[0]); err != nil {
		t.Fatal(err)
	}
	if result, err := s.DeleteContent(ctx, command); err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal("exact partial-unlink retry failed", result, err)
	}
}

func TestPrivacyStagedAttemptExactRetryReconcilesWithoutDuplicateLineage(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	parent, err := s.State(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "child complete", UsageKnown: true}}})
	request := AttemptOptions{ParentTask: options.TaskID, NewTask: "retry-staged-child", ExpectedParentSequence: parent.TaskSeq, Fixture: fixture, AllowUnverified: true}
	s.attemptStageFault = func(int) error { return errors.New("interrupted before TaskCreated") }
	if _, err = s.NewAttempt(ctx, request); err == nil {
		t.Fatal("fault was not reached")
	}
	s.attemptStageFault = nil
	if state, err := s.NewAttempt(ctx, request); err != nil || state.Execution != c.Ready {
		t.Fatal("exact request did not reconcile allocation", state, err)
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	view, err := readPrivacy(root)
	root.Close()
	if err != nil || len(view.Attempts) != 1 {
		t.Fatal("retry duplicated durable lineage", view.Attempts, err)
	}
	if _, err = s.DeletionPreview(ctx, parent.TaskID); err == nil {
		t.Fatal("published derived child no longer pinned parent")
	}
	if _, err = s.Run(ctx, request.NewTask); err != nil {
		t.Fatal(err)
	}
	childPlan, err := s.DeletionPreview(ctx, request.NewTask)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "delete-reconciled-child", Plan: childPlan}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.DeletionPreview(ctx, parent.TaskID)
	if err != nil || len(plan.StagedAttempts) != 0 {
		t.Fatal("deleted published child left a false orphan pin", plan.StagedAttempts, err)
	}
}
