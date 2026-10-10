package agent

import (
	"context"
	"errors"
	"fmt"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func configureExpiry(t *testing.T, s *Session, task, id string) c.RetentionPolicy {
	t.Helper()
	policy, err := s.SetRetention(context.Background(), task, RetentionOptions{CommandID: id, Mode: "EXPIRE", Deadline: time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano), Acknowledgement: c.RetentionAcknowledgement})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
func awaitRetentionDeadline(t *testing.T, policy c.RetentionPolicy) {
	t.Helper()
	deadline, err := c.RetentionTime(policy.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(deadline); d > 0 {
		time.Sleep(d + time.Millisecond)
	}
}

func TestRetentionActualExpiryPurgesManagedBackupWithoutChangingRiskLedger(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	backup := t.TempDir() + "/backup"
	if _, err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	before, err := s.Journal.ResourceLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policy := configureExpiry(t, s, options.TaskID, "retention-enable")
	if run, err := s.RunRetention(ctx); err != nil || len(run.Items) != 0 {
		t.Fatal(run, err)
	}
	awaitRetentionDeadline(t, policy)
	run, err := s.RunRetention(ctx)
	if err != nil || len(run.Items) != 1 || run.Items[0].Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal(run, err)
	}
	state, err := s.State(ctx, options.TaskID)
	digest, _ := c.Digest(policy)
	if err != nil || state.Deletion == nil || state.Deletion.Status != "PURGED" || state.Deletion.RetentionDigest != digest {
		t.Fatal(state, err)
	}
	after, err := s.Journal.ResourceLedger(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("risk ledger changed", err)
	}
	if _, _, err = s.Load(ctx, options.TaskID); err == nil {
		t.Fatal("expired content remained readable")
	}
	if _, err = RestoreBackup(ctx, backup, t.TempDir()+"/expired-restore"); err == nil {
		t.Fatal("old backup usable")
	}
	status, err := s.RetentionStatus(ctx, options.TaskID)
	if err != nil || status.Status != "PURGED" {
		t.Fatal(status, err)
	}
	if run, err = s.RunRetention(ctx); err != nil || len(run.Items) != 0 {
		t.Fatal("duplicate expiry", run, err)
	}
	history, err := s.Journal.History(ctx, options.TaskID, 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range history.Records {
		if record.Event.Type == "TaskContentDeleted" {
			found = true
			if record.Event.Actor != "retention" {
				t.Fatal("expiry impersonated user")
			}
		}
	}
	if !found {
		t.Fatal("missing expiry receipt")
	}
}

func TestRetentionRevisionConsentDedupAndEarlyForgeryPreserveContent(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	state, err := s.State(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	bad := RetentionOptions{CommandID: "missing-consent", Mode: "EXPIRE", Deadline: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if _, err = s.SetRetention(ctx, options.TaskID, bad); err == nil {
		t.Fatal("implicit expiry enabled")
	}
	policy := configureExpiry(t, s, options.TaskID, "enable-once")
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	forged := DeletionCommand{CommandID: "early-forgery", Plan: plan, Expiry: &c.RetentionExpiry{Policy: policy, ObservedAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)}}
	if _, err = s.DeleteContent(ctx, forged); err == nil {
		t.Fatal("future observation authorized early expiry")
	}
	if _, err = s.SetRetention(ctx, options.TaskID, RetentionOptions{CommandID: "stale-disable", Mode: "KEEP"}); err == nil {
		t.Fatal("stale revision accepted")
	}
	keep := RetentionOptions{CommandID: "keep-current", ExpectedRevision: 1, Mode: "KEEP"}
	kept, err := s.SetRetention(ctx, options.TaskID, keep)
	if err != nil || kept.Revision != 2 {
		t.Fatal(kept, err)
	}
	directory := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	retry, err := reopened.SetRetention(ctx, options.TaskID, keep)
	if err != nil || !reflect.DeepEqual(retry, kept) {
		t.Fatal(retry, err)
	}
	keep.Mode = "EXPIRE"
	if _, err = reopened.SetRetention(ctx, options.TaskID, keep); err == nil {
		t.Fatal("changed command adopted")
	}
	awaitRetentionDeadline(t, policy)
	if run, err := reopened.RunRetention(ctx); err != nil || len(run.Items) != 0 {
		t.Fatal("disabled policy expired", run, err)
	}
	current, err := reopened.State(ctx, options.TaskID)
	if err != nil || current.TaskSeq != state.TaskSeq || current.DocumentDigest != state.DocumentDigest || current.Deletion != nil {
		t.Fatal("retention mutated task execution", current, err)
	}
}

func TestRetentionActualCrashRecoveryUsesImmutableExpiryIntent(t *testing.T) {
	for _, stage := range []string{"intent", "pending", "object", "before-purged"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			s, options, _ := deletionFixture(t)
			policy := configureExpiry(t, s, options.TaskID, "enable-"+stage)
			directory := s.directory
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			awaitRetentionDeadline(t, policy)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(exe, "-test.run=^TestRetentionCrashHelper$")
			child.Env = append(os.Environ(), "VIBER_RETENTION_TEST_STORE="+directory, "VIBER_RETENTION_TEST_STAGE="+stage)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 84 {
				t.Fatalf("actual child did not die at %s: %v %s", stage, err, output)
			}
			reopened, err := OpenExisting(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			view, err := reopened.retentionView(ctx)
			if err != nil {
				t.Fatal(err)
			}
			original := view.Deletions[options.TaskID]
			if original.Expiry == nil {
				t.Fatal("missing external expiry intent")
			}
			before, _ := c.Digest(original)
			run, err := reopened.RunRetention(ctx)
			if err != nil || len(run.Items) != 1 || run.Items[0].Status != "PURGED_MANAGED_LOCAL_CONTENT" {
				t.Fatal(run, err)
			}
			view, err = reopened.retentionView(ctx)
			after, _ := c.Digest(view.Deletions[options.TaskID])
			if err != nil || before != after || view.Watermark != 1 {
				t.Fatal("expiry intent reset", view.Watermark, err)
			}
			if run, err = reopened.RunRetention(ctx); err != nil || len(run.Items) != 0 {
				t.Fatal("expiry was duplicated", run, err)
			}
		})
	}
}
func TestRetentionCrashHelper(t *testing.T) {
	stage := os.Getenv("VIBER_RETENTION_TEST_STAGE")
	directory := os.Getenv("VIBER_RETENTION_TEST_STORE")
	if stage == "" || directory == "" {
		t.Skip("explicit disposable child only")
	}
	s, err := OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	s.retentionFault = func(at string) error {
		if at == stage {
			os.Exit(84)
		}
		return nil
	}
	if _, err = s.RunRetention(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("fault boundary was not reached")
}

func TestRetentionBoundedPassDoesNotStarveEligibleTasksBehindActiveTasks(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Text: "unused", UsageKnown: true}}, true)
	defer s.Close()
	var last c.RetentionPolicy
	for i := 0; i < 17; i++ {
		if i > 0 {
			options.TaskID = fmt.Sprintf("task-%02d", i)
			if _, err := s.Create(ctx, options); err != nil {
				t.Fatal(err)
			}
		}
		if i == 16 {
			if _, err := s.Controls(ctx, options.TaskID, "cancel"); err != nil {
				t.Fatal(err)
			}
		}
		last = configureExpiry(t, s, options.TaskID, fmt.Sprintf("consent-%02d", i))
	}
	awaitRetentionDeadline(t, last)
	first, err := s.RunRetention(ctx)
	if err != nil || !first.HasMore || len(first.Items) != 16 {
		t.Fatal(first, err)
	}
	for _, item := range first.Items {
		if item.Status != "BLOCKED" || item.Deletion != nil {
			t.Fatal("active task was expired", item)
		}
	}
	second, err := s.RunRetention(ctx)
	if err != nil || len(second.Items) != 16 || second.Items[0].TaskID != options.TaskID || second.Items[0].Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal("eligible task was starved", second, err)
	}
	view, err := s.retentionView(ctx)
	if err != nil || len(view.Deletions) != 1 || view.Watermark != 1 {
		t.Fatal("blocked tasks published deletion intents", view.Watermark, err)
	}
}

func TestRetentionUnknownRiskAndRetainedDescendantRemainPinned(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown", func(t *testing.T) {
		s, options, _ := startFixture(t, []Turn{{Text: "unknown charge", UsageKnown: false}}, true)
		defer s.Close()
		if _, err := s.Run(ctx, options.TaskID); err != nil {
			t.Fatal(err)
		}
		before, err := s.Journal.ResourceLedger(ctx)
		if err != nil {
			t.Fatal(err)
		}
		policy := configureExpiry(t, s, options.TaskID, "unknown-consent")
		awaitRetentionDeadline(t, policy)
		run, err := s.RunRetention(ctx)
		if err != nil || len(run.Items) != 1 || run.Items[0].Status != "BLOCKED" || run.Items[0].Deletion != nil {
			t.Fatal("UNKNOWN risk was waived", run, err)
		}
		after, err := s.Journal.ResourceLedger(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("UNKNOWN ledger changed", err)
		}
		if _, _, err := s.Load(ctx, options.TaskID); err != nil {
			t.Fatal("blocked content became unavailable", err)
		}
	})
	t.Run("retained-descendant", func(t *testing.T) {
		s, options, _ := deletionFixture(t)
		parent, err := s.State(ctx, options.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.NewAttempt(ctx, AttemptOptions{ParentTask: options.TaskID, NewTask: "retained-child", ExpectedParentSequence: parent.TaskSeq, Fixture: options.Fixture, AllowUnverified: true}); err != nil {
			t.Fatal(err)
		}
		policy := configureExpiry(t, s, options.TaskID, "parent-consent")
		awaitRetentionDeadline(t, policy)
		run, err := s.RunRetention(ctx)
		if err != nil || len(run.Items) != 1 || run.Items[0].Status != "BLOCKED" || run.Items[0].Deletion != nil {
			t.Fatal("retained child lost parent evidence", run, err)
		}
		view, err := s.retentionView(ctx)
		if err != nil || len(view.Deletions) != 0 || view.Watermark != 0 {
			t.Fatal("pinned parent published intent", err)
		}
	})
}

func TestRetentionReservesPendingDeletionReceiptNamespacesAndRejectsRehashedConsent(t *testing.T) {
	ctx := context.Background()
	t.Run("reserved-purge", func(t *testing.T) {
		s, options, _ := deletionFixture(t)
		commandID := "future-user-deletion"
		reserved := "privacy-purge-" + c.HashBytes([]byte(commandID))
		if _, err := s.SetRetention(ctx, options.TaskID, RetentionOptions{CommandID: reserved, Mode: "KEEP"}); err != nil {
			t.Fatal(err)
		}
		plan, err := s.DeletionPreview(ctx, options.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: commandID, Plan: plan}); err == nil {
			t.Fatal("deletion reused retention identity")
		}
		view, err := s.retentionView(ctx)
		if err != nil || len(view.Deletions) != 0 || view.Watermark != 0 {
			t.Fatal("collision published irreversible intent", err)
		}
		if _, _, err := s.Load(ctx, options.TaskID); err != nil {
			t.Fatal(err)
		}
	})
	for _, attack := range []string{"foreign-authority", "input-digest", "revision-gap"} {
		t.Run(attack, func(t *testing.T) {
			s, options, _ := deletionFixture(t)
			policy, err := s.SetRetention(ctx, options.TaskID, RetentionOptions{CommandID: "valid-consent", Mode: "KEEP"})
			if err != nil {
				t.Fatal(err)
			}
			policy.Revision = 2
			policy.ChangedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if attack == "foreign-authority" {
				policy.AuthorityDigest = c.HashBytes([]byte("different authority"))
			}
			if attack == "revision-gap" {
				policy.Revision = 4
			}
			input, _ := retentionInputDigest(options.TaskID, RetentionOptions{CommandID: "forged", Mode: "KEEP", ExpectedRevision: policy.Revision - 1})
			if attack == "input-digest" {
				input = c.HashBytes([]byte("unrelated input"))
			}
			root, err := openPrivacyRoot(*s.privacy)
			if err != nil {
				t.Fatal(err)
			}
			view, err := readPrivacy(root)
			if err == nil {
				err = appendPrivacy(root, view, privacyRecord{Type: "RETENTION_POLICY", Retention: &privacyRetentionRecord{CommandID: "forged", InputDigest: input, Policy: policy}})
			}
			root.Close()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.RunRetention(ctx); err == nil {
				t.Fatal("rehashed malformed consent accepted")
			}
			states, err := s.Journal.Replay(ctx, options.TaskID)
			if err != nil || states[options.TaskID].Deletion != nil {
				t.Fatal("invalid consent mutated kernel", err)
			}
		})
	}
}

func TestRetentionPendingExpiryKeepsConsentAndPurgesRegisteredRestoredOwner(t *testing.T) {
	ctx := context.Background()
	s, options, _ := deletionFixture(t)
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackup(ctx, backup, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
	other := options
	other.TaskID = "other-task"
	if _, err = s.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	policy := configureExpiry(t, s, options.TaskID, "family-consent")
	awaitRetentionDeadline(t, policy)
	s.retentionFault = func(at string) error {
		if at == "intent" {
			return errors.New("fixture stops after immutable intent")
		}
		return nil
	}
	interrupted, err := s.RunRetention(ctx)
	if err != nil || len(interrupted.Items) != 1 || interrupted.Items[0].Status != "BLOCKED" {
		t.Fatal(interrupted, err)
	}
	view, err := s.retentionView(ctx)
	if err != nil || len(view.Deletions[options.TaskID].Plan.Stores) != 1 {
		t.Fatal("restored owner missing from intent", err)
	}
	command := view.Deletions[options.TaskID]
	if _, err = s.SetRetention(ctx, options.TaskID, RetentionOptions{CommandID: "disable-pending", Mode: "KEEP", ExpectedRevision: 1}); err == nil {
		t.Fatal("pending irreversible consent was revised")
	}
	for _, id := range deletionCommandIDs(command) {
		if _, err = s.SetRetention(ctx, other.TaskID, RetentionOptions{CommandID: id, Mode: "KEEP"}); err == nil {
			t.Fatal("pending receipt namespace reused", id)
		}
	}
	s.retentionFault = nil
	run, err := s.RunRetention(ctx)
	if err != nil || len(run.Items) != 1 || run.Items[0].Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal(run, err)
	}
	restored, err = OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	state, err := restored.State(ctx, options.TaskID)
	if err != nil || state.Deletion == nil || state.Deletion.Status != "PURGED" || state.Deletion.RetentionDigest == "" {
		t.Fatal("restored owner expiry receipt missing", state, err)
	}
	if _, _, err = restored.Load(ctx, options.TaskID); err == nil {
		t.Fatal("restored content survived expiry")
	}
}
