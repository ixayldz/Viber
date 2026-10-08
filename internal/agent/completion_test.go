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

func TestBoundedFinalRepairRetainsGoalAndCannotLoopForever(t *testing.T) {
	s, options, _ := planFixture(t, false)
	defer s.Close()
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	fixtureRaw, err := s.Archive.GetBytes(doc.TaskID, doc.FixtureDigest)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := ParseFixture(fixtureRaw)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Turns = append(fixture.Turns, Turn{Text: "still claims done", UsageKnown: true, InputTokens: 10, OutputTokens: 10}, Turn{Text: "again", UsageKnown: true, InputTokens: 10, OutputTokens: 10})
	fixtureRaw, _ = json.Marshal(fixture)
	doc.FixtureDigest, err = s.Archive.PutBytes(doc.TaskID, fixtureRaw)
	if err != nil {
		t.Fatal(err)
	}
	doc.MaxRepairs = 2
	goal := doc.Spec.Goal
	inputs, _ := c.Digest(doc.Spec.Inputs)
	if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err = s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterInputs, _ := c.Digest(doc.Spec.Inputs)
	if state.Execution != c.WaitingUser || doc.RepairAttempts != 2 || doc.Blocker != "PLAN_NODES_PENDING" || doc.Spec.Goal != goal || inputs != afterInputs || doc.FixtureCursor != 6 {
		t.Fatal(state, doc.Blocker, doc.RepairAttempts, doc.FixtureCursor)
	}
	if doc.Budget.Steps != 6 || doc.Budget.UsedInput != 60 || doc.FinalArtifactDigest != "" {
		t.Fatal("repair free or premature report", doc.Budget)
	}
}
func TestAnalysisTaskRejectsWritesAndPublishesBoundReport(t *testing.T) {
	s, options, source := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true, InputTokens: 10, OutputTokens: 10}, {Text: "Analysis with no changes", UsageKnown: true, InputTokens: 10, OutputTokens: 10}}, true)
	defer s.Close()
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.TaskKind = "ANALYSIS"
	if _, err = s.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err = s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Quality != c.Unverified || doc.FinalArtifactDigest == "" || doc.Candidate != doc.Baseline || !doc.Messages[2].Replies[0].IsError {
		t.Fatal("analysis wrote or report missing", doc)
	}
	raw, err := s.Archive.GetBytes(doc.TaskID, doc.FinalArtifactDigest)
	if err != nil {
		t.Fatal(err)
	}
	var report FinalArtifact
	if err = c.DecodeStrict(raw, &report); err != nil || report.Kind != "ANALYSIS" || report.Trust != "MODEL_AUTHORED_UNREVIEWED" {
		t.Fatal(report, err)
	}
	export := filepath.Join(t.TempDir(), "export")
	if _, err = s.Export(context.Background(), options.TaskID, export); err != nil {
		t.Fatal(err)
	}
	if raw, err = os.ReadFile(filepath.Join(export, "final-artifact.json")); err != nil || !strings.Contains(string(raw), "ANALYSIS") {
		t.Fatal(err)
	}
	if raw, err = os.ReadFile(filepath.Join(source, "a.txt")); err != nil || string(raw) != "before\r\n" {
		t.Fatal("source changed", err)
	}
	doc.FinalSummary = "altered report"
	if err = s.validateFinalArtifact(doc); err == nil {
		t.Fatal("report mismatch accepted")
	}
}
func TestUnchangedCodeIsExplicitNoChangesWithoutVerifiedClaim(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{Text: "no modifications needed", UsageKnown: true, InputTokens: 1, OutputTokens: 1}}, true)
	defer s.Close()
	if _, err := s.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	value, err := s.Observe(context.Background(), options.TaskID, Observation{Kind: "report"})
	if err != nil {
		t.Fatal(err)
	}
	report := value.(FinalArtifact)
	if report.Kind != "NO_CHANGES" || report.InputsDigest == "" {
		t.Fatal(report)
	}
}
func TestRegisteredChecksCannotBeSkippedByFinalSummary(t *testing.T) {
	s, options := checkFixture(t, "golang@sha256:"+strings.Repeat("a", 64), []Turn{{Text: "everything passed", UsageKnown: true, InputTokens: 1, OutputTokens: 1}})
	state, err := s.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.WaitingUser || doc.Blocker != "CURRENT_REGISTERED_CHECKS_REQUIRED" || doc.FinalArtifactDigest != "" {
		t.Fatal(state, doc.Blocker, err)
	}
}
func TestContextObservationPagesExactRequestWithoutEffect(t *testing.T) {
	s, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true, InputTokens: 1, OutputTokens: 1}}, true)
	defer s.Close()
	if _, err := s.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	state, doc, err := s.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	value, err := s.Observe(context.Background(), options.TaskID, Observation{Kind: "context-why"})
	if err != nil {
		t.Fatal(err)
	}
	why := value.(ContextExplanation)
	if !why.Available || !why.ProtocolMandatory || len(why.Components) != 3 || why.RequestDigest != doc.Context.RequestDigest {
		t.Fatal(why)
	}
	raw, err := s.Archive.GetBytes(doc.TaskID, why.RequestDigest)
	if err != nil {
		t.Fatal(err)
	}
	pages := []byte{}
	for offset := int64(0); offset < int64(len(raw)); {
		value, err = s.Observe(context.Background(), options.TaskID, Observation{Kind: "context-page", Offset: offset, Limit: 123})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(value)
		var page struct {
			Bytes []byte `json:"exact_bytes"`
			Next  int64  `json:"next_offset"`
		}
		if err = json.Unmarshal(encoded, &page); err != nil || page.Next <= offset {
			t.Fatal(err)
		}
		pages = append(pages, page.Bytes...)
		offset = page.Next
	}
	if string(pages) != string(raw) {
		t.Fatal("request paging lost bytes")
	}
	after, err := s.State(context.Background(), options.TaskID)
	if err != nil || after.TaskSeq != state.TaskSeq {
		t.Fatal("read executed mutation", err)
	}
	for _, query := range []Observation{{Kind: "context-page", Offset: -1, Limit: 10}, {Kind: "context-page", Limit: 65537}, {Kind: "context-why", RunID: "other"}, {Kind: "report", Limit: 1}} {
		if _, err = s.Observe(context.Background(), options.TaskID, query); err == nil {
			t.Fatal(query)
		}
	}
}
