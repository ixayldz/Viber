package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResponsesPreserveNativeOptionalToolSchemas(t *testing.T) {
	parameters := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1},"cursor":{"type":"string"}},"required":["path"],"additionalProperties":false}`)
	for _, provider := range []string{"openai", "chatgpt"} {
		t.Run(provider, func(t *testing.T) {
			request := requestFixture()
			if provider == "chatgpt" {
				request = planRequest()
			}
			request.Tools[0].Parameters = parameters
			body, _, err := encodeRequest(provider, request)
			if err != nil {
				t.Fatal(err)
			}
			tools := body.(map[string]any)["tools"].([]any)
			if provider == "chatgpt" {
				tools = tools[0].(map[string]any)["tools"].([]any)
			}
			function := tools[0].(map[string]any)
			if strict, present := function["strict"]; !present || strict != false {
				t.Fatal("optional native fields would be normalized or rejected by Responses", function)
			}
			if string(function["parameters"].(json.RawMessage)) != string(parameters) || string(request.Tools[0].Parameters) != string(parameters) {
				t.Fatal("optional fields were made required/nullable or request schema was mutated")
			}
		})
	}
}

func TestChatGPTJSONErrorRetainsBoundedPrivateReceipt(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		client := planClient(t, "", nil)
		wire := `{"error":{"type":"invalid_request_error","code":"invalid_function_parameters","message":"private user material"}}`
		client.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
		})
		result, err := client.Complete(context.Background(), planRequest())
		client.Close()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != "HTTP_ERROR" || failure.StatusCode != status || failure.UsageUnknown != (status >= 500) || failure.Retryable {
			t.Fatalf("JSON error was treated as incomplete SSE or transparently retryable: %v", err)
		}
		if string(result.Raw) != wire || strings.Contains(err.Error(), "private user material") || len(result.Calls) != 0 || result.Usage.Known {
			t.Fatal("private error receipt was lost, exposed, or accepted as model output")
		}
	}
}

func TestChatGPTOptionalSchemaPassesProviderValidationBeforeReceipt(t *testing.T) {
	r := planRequest()
	r.Tools[0].Parameters = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path"],"additionalProperties":false}`)
	client := planClient(t, streamEvent("response.completed", planResponse("completed", "viber")), func(_ *http.Request, raw []byte) {
		var wire struct {
			Tools []struct {
				Tools []struct {
					Strict     bool `json:"strict"`
					Parameters struct {
						Properties map[string]any `json:"properties"`
						Required   []string       `json:"required"`
					} `json:"parameters"`
				} `json:"tools"`
			} `json:"tools"`
		}
		if json.Unmarshal(raw, &wire) != nil || len(wire.Tools) != 1 || len(wire.Tools[0].Tools) != 1 {
			t.Fatal("invalid namespace request")
		}
		tool := wire.Tools[0].Tools[0]
		if tool.Strict && len(tool.Parameters.Required) != len(tool.Parameters.Properties) {
			t.Fatal("provider would return invalid_function_parameters before inference")
		}
	})
	defer client.Close()
	result, err := client.Complete(context.Background(), r)
	if err != nil || !result.Usage.Known || result.CompletionStatus != "COMPLETE" || len(result.Calls) != 1 {
		t.Fatal("valid omitted optional argument lost its sealed receipt", result, err)
	}
	if string(result.Calls[0].Arguments) != `{"path":"a.txt"}` {
		t.Fatal("omitted optional fields were injected into native arguments")
	}
}
