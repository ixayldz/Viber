package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

type FinalArtifact struct {
	SchemaVersion int    `json:"schema_version"`
	TaskID        string `json:"task_id"`
	SpecVersion   int64  `json:"spec_version"`
	Candidate     string `json:"candidate"`
	InputsDigest  string `json:"inputs_digest"`
	Kind          string `json:"kind"`
	Summary       string `json:"untrusted_model_summary"`
	Trust         string `json:"trust"`
}

func taskKind(doc Document) string {
	if doc.TaskKind == "" {
		return "CODE"
	}
	return doc.TaskKind
}
func validateLoop(doc Document) error {
	if taskKind(doc) != "CODE" && taskKind(doc) != "ANALYSIS" || doc.MaxRepairs < 0 || doc.MaxRepairs > 8 || doc.RepairAttempts < 0 || doc.RepairAttempts > doc.MaxRepairs {
		return c.Fail(c.InvalidArgument, "invalid task kind or bounded repair state")
	}
	if taskKind(doc) == "ANALYSIS" && doc.Candidate.SnapshotDigest != doc.Baseline.SnapshotDigest {
		return c.Fail(c.PolicyDenied, "analysis task cannot mutate its candidate")
	}
	return nil
}
func finalArtifact(doc Document) FinalArtifact {
	inputs, _ := c.Digest(doc.Spec.Inputs)
	kind := taskKind(doc)
	if kind == "CODE" && doc.Candidate.SnapshotDigest == doc.Baseline.SnapshotDigest {
		kind = "NO_CHANGES"
	}
	return FinalArtifact{SchemaVersion: 1, TaskID: doc.TaskID, SpecVersion: doc.Spec.Version, Candidate: doc.Candidate.SnapshotDigest, InputsDigest: inputs, Kind: kind, Summary: doc.FinalSummary, Trust: "MODEL_AUTHORED_UNREVIEWED"}
}
func (s *Session) validateFinalArtifact(doc Document) error {
	if err := validateLoop(doc); err != nil {
		return err
	}
	if doc.FinalArtifactDigest == "" {
		return nil
	}
	if !doc.FinalReady {
		return c.Fail(c.StoreIntegrityError, "report without final boundary")
	}
	raw, err := s.Archive.GetBytes(doc.TaskID, doc.FinalArtifactDigest)
	if err != nil {
		return c.Fail(c.StoreIntegrityError, "final artifact unavailable")
	}
	var actual FinalArtifact
	if err = c.DecodeStrict(raw, &actual); err != nil {
		return err
	}
	expected, _ := c.Digest(finalArtifact(doc))
	got, _ := c.Digest(actual)
	if expected != got {
		return c.Fail(c.StoreIntegrityError, "final artifact source/candidate/summary binding mismatch")
	}
	return nil
}
func (s *Session) finalBlocker(doc Document, state c.TaskState) (string, error) {
	if !utf8.ValidString(doc.FinalSummary) || strings.TrimSpace(doc.FinalSummary) == "" || len(doc.FinalSummary) > 64<<10 {
		return "BOUNDED_FINAL_ARTIFACT_REQUIRED", nil
	}
	if !doc.Plan.Complete() {
		return "PLAN_NODES_PENDING", nil
	}
	if doc.CheckRuntime != nil {
		checks, err := s.CheckSummaries(doc, state)
		if err != nil {
			return "", err
		}
		for _, check := range checks {
			if !check.Current || (check.Outcome != "EXIT_ZERO" && check.Outcome != "OBSERVER_PASS") {
				return "CURRENT_REGISTERED_CHECKS_REQUIRED", nil
			}
		}
	}
	return "", nil
}
func (s *Session) repairFinal(ctx context.Context, state c.TaskState, doc Document, reason string) (c.TaskState, error) {
	if doc.RepairAttempts >= doc.MaxRepairs {
		return s.stop(ctx, state, doc, reason, c.WaitingUser)
	}
	doc.RepairAttempts++
	doc.FinalReady = false
	doc.FinalArtifactDigest = ""
	doc.VerificationReport = ""
	doc.Blocker = ""
	doc.Messages = append(doc.Messages, model.Message{Role: "user", Text: "Kernel completion feedback (not new user intent or permissions): " + reason + ". Continue the existing authorized goal through native tools. Complete pending plan nodes and run current registered checks. Return a bounded final summary only when these obligations are resolved. This repair consumes the existing work budget."})
	return s.record(ctx, state, doc, "SessionRecorded", c.EventPayload{})
}
