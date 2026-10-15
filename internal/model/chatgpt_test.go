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

func jsonStreamEvent(kind string, fields map[string]any) string {
	fields["type"] = kind
	raw, _ := json.Marshal(fields)
	return "event: " + kind + "\ndata: " + string(raw) + "\n\n"
}

func reconstructedChatGPTEvents() (string, string, string, string, string) {
	created := jsonStreamEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_plan", "model": ChatGPTModel, "status": "in_progress"}, "sequence_number": 1})
	added := jsonStreamEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{
		"id": "item_plan", "type": "function_call", "status": "in_progress", "arguments": "", "call_id": "call_plan", "name": "fs_read", "namespace": "viber",
	}, "sequence_number": 2})
	delta := jsonStreamEvent("response.function_call_arguments.delta", map[string]any{"item_id": "item_plan", "output_index": 0, "delta": `{"path":"a.txt"}`, "sequence_number": 3})
	argumentsDone := jsonStreamEvent("response.function_call_arguments.done", map[string]any{"item_id": "item_plan", "output_index": 0, "arguments": `{"path":"a.txt"}`, "sequence_number": 4})
	done := jsonStreamEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{
		"id": "item_plan", "type": "function_call", "status": "completed", "arguments": `{"path":"a.txt"}`, "call_id": "call_plan", "name": "fs_read", "namespace": "viber",
	}, "sequence_number": 5})
	return created, added, delta, argumentsDone, done
}

func reconstructedChatGPTTerminal(terminalKind, status string, output []any) string {
	response := map[string]any{"id": "resp_plan", "model": ChatGPTModel, "status": status, "output": output, "usage": map[string]any{"input_tokens": 2688, "output_tokens": 24}}
	return jsonStreamEvent(terminalKind, map[string]any{"response": response, "sequence_number": 6})
}

func reconstructedChatGPTWire(terminalKind, status string, output []any) string {
	created, added, delta, argumentsDone, done := reconstructedChatGPTEvents()
	return created + added + delta + argumentsDone + done + reconstructedChatGPTTerminal(terminalKind, status, output) + "data: [DONE]\n\n"
}

func reconstructedReasoningChatGPTWire() string {
	created := jsonStreamEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_reasoning", "model": ChatGPTModel, "status": "in_progress"}, "sequence_number": 1})
	reasoningAdded := jsonStreamEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "reason_plan", "type": "reasoning", "status": "in_progress", "encrypted_content": "", "summary": []any{}}, "sequence_number": 2})
	functionAdded := jsonStreamEvent("response.output_item.added", map[string]any{"output_index": 1, "item": map[string]any{
		"id": "item_plan", "type": "function_call", "status": "in_progress", "arguments": "", "call_id": "call_plan", "name": "fs_read", "namespace": "viber",
	}, "sequence_number": 3})
	delta := jsonStreamEvent("response.function_call_arguments.delta", map[string]any{"item_id": "item_plan", "output_index": 1, "delta": `{"path":"a.txt"}`, "sequence_number": 4})
	argumentsDone := jsonStreamEvent("response.function_call_arguments.done", map[string]any{"item_id": "item_plan", "output_index": 1, "arguments": `{"path":"a.txt"}`, "sequence_number": 5})
	reasoningDone := jsonStreamEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"id": "reason_plan", "type": "reasoning", "encrypted_content": "opaque", "summary": []any{}}, "sequence_number": 6})
	functionDone := jsonStreamEvent("response.output_item.done", map[string]any{"output_index": 1, "item": map[string]any{
		"id": "item_plan", "type": "function_call", "status": "completed", "arguments": `{"path":"a.txt"}`, "call_id": "call_plan", "name": "fs_read", "namespace": "viber",
	}, "sequence_number": 7})
	terminal := jsonStreamEvent("response.completed", map[string]any{"response": map[string]any{"id": "resp_reasoning", "model": ChatGPTModel, "status": "completed", "output": []any{}, "usage": map[string]any{"input_tokens": 2688, "output_tokens": 24}}, "sequence_number": 8})
	return created + reasoningAdded + functionAdded + delta + argumentsDone + reasoningDone + functionDone + terminal + "data: [DONE]\n\n"
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

func TestChatGPTReconstructsCompletedOutputItemsWhenTerminalOutputIsEmpty(t *testing.T) {
	r := planRequest()
	wire := reconstructedChatGPTWire("response.completed", "completed", []any{})
	result, err := planClient(t, wire, nil).Complete(context.Background(), r)
	if err != nil {
		t.Fatalf("bounded output-item reconstruction failed: %+v %v", result, err)
	}
	if result.CompletionStatus != "COMPLETE" || len(result.Calls) != 1 || result.Calls[0].ID != "call_plan" || result.Calls[0].Name != "fs_read" || string(result.Calls[0].Arguments) != `{"path":"a.txt"}` {
		t.Fatalf("reconstructed function call was not sealed: %+v", result)
	}
	if !result.Usage.Known || result.Usage.Input != 2688 || result.Usage.Output != 24 || len(result.Continuation) != 1 {
		t.Fatalf("terminal identity/usage or opaque continuation was not preserved: %+v", result)
	}
	item, err := object(result.Continuation[0])
	if err != nil || text(item, "id") != "item_plan" || text(item, "type") != "function_call" || text(item, "namespace") != "viber" {
		t.Fatalf("sealed output item was not retained opaquely: %s %v", result.Continuation[0], err)
	}
	sealed, err := DecodeReceipt(r, "chatgpt", result.Raw)
	if err != nil || len(sealed.Calls) != 1 || sealed.Usage.Input != 2688 {
		t.Fatalf("retained reconstructed receipt did not revalidate: %+v %v", sealed, err)
	}
}

func TestChatGPTReconstructionAllowsStatuslessCompletedReasoning(t *testing.T) {
	result, err := planClient(t, reconstructedReasoningChatGPTWire(), nil).Complete(context.Background(), planRequest())
	if err != nil || result.CompletionStatus != "COMPLETE" || len(result.Calls) != 1 || len(result.Continuation) != 2 {
		t.Fatalf("statusless reasoning done item was rejected: %+v %v", result, err)
	}
	reasoning, err := object(result.Continuation[0])
	if err != nil || text(reasoning, "type") != "reasoning" || text(reasoning, "status") != "" || text(reasoning, "encrypted_content") != "opaque" {
		t.Fatalf("reasoning continuation was not preserved: %s %v", result.Continuation[0], err)
	}
}

func TestChatGPTReconstructionBindsFunctionMetadataAcrossItemLifecycle(t *testing.T) {
	created, added, delta, argumentsDone, done := reconstructedChatGPTEvents()
	terminal := reconstructedChatGPTTerminal("response.completed", "completed", []any{}) + "data: [DONE]\n\n"
	cases := map[string]string{
		"changed name":      created + strings.Replace(added, `"name":"fs_read"`, `"name":"fs_stat"`, 1) + delta + argumentsDone + done + terminal,
		"changed call ID":   created + strings.Replace(added, `"call_id":"call_plan"`, `"call_id":"other_call"`, 1) + delta + argumentsDone + done + terminal,
		"changed namespace": created + added + delta + argumentsDone + strings.Replace(done, `"namespace":"viber"`, `"namespace":"other"`, 1) + terminal,
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := planClient(t, wire, nil).Complete(context.Background(), planRequest())
			var failure *Failure
			if err == nil || !errors.As(err, &failure) || !failure.UsageUnknown || len(result.Calls) != 0 {
				t.Fatalf("function metadata mutation was accepted: %+v %v", result, err)
			}
		})
	}
}

func TestOpenAIIncompleteLifecycleKeepsTerminalUsageAccounting(t *testing.T) {
	r := requestFixture()
	r.Stream = true
	wire := reconstructedChatGPTWire("response.incomplete", "incomplete", []any{})
	wire = strings.ReplaceAll(wire, ChatGPTModel, r.Model)
	wire = strings.ReplaceAll(wire, `,"namespace":"viber"`, "")
	result, err := DecodeReceipt(r, "openai", []byte(wire))
	if err != nil || !result.Usage.Known || result.CompletionStatus == "COMPLETE" || len(result.Calls) != 0 || len(result.Continuation) != 0 {
		t.Fatalf("OpenAI incomplete lifecycle lost terminal accounting: %+v %v", result, err)
	}
}

func TestChatGPTOutputItemReconstructionRejectsUnsealedOrConflictingEvents(t *testing.T) {
	created, added, delta, argumentsDone, done := reconstructedChatGPTEvents()
	terminal := reconstructedChatGPTTerminal("response.completed", "completed", []any{})
	finish := "data: [DONE]\n\n"
	vectors := map[string]string{
		"duplicate added":             strings.Replace(created+added+delta+argumentsDone+done+terminal+finish, argumentsDone, added+argumentsDone, 1),
		"out of order done":           created + added + delta + argumentsDone + strings.Replace(done, `"output_index":0`, `"output_index":1`, 1) + terminal + finish,
		"wrong namespace":             created + strings.ReplaceAll(added+delta+argumentsDone+done, `"namespace":"viber"`, `"namespace":"attacker"`) + terminal + finish,
		"incomplete item":             created + added + delta + argumentsDone + strings.Replace(done, `"status":"completed"`, `"status":"incomplete"`, 1) + terminal + finish,
		"missing argument seal":       created + added + delta + done + terminal + finish,
		"conflicting terminal output": created + added + delta + argumentsDone + done + reconstructedChatGPTTerminal("response.completed", "completed", []any{map[string]any{"type": "function_call", "status": "completed", "namespace": "viber", "call_id": "other", "name": "fs_read", "arguments": `{"path":"other.txt"}`}}) + finish,
		"incomplete response":         created + added + delta + argumentsDone + done + reconstructedChatGPTTerminal("response.incomplete", "incomplete", []any{}) + finish,
		"failed response":             created + added + delta + argumentsDone + done + reconstructedChatGPTTerminal("response.failed", "failed", []any{}) + finish,
		"disconnected":                created + added + delta + argumentsDone + done,
	}
	for name, wire := range vectors {
		t.Run(name, func(t *testing.T) {
			result, err := planClient(t, wire, nil).Complete(context.Background(), planRequest())
			var failure *Failure
			if err == nil || !errors.As(err, &failure) || !failure.UsageUnknown || len(result.Calls) != 0 || len(result.Continuation) != 0 {
				t.Fatalf("unsafe reconstructed stream was accepted: %+v %v", result, err)
			}
		})
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
