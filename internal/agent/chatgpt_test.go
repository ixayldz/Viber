package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
)

func TestChatGPTTaskBindsAccountConsentAndFullOutputCeiling(t *testing.T) {
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "a.txt"), []byte("public source"), 0600)
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runtime := &LocalRuntime{SchemaVersion: 1, Provider: "chatgpt", Endpoint: "https://api.openai.com", Model: model.ChatGPTModel, AllowRemote: true, AuthDirectory: t.TempDir(), AuthProfile: c.HashBytes([]byte("account")), ContextLimit: model.ChatGPTContextCapacity, OutputLimit: model.ChatGPTOutputCeiling, TimeoutMillis: 1000}
	budget := DefaultBudget()
	budget.MaxOutputTokens = 1 << 20
	state, err := s.Create(context.Background(), StartOptions{Root: source, TaskID: "plan-auth", Prompt: []byte("Inspect source"), Budget: budget, Autonomy: "guided", Runtime: runtime, AllowUnverified: true})
	if err != nil {
		t.Fatal(err)
	}
	state, doc, err := s.Load(context.Background(), state.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	r, raw, manifest, err := compileOfflineRequest(doc, state, s.layers(state, doc))
	if err != nil {
		t.Fatal(err)
	}
	if r.ContextWindow != 0 || r.MaxOutputTokens != 128000 || r.Model != model.ChatGPTModel || manifest.Estimator != model.ChatGPTProfile || strings.Contains(string(raw), runtime.AuthDirectory) || strings.Contains(string(raw), runtime.AuthProfile) {
		t.Fatal("account/ceiling/context compiler binding")
	}
	if err = policy.Admit(s.layers(state, doc), policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Remote: true, Provider: "chatgpt", Effect: "model.infer"}); err != nil {
		t.Fatal(err)
	}
	if err = policy.Admit(s.layers(state, doc), policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Remote: true, Provider: "openai", Effect: "model.infer"}); err == nil {
		t.Fatal("plan consent granted API provider")
	}
	for _, change := range []func(*LocalRuntime){
		func(r *LocalRuntime) { r.AllowRemote = false }, func(r *LocalRuntime) { r.OutputLimit = 512 }, func(r *LocalRuntime) { r.Endpoint = "https://chatgpt.com" }, func(r *LocalRuntime) { r.Model = "unknown" }, func(r *LocalRuntime) { r.DeclaredLocal = true }, func(r *LocalRuntime) { r.AuthProfile = "" }, func(r *LocalRuntime) { r.AuthDirectory = "relative" },
	} {
		bad := *runtime
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("unsafe plan profile admitted")
		}
	}
	bad := *runtime
	bad.AuthDirectory = source
	if _, err = s.Create(context.Background(), StartOptions{Root: source, TaskID: "secret-source", Prompt: []byte("x"), Budget: budget, Autonomy: "guided", Runtime: &bad}); err == nil {
		t.Fatal("credential/source overlap accepted")
	}
}

func TestRemoteAPIRuntimePersistsHandleWithoutCredentialAndRestrictsProvider(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			source := t.TempDir()
			os.WriteFile(filepath.Join(source, "a.txt"), []byte("source"), 0600)
			s, err := Open(context.Background(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			endpoint, handle := "https://api.openai.com", "OPENAI_API_KEY"
			if provider == "anthropic" {
				endpoint, handle = "https://api.anthropic.com", "ANTHROPIC_API_KEY"
			}
			t.Setenv(handle, "test-never-persisted-secret")
			runtime := &LocalRuntime{SchemaVersion: 1, Provider: provider, Endpoint: endpoint, Model: "operator-model", AllowRemote: true, SecretHandle: handle, ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 1000}
			pricePolicy := c.DefaultResourcePolicy()
			pricePolicy.Limits.MoneyMicros = 1 << 30
			pricePolicy.Prices = []c.ModelPrice{{Version: "offline-test-price-v1", Provider: provider, Model: runtime.Model, InputMicrosPerMillion: 1000000, OutputMicrosPerMillion: 1000000}}
			state, err := s.Create(context.Background(), StartOptions{ResourcePolicy: &pricePolicy, Root: source, TaskID: "remote-api", Prompt: []byte("Inspect source"), Budget: DefaultBudget(), Autonomy: "guided", Runtime: runtime})
			if err != nil {
				t.Fatal(err)
			}
			state, doc, err := s.Load(context.Background(), state.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := s.Archive.GetBytes(state.TaskID, state.DocumentDigest)
			if err != nil || strings.Contains(string(raw), "test-never-persisted-secret") {
				t.Fatal("credential entered task doc", err)
			}
			request, encoded, manifest, err := compileOfflineRequest(doc, state, s.layers(state, doc))
			if err != nil || request.ContextWindow != 0 || manifest.Estimator != "REMOTE_DECLARED_BYTE_UPPER_BOUND_V1" || strings.Contains(string(encoded), "test-never-persisted-secret") {
				t.Fatal("remote compiler", err)
			}
			if err = policy.Admit(s.layers(state, doc), policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Remote: true, Provider: provider, Effect: "model.infer"}); err != nil {
				t.Fatal(err)
			}
			if err = policy.Admit(s.layers(state, doc), policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Remote: true, Provider: "chatgpt", Effect: "model.infer"}); err == nil {
				t.Fatal("remote credential broadened provider")
			}
			for _, change := range []func(*LocalRuntime){func(r *LocalRuntime) { r.SecretHandle = "PATH" }, func(r *LocalRuntime) { r.AllowRemote = false }, func(r *LocalRuntime) { r.Endpoint = "https://attacker.test" }, func(r *LocalRuntime) { r.AuthProfile = c.HashBytes([]byte("other")) }, func(r *LocalRuntime) { r.DeclaredLocal = true }} {
				bad := *runtime
				change(&bad)
				if bad.Validate() == nil {
					t.Fatal("invalid remote profile admitted")
				}
			}
		})
	}
}

func TestCredentialPreflightSettlesZeroWithoutClaimingProviderUsage(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "chatgpt"} {
		t.Run(provider, func(t *testing.T) {
			source := t.TempDir()
			os.WriteFile(filepath.Join(source, "a.txt"), []byte("source"), 0600)
			s, err := Open(context.Background(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			runtime := &LocalRuntime{SchemaVersion: 1, Provider: provider, Endpoint: "https://api.openai.com", Model: "operator-model", AllowRemote: true, SecretHandle: "OPENAI_API_KEY", ContextLimit: 32768, OutputLimit: 512, TimeoutMillis: 1000}
			budget := DefaultBudget()
			switch provider {
			case "anthropic":
				runtime.Endpoint = "https://api.anthropic.com"
				runtime.SecretHandle = "ANTHROPIC_API_KEY"
			case "chatgpt":
				runtime.SecretHandle = ""
				runtime.AuthDirectory = filepath.Join(t.TempDir(), "missing")
				runtime.AuthProfile = c.HashBytes([]byte("unavailable-account"))
				runtime.Model = model.ChatGPTModel
				runtime.ContextLimit = model.ChatGPTContextCapacity
				runtime.OutputLimit = model.ChatGPTOutputCeiling
				budget.MaxOutputTokens = 1 << 20
			}
			if runtime.SecretHandle != "" {
				t.Setenv(runtime.SecretHandle, "")
			}
			policy := c.DefaultResourcePolicy()
			policy.Limits.MoneyMicros = 1 << 30
			if provider != "chatgpt" {
				policy.Prices = []c.ModelPrice{{Version: "offline-test-price-v1", Provider: provider, Model: runtime.Model, InputMicrosPerMillion: 1000000, OutputMicrosPerMillion: 1000000}}
			}
			options := StartOptions{ResourcePolicy: &policy, Root: source, TaskID: "no-credential", Prompt: []byte("Inspect"), Budget: budget, Autonomy: "guided", Runtime: runtime}
			if _, err = s.Create(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			state, err := s.Run(context.Background(), options.TaskID)
			if err != nil || state.Execution != c.WaitingResource {
				t.Fatal("preflight became unknown", state, err)
			}
			state, doc, err := s.Load(context.Background(), options.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if doc.UnknownEffect || doc.Pending != nil || doc.Budget.ReservedInput != 0 || doc.Budget.ReservedOutput != 0 || !doc.LastNoDispatch || len(state.Tokens.Reservations) != 1 || state.Tokens.Reservations[0].UsageSource != "KERNEL_NO_DISPATCH" || state.Tokens.Reservations[0].Used != (c.TokenLimits{}) {
				t.Fatal("unproven zero release", doc)
			}
			backup := filepath.Join(t.TempDir(), "backup")
			restore := filepath.Join(t.TempDir(), "restore")
			if _, err = s.Backup(context.Background(), backup); err != nil {
				t.Fatal(err)
			}
			if _, err = RestoreBackup(context.Background(), backup, restore); err != nil {
				t.Fatal(err)
			}
			restored, err := OpenExisting(context.Background(), restore)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if state, err = restored.Run(context.Background(), options.TaskID); err != nil || state.Execution != c.WaitingResource || len(state.Tokens.Reservations) != 2 {
				t.Fatal("restored preflight cannot retry explicitly", state, err)
			}
			ledger, err := restored.Journal.TokenLedger(context.Background())
			if err != nil || ledger.Used != (c.TokenLimits{}) || ledger.Reserved != (c.TokenLimits{}) {
				t.Fatal("preflight charge leaked", ledger, err)
			}
		})
	}
}
