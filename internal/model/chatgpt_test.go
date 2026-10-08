package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func planRequest() Request {
	r := requestFixture()
	r.Model = ChatGPTModel
	r.MaxOutputTokens = ChatGPTOutputCeiling
	return r
}
func streamEvent(kind string, response string) string {
	return "event: " + kind + "\ndata: {\"type\":\"" + kind + "\",\"response\":" + response + "}\n\n"
}
func planResponse(status, namespace string) string {
	raw := map[string]any{"id": "resp_plan", "model": ChatGPTModel, "status": status, "output": []any{map[string]any{"type": "reasoning", "encrypted_content": "opaque", "summary": []any{}}, map[string]any{"type": "function_call", "status": "completed", "namespace": namespace, "call_id": "call_plan", "name": "fs_read", "arguments": `{"path":"a.txt"}`}}, "usage": map[string]any{"input_tokens": 50, "output_tokens": 10}}
	bytes, _ := json.Marshal(raw)
	return string(bytes)
}
func planClient(t *testing.T, wire string, inspect func(*http.Request, []byte)) *Client {
	t.Helper()
	admission := func(context.Context) (Admission, error) {
		a, _ := testAdmission(context.Background())
		a.Layers[0].Providers = append(a.Layers[0].Providers, "chatgpt")
		return a, nil
	}
	client, err := New(Config{Provider: "chatgpt", Endpoint: "https://api.openai.com", Timeout: time.Second, MaxResponseBytes: 1 << 20, Authority: admission, Secret: func(context.Context) (string, error) { return "oauth-fixture", nil }})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if inspect != nil {
			inspect(r, raw)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
	})
	return client
}
func TestChatGPTPlanUsesNamespaceStreamingAndPublishedCeilingReservation(t *testing.T) {
	r := planRequest()
	wire := streamEvent("response.completed", planResponse("completed", "viber"))
	inspect := func(request *http.Request, raw []byte) {
		if request.URL.String() != "https://api.openai.com/v1/responses" || request.Header.Get("Authorization") != "Bearer oauth-fixture" || request.Header.Get("x-api-key") != "" || request.Header.Get("Accept") != "text/event-stream" {
			t.Fatal("plan auth/endpoint")
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) != nil {
			t.Fatal("request JSON")
		}
		if string(body["store"]) != "false" || string(body["stream"]) != "true" {
			t.Fatal("plan stateless stream")
		}
		for _, field := range []string{"max_output_tokens", "truncation", "previous_response_id", "background", "metadata", "conversation"} {
			if body[field] != nil {
				t.Fatalf("forbidden plan field %s", field)
			}
		}
		var tools []map[string]json.RawMessage
		json.Unmarshal(body["tools"], &tools)
		if len(tools) != 1 || string(tools[0]["type"]) != `"namespace"` || string(tools[0]["name"]) != `"viber"` {
			t.Fatal("flat tools on plan route")
		}
	}
	client := planClient(t, wire, inspect)
	result, err := client.Complete(context.Background(), r)
	if err != nil || len(result.Calls) != 1 || !result.Usage.Known || result.CompletionStatus != "COMPLETE" {
		t.Fatalf("terminal receipt %+v %v", result, err)
	}
	sealed, err := DecodeReceipt(r, "chatgpt", result.Raw)
	if err != nil || len(sealed.Calls) != 1 {
		t.Fatal("retained stream cannot validate", err)
	}
	assistant, err := result.AssistantMessage()
	if err != nil {
		t.Fatal(err)
	}
	r.Messages = append(r.Messages, assistant, Message{Role: "tool", Replies: []Reply{{CallID: "call_plan", Content: "exact bytes"}}})
	wireBody, _, err := encodeRequest("chatgpt", r)
	if err != nil {
		t.Fatal("namespace continuation", err)
	}
	body := wireBody.(map[string]any)
	found := false
	for _, item := range body["input"].([]any) {
		if raw, ok := item.(json.RawMessage); ok && strings.Contains(string(raw), `"function_call"`) {
			found = strings.Contains(string(raw), `"namespace":"viber"`)
		}
	}
	if !found {
		t.Fatal("namespace continuation lost")
	}
	r.MaxOutputTokens = 512
	if err = ValidateRequest(r, "chatgpt"); err == nil {
		t.Fatal("smaller optimistic output reserve admitted")
	}
	r = planRequest()
	r.Model = "unknown-model"
	if err = ValidateRequest(r, "chatgpt"); err == nil {
		t.Fatal("unregistered output ceiling admitted")
	}
}
func TestChatGPTPartialMalformedOrWrongNamespaceNeverDispatchesCalls(t *testing.T) {
	complete := streamEvent("response.completed", planResponse("completed", "viber"))
	for name, wire := range map[string]string{
		"delta only":       "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n\n",
		"unterminated":     strings.TrimSuffix(complete, "\n\n"),
		"wrong namespace":  streamEvent("response.completed", planResponse("completed", "attacker")),
		"wrong model":      strings.ReplaceAll(complete, ChatGPTModel, "different-model"),
		"status mismatch":  streamEvent("response.completed", planResponse("incomplete", "viber")),
		"duplicate JSON":   strings.Replace(complete, `"status":"completed"`, `"status":"completed","status":"completed"`, 1),
		"after terminal":   complete + complete,
		"missing terminal": "data: [DONE]\n\n",
		"out of order":     "data: {\"type\":\"response.in_progress\",\"sequence_number\":3}\n\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":2}\n\n" + complete,
	} {
		t.Run(name, func(t *testing.T) {
			r := planRequest()
			result, err := planClient(t, wire, nil).Complete(context.Background(), r)
			var f *Failure
			if err == nil || !errors.As(err, &f) || !f.UsageUnknown || len(result.Calls) != 0 || len(result.Continuation) != 0 {
				t.Fatalf("unsafe partial result %+v %v", result, err)
			}
		})
	}
	for _, status := range []string{"incomplete", "failed"} {
		r := planRequest()
		result, err := planClient(t, streamEvent("response."+status, planResponse(status, "viber")), nil).Complete(context.Background(), r)
		if err != nil || !result.Usage.Known || result.CompletionStatus == "COMPLETE" || len(result.Calls) != 0 {
			t.Fatalf("incomplete dispatch %+v %v", result, err)
		}
		if _, err = result.AssistantMessage(); err == nil {
			t.Fatal("incomplete continuation")
		}
	}
}
func TestDeniedPlanPolicyCannotRenewCredentialOrSendInference(t *testing.T) {
	r := planRequest()
	client := planClient(t, "", nil)
	calls := 0
	client.config.Authority = func(context.Context) (Admission, error) { return Admission{}, errors.New("stale") }
	client.config.Secret = func(context.Context) (string, error) { calls++; return "never", nil }
	client.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("sent") })
	if _, err := client.Complete(context.Background(), r); err == nil || calls != 0 {
		t.Fatal("denied policy caused auth/network side effect")
	}
}

func TestPreflightClassificationCannotReleaseDispatchedUnknownRequest(t *testing.T) {
	r := planRequest()
	client := planClient(t, "", nil)
	client.config.Secret = func(context.Context) (string, error) { return "", errors.New("missing credential") }
	if _, err := client.Complete(context.Background(), r); err == nil {
		t.Fatal("credential missing accepted")
	} else {
		var noDispatch *PreflightFailure
		if !errors.As(err, &noDispatch) {
			t.Fatal("preflight not distinguished", err)
		}
	}
	client = planClient(t, "", nil)
	client.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("lost transport reply") })
	result, err := client.Complete(context.Background(), r)
	var noDispatch *PreflightFailure
	var failure *Failure
	if err == nil || errors.As(err, &noDispatch) || !errors.As(err, &failure) || !failure.UsageUnknown || result.Usage.Known {
		t.Fatal("possibly sent request falsely released", result, err)
	}
	client = planClient(t, `{"decision":"KERNEL_PREFLIGHT_DECLINED","input_tokens":0,"output_tokens":0}`, nil)
	if _, err = client.Complete(context.Background(), r); err == nil || errors.As(err, &noDispatch) {
		t.Fatal("provider forged kernel preflight")
	}
}
