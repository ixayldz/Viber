package agent

import (
	"context"
	"testing"

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
