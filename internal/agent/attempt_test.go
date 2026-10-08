package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestTerminalAttemptPreservesHistoryIntentFreshCaptureAndLedger(t *testing.T) {
	s, options, source := startFixture(t, []Turn{{Text: "first result", UsageKnown: true, InputTokens: 11, OutputTokens: 7}}, true)
	defer s.Close()
	parent, err := s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, original, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "user.txt"), []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "second result", UsageKnown: true, InputTokens: 3, OutputTokens: 2}}})
	request := AttemptOptions{ParentTask: options.TaskID, NewTask: "attempt-two", ExpectedParentSequence: parent.TaskSeq, Fixture: fixture, AllowUnverified: true}
	next, err := s.NewAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := s.Load(context.Background(), request.NewTask)
	if err != nil {
		t.Fatal(err)
	}
	if next.Execution != c.Ready || next.Quality != c.Unverified || created.Budget.UsedInput != 0 || created.Baseline == original.Baseline || created.Spec.Goal != original.Spec.Goal || created.Spec.Requirements[0] != original.Spec.Requirements[0] {
		t.Fatal("attempt copied old evidence or dropped goal", next)
	}
	if created.AttemptOrigin.ParentDocument != parent.DocumentDigest || created.AttemptOrigin.ParentSequence != parent.TaskSeq {
		t.Fatal("lineage missing")
	}
	captured, err := s.Archive.Get(created.Baseline)
	if err != nil || string(captured.Contents["user.txt"]) != "user edit" {
		t.Fatal("fresh user edit missing", err)
	}
	same, err := s.NewAttempt(context.Background(), request)
	if err != nil || same.TaskSeq != next.TaskSeq {
		t.Fatal("duplicate creation changed state", err)
	}
	request.AllowUnverified = false
	if _, err = s.NewAttempt(context.Background(), request); err == nil {
		t.Fatal("attempt id conflict accepted")
	}
	request.AllowUnverified = true
	if _, err = s.Run(context.Background(), request.NewTask); err != nil {
		t.Fatal(err)
	}
	after, err := s.State(context.Background(), options.TaskID)
	if err != nil || after.DocumentDigest != parent.DocumentDigest || after.TaskSeq != parent.TaskSeq || after.Tokens.Used.Input != 11 {
		t.Fatal("parent mutated", after, err)
	}
	ledger, err := s.Journal.TokenLedger(context.Background())
	if err != nil || ledger.Used.Input != 14 || ledger.Tasks != 2 {
		t.Fatal(ledger, err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	restore := filepath.Join(t.TempDir(), "restore")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if _, err = RestoreBackup(context.Background(), backup, restore); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(context.Background(), restore)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, _, err = restored.Load(context.Background(), request.NewTask); err != nil {
		t.Fatal(err)
	}
}
func TestAttemptRequiresExactTerminalParentAndNoUnknownEffects(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{Text: "usage unknown", UsageKnown: false}}, true)
	defer s.Close()
	fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "retry", UsageKnown: true}}})
	request := AttemptOptions{ParentTask: options.TaskID, NewTask: "attempt-two", ExpectedParentSequence: 3, Fixture: fixture}
	if _, err := s.NewAttempt(context.Background(), request); err == nil {
		t.Fatal("nonterminal reopened")
	}
	if _, err := s.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	parent, err := s.Controls(context.Background(), options.TaskID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedParentSequence = parent.TaskSeq
	if _, err = s.NewAttempt(context.Background(), request); err == nil {
		t.Fatal("unknown effect blindly repeated")
	}
	request.NewTask = options.TaskID
	if _, err = s.NewAttempt(context.Background(), request); err == nil {
		t.Fatal("same terminal task reopened")
	}
}

func TestAttemptReconcilesInterruptedCreationAcrossOwnerRestart(t *testing.T) {
	for _, point := range []c.ExecutionState{c.Created, c.Scoping} {
		t.Run(string(point), func(t *testing.T) {
			s, options, _ := startFixture(t, []Turn{{Text: "parent complete", UsageKnown: true, InputTokens: 7, OutputTokens: 2}}, true)
			parent, err := s.Run(context.Background(), options.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			fixture, _ := json.Marshal(Fixture{SchemaVersion: 1, Turns: []Turn{{Text: "child complete", UsageKnown: true, InputTokens: 3, OutputTokens: 1}}})
			request := AttemptOptions{ParentTask: options.TaskID, NewTask: "interrupted-attempt", ExpectedParentSequence: parent.TaskSeq, Fixture: fixture, AllowUnverified: true}
			s.creationFault = func(actual c.ExecutionState) error {
				if actual == point {
					return errors.New("creation interrupted")
				}
				return nil
			}
			state, err := s.NewAttempt(context.Background(), request)
			if err == nil || state.Execution != point {
				t.Fatal("injection not reached", state, err)
			}
			directory := s.directory
			s.Close()
			restored, err := OpenExisting(context.Background(), directory)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			state, err = restored.NewAttempt(context.Background(), request)
			if err != nil || state.Execution != c.Ready || state.KernelGeneration != restored.Journal.Generation() {
				t.Fatal("creation not reconciled", state, err)
			}
			state, err = restored.Run(context.Background(), request.NewTask)
			if err != nil || state.Execution != c.Terminated {
				t.Fatal("reconciled attempt unusable", state, err)
			}
			ledger, err := restored.Journal.TokenLedger(context.Background())
			if err != nil || ledger.Tasks != 2 || ledger.Used.Input != 10 {
				t.Fatal("creation retry charged twice", ledger, err)
			}
			after, err := restored.State(context.Background(), parent.TaskID)
			if err != nil || after.TaskSeq != parent.TaskSeq {
				t.Fatal("parent changed", err)
			}
		})
	}
}
