package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func mergedParentFixture(t *testing.T) (*Session, StartOptions, c.TaskState) {
	t.Helper()
	s, options := checkFixture(t, "golang@sha256:"+strings.Repeat("a", 64), []Turn{{Text: "parent complete", UsageKnown: true}})
	// Storage-only old check fixture. Actual native rerun is a separate opt-in.
	syntheticCheck(t, s, options)
	parent, err := s.Run(context.Background(), options.TaskID)
	if err != nil || parent.Execution != c.Terminated {
		t.Fatal(parent, err)
	}
	return s, options, parent
}

func mergedCommand(t *testing.T, s *Session, parent c.TaskState, newTask string) DeliveryReverificationCommand {
	t.Helper()
	output := filepath.Join(t.TempDir(), "preview")
	manifest, err := s.DeliveryPreview(context.Background(), parent.TaskID, output)
	if err != nil || manifest.Preview.Status != "READY" {
		t.Fatal(manifest, err)
	}
	return DeliveryReverificationCommand{CommandID: "reverify-" + newTask, ParentTask: parent.TaskID, NewTask: newTask, ParentSequence: parent.TaskSeq, PreviewDirectory: output, ManifestDigest: manifest.Digest, AllowUnverified: true}
}

func TestMergedReverificationImportsFreshImmutableCandidateWithoutOldAuthority(t *testing.T) {
	ctx := context.Background()
	s, options, parent := mergedParentFixture(t)
	if err := os.WriteFile(filepath.Join(options.Root, "user.txt"), []byte("independent user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	command := mergedCommand(t, s, parent, "merged-child")
	child, err := s.CreateDeliveryReverification(ctx, command)
	if err != nil || child.Execution != c.Ready || child.Quality != c.Unverified {
		t.Fatal(child, err)
	}
	_, doc, err := s.Load(ctx, command.NewTask)
	if err != nil || doc.MergedDelivery == nil || doc.Baseline.SnapshotDigest != doc.MergedDelivery.Result || doc.Baseline != doc.Candidate || len(doc.CheckRuns) != 0 || doc.GoalCoverage != nil || len(doc.Requests) != 0 || doc.Runtime != nil || doc.Budget.Steps != 0 || doc.Budget.UsedInput != 0 || taskKind(doc) != "ANALYSIS" {
		t.Fatal("old checks/approval/context/runtime were carried", doc, err)
	}
	capture, err := s.Archive.Get(doc.Candidate)
	if err != nil || string(capture.Contents["user.txt"]) != "independent user edit" || string(capture.Contents["a.txt"]) != "before\r\n" {
		t.Fatal("merged bytes lost", capture, err)
	}
	fixtureRaw, err := s.Archive.GetBytes(doc.TaskID, doc.FixtureDigest)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := ParseFixture(fixtureRaw)
	if err != nil || len(fixture.Turns) != 2 || len(fixture.Turns[0].Calls) != 1 || fixture.Turns[0].Calls[0].Name != "check_run" {
		t.Fatal("scheduler did not require fresh registered checks", fixture, err)
	}
	if _, _, err = s.executeTool(ctx, child, &doc, model.Call{Name: "candidate_propose"}); err == nil {
		t.Fatal("merged reverification permitted candidate mutation")
	}
	if duplicate, err := s.CreateDeliveryReverification(ctx, command); err != nil || duplicate.TaskSeq != child.TaskSeq {
		t.Fatal("dedup changed task", duplicate, err)
	}
	changed := command
	changed.CommandID += "-changed"
	if _, err = s.CreateDeliveryReverification(ctx, changed); err == nil {
		t.Fatal("new ID adopted existing child")
	}
	after, err := s.State(ctx, parent.TaskID)
	if err != nil || after.TaskSeq != parent.TaskSeq || after.DocumentDigest != parent.DocumentDigest || after.Quality != parent.Quality {
		t.Fatal("parent authority changed", after, err)
	}
	// Lost creation responses return the recorded task, even if today's source
	// changes. No new source capture or native dispatch occurs on this retry.
	if err = os.WriteFile(filepath.Join(options.Root, "user.txt"), []byte("later user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := s.CreateDeliveryReverification(ctx, command); err != nil || duplicate.TaskSeq != child.TaskSeq {
		t.Fatal("creation retry reinterpreted new live source", duplicate, err)
	}
}

func TestMergedReverificationRejectsStaleTamperedAndUnregisteredPreviews(t *testing.T) {
	for _, attack := range []string{"live-changed", "manifest-changed", "foreign-output", "wrong-cursor", "wrong-manifest", "protected-closure-changed"} {
		t.Run(attack, func(t *testing.T) {
			ctx := context.Background()
			s, options, parent := mergedParentFixture(t)
			if attack == "protected-closure-changed" {
				if err := os.Mkdir(filepath.Join(options.Root, "checks"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(options.Root, "checks", "weakened.txt"), []byte("new tests outside original protected origin"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := mergedCommand(t, s, parent, "rejected-child")
			switch attack {
			case "live-changed":
				if err := os.WriteFile(filepath.Join(options.Root, "new.txt"), []byte("new user bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "manifest-changed":
				if err := os.WriteFile(filepath.Join(command.PreviewDirectory, "manifest.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-output":
				foreign := t.TempDir()
				raw, err := os.ReadFile(filepath.Join(command.PreviewDirectory, "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(foreign, "manifest.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				command.PreviewDirectory = foreign
			case "wrong-cursor":
				command.ParentSequence++
			case "wrong-manifest":
				command.ManifestDigest = c.HashBytes([]byte("foreign"))
			}
			if _, err := s.CreateDeliveryReverification(ctx, command); err == nil {
				t.Fatal("unsafe preview admitted", attack)
			}
			states, err := s.Journal.Replay(ctx, "")
			if err != nil || len(states) != 1 {
				t.Fatal("rejected preview published a child", states, err)
			}
			objects, err := s.Archive.TaskInventory(ctx, command.NewTask)
			if err != nil || len(objects) != 0 {
				t.Fatal("rejected preview wrote raw content", objects, err)
			}
		})
	}
}

func TestMergedReverificationReconcilesCreatedTaskAfterRestart(t *testing.T) {
	ctx := context.Background()
	s, _, parent := mergedParentFixture(t)
	command := mergedCommand(t, s, parent, "interrupted-merged")
	s.creationFault = func(point c.ExecutionState) error {
		if point == c.Created {
			return errors.New("creation interrupted")
		}
		return nil
	}
	state, err := s.CreateDeliveryReverification(ctx, command)
	if err == nil || state.Execution != c.Created {
		t.Fatal(state, err)
	}
	directory := s.directory
	s.Close()
	s, err = OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, err = s.CreateDeliveryReverification(ctx, command)
	if err != nil || state.Execution != c.Ready || state.KernelGeneration != s.Journal.Generation() {
		t.Fatal(state, err)
	}
}

func TestActualDockerMergedCandidateReverificationRunsFreshRegisteredChecks(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned real Docker image required")
	}
	ctx := context.Background()
	turns := []Turn{{Calls: []model.Call{{ID: "parent-native-check", Name: "check_run", Arguments: []byte(`{"check_id":"registered"}`)}}, UsageKnown: true}, {Text: "parent complete", UsageKnown: true}}
	s, options := checkFixture(t, image, turns)
	parent, err := s.Run(ctx, options.TaskID)
	if err != nil || parent.Execution != c.Terminated {
		t.Fatal(parent, err)
	}
	if err = os.WriteFile(filepath.Join(options.Root, "user.txt"), []byte("preserved user bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	command := mergedCommand(t, s, parent, "actual-merged")
	if _, err = s.CreateDeliveryReverification(ctx, command); err != nil {
		t.Fatal(err)
	}
	state, err := s.Run(ctx, command.NewTask)
	if err != nil || state.Execution != c.Terminated || state.Quality != c.Unverified {
		t.Fatal(state, err)
	}
	_, doc, err := s.Load(ctx, command.NewTask)
	if err != nil || len(doc.CheckRuns) != 1 || doc.CheckRuns[0].ID != "merged-check-00" || doc.MergedDelivery == nil {
		t.Fatal(doc, err)
	}
	record, err := s.readCheckRun(doc, doc.CheckRuns[0])
	if err != nil || record.Result.CandidateDigest != doc.MergedDelivery.Result || record.TaskID != command.NewTask || record.Outcome != "EXIT_ZERO" || !record.Result.SourceReadOnly || !record.Result.ProcessTreeQuiescent {
		t.Fatal(record, err)
	}
	after, err := s.State(ctx, parent.TaskID)
	if err != nil || after.TaskSeq != parent.TaskSeq || after.DocumentDigest != parent.DocumentDigest {
		t.Fatal("original task changed", after, err)
	}
	raw, err := os.ReadFile(filepath.Join(options.Root, "user.txt"))
	if err != nil || string(raw) != "preserved user bytes" {
		t.Fatal("user source changed", err)
	}
}
