package agent

import (
	"context"
	"github.com/ixayldz/Viber/internal/auth"
	"github.com/ixayldz/Viber/internal/fileguard"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

const localContextProfile = "LOCAL_DECLARED_BYTE_UPPER_BOUND_V1"

// LocalRuntime remains a source-compatible alias for earlier local-only callers.
type LocalRuntime = Runtime
type Runtime struct {
	Stream        bool   `json:"stream,omitempty"`
	SecretHandle  string `json:"secret_handle,omitempty"`
	AuthDirectory string `json:"auth_directory,omitempty"`
	AuthProfile   string `json:"auth_profile,omitempty"`
	AllowRemote   bool   `json:"operator_allowed_remote,omitempty"`
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
	if r.Provider == "openai" || r.Provider == "anthropic" {
		origin, handle := "https://api.openai.com", "OPENAI_API_KEY"
		if r.Provider == "anthropic" {
			origin, handle = "https://api.anthropic.com", "ANTHROPIC_API_KEY"
		}
		if r.SchemaVersion != 1 || r.Endpoint != origin || r.SecretHandle != handle || !r.AllowRemote || r.DeclaredLocal || r.AuthDirectory != "" || r.AuthProfile != "" || !utf8.ValidString(r.Model) || strings.TrimSpace(r.Model) != r.Model || r.Model == "" || len(r.Model) > 256 || strings.ContainsAny(r.Model, "\r\n\x00") || r.ContextLimit < 4096 || r.ContextLimit > 1<<20 || r.OutputLimit < 128 || r.OutputLimit > 32768 || r.OutputLimit+4096 >= r.ContextLimit || r.TimeoutMillis < 1000 || r.TimeoutMillis > 300000 {
			return c.Fail(c.PolicyDenied, "invalid declared remote provider/context/output/credential profile")
		}
		return nil
	}
	if r.Provider == "chatgpt" {
		if r.Stream || r.SchemaVersion != 1 || r.Endpoint != "https://api.openai.com" || r.Model != model.ChatGPTModel || r.DeclaredLocal || r.SecretHandle != "" || !r.AllowRemote || !filepath.IsAbs(r.AuthDirectory) || len(r.AuthDirectory) > 1024 || strings.ContainsAny(r.AuthDirectory, "\r\n\x00") || !c.ValidDigest(r.AuthProfile) || r.ContextLimit != model.ChatGPTContextCapacity || r.OutputLimit != model.ChatGPTOutputCeiling || r.TimeoutMillis < 1000 || r.TimeoutMillis > 300000 {
			return c.Fail(c.PolicyDenied, "ChatGPT requires explicit remote consent, selected private account and registered bounded model profile")
		}
		return nil
	}
	if r.AuthDirectory != "" || r.AuthProfile != "" || r.AllowRemote || r.SecretHandle != "" {
		return c.Fail(c.PolicyDenied, "local runtime cannot carry remote credentials")
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
	var secret func(context.Context) (string, error)
	if runtime.Provider == "openai" || runtime.Provider == "anthropic" {
		secret = func(ctx context.Context) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			value, present := os.LookupEnv(runtime.SecretHandle)
			if !present || value == "" || len(value) > 16384 || strings.ContainsAny(value, "\r\n\x00") {
				return "", c.Fail(c.PolicyDenied, "selected provider credential handle unavailable")
			}
			return value, nil
		}
	}
	if runtime.Provider == "chatgpt" {
		base, err := s.Archive.Get(doc.Baseline)
		if err != nil {
			return nil, err
		}
		if !fileguard.Disjoint(runtime.AuthDirectory, base.Snapshot.Root) || !fileguard.Disjoint(runtime.AuthDirectory, s.directory) {
			return nil, c.Fail(c.PolicyDenied, "credentials must stay outside source and task store")
		}
		secret = func(ctx context.Context) (string, error) {
			credentials, err := auth.Open(runtime.AuthDirectory, false)
			if err != nil {
				return "", err
			}
			defer credentials.Close()
			client := auth.NewClient()
			defer client.Close()
			return client.AccessForModel(ctx, credentials, runtime.AuthProfile, runtime.Model)
		}
	}
	return model.New(model.Config{Secret: secret, Provider: runtime.Provider, Endpoint: runtime.Endpoint, Timeout: time.Duration(runtime.TimeoutMillis) * time.Millisecond, MaxResponseBytes: 8 << 20, Authority: func(ctx context.Context) (model.Admission, error) {
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

func requestContextWindow(runtime *LocalRuntime) int64 {
	if runtime.Provider == "ollama" {
		return runtime.ContextLimit
	}
	return 0
}
