package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func privacyEvaluatorOptions(task string) StartOptions {
	fixture, _ := c.CanonicalV1(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "independent fixture complete", UsageKnown: true}}})
	return StartOptions{TaskID: task, TaskKind: "ANALYSIS", Prompt: []byte("independent fixed checks"), Fixture: fixture, Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true}
}

func TestEvaluationPrivacyChildFirstPurgesReportsBackupsAndRestoredOwners(t *testing.T) {
	ctx := context.Background()
	original, options, _ := deletionFixture(t)
	t.Cleanup(func() {
		if original != nil {
			original.Close()
		}
	})
	originalDirectory := original.directory
	source, _, err := original.FreezeEvaluationSource(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err = os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	child, err := original.OpenIndependentEvaluator(ctx, filepath.Join(bundle, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child != nil {
			child.Close()
		}
	})
	task := child.EvaluationTaskID()
	if _, err = child.CreateIndependentEvaluator(ctx, original, source, privacyEvaluatorOptions(task)); err != nil {
		t.Fatal(err)
	}
	if _, err = child.Run(ctx, task); err != nil {
		t.Fatal(err)
	}
	if state, err := child.State(ctx, task); err != nil {
		t.Fatal(err)
	} else if state.Execution != c.Terminated {
		if _, err = child.ControlsWithID(ctx, task, "cancel", "close-eval-fixture"); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err = original.RegisterEvaluationReport(ctx, child, root, source); err != nil {
		t.Fatal(err)
	}
	raw, _ := c.CanonicalV1(source)
	if err = fileguard.Publish(root, "source.json", raw); err != nil {
		t.Fatal(err)
	}
	root.Close()
	if _, err = original.DeletionPreview(ctx, options.TaskID); err == nil {
		t.Fatal("active evaluator did not fence parent deletion")
	}
	if _, err = child.DeletionPreview(ctx, task); err == nil {
		t.Fatal("active original did not fence child deletion")
	}
	if err = child.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = original.DeletionPreview(ctx, options.TaskID); err == nil {
		t.Fatal("retained evaluator did not pin parent")
	}
	if err = original.Close(); err != nil {
		t.Fatal(err)
	}
	child, err = OpenExisting(ctx, filepath.Join(bundle, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := child.Journal.TokenLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = child.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err = child.Close(); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(ctx, backup, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, doc, err := restored.Load(ctx, task); err != nil || doc.EvaluationOrigin == nil {
		t.Fatal("restored evaluator lost immutable lineage", err)
	}
	restored.Close()
	child, err = OpenExisting(ctx, filepath.Join(bundle, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := child.DeletionPreview(ctx, task)
	if err != nil || len(plan.Stores) != 1 || len(plan.Copies) < 2 {
		t.Fatal("missing report/backup/restore scope", len(plan.Stores), len(plan.Copies), err)
	}
	command := DeletionCommand{CommandID: "delete-evaluator", Plan: plan}
	result, err := child.DeleteContent(ctx, command)
	if err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal(result, err)
	}
	after, err := child.Journal.TokenLedger(ctx)
	if err != nil || !reflect.DeepEqual(ledger, after) {
		t.Fatal("deletion rewrote child accounting", err)
	}
	if _, err = os.Stat(filepath.Join(bundle, "source.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source report survived", err)
	}
	if _, _, err = child.Load(ctx, task); err == nil {
		t.Fatal("deleted evaluator content accessible")
	}
	child.Close()
	if _, err = RestoreBackup(ctx, backup, filepath.Join(t.TempDir(), "resurrection")); err == nil {
		t.Fatal("old evaluator backup resurrected")
	}
	restored, err = OpenExisting(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := restored.Archive.TaskInventory(ctx, task)
	if err != nil || len(objects) != 0 {
		t.Fatal("restored evaluator retained raw content", objects, err)
	}
	restored.Close()
	original, err = OpenExisting(ctx, originalDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	plan, err = original.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal("purged child still pins parent", err)
	}
	if result, err = original.DeleteContent(ctx, DeletionCommand{CommandID: "delete-original", Plan: plan}); err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal(result, err)
	}
}

func TestEvaluationPrivacyStageCrashHelper(t *testing.T) {
	directory := os.Getenv("VIBER_EVAL_PRIVACY_CRASH_ORIGINAL")
	if directory == "" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	original, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := original.FreezeEvaluationSource(ctx, os.Getenv("VIBER_EVAL_PRIVACY_CRASH_TASK"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := original.OpenIndependentEvaluator(ctx, os.Getenv("VIBER_EVAL_PRIVACY_CRASH_CHILD"))
	if err != nil {
		t.Fatal(err)
	}
	child.attemptStageFault = func(int) error { os.Exit(72); return nil }
	if _, err = child.CreateIndependentEvaluator(ctx, original, source, privacyEvaluatorOptions(child.EvaluationTaskID())); err != nil {
		t.Fatal(err)
	}
	t.Fatal("first candidate write did not reach crash hook")
}

func TestEvaluationPrivacyUnpublishedProcessCrashPurgesImportedCandidate(t *testing.T) {
	ctx := context.Background()
	original, options, _ := deletionFixture(t)
	t.Cleanup(func() {
		if original != nil {
			original.Close()
		}
	})
	directory := original.directory
	before, err := original.Journal.TokenLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original.Close()
	childPath := filepath.Join(t.TempDir(), "owner")
	timeout, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	command := exec.CommandContext(timeout, os.Args[0], "-test.run=^TestEvaluationPrivacyStageCrashHelper$")
	command.Env = append(os.Environ(), "VIBER_EVAL_PRIVACY_CRASH_ORIGINAL="+directory, "VIBER_EVAL_PRIVACY_CRASH_TASK="+options.TaskID, "VIBER_EVAL_PRIVACY_CRASH_CHILD="+childPath)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 72 {
		t.Fatal("candidate copy did not crash before journal publication", err, string(output))
	}
	original, err = OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := original.DeletionPreview(ctx, options.TaskID)
	if err != nil || len(plan.StagedAttempts) != 1 || len(plan.StagedAttempts[0].Objects) == 0 {
		t.Fatal("unpublished evaluator was not inventoried", plan, err)
	}
	task := plan.StagedAttempts[0].Allocation.ChildTask
	if result, err := original.DeleteContent(ctx, DeletionCommand{CommandID: "delete-crashed-eval-parent", Plan: plan}); err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
		t.Fatal(result, err)
	}
	after, err := original.Journal.TokenLedger(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("staged purge rewrote parent ledger", err)
	}
	child, err := OpenExisting(ctx, childPath)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	defer original.Close()
	states, err := child.Journal.Replay(ctx, "")
	if err != nil || len(states) != 0 {
		t.Fatal("purge manufactured evaluator history", states, err)
	}
	objects, err := child.Archive.TaskInventory(ctx, task)
	if err != nil || len(objects) != 0 {
		t.Fatal("staged candidate survived", objects, err)
	}
	if _, err = child.Create(ctx, privacyEvaluatorOptions(task)); err == nil {
		t.Fatal("deleted staged evaluator resurrected")
	}
}

func TestEvaluationPrivacyFreshOwnersHaveUniqueReservedTaskIdentities(t *testing.T) {
	ctx := context.Background()
	original, options, _ := deletionFixture(t)
	defer original.Close()
	source, _, err := original.FreezeEvaluationSource(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(t.TempDir(), "first")
	first, err := original.OpenIndependentEvaluator(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := original.OpenIndependentEvaluator(ctx, filepath.Join(t.TempDir(), "second"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.EvaluationTaskID() == second.EvaluationTaskID() {
		t.Fatal("evaluator task namespace collided")
	}
	if _, err = original.OpenIndependentEvaluator(ctx, firstPath); err == nil {
		t.Fatal("existing owner adopted")
	}
	if _, err = second.Create(ctx, privacyEvaluatorOptions(first.EvaluationTaskID())); err == nil {
		t.Fatal("generic creation borrowed evaluator identity")
	}
	alien, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer alien.Close()
	if _, err = alien.CreateIndependentEvaluator(ctx, original, source, privacyEvaluatorOptions(alien.EvaluationTaskID())); err == nil {
		t.Fatal("unrelated deletion authority imported parent")
	}
	if _, err = first.CreateIndependentEvaluator(ctx, original, source, privacyEvaluatorOptions(first.EvaluationTaskID())); err != nil {
		t.Fatal(err)
	}
	if _, err = second.CreateIndependentEvaluator(ctx, original, source, privacyEvaluatorOptions(second.EvaluationTaskID())); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluationPrivacyForeignReportAndOwnerReplacementFailClosed(t *testing.T) {
	ctx := context.Background()
	original, options, _ := deletionFixture(t)
	t.Cleanup(func() { original.Close() })
	source, _, err := original.FreezeEvaluationSource(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	childPath := filepath.Join(bundle, "owner")
	child, err := original.OpenIndependentEvaluator(ctx, childPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Close() })
	task := child.EvaluationTaskID()
	if _, err = child.CreateIndependentEvaluator(ctx, original, source, privacyEvaluatorOptions(task)); err != nil {
		t.Fatal(err)
	}
	if _, err = child.ControlsWithID(ctx, task, "cancel", "close-report-fixture"); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err = original.RegisterEvaluationReport(ctx, child, root, source); err != nil {
		t.Fatal(err)
	}
	root.Close()
	if err = os.WriteFile(filepath.Join(bundle, "foreign.txt"), []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	original.Close()
	if _, err = child.DeletionPreview(ctx, task); err == nil {
		t.Fatal("foreign report file was admitted to purge")
	}
	if err = os.Remove(filepath.Join(bundle, "foreign.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = child.DeletionPreview(ctx, task); err != nil {
		t.Fatal("valid report scope rejected", err)
	}
	child.Close()
	if err = os.Rename(childPath, filepath.Join(bundle, "owned-original")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(childPath, 0700); err != nil {
		t.Fatal(err)
	}
	original, err = OpenExisting(ctx, original.directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = original.DeletionPreview(ctx, options.TaskID); err == nil {
		t.Fatal("replacement evaluator owner supplied deletion authority")
	}
	if _, err = os.Stat(filepath.Join(bundle, "owned-original", "state.sqlite")); err != nil {
		t.Fatal("foreign replacement led to owned journal removal", err)
	}
}
