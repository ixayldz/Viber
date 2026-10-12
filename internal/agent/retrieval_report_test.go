package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/plan"
	"github.com/ixayldz/Viber/internal/retrieval"
)

func TestCapturedRetrievalReportExportsActualRuntimePair(t *testing.T) {
	args := json.RawMessage(`{"query":"before","mode":"ranked","intent":"IDENTIFIER","limit":8}`)
	session, options := retrievalReportFixture(t, []Turn{
		{Calls: []model.Call{{ID: "search-1", Name: "fs_search", Arguments: args}}, UsageKnown: true, InputTokens: 4, OutputTokens: 2},
		{Text: "finished", UsageKnown: true, InputTokens: 4, OutputTokens: 2},
	})
	defer session.Close()
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.Terminated {
		t.Fatalf("fixture task did not settle: state=%+v err=%v", state, err)
	}
	currentState, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	callIndex, replyIndex := fixtureSearchPair(t, doc)
	var exactArguments []byte
	var exactCallID string
	for _, call := range doc.Messages[callIndex].Calls {
		if call.ID == "search-1" {
			exactCallID = call.ID
			exactArguments = append([]byte{}, call.Arguments...)
		}
	}
	var exactReply string
	for _, reply := range doc.Messages[replyIndex].Replies {
		if reply.CallID == "search-1" {
			exactReply = reply.Content
		}
	}
	report, err := session.CapturedRetrievalReport(context.Background(), options.TaskID, "search-1")
	if err != nil {
		t.Fatal(err)
	}
	if report.Kind != capturedRetrievalReportKind || report.TaskSequence != currentState.TaskSeq || report.JournalSequence != currentState.StoreSeq || report.DocumentDigest != currentState.DocumentDigest || report.KernelGeneration != currentState.KernelGeneration || report.PolicyEpoch != currentState.PolicyEpoch || report.CallID != "search-1" || report.ArgumentsJSON != string(exactArguments) || report.ArgumentsSHA256 != c.HashBytes(exactArguments) || report.FSSearchReply != exactReply || report.ReplySHA256 != c.HashBytes([]byte(report.FSSearchReply)) || exactCallID == "" || len(exactArguments) == 0 {
		t.Fatalf("export did not preserve exact evidence: kind=%q task_seq=%d/%d journal_seq=%d/%d document=%v kernel=%d/%d policy=%d/%d call=%q args_equal=%v args_hash=%v reply_equal=%v reply_hash=%v call_doc=%q", report.Kind, report.TaskSequence, currentState.TaskSeq, report.JournalSequence, currentState.StoreSeq, report.DocumentDigest == currentState.DocumentDigest, report.KernelGeneration, currentState.KernelGeneration, report.PolicyEpoch, currentState.PolicyEpoch, report.CallID, report.ArgumentsJSON == string(exactArguments), report.ArgumentsSHA256 == c.HashBytes(exactArguments), report.FSSearchReply == exactReply, report.ReplySHA256 == c.HashBytes([]byte(report.FSSearchReply)), exactCallID)
	}
	var page capturedSearchPage
	if err = c.DecodeStrict([]byte(report.FSSearchReply), &page); err != nil {
		t.Fatal(err)
	}
	if report.Query != "before" || report.Intent != "IDENTIFIER" || page.Retrieval.Manifest.Binding.Candidate != report.CandidateDigest || page.Retrieval.Manifest.Binding.Policy != report.PolicyDigest || page.Retrieval.Manifest.SourceSet != report.AuthorizedSourceSet || len(page.Retrieval.Hits) == 0 {
		t.Fatal("exported reply is not bound to the current candidate and authorized source set", report, page.Retrieval.Manifest)
	}
	if _, err = session.CapturedRetrievalReport(context.Background(), options.TaskID, "missing-call"); err == nil {
		t.Fatal("export accepted an unpaired or missing call ID")
	}
}

func TestCapturedRetrievalReportRejectsStaleCurrentPolicyBinding(t *testing.T) {
	args := json.RawMessage(`{"query":"before","mode":"ranked","intent":"IDENTIFIER","limit":8}`)
	session, options := retrievalReportFixture(t, []Turn{
		{Calls: []model.Call{{ID: "search-1", Name: "fs_search", Arguments: args}}, UsageKnown: true, InputTokens: 4, OutputTokens: 2},
		{Text: "finished", UsageKnown: true, InputTokens: 4, OutputTokens: 2},
	}, true)
	defer session.Close()
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.Terminated {
		t.Fatalf("fixture task did not settle: state=%+v err=%v", state, err)
	}
	if options.Config == nil || options.Config.Authority == nil {
		t.Fatal("fixture did not bind a current operator policy")
	}
	changed, err := c.CanonicalV1(config.Authority{SchemaVersion: 1, Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(options.Config.Authority.Path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = session.CapturedRetrievalReport(context.Background(), options.TaskID, "search-1")
	var failure *c.Error
	if !errors.As(err, &failure) || failure.Code != c.StaleAuthority {
		t.Fatalf("report accepted a changed current operator policy: err=%v", err)
	}
}

func TestCapturedRetrievalReportDoesNotHydrateDeniedCandidateBlob(t *testing.T) {
	args := json.RawMessage(`{"query":"before","mode":"ranked","intent":"IDENTIFIER","limit":8}`)
	session, options := retrievalReportFixture(t, []Turn{
		{Calls: []model.Call{{ID: "search-1", Name: "fs_search", Arguments: args}}, UsageKnown: true, InputTokens: 4, OutputTokens: 2},
		{Text: "finished", UsageKnown: true, InputTokens: 4, OutputTokens: 2},
	}, true)
	defer session.Close()
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	requirements := []string{}
	for _, requirement := range doc.Spec.Requirements {
		if requirement.Required {
			requirements = append(requirements, requirement.ID)
		}
	}
	if len(requirements) == 0 {
		t.Fatal("fixture task has no required plan obligation")
	}
	definition := plan.Definition{SchemaVersion: 1, ID: "bounded-read", SpecVersion: doc.Spec.Version, PolicyEpoch: state.PolicyEpoch, BaseCandidate: doc.Candidate.SnapshotDigest, Nodes: []plan.Node{{ID: "read-source", Goal: "inspect source", InputContract: "current candidate", OutputContract: "bounded report", ReadScope: []string{"a.txt"}, WriteScope: []string{}, RequirementIDs: requirements, RequiredChecks: []string{}, Mutex: []string{}}}, Relations: []plan.Relation{}}
	propose, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	turns := []Turn{
		{Calls: []model.Call{{ID: "plan-1", Name: "plan_propose", Arguments: propose}}, UsageKnown: true, InputTokens: 4, OutputTokens: 2},
		{Calls: []model.Call{{ID: "next-1", Name: "plan_next", Arguments: json.RawMessage(`{}`)}}, UsageKnown: true, InputTokens: 4, OutputTokens: 2},
		{Calls: []model.Call{{ID: "search-1", Name: "fs_search", Arguments: args}}, UsageKnown: true, InputTokens: 4, OutputTokens: 2},
		{Text: "finished", UsageKnown: true, InputTokens: 4, OutputTokens: 2},
	}
	fixtureBytes, err := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	if err != nil {
		t.Fatal(err)
	}
	doc.FixtureDigest, err = session.Archive.PutBytes(options.TaskID, fixtureBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.WaitingUser {
		t.Fatalf("fixture task did not settle: state=%+v err=%v", state, err)
	}
	_, doc, err = session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := session.Archive.ReadManifest(doc.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	secretDigest := ""
	for _, entry := range manifest.Snapshot.Entries {
		if entry.Path == "secret.txt" {
			secretDigest = entry.Hash
		}
	}
	if secretDigest == "" {
		t.Fatal("fixture candidate omitted its denied source")
	}
	blob := filepath.Join(session.directory, "artifacts", "tasks", doc.TaskID, "blobs", secretDigest[:2], secretDigest)
	if err = os.Remove(blob); err != nil {
		t.Fatalf("remove denied blob to detect accidental hydration: %v", err)
	}
	if _, err = session.CapturedRetrievalReport(context.Background(), options.TaskID, "search-1"); err != nil {
		t.Fatalf("report hydrated a source excluded by current policy: %v", err)
	}
	expected, err := retrievalSourceSet([]retrieval.Document{{Path: "a.txt", Digest: c.HashBytes([]byte("before\r\n")), Bytes: []byte("before\r\n")}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := session.CapturedRetrievalReport(context.Background(), options.TaskID, "search-1")
	if err != nil || report.AuthorizedSourceSet != expected {
		t.Fatalf("report did not bind exactly the currently readable sources: digest=%q expected=%q err=%v", report.AuthorizedSourceSet, expected, err)
	}
}

func fixtureSearchPair(t *testing.T, doc Document) (int, int) {
	t.Helper()
	callAt, replyAt := -1, -1
	for i, message := range doc.Messages {
		for _, call := range message.Calls {
			if call.ID == "search-1" && call.Name == "fs_search" {
				callAt = i
			}
		}
		for _, reply := range message.Replies {
			if reply.CallID == "search-1" {
				replyAt = i
			}
		}
	}
	if callAt < 0 || replyAt != callAt+1 || replyAt >= len(doc.Messages) || len(doc.Messages[replyAt].Replies) == 0 {
		t.Fatal("fixture did not retain its exact paired fs_search exchange", len(doc.Messages))
	}
	for _, reply := range doc.Messages[replyAt].Replies {
		if reply.CallID == "search-1" {
			return callAt, replyAt
		}
	}
	t.Fatal("fixture fs_search reply disappeared")
	return -1, -1
}

func retrievalReportFixture(t *testing.T, turns []Turn, bindAuthority ...bool) (*Session, StartOptions) {
	t.Helper()
	cacheRoot, err := filepath.Abs(filepath.Join("..", "..", ".cache"))
	if err != nil {
		t.Fatalf("resolve workspace cache root: %v", err)
	}
	base, err := os.MkdirTemp(cacheRoot, "viber-retrieval-report-test-")
	if err != nil {
		t.Fatalf("create private test root under workspace cache: %v", err)
	}
	t.Cleanup(func() {
		resolved, resolveErr := filepath.Abs(base)
		name := filepath.Base(resolved)
		prefix := "viber-retrieval-report-test-"
		if resolveErr == nil && filepath.Dir(resolved) == cacheRoot && len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			_ = os.RemoveAll(resolved)
		}
	})
	source := filepath.Join(base, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		t.Fatalf("create fixture source root: %v", err)
	}
	if err = os.WriteFile(filepath.Join(source, "a.txt"), []byte("before\r\n"), 0600); err != nil {
		t.Fatalf("write fixture source file: %v", err)
	}
	if len(bindAuthority) > 0 && bindAuthority[0] {
		if err = os.WriteFile(filepath.Join(source, "secret.txt"), []byte("confidential source\r\n"), 0600); err != nil {
			t.Fatalf("write policy-denied fixture file: %v", err)
		}
	}
	store := filepath.Join(base, "store")
	session, err := Open(context.Background(), store)
	if err != nil {
		t.Fatalf("open fixture store: %v", err)
	}
	fixtureBytes, err := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	options := StartOptions{Root: source, Prompt: []byte("Search the captured source."), TaskID: "retrieval-report-task", Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixtureBytes}
	if len(bindAuthority) > 0 && bindAuthority[0] {
		authorityPath := filepath.Join(base, "operator-policy.json")
		authorityBytes, marshalErr := c.CanonicalV1(config.Authority{SchemaVersion: 1, Revision: 1})
		if marshalErr != nil {
			_ = session.Close()
			t.Fatal(marshalErr)
		}
		if err = os.WriteFile(authorityPath, authorityBytes, 0600); err != nil {
			_ = session.Close()
			t.Fatal(err)
		}
		bound, bindErr := config.BindAuthority(authorityPath, source)
		if bindErr != nil {
			_ = session.Close()
			t.Fatal(bindErr)
		}
		options.Config = &config.Resolved{SchemaVersion: 1, Authority: &bound}
	}
	if _, err = session.Create(context.Background(), options); err != nil {
		_ = session.Close()
		t.Fatalf("create fixture task: %v", err)
	}
	return session, options
}
