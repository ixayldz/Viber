package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

func testAdmission(context.Context) (Admission, error) {
	return Admission{Epoch: 1, Generation: 1, Layers: []policy.Policy{{SchemaVersion: 1, Epoch: 1, Generation: 1, Effects: []string{"model.infer"}, RemoteInference: true, Providers: []string{"openai", "anthropic", "ollama"}}}}, nil
}
func requestFixture() Request {
	return Request{SchemaVersion: 1, ID: "request-one", Model: "fixture-model", Instructions: "trusted instructions", Messages: []Message{{Role: "user", Text: "read a.txt"}}, Tools: []Tool{{Name: "fs_read", Description: "Read immutable source", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}}, MaxOutputTokens: 512}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureClient(t *testing.T, provider, body string, capture func(*http.Request, []byte)) *Client {
	t.Helper()
	endpoint := "https://api.openai.com"
	if provider == "anthropic" {
		endpoint = "https://api.anthropic.com"
	}
	client, err := New(Config{Provider: provider, Endpoint: endpoint, Timeout: time.Second, MaxResponseBytes: 1 << 20, Secret: func(context.Context) (string, error) { return "fixture-secret", nil }, Authority: testAdmission})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if capture != nil {
			capture(r, raw)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"fixture-header"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return client
}

const openAIToolFixture = `{"id":"resp_one","status":"completed","temperature":1.0,"output":[{"id":"reason","type":"reasoning","encrypted_content":"opaque","summary":[]},{"id":"item_one","type":"function_call","status":"completed","call_id":"call_one","name":"fs_read","arguments":"{\"path\":\"a.txt\"}"}],"usage":{"input_tokens":50,"output_tokens":10,"input_tokens_details":{"cached_tokens":12}}}`
const anthropicToolFixture = `{"id":"msg_one","role":"assistant","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"opaque","signature":"signature"},{"type":"tool_use","id":"tool_one","name":"fs_read","input":{"path":"a.txt"}}],"usage":{"input_tokens":50,"output_tokens":10,"cache_read_input_tokens":12,"cache_creation_input_tokens":3}}`

func TestRemoteProtocolsPreserveCallsContinuationUsageAndClientToolsOnly(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			body := openAIToolFixture
			if provider == "anthropic" {
				body = anthropicToolFixture
			}
			count := 0
			client := fixtureClient(t, provider, body, func(r *http.Request, raw []byte) {
				count++
				var encoded map[string]json.RawMessage
				if err := json.Unmarshal(raw, &encoded); err != nil {
					t.Fatal(err)
				}
				if string(encoded["stream"]) != "false" {
					t.Fatal("stream profile wrong")
				}
				var tools []map[string]json.RawMessage
				if err := json.Unmarshal(encoded["tools"], &tools); err != nil || len(tools) != 1 {
					t.Fatal("tools", err)
				}
				if provider == "openai" {
					if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer fixture-secret" || string(encoded["store"]) != "false" || string(encoded["truncation"]) != `"disabled"` || string(tools[0]["type"]) != `"function"` {
						t.Fatal("OpenAI protocol changed")
					}
				} else if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "fixture-secret" || string(tools[0]["name"]) != `"fs_read"` {
					t.Fatal("Anthropic protocol changed")
				}
			})
			request := requestFixture()
			result, err := client.Complete(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Calls) != 1 || result.Calls[0].Name != "fs_read" || !result.Usage.Known || result.Usage.CachedInput != 12 || result.CompletionStatus != "COMPLETE" {
				t.Fatal("result", result)
			}
			if provider == "anthropic" && result.Usage.Input != 65 {
				t.Fatal("cache accounting wrong")
			}
			message, err := result.AssistantMessage()
			if err != nil {
				t.Fatal(err)
			}
			request.ID = "request-two"
			request.Messages = append(request.Messages, message, Message{Role: "tool", Replies: []Reply{{CallID: result.Calls[0].ID, Content: "exact source bytes"}}})
			// Second request preserves opaque reasoning/signature and exact call ID.
			encoded, _, err := encodeRequest(provider, request)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(encoded)
			if !strings.Contains(string(raw), result.Calls[0].ID) {
				t.Fatal("call ID lost")
			}
			if provider == "openai" && !strings.Contains(string(raw), "encrypted_content") || provider == "anthropic" && !strings.Contains(string(raw), "signature") {
				t.Fatal("continuation lost")
			}
			if count != 1 {
				t.Fatal("unexpected retry")
			}
		})
	}
}
func TestIncompleteMalformedDuplicateUnknownAndServerToolResponsesNeverExecute(t *testing.T) {
	vectors := []string{
		strings.Replace(openAIToolFixture, `"status":"completed"`, `"status":"incomplete"`, 1),
		strings.Replace(openAIToolFixture, `"name":"fs_read"`, `"name":"shell_exec"`, 1),
		strings.Replace(openAIToolFixture, `"type":"function_call"`, `"type":"web_search_call"`, 1),
		strings.Replace(openAIToolFixture, `"arguments":"{\"path\":\"a.txt\"}"`, `"arguments":"{"`, 1),
		strings.Replace(openAIToolFixture, `"status":"completed"`, `"status":"completed","status":"incomplete"`, 1),
		openAIToolFixture[:len(openAIToolFixture)-20],
	}
	for n, body := range vectors {
		t.Run(string(rune('a'+n)), func(t *testing.T) {
			client := fixtureClient(t, "openai", body, nil)
			result, err := client.Complete(context.Background(), requestFixture())
			if err == nil && result.CompletionStatus == "COMPLETE" {
				t.Fatal("invalid response complete")
			}
			if len(result.Calls) > 0 {
				t.Fatal("partial/invalid calls published")
			}
		})
	}
}
func TestToolResultBoundaryAndProviderSwitchCannotCarryOpaqueAuthority(t *testing.T) {
	request := requestFixture()
	result := Result{Provider: "openai", Model: request.Model, CompletionStatus: "COMPLETE", Calls: []Call{{ID: "call", Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}}
	assistant, _ := result.AssistantMessage()
	request.Messages = append(request.Messages, assistant)
	if err := validateRequest(request, "openai"); err == nil {
		t.Fatal("unresolved boundary accepted")
	}
	request.Messages = append(request.Messages, Message{Role: "tool", Replies: []Reply{{CallID: "wrong", Content: "output"}}})
	if err := validateRequest(request, "openai"); err == nil {
		t.Fatal("wrong reply ID accepted")
	}
	request.Messages[len(request.Messages)-1].Replies[0].CallID = "call"
	if err := validateRequest(request, "openai"); err != nil {
		t.Fatal(err)
	}
	request.Messages[1].Continuation = []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}
	if err := validateRequest(request, "anthropic"); err == nil {
		t.Fatal("cross provider continuation admitted")
	}
}
func TestDispatchRechecksSteeringBarrierAndNeverRetriesUnknownCharge(t *testing.T) {
	count := 0
	client := fixtureClient(t, "openai", openAIToolFixture, func(*http.Request, []byte) { count++ })
	client.config.Authority = func(context.Context) (Admission, error) {
		admission, _ := testAdmission(context.Background())
		admission.InputBarrier = true
		return admission, nil
	}
	if _, err := client.Complete(context.Background(), requestFixture()); err == nil || count != 0 {
		t.Fatal("barrier bypass")
	}
	client.config.Authority = testAdmission
	client.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		count++
		return nil, errors.New("fixture lost billed response")
	})
	result, err := client.Complete(context.Background(), requestFixture())
	var failure *Failure
	if !errors.As(err, &failure) || !failure.UsageUnknown || failure.Retryable || count != 1 || result.Usage.Known {
		t.Fatal("unknown effect hidden", err, count)
	}
}
func TestHTTPAuthErrorsDoNotPrintBodyAndRedirectCannotLeakCredential(t *testing.T) {
	client := fixtureClient(t, "openai", openAIToolFixture, nil)
	count := 0
	client.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		count++
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("fixture-secret sensitive-user-body"))}, nil
	})
	_, err := client.Complete(context.Background(), requestFixture())
	if err == nil || strings.Contains(err.Error(), "fixture-secret") || count != 1 {
		t.Fatal("secret error/retry", err)
	}
}
func TestLocalOllamaRoundTripRedirectAndCancellation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/chat" {
			t.Error("path")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("remote credential leaked to local")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"fs_read","arguments":{"path":"a.txt"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":3}`)
	}))
	defer server.Close()
	client, err := New(Config{Provider: "ollama", Endpoint: server.URL, Timeout: time.Second, MaxResponseBytes: 1 << 20, Authority: testAdmission})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Complete(context.Background(), requestFixture())
	if err != nil || len(result.Calls) != 1 || !result.Usage.Known {
		t.Fatal(result, err)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", server.URL+"/api/chat")
		w.WriteHeader(307)
	}))
	defer redirect.Close()
	blocked, err := New(Config{Provider: "ollama", Endpoint: redirect.URL, Timeout: time.Second, MaxResponseBytes: 1 << 20, Authority: testAdmission})
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	if _, err = blocked.Complete(context.Background(), requestFixture()); err == nil || calls != 1 {
		t.Fatal("redirect followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.Complete(ctx, requestFixture()); err == nil || calls != 1 {
		t.Fatal("cancelled request dispatched")
	}
}
func TestUnsafeOriginsAndSilentAuthorityFallbackDenied(t *testing.T) {
	for _, config := range []Config{{Provider: "openai", Endpoint: "https://evil.invalid"}, {Provider: "ollama", Endpoint: "http://localhost:11434"}, {Provider: "ollama", Endpoint: "http://169.254.169.254"}, {Provider: "ollama", Endpoint: "http://127.0.0.1/x"}, {Provider: "ollama", Endpoint: "http://user:pass@127.0.0.1"}} {
		config.Timeout = time.Second
		config.MaxResponseBytes = 1 << 20
		config.Authority = testAdmission
		if _, err := New(config); err == nil {
			t.Fatal("unsafe origin", config.Endpoint)
		}
	}
	_ = c.SchemaVersion
}
