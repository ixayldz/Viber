package agent

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

const localContextProfile = "LOCAL_DECLARED_BYTE_UPPER_BOUND_V1"

type LocalRuntime struct {
	SchemaVersion int    `json:"schema_version"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	Model         string `json:"model"`
	DeclaredLocal bool   `json:"operator_declared_local"`
	ContextLimit  int64  `json:"context_limit"`
	OutputLimit   int64  `json:"output_limit"`
	TimeoutMillis int64  `json:"timeout_millis"`
}

func (r *LocalRuntime) Validate() error {
	if r == nil {
		return nil
	}
	name := strings.ToLower(r.Model)
	if r.SchemaVersion != 1 || r.Provider != "ollama" || !r.DeclaredLocal || !utf8.ValidString(r.Model) || strings.TrimSpace(r.Model) != r.Model || r.Model == "" || len(r.Model) > 256 || strings.ContainsAny(r.Model, "\x00\r\n") || strings.Contains(name, ":cloud") || strings.HasSuffix(name, "-cloud") || r.ContextLimit < 4096 || r.ContextLimit > 1<<20 || r.OutputLimit < 128 || r.OutputLimit > 32768 || r.OutputLimit+4096 >= r.ContextLimit || r.TimeoutMillis < 1000 || r.TimeoutMillis > 300000 {
		return c.Fail(c.PolicyDenied, "invalid local model profile; literal loopback, local declaration and bounded context/output/timeout required")
	}
	client, err := model.New(model.Config{Provider: r.Provider, Endpoint: r.Endpoint, Timeout: time.Duration(r.TimeoutMillis) * time.Millisecond, MaxResponseBytes: 8 << 20, Authority: func(context.Context) (model.Admission, error) { return model.Admission{}, nil }})
	if err != nil {
		return err
	}
	client.Close()
	return nil
}
func (s *Session) validateRuntime(doc Document) error {
	if doc.Runtime == nil {
		return nil
	}
	if err := doc.Runtime.Validate(); err != nil {
		return err
	}
	if doc.FixtureDigest != "" || doc.FixtureCursor != 0 {
		return c.Fail(c.StoreIntegrityError, "local runtime must not mix fixture execution")
	}
	return nil
}
func (s *Session) runtimeClient(doc Document) (*model.Client, error) {
	if err := s.validateRuntime(doc); err != nil {
		return nil, err
	}
	runtime := doc.Runtime
	return model.New(model.Config{Provider: runtime.Provider, Endpoint: runtime.Endpoint, Timeout: time.Duration(runtime.TimeoutMillis) * time.Millisecond, MaxResponseBytes: 8 << 20, Authority: func(ctx context.Context) (model.Admission, error) {
		state, err := s.State(ctx, doc.TaskID)
		if err != nil {
			return model.Admission{}, err
		}
		if state.Execution != c.Running || state.SpecVersion != doc.Spec.Version || state.KernelGeneration != s.Journal.Generation() {
			return model.Admission{}, c.Fail(c.StaleAuthority, "model inference is not in the active task/spec/generation")
		}
		return model.Admission{Layers: s.layers(state, doc), Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier}, nil
	}})
}
