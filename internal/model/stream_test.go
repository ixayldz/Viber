package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func apiStreamEvent(kind, body string) string {
	return "event: " + kind + "\ndata: " + body + "\n\n"
}

func anthropicStreamFixture(stop string) string {
	return apiStreamEvent("message_start", `{"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","model":"fixture-model","content":[],"stop_reason":null,"usage":{"input_tokens":50,"output_tokens":1,"cache_read_input_tokens":12,"cache_creation_input_tokens":3}}}`) +
		apiStreamEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`) +
		apiStreamEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"opaque reasoning"}}`) +
		apiStreamEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-signature"}}`) +
		apiStreamEvent("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		apiStreamEvent("ping", `{"type":"ping"}`) +
		apiStreamEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_stream","name":"fs_read","input":{}}}`) +
		apiStreamEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`) +
		apiStreamEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a.txt\"}"}}`) +
		apiStreamEvent("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		apiStreamEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+stop+`","stop_sequence":null},"usage":{"output_tokens":10}}`) +
		apiStreamEvent("message_stop", `{"type":"message_stop"}`)
}
func ollamaStreamFixture(reason string) string {
	return `{"model":"fixture-model","message":{"role":"assistant","content":"read "},"done":false}` + "\n" +
		`{"model":"fixture-model","message":{"role":"assistant","content":"source","tool_calls":[{"function":{"index":0,"name":"fs_read","arguments":{"path":"a.txt"}}}]},"done":false}` + "\n" +
		`{"model":"fixture-model","message":{"role":"assistant","content":""},"done":true,"done_reason":"` + reason + `","prompt_eval_count":50,"eval_count":10}` + "\n"
}
func openAIStreamFixture(status string) string {
	response := strings.Replace(openAIToolFixture, `"status":"completed"`, `"status":"`+status+`"`, 1)
	response = strings.Replace(response, `"id":"resp_one"`, `"id":"resp_one","model":"fixture-model"`, 1)
	return streamEvent("response."+status, response)
}

func TestAPIStreamsSealToolsUsageAndOpaqueContinuationOnlyAtTerminal(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			r := requestFixture()
			r.Stream = true
			wire := openAIStreamFixture("completed")
			if provider == "anthropic" {
				wire = anthropicStreamFixture("tool_use")
			}
			if provider == "ollama" {
				wire = ollamaStreamFixture("stop")
			}
			// DecodeReceipt is also the agent's durable recovery path.
			result, err := DecodeReceipt(r, provider, []byte(wire))
			if err != nil || result.CompletionStatus != "COMPLETE" || len(result.Calls) != 1 || !result.Usage.Known || result.Usage.Output != 10 || string(result.Calls[0].Arguments) != `{"path":"a.txt"}` {
				t.Fatalf("sealed stream %+v %v", result, err)
			}
			if provider == "anthropic" && (result.Usage.Input != 65 || result.Usage.CachedInput != 12) {
				t.Fatal("cumulative/cache usage lost")
			}
			if provider == "ollama" && result.Text != "read source" {
				t.Fatal("NDJSON content lost")
			}
			assistant, err := result.AssistantMessage()
			if err != nil {
				t.Fatal(err)
			}
			next := r
			next.Messages = append(next.Messages, assistant, Message{Role: "tool", Replies: []Reply{{CallID: result.Calls[0].ID, Content: "source bytes"}}})
			if err = ValidateRequest(next, provider); err != nil {
				t.Fatal("paired replay", err)
			}
			body, _, err := encodeRequest(provider, next)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(body)
			if !strings.Contains(string(encoded), `"stream":true`) {
				t.Fatal("wire streaming lost")
			}
			if provider == "anthropic" && !strings.Contains(string(encoded), "opaque-signature") {
				t.Fatal("thinking continuation lost")
			}
			if provider != "ollama" {
				calls := 0
				client := fixtureClient(t, provider, wire, func(request *http.Request, body []byte) {
					calls++
					if request.Header.Get("Accept") != "text/event-stream" || !strings.Contains(string(body), `"stream":true`) {
						t.Fatal("stream negotiation")
					}
				})
				received, err := client.Complete(context.Background(), r)
				if err != nil || len(received.Calls) != 1 || calls != 1 {
					t.Fatal("transport/auto-retry", err, calls)
				}
			}
		})
	}
}

func TestDisconnectedMalformedOrHostileStreamsNeverPublishCalls(t *testing.T) {
	r := requestFixture()
	r.Stream = true
	vectors := map[string]map[string]string{
		"openai": {
			"missing-terminal": `data: {"type":"response.function_call_arguments.delta","delta":"{\"path\":\"a.txt\"}"}

`,
			"duplicate-terminal": openAIStreamFixture("completed") + openAIStreamFixture("completed"),
			"model-change":       strings.ReplaceAll(openAIStreamFixture("completed"), "fixture-model", "other-model"),
			"hosted-tool":        strings.Replace(openAIStreamFixture("completed"), `"type":"function_call"`, `"type":"web_search_call"`, 1),
			"truncated-boundary": strings.TrimSuffix(openAIStreamFixture("completed"), "\n\n"),
			"wrong-namespace":    strings.Replace(openAIStreamFixture("completed"), `"call_id":"call_one"`, `"namespace":"attacker","call_id":"call_one"`, 1),
		},
		"anthropic": {
			"missing-stop":          strings.Replace(anthropicStreamFixture("tool_use"), apiStreamEvent("message_stop", `{"type":"message_stop"}`), "", 1),
			"duplicate-start":       anthropicStreamFixture("tool_use") + anthropicStreamFixture("tool_use"),
			"model-change":          strings.ReplaceAll(anthropicStreamFixture("tool_use"), "fixture-model", "other-model"),
			"wrong-index":           strings.Replace(anthropicStreamFixture("tool_use"), `"index":1`, `"index":7`, 1),
			"wrong-delta-kind":      strings.Replace(anthropicStreamFixture("tool_use"), `"type":"input_json_delta"`, `"type":"thinking_delta"`, 1),
			"bad-json":              strings.Replace(anthropicStreamFixture("tool_use"), `"\"a.txt\"}"`, `"\"a.txt\""`, 1),
			"forged-signature":      strings.Replace(anthropicStreamFixture("tool_use"), "opaque-signature", "", 1),
			"server-fallback":       strings.Replace(anthropicStreamFixture("tool_use"), `"type":"thinking","thinking":""`, `"type":"fallback"`, 1),
			"duplicate-key":         strings.Replace(anthropicStreamFixture("tool_use"), `"index":0`, `"index":0,"index":1`, 1),
			"cumulative-regression": strings.Replace(anthropicStreamFixture("tool_use"), `"output_tokens":10`, `"output_tokens":0`, 1),
			"error-after-tools":     strings.Replace(anthropicStreamFixture("tool_use"), apiStreamEvent("message_stop", `{"type":"message_stop"}`), apiStreamEvent("error", `{"type":"error","error":{"message":"secret-do-not-print"}}`), 1),
		},
		"ollama": {
			"missing-done":     strings.Split(ollamaStreamFixture("stop"), "\n")[0] + "\n",
			"extra-done":       ollamaStreamFixture("stop") + ollamaStreamFixture("stop"),
			"model-change":     strings.ReplaceAll(ollamaStreamFixture("stop"), "fixture-model", "other-model"),
			"malformed-done":   strings.Replace(ollamaStreamFixture("stop"), `"done":true`, `"done":"true"`, 1),
			"partial-args":     strings.Replace(ollamaStreamFixture("stop"), `"arguments":{"path":"a.txt"}`, `"arguments":"{\"path\":"`, 1),
			"unexpected-index": strings.Replace(ollamaStreamFixture("stop"), `"index":0`, `"index":9`, 1),
			"duplicate-key":    strings.Replace(ollamaStreamFixture("stop"), `"done":true`, `"done":true,"done":false`, 1),
		},
	}
	for provider, cases := range vectors {
		for name, wire := range cases {
			t.Run(provider+"/"+name, func(t *testing.T) {
				result, err := DecodeReceipt(r, provider, []byte(wire))
				var failure *Failure
				if err == nil || !errors.As(err, &failure) || !failure.UsageUnknown || len(result.Calls) != 0 || len(result.Continuation) != 0 || result.CompletionStatus == "COMPLETE" {
					t.Fatalf("unsafe stream result %+v %v", result, err)
				}
				if strings.Contains(err.Error(), "secret-do-not-print") {
					t.Fatal("untrusted error leaked")
				}
			})
		}
	}
}

func TestCompleteStreamWithOutputLimitRetainsUsageWithoutToolAuthority(t *testing.T) {
	r := requestFixture()
	r.Stream = true
	for provider, wire := range map[string]string{
		"openai":    openAIStreamFixture("incomplete"),
		"ollama":    ollamaStreamFixture("length"),
		"anthropic": apiStreamEvent("message_start", `{"type":"message_start","message":{"type":"message","id":"msg_limit","role":"assistant","model":"fixture-model","content":[],"stop_reason":null,"usage":{"input_tokens":50,"output_tokens":1}}}`) + apiStreamEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":10}}`) + apiStreamEvent("message_stop", `{"type":"message_stop"}`),
	} {
		result, err := DecodeReceipt(r, provider, []byte(wire))
		if err != nil || !result.Usage.Known || result.CompletionStatus == "COMPLETE" || len(result.Calls) != 0 || len(result.Continuation) != 0 {
			t.Fatal(provider, result, err)
		}
	}
}

type observedStreamBody struct {
	io.ReadCloser
	once sync.Once
	read chan struct{}
}

func (body *observedStreamBody) Read(raw []byte) (int, error) {
	n, err := body.ReadCloser.Read(raw)
	if n > 0 {
		body.once.Do(func() { close(body.read) })
	}
	return n, err
}

func TestStreamingLocalHTTPWaitsForTerminalAndCancellationRetainsRawPrefix(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal", true: "cancelled"}[cancelEarly], func(t *testing.T) {
			chunkSent := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				io.WriteString(w, `{"model":"fixture-model","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"fs_read","arguments":{"path":"a.txt"}}}]},"done":false}`+"\n")
				w.(http.Flusher).Flush()
				close(chunkSent)
				select {
				case <-request.Context().Done():
					return
				case <-release:
				}
				io.WriteString(w, `{"model":"fixture-model","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":50,"eval_count":10}`+"\n")
			}))
			defer server.Close()
			client, err := New(Config{Provider: "ollama", Endpoint: server.URL, Timeout: time.Second, MaxResponseBytes: 1 << 20, Authority: testAdmission})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			received := make(chan struct{})
			transport := client.http.Transport
			defer transport.(*http.Transport).CloseIdleConnections()
			client.http.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(request)
				if err == nil {
					response.Body = &observedStreamBody{ReadCloser: response.Body, read: received}
				}
				return response, err
			})
			request := requestFixture()
			request.Stream = true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completed struct {
				result Result
				err    error
			}
			done := make(chan completed, 1)
			go func() { result, err := client.Complete(ctx, request); done <- completed{result, err} }()
			select {
			case <-chunkSent:
			case <-time.After(time.Second):
				t.Fatal("stream not sent")
			}
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("client did not receive stream prefix")
			}
			select {
			case <-done:
				t.Fatal("partial tool boundary published")
			default:
			}
			if cancelEarly {
				cancel()
			} else {
				close(release)
			}
			select {
			case completed := <-done:
				if cancelEarly {
					var failure *Failure
					if completed.err == nil || !errors.As(completed.err, &failure) || !failure.UsageUnknown || len(completed.result.Calls) != 0 || len(completed.result.Raw) == 0 {
						t.Fatal("cancelled prefix authority", completed)
					}
				} else if completed.err != nil || len(completed.result.Calls) != 1 || !completed.result.Usage.Known {
					t.Fatal("terminal receipt", completed)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stream cancellation/terminal stuck")
			}
		})
	}
}

func TestTerminalStreamCannotPublishUnboundedToolCallBatch(t *testing.T) {
	r := requestFixture()
	r.Stream = true
	output := []any{}
	for i := 0; i < 33; i++ {
		output = append(output, map[string]any{"type": "function_call", "status": "completed", "name": "fs_read", "call_id": fmt.Sprintf("call-%d", i), "arguments": "{\"path\":\"a.txt\"}"})
	}
	wire, _ := json.Marshal(map[string]any{"id": "resp_many", "model": r.Model, "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 50, "output_tokens": 10}})
	result, err := DecodeReceipt(r, "openai", []byte(streamEvent("response.completed", string(wire))))
	if err == nil || len(result.Calls) != 0 || len(result.Continuation) != 0 || result.CompletionStatus == "COMPLETE" {
		t.Fatal("unbounded batch admitted", result, err)
	}
}

func TestInvalidAnthropicUsageCannotBeMaskedByCacheAddition(t *testing.T) {
	r := requestFixture()
	for name, wire := range map[string]string{
		"negative-input":        strings.Replace(anthropicToolFixture, `"input_tokens":50`, `"input_tokens":-1`, 1),
		"malformed-cache-read":  strings.Replace(anthropicToolFixture, `"cache_read_input_tokens":12`, `"cache_read_input_tokens":"12"`, 1),
		"null-cache-read":       strings.Replace(anthropicToolFixture, `"cache_read_input_tokens":12`, `"cache_read_input_tokens":null`, 1),
		"malformed-cache-write": strings.Replace(anthropicToolFixture, `"cache_creation_input_tokens":3`, `"cache_creation_input_tokens":"3"`, 1),
		"negative-cache-read":   strings.Replace(anthropicToolFixture, `"cache_read_input_tokens":12`, `"cache_read_input_tokens":-1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := DecodeReceipt(r, "anthropic", []byte(wire))
			if err == nil || len(result.Calls) != 0 || len(result.Continuation) != 0 {
				t.Fatal("invalid raw usage turned into charge/tool receipt", result, err)
			}
		})
	}
}
