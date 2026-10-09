package agent

import (
	"context"
	"encoding/json"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/kernel"
	"github.com/ixayldz/Viber/internal/model"
	"os"
	"path/filepath"
	"testing"
)

func TestActualObserverBaselineFailCandidatePassAndForgedPASS(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned real Docker image required")
	}
	for _, scenario := range []struct {
		name    string
		script  []byte
		success bool
	}{{"fixed production", []byte("tr a-z A-Z\n"), true}, {"self report PASS", []byte("printf PASS\n"), false}} {
		t.Run(scenario.name, func(t *testing.T) {
			patchArgs, _ := json.Marshal(struct {
				Read    []c.ReadCondition `json:"read_set"`
				Changes []c.Change        `json:"changes"`
			}{
				[]c.ReadCondition{{Path: "app.sh", Kind: "FILE", Digest: c.HashBytes([]byte("cat\n"))}},
				[]c.Change{{Path: "app.sh", BeforeDigest: c.HashBytes([]byte("cat\n")), After: scenario.script}},
			})
			turns := []Turn{{Calls: []model.Call{{ID: "repair", Name: "candidate_propose", Arguments: patchArgs}}, UsageKnown: true, InputTokens: 10, OutputTokens: 10}, {Calls: []model.Call{{ID: "verify", Name: "check_run", Arguments: json.RawMessage(`{"check_id":"echo"}`)}}, UsageKnown: true, InputTokens: 10, OutputTokens: 10}, {Text: "Candidate delivery.", UsageKnown: true, InputTokens: 10, OutputTokens: 10}}
			s, options := observedFixture(t, image, true, turns, func(options *StartOptions) {
				options.Prompt = []byte("Transform the supplied ASCII bytes to uppercase, with no stderr.")
				options.GoalReview.InputDigests = []string{c.HashBytes(options.Prompt)}
				suite := &options.CheckRuntime.ObserverSuites[0]
				suite.Cases[0].Input = []byte("word\n")
				suite.Cases[0].Stdout = []byte("WORD\n")
				plan, err := ParseCheckPlan(options.CheckPlan)
				if err != nil {
					t.Fatal(err)
				}
				plan.Checks[0].ObserverDigest, _ = c.Digest(*suite)
				options.CheckPlan, _ = c.CanonicalV1(plan)
			})
			state, err := s.Run(context.Background(), options.TaskID)
			if err != nil {
				t.Fatal(state, err)
			}
			_, doc, err := s.Load(context.Background(), options.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			report, err := s.deriveVerification(doc, state)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Baselines) != 1 || report.Baselines[0].Verdict != c.FailVerdict {
				t.Fatal("baseline failure missing", report.Baselines)
			}
			if scenario.success {
				if !kernel.StrictSuccess(state) || doc.Candidate.SnapshotDigest == doc.Baseline.SnapshotDigest || report.Assessment.Quality != c.Verified {
					t.Fatal(state, doc.Blocker, report.Assessment)
				}
			} else if kernel.StrictSuccess(state) || state.Quality != c.QualityFailed || report.Assessment.Quality != c.QualityFailed {
				t.Fatal("self-report accepted", state, report.Assessment)
			}
			live, err := os.ReadFile(filepath.Join(options.Root, "app.sh"))
			if err != nil || string(live) != "cat\n" {
				t.Fatal("user source modified", err)
			}
		})
	}
}
