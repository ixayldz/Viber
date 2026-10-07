package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func pending(t *testing.T, s *Session, task, kind string) UserRequest {
	t.Helper()
	requests, err := s.Requests(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range requests {
		if r.Kind == kind && r.Status == "PENDING" {
			return r
		}
	}
	t.Fatal("pending request missing", requests)
	return UserRequest{}
}
func TestBoundReviewApprovalIsOneShotAndDoesNotAuthorizeNextProposal(t *testing.T) {
	first := patchCall()
	raw, _ := json.Marshal(struct {
		Read    []c.ReadCondition `json:"read_set"`
		Changes []c.Change        `json:"changes"`
	}{
		[]c.ReadCondition{{Path: "a.txt", Kind: "FILE", Digest: c.HashBytes([]byte("after\r\n"))}},
		[]c.Change{{Path: "a.txt", BeforeDigest: c.HashBytes([]byte("after\r\n")), After: []byte("third")}},
	})
	second := model.Call{ID: "write-two", Name: "candidate_propose", Arguments: raw}
	session, options, _ := startFixture(t, []Turn{{Calls: []model.Call{first}, UsageKnown: true}, {Calls: []model.Call{second}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, false)
	defer session.Close()
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Autonomy = "review"
	if _, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.WaitingUser || state.CandidateDigest != state.BaselineDigest {
		t.Fatal("unapproved candidate changed", err, state)
	}
	r := pending(t, session, options.TaskID, "CANDIDATE_WRITE")
	if r.Action == nil || r.Action.ID != first.ID {
		t.Fatal("review action not concrete", r)
	}
	// A plain resume must keep the same request and perform no model/tool call.
	if _, err = session.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	if again := pending(t, session, options.TaskID, "CANDIDATE_WRITE"); again.ID != r.ID {
		t.Fatal("resume lost request binding")
	}
	response := ResponseTemplate(r, "approve-first", "approve")
	accepted, err := session.Respond(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := session.Respond(context.Background(), response)
	if err != nil || !reflect.DeepEqual(accepted, duplicate) {
		t.Fatal("response not idempotent", err)
	}
	changed := response
	changed.Response.Decision = "reject"
	if _, err = session.Respond(context.Background(), changed); err == nil {
		t.Fatal("command ID input conflict accepted")
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.WaitingUser || state.CandidateDigest == state.BaselineDigest {
		t.Fatal("approved candidate not applied", err, state)
	}
	secondRequest := pending(t, session, options.TaskID, "CANDIDATE_WRITE")
	if secondRequest.ID == r.ID || secondRequest.Action.ID != "write-two" {
		t.Fatal("approval leaked to second write")
	}
	if _, err = session.Respond(context.Background(), ResponseTemplate(secondRequest, "reject-second", "reject")); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.WaitingUser {
		t.Fatal(err, state)
	}
	_, doc, err = session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := session.Archive.Get(doc.Candidate)
	if err != nil || string(candidate.Contents["a.txt"]) != "after\r\n" {
		t.Fatal("rejected mutation executed", err)
	}
	delivery := pending(t, session, options.TaskID, "LIMITED_UNVERIFIED_DELIVERY")
	if _, err = session.Respond(context.Background(), ResponseTemplate(delivery, "accept-delivery", "approve")); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.Terminated || state.Quality != c.Unverified || state.OpenRequiredObligations != 1 {
		t.Fatal("approval forged verification", err, state)
	}
}
func TestResponsesRejectChangedBindingsExpiryAndUnknownEffects(t *testing.T) {
	for _, attack := range []string{"action", "policy", "spec", "attempt", "task", "request", "expiry", "unknown", "barrier"} {
		t.Run(attack, func(t *testing.T) {
			session, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
			defer session.Close()
			if _, err := session.Run(context.Background(), options.TaskID); err != nil {
				t.Fatal(err)
			}
			r := pending(t, session, options.TaskID, "LIMITED_UNVERIFIED_DELIVERY")
			response := ResponseTemplate(r, "response-one", "approve")
			state, doc, err := session.Load(context.Background(), options.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			switch attack {
			case "action":
				response.ActionDigest = c.HashBytes([]byte("other"))
			case "policy":
				response.ExpectedPolicyDigest = c.HashBytes([]byte("other"))
			case "spec":
				response.ExpectedSpecVersion++
			case "attempt":
				response.Attempt++
			case "task":
				response.TaskID = "other"
			case "request":
				response.RequestID = "other"
			case "expiry":
				doc.Requests[0].ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
				_, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{})
			case "unknown":
				doc.UnknownEffect = true
				_, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{})
			case "barrier":
				_, err = session.record(context.Background(), state, doc, "InputRecorded", c.EventPayload{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = session.Respond(context.Background(), response); err == nil {
				t.Fatal("unsafe response accepted")
			}
		})
	}
}
func TestApprovalSurvivesOwnerRestartButExpiredApprovalCannotDispatch(t *testing.T) {
	session, options, _ := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	state, doc, err := session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	doc.Autonomy = "review"
	if _, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	if _, err = session.Run(context.Background(), options.TaskID); err != nil {
		t.Fatal(err)
	}
	r := pending(t, session, options.TaskID, "CANDIDATE_WRITE")
	response := ResponseTemplate(r, "approval", "approve")
	if _, err = session.Respond(context.Background(), response); err != nil {
		t.Fatal(err)
	}
	path := session.directory
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err = session.Respond(context.Background(), response); err != nil {
		t.Fatal("dedup after owner change", err)
	}
	state, doc, err = session.Load(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = session.recover(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	doc.Requests[0].ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if _, err = session.record(context.Background(), state, doc, "SessionRecorded", c.EventPayload{}); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil || state.Execution != c.WaitingUser || state.CandidateDigest != state.BaselineDigest {
		t.Fatal("expired authority dispatched", err, state)
	}
	fresh := pending(t, session, options.TaskID, "CANDIDATE_WRITE")
	if fresh.ID == r.ID {
		t.Fatal("expired approval renewed implicitly")
	}
	if _, err = session.Respond(context.Background(), ResponseTemplate(fresh, "fresh-approval", "approve")); err != nil {
		t.Fatal(err)
	}
	state, err = session.Run(context.Background(), options.TaskID)
	if err != nil || state.Outcome != c.Finished {
		t.Fatal(err, state)
	}
}
