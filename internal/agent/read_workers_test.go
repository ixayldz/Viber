package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestReadWorkersRunSourceBoundFleetAndPersistFullCharge(t *testing.T) {
	session, options, _ := startFixture(t, []Turn{{Text: "fixture output", UsageKnown: true}}, true)
	defer session.Close()
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	request := ReadWorkersRequest{
		OperationID: "0123456789abcdef0123456789abcdef", CandidateDigest: doc.Candidate.SnapshotDigest,
		Workers: []ReadWorkerKind{ReadWorkerRequirements, ReadWorkerInventory}, TimeoutMillis: 250,
	}
	result, err := session.RunReadWorkers(context.Background(), options.TaskID, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "COMPLETE" || result.Failure != nil || len(result.Workers) != 2 || !c.ValidDigest(result.ResultDigest) || result.Binding.DocumentDigest != state.DocumentDigest || result.Binding.CandidateDigest != doc.Candidate.SnapshotDigest || result.Charge.Admission.OperationID != request.OperationID {
		t.Fatalf("read-worker result lost its source or admission binding: %#v", result)
	}
	charged, chargedDoc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if charged.TaskSeq != state.TaskSeq+1 || charged.WorkBudget == nil || charged.WorkBudget.Steps != 2 || charged.WorkBudget.ToolCalls != 2 || charged.WorkBudget.ActiveMillis != 250 || len(charged.ReadWorkerAdmissions) != 1 || chargedDoc.Budget.Steps != 2 || chargedDoc.Budget.ToolCalls != 2 || chargedDoc.Budget.ActiveMillis != 250 {
		t.Fatalf("full fixed charge was not durable in both journal and document: %#v %#v", charged.WorkBudget, chargedDoc.Budget)
	}
	if _, err = session.RunReadWorkers(context.Background(), options.TaskID, request); err == nil {
		t.Fatal("the same operation ID was allowed to spend a second charge")
	}
	unchanged, unchangedDoc, err := session.Load(context.Background(), options.TaskID)
	if err != nil || unchanged.TaskSeq != charged.TaskSeq || unchangedDoc.Budget != chargedDoc.Budget {
		t.Fatalf("duplicate operation changed the task budget: %v", err)
	}
}

func TestReadWorkersAdmissionUsesReopenedOwnerGenerationAndOriginalSourceCursor(t *testing.T) {
	session, options, _ := startFixture(t, []Turn{{Text: "fixture output", UsageKnown: true}}, true)
	directory := session.directory
	original, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if original.Execution != c.Ready {
		t.Fatalf("fixture must leave a quiescent Ready task, got %s", original.Execution)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	currentGeneration := reopened.Journal.Generation()
	if currentGeneration <= original.KernelGeneration {
		t.Fatalf("reopened store did not advance owner generation: old=%d current=%d", original.KernelGeneration, currentGeneration)
	}
	stale, currentDoc, err := reopened.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.KernelGeneration != original.KernelGeneration || stale.TaskSeq != original.TaskSeq || stale.DocumentDigest != original.DocumentDigest || currentDoc.Candidate.SnapshotDigest != doc.Candidate.SnapshotDigest {
		t.Fatalf("reopen unexpectedly mutated the original source cursor: %#v", stale)
	}
	request := ReadWorkersRequest{
		OperationID: "5123456789abcdef0123456789abcdef", CandidateDigest: currentDoc.Candidate.SnapshotDigest,
		ExpectedDocumentDigest: original.DocumentDigest, Workers: []ReadWorkerKind{ReadWorkerRequirements, ReadWorkerInventory}, TimeoutMillis: 250,
	}
	result, err := reopened.RunReadWorkers(context.Background(), options.TaskID, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "COMPLETE" || result.Failure != nil || result.Binding.DocumentDigest != original.DocumentDigest ||
		result.Binding.TaskSeq != original.TaskSeq || result.Binding.KernelGeneration != currentGeneration ||
		result.Charge.Admission.SourceTaskSeq != original.TaskSeq || result.Charge.Admission.SourceDocumentDigest != original.DocumentDigest ||
		result.Charge.Admission.KernelGeneration != currentGeneration {
		t.Fatalf("read-worker admission lost the reopened generation or original source cursor: %#v", result)
	}
	final, _, err := reopened.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Execution != c.Ready || final.TaskSeq != original.TaskSeq+1 || final.KernelGeneration != currentGeneration {
		t.Fatalf("read-only admission changed execution state or used the wrong task cursor: %#v", final)
	}
}

func TestReadWorkersChargesBeforeCandidateManifestInspection(t *testing.T) {
	makeRequest := func(doc Document) ReadWorkersRequest {
		return ReadWorkersRequest{
			OperationID: "1123456789abcdef0123456789abcdef", CandidateDigest: doc.Candidate.SnapshotDigest,
			Workers: []ReadWorkerKind{ReadWorkerRequirements, ReadWorkerInventory}, TimeoutMillis: 250,
		}
	}
	removeManifest := func(t *testing.T, session *Session, ref artifact.Ref) {
		t.Helper()
		relative, err := artifact.ScopedSnapshotPath(ref.TaskID, ref.SnapshotDigest)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(session.directory, "artifacts", filepath.FromSlash(relative))
		if err = os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	durableStateAndDocument := func(t *testing.T, session *Session, task string) (c.TaskState, Document) {
		t.Helper()
		state, err := session.State(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := session.Archive.GetBytes(task, state.DocumentDigest)
		if err != nil {
			t.Fatal(err)
		}
		var doc Document
		if err = c.DecodeStrict(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return state, doc
	}

	t.Run("exhausted parent rejects before reading the missing manifest", func(t *testing.T) {
		session, options, _ := startFixtureConfigured(t, []Turn{{Text: "fixture output", UsageKnown: true}}, true, func(options *StartOptions) {
			options.Budget.MaxSteps = 1
		})
		defer session.Close()
		state, doc, err := session.Load(context.Background(), options.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		removeManifest(t, session, doc.Candidate)
		_, err = session.RunReadWorkers(context.Background(), options.TaskID, makeRequest(doc))
		var typed *c.Error
		if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
			t.Fatalf("budget admission should reject before candidate manifest inspection, got %v", err)
		}
		current, currentDoc := durableStateAndDocument(t, session, options.TaskID)
		if current.TaskSeq != state.TaskSeq || currentDoc.Budget.Steps != 0 || len(current.ReadWorkerAdmissions) != 0 {
			t.Fatalf("failed preflight must not inspect or charge the source: state=%#v", current)
		}
	})

	t.Run("source inspection failure remains durably charged", func(t *testing.T) {
		session, options, _ := startFixture(t, []Turn{{Text: "fixture output", UsageKnown: true}}, true)
		defer session.Close()
		state, doc, err := session.Load(context.Background(), options.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		removeManifest(t, session, doc.Candidate)
		result, err := session.RunReadWorkers(context.Background(), options.TaskID, makeRequest(doc))
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "CHARGED_SOURCE_VALIDATION_FAILED" || result.Failure == nil || result.Failure.Code != c.StoreIntegrityError || !c.ValidDigest(result.ResultDigest) {
			t.Fatalf("missing candidate manifest should be an explicit charged failure: %#v", result)
		}
		current, currentDoc := durableStateAndDocument(t, session, options.TaskID)
		if current.TaskSeq != state.TaskSeq+1 || current.WorkBudget == nil || current.WorkBudget.Steps != 2 || current.WorkBudget.ToolCalls != 2 || current.WorkBudget.ActiveMillis != 250 || len(current.ReadWorkerAdmissions) != 1 || currentDoc.Budget.Steps != 2 || currentDoc.Budget.ToolCalls != 2 || currentDoc.Budget.ActiveMillis != 250 {
			t.Fatalf("source inspection failure did not retain its full durable charge: %#v %#v", current.WorkBudget, currentDoc.Budget)
		}
	})
}
