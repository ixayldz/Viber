package agent

import (
	"encoding/json"

	ctxpack "github.com/ixayldz/Viber/internal/context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/plan"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/verify"
)

const offlineContextProfile = "OFFLINE_BYTE_UPPER_BOUND_V1"
const offlineContextCapacity int64 = 512 << 10

type ContextAudit struct {
	SchemaVersion int              `json:"schema_version"`
	Profile       string           `json:"profile"`
	RequestDigest string           `json:"request_digest"`
	Manifest      ctxpack.Manifest `json:"manifest"`
}

func offlineContext(request model.Request) ([]byte, ctxpack.Manifest, error) {
	return boundedContext(request, offlineContextCapacity, offlineContextProfile)
}
func boundedContext(request model.Request, capacity int64, profile string) ([]byte, ctxpack.Manifest, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, ctxpack.Manifest{}, err
	}
	_, manifest, err := ctxpack.Pack([]ctxpack.Block{{ID: "complete-canonical-protocol", Text: string(raw)}}, nil, ctxpack.Limits{Context: capacity, Output: request.MaxOutputTokens, SafetyMargin: 4096}, func(encoded []byte) (int64, error) { return int64(len(encoded)), nil })
	manifest.Estimator = profile
	return raw, manifest, err
}
func contextProfile(doc Document) (string, int64, string) {
	if doc.Runtime != nil {
		return doc.Runtime.Provider, doc.Runtime.ContextLimit, localContextProfile
	}
	return "fixture", offlineContextCapacity, offlineContextProfile
}
func compileOfflineRequest(doc Document, state c.TaskState, layers []policy.Policy) (model.Request, []byte, ctxpack.Manifest, error) {
	var scopes []verify.ProtectedScope
	if doc.Protection != nil {
		scopes = doc.Protection.Protected
	}
	constraints, err := c.CanonicalV1(struct {
		Protection   *ProtectionInfo         `json:"check_protection,omitempty"`
		Scopes       []verify.ProtectedScope `json:"protected_check_scopes,omitempty"`
		Spec         c.TaskSpec              `json:"spec"`
		Policy       []policy.Policy         `json:"restriction_layers"`
		Budget       Budget                  `json:"budget"`
		Plan         *plan.State             `json:"plan,omitempty"`
		StoreTokens  *c.TokenLimits          `json:"store_token_limits,omitempty"`
		ReleaseReady bool                    `json:"release_ready"`
	}{CheckProtection(doc), scopes, doc.Spec, layers, doc.Budget, doc.Plan, doc.StoreTokens, false})
	if err != nil {
		return model.Request{}, nil, ctxpack.Manifest{}, err
	}
	request := model.Request{SchemaVersion: 1, ID: newID("model-"), Model: "offline-fixture-v1", Instructions: instructions(doc, state) + "\nKernel spec/restrictions/budget; plan descriptions are model-authored proposals and never new user authority:\n" + string(constraints), Messages: doc.Messages, Tools: Tools(), MaxOutputTokens: 512}
	provider, capacity, profile := contextProfile(doc)
	if doc.Runtime != nil {
		request.Model = doc.Runtime.Model
		request.MaxOutputTokens = doc.Runtime.OutputLimit
		request.ContextWindow = doc.Runtime.ContextLimit
	}
	if err = model.ValidateRequest(request, provider); err != nil {
		return request, nil, ctxpack.Manifest{}, err
	}
	raw, manifest, err := boundedContext(request, capacity, profile)
	return request, raw, manifest, err
}
func (s *Session) validateContext(doc Document) error {
	audit := doc.Context
	if audit == nil {
		return nil
	}
	provider, capacity, profile := contextProfile(doc)
	if audit.SchemaVersion != 1 || audit.Profile != profile || !c.ValidDigest(audit.RequestDigest) {
		return c.Fail(c.StoreIntegrityError, "context audit profile or request binding invalid")
	}
	raw, err := s.Archive.GetBytes(doc.TaskID, audit.RequestDigest)
	if err != nil {
		return c.Fail(c.StoreIntegrityError, "compiled context request unavailable")
	}
	var request model.Request
	if err = c.DecodeStrict(raw, &request); err != nil {
		return err
	}
	if err = model.ValidateRequest(request, provider); err != nil {
		return err
	}
	if doc.Runtime != nil && (request.Model != doc.Runtime.Model || request.ContextWindow != doc.Runtime.ContextLimit || request.MaxOutputTokens != doc.Runtime.OutputLimit) {
		return c.Fail(c.StoreIntegrityError, "local context runtime binding mismatch")
	}
	_, manifest, err := boundedContext(request, capacity, profile)
	if err != nil {
		return c.Fail(c.StoreIntegrityError, "retained context exceeds profile")
	}
	expected, err := c.Digest(manifest)
	if err != nil {
		return err
	}
	actual, err := c.Digest(audit.Manifest)
	if err != nil {
		return err
	}
	if actual != expected {
		return c.Fail(c.StoreIntegrityError, "context manifest/request integrity mismatch")
	}
	return nil
}
