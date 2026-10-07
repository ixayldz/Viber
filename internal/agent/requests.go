package agent

import (
	"context"

	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/store"
	"github.com/ixayldz/Viber/internal/workspace"
)

type UserRequest struct {
	ID              string      `json:"request_id"`
	TaskID          string      `json:"task_id"`
	Attempt         int64       `json:"attempt"`
	Kind            string      `json:"kind"`
	ActionDigest    string      `json:"action_digest"`
	SpecVersion     int64       `json:"spec_version"`
	PolicyDigest    string      `json:"policy_digest"`
	Candidate       string      `json:"candidate"`
	ExpiresAt       string      `json:"expires_at"`
	Options         []string    `json:"options"`
	ResumeCondition string      `json:"resume_condition"`
	Action          *model.Call `json:"action"`
	Status          string      `json:"status"`
}
type UserResponse struct {
	CommandID            string `json:"command_id"`
	TaskID               string `json:"task_id"`
	RequestID            string `json:"request_id"`
	Attempt              int64  `json:"attempt"`
	ExpectedSpecVersion  int64  `json:"expected_spec_version"`
	ExpectedPolicyDigest string `json:"expected_policy_digest"`
	ActionDigest         string `json:"action_digest"`
	Response             struct {
		Decision string `json:"decision"`
	} `json:"response"`
}

func requestPolicy(state c.TaskState, doc Document) string {
	digest, _ := c.Digest(struct {
		Epoch    int64  `json:"epoch"`
		Autonomy string `json:"autonomy"`
		Profile  string `json:"profile"`
	}{state.PolicyEpoch, doc.Autonomy, "OFFLINE_CANDIDATE_ONLY_V1"})
	return digest
}
func requestAction(state c.TaskState, doc Document, kind string, call *model.Call) string {
	digest, _ := c.Digest(struct {
		Task      string      `json:"task"`
		Spec      int64       `json:"spec"`
		Candidate string      `json:"candidate"`
		Kind      string      `json:"kind"`
		Policy    string      `json:"policy"`
		Call      *model.Call `json:"call"`
	}{doc.TaskID, doc.Spec.Version, doc.Candidate.SnapshotDigest, kind, requestPolicy(state, doc), call})
	return digest
}
func requestCurrent(request UserRequest, state c.TaskState, doc Document) bool {
	expiry, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
	return err == nil && time.Now().Before(expiry) && request.TaskID == doc.TaskID && request.Attempt == 1 && request.SpecVersion == doc.Spec.Version && request.PolicyDigest == requestPolicy(state, doc) && request.Candidate == doc.Candidate.SnapshotDigest && request.ActionDigest == requestAction(state, doc, request.Kind, request.Action)
}
func boundRequest(doc Document, state c.TaskState, kind string, call *model.Call) int {
	digest := requestAction(state, doc, kind, call)
	for n := len(doc.Requests) - 1; n >= 0; n-- {
		r := doc.Requests[n]
		if r.ActionDigest == digest && r.Status != "CONSUMED" && requestCurrent(r, state, doc) {
			return n
		}
	}
	return -1
}
func ensureRequest(doc *Document, state c.TaskState, kind string, call *model.Call) (int, error) {
	if n := boundRequest(*doc, state, kind, call); n >= 0 {
		return n, nil
	}
	if len(doc.Requests) >= 256 {
		return -1, c.Fail(c.UnsupportedCapability, "request history quota exceeded")
	}
	request := UserRequest{ID: newID("request-"), TaskID: doc.TaskID, Attempt: 1, Kind: kind, ActionDigest: requestAction(state, *doc, kind, call), SpecVersion: doc.Spec.Version, PolicyDigest: requestPolicy(state, *doc), Candidate: doc.Candidate.SnapshotDigest, ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano), Options: []string{"approve", "reject"}, ResumeCondition: "explicit response then resume with current admission", Action: call, Status: "PENDING"}
	doc.Requests = append(doc.Requests, request)
	return len(doc.Requests) - 1, nil
}
func callApproved(doc Document, state c.TaskState, call model.Call) bool {
	n := boundRequest(doc, state, "CANDIDATE_WRITE", &call)
	return n >= 0 && doc.Requests[n].Status == "APPROVED"
}
func (s *Session) Requests(ctx context.Context, task string) ([]UserRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return nil, err
	}
	requests := append([]UserRequest{}, doc.Requests...)
	for n := range requests {
		if requests[n].Status == "PENDING" || requests[n].Status == "APPROVED" {
			if !requestCurrent(requests[n], state, doc) || state.Execution == c.Terminated {
				requests[n].Status = "STALE"
			}
		}
	}
	return requests, nil
}
func (s *Session) Respond(ctx context.Context, response UserResponse) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state c.TaskState
	if response.CommandID == "" || len(response.CommandID) > 128 || response.RequestID == "" || response.TaskID == "" || response.Attempt != 1 || response.ExpectedSpecVersion < 1 || !c.ValidDigest(response.ExpectedPolicyDigest) || !c.ValidDigest(response.ActionDigest) || response.Response.Decision != "approve" && response.Response.Decision != "reject" {
		return state, c.Fail(c.InvalidArgument, "invalid bound user response")
	}
	digest, err := c.Digest(response)
	if err != nil {
		return state, err
	}
	receipt, event, payload, found, err := s.Journal.CommandReceipt(ctx, response.CommandID)
	if err != nil {
		return state, err
	}
	if found {
		if event.Type != "UserResponseRecorded" || event.Actor != "user" || event.TaskID != response.TaskID || payload.Reason != digest {
			return state, c.Fail(c.CommandIDConflict, "response command ID reused with different input")
		}
		return receipt, nil
	}
	state, doc, err := s.Load(ctx, response.TaskID)
	if err != nil {
		return state, err
	}
	if state.Execution == c.Terminated || state.InputBarrier || doc.Pending != nil || doc.UnknownEffect {
		return state, c.Fail(c.PolicyDenied, "response cannot authorize terminal, barred or unknown-effect task")
	}
	n := -1
	for i := range doc.Requests {
		if doc.Requests[i].ID == response.RequestID {
			n = i
			break
		}
	}
	if n < 0 {
		return state, c.Fail(c.StaleRequest, "request not found")
	}
	request := doc.Requests[n]
	if request.Status != "PENDING" || !requestCurrent(request, state, doc) || request.Attempt != response.Attempt || request.SpecVersion != response.ExpectedSpecVersion || request.PolicyDigest != response.ExpectedPolicyDigest || request.ActionDigest != response.ActionDigest {
		return state, c.Fail(c.StaleRequest, "request expired, superseded or binding changed")
	}
	state, err = s.recover(ctx, state)
	if err != nil {
		return state, err
	}
	doc.Requests[n].Status = "REJECTED"
	if response.Response.Decision == "approve" {
		doc.Requests[n].Status = "APPROVED"
	}
	doc.Blocker = ""
	raw, err := c.CanonicalV1(doc)
	if err != nil {
		return state, err
	}
	document, err := s.Archive.PutBytes(doc.TaskID, raw)
	if err != nil {
		return state, err
	}
	return s.Journal.Execute(ctx, store.Command{ID: response.CommandID, TaskID: response.TaskID, Actor: "user", ExpectedTaskSeq: state.TaskSeq, Type: "UserResponseRecorded", Payload: c.EventPayload{DocumentDigest: document, InputID: request.ID, Reason: digest}})
}

// validateReviewCall prepares a pure preview before requesting approval. It
// changes neither the archive candidate pointer nor any source bytes.
func (s *Session) validateReviewCall(state c.TaskState, doc Document, call model.Call) error {
	var args struct {
		ReadSet []c.ReadCondition `json:"read_set"`
		Changes []c.Change        `json:"changes"`
	}
	if err := c.DecodeStrict(call.Arguments, &args); err != nil {
		return err
	}
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return err
	}
	layers := s.layers(state, doc)
	layers[0].Effects = append(layers[0].Effects, "candidate.write")
	p := c.Proposal{SchemaVersion: 1, ID: call.ID, TaskID: doc.TaskID, SpecVersion: doc.Spec.Version, BaseSnapshot: doc.Candidate.SnapshotDigest, PolicyEpoch: state.PolicyEpoch, KernelGeneration: state.KernelGeneration, ReadSet: args.ReadSet, Changes: args.Changes}
	_, err = workspace.Preview(candidate, candidate, p, layers, state)
	return err
}

// ResponseTemplate fills immutable bindings; the operator supplies a fresh
// command ID and explicitly chooses approve or reject.
func ResponseTemplate(request UserRequest, commandID, decision string) UserResponse {
	response := UserResponse{CommandID: commandID, TaskID: request.TaskID, RequestID: request.ID, Attempt: request.Attempt, ExpectedSpecVersion: request.SpecVersion, ExpectedPolicyDigest: request.PolicyDigest, ActionDigest: request.ActionDigest}
	response.Response.Decision = decision
	return response
}
