package model

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ChatGPT plan requests use the public stateless Responses route. The model's
// published full output ceiling is a reservation, not a wire output cap.
const ChatGPTModel = "gpt-6.1-sol"
const ChatGPTOutputCeiling int64 = 128000
const ChatGPTContextCapacity int64 = 1 << 20
const ChatGPTProfile = "OPENAI_SIWC_GPT_6_1_SOL_2026_10_08_BYTE_UPPER_V1"

func encodeChatGPT(r Request) (any, string, error) {
	wire, path, err := encodeRequest("openai", r)
	if err != nil {
		return nil, "", err
	}
	body := wire.(map[string]any)
	delete(body, "max_output_tokens")
	delete(body, "truncation")
	body["stream"] = true
	functions := body["tools"].([]any)
	body["tools"] = []any{map[string]any{"type": "namespace", "name": "viber", "description": "Viber bounded candidate, inspection, planning and registered check tools.", "tools": functions}}
	for _, item := range body["input"].([]any) {
		if raw, ok := item.(json.RawMessage); ok {
			block, err := object(raw)
			if err != nil {
				return nil, "", err
			}
			if text(block, "type") == "function_call" && text(block, "namespace") != "viber" {
				return nil, "", &Failure{Kind: "INVALID_TOOL_NAMESPACE"}
			}
		}
		if block, ok := item.(map[string]any); ok && block["type"] == "function_call" {
			block["namespace"] = "viber"
		}
	}
	return body, path, nil
}

// decodeChatGPTStream accepts only a terminal event as a receipt. Deltas cannot
// trigger execution. Every retained event is bounded and structurally checked.
func decodeChatGPTStream(r Request, raw []byte, result *Result) error {
	return decodeResponsesStream(r, raw, result, true)
}

func decodeResponsesStream(r Request, raw []byte, result *Result, namespace bool) error {
	fail := func(kind string) error { return streamFailure(result, kind) }

	if len(raw) == 0 || len(raw) > 8<<20 || !utf8.Valid(raw) {
		return fail("INVALID_STREAM")
	}
	var eventName string
	data := []string{}
	terminal := false
	createdID := ""
	events := 0
	lastSeq := int64(-1)
	flush := func() error {
		if len(data) == 0 {
			eventName = ""
			return nil
		}
		payload := []byte(strings.Join(data, "\n"))
		data = nil
		if bytes.Equal(payload, []byte("[DONE]")) {
			if !terminal {
				return fail("STREAM_ENDED_WITHOUT_TERMINAL")
			}
			eventName = ""
			return nil
		}
		if terminal {
			return fail("EVENT_AFTER_TERMINAL")
		}
		if validateWire(payload) != nil {
			return fail("INVALID_STREAM_EVENT")
		}
		event, err := object(payload)
		if err != nil {
			return fail("INVALID_STREAM_EVENT")
		}
		kind := text(event, "type")
		if !strings.HasPrefix(kind, "response.") && kind != "error" || eventName != "" && eventName != kind {
			return fail("INVALID_STREAM_EVENT_TYPE")
		}
		if seq, present := event["sequence_number"]; present {
			var n int64
			if json.Unmarshal(seq, &n) != nil || n < 0 || n <= lastSeq {
				return fail("INVALID_STREAM_SEQUENCE")
			}
			lastSeq = n
		}
		eventName = ""
		events++
		if events > 32768 {
			return fail("STREAM_EVENT_QUOTA")
		}
		if kind == "error" {
			return fail("STREAM_ERROR")
		}
		if kind == "response.created" {
			response, err := object(event["response"])
			if err != nil || createdID != "" || text(response, "id") == "" {
				return fail("INVALID_STREAM_IDENTITY")
			}
			createdID = text(response, "id")
		}
		switch kind {
		case "response.completed", "response.incomplete", "response.failed":
			response, err := object(event["response"])
			if err != nil {
				return fail("INVALID_TERMINAL_RESPONSE")
			}
			if text(response, "id") == "" || createdID != "" && createdID != text(response, "id") || text(response, "model") != r.Model || text(response, "status") != strings.TrimPrefix(kind, "response.") {
				return fail("STREAM_PROFILE_OR_STATUS_MISMATCH")
			}
			output, err := list(response, "output")
			if err != nil {
				return fail("INVALID_TERMINAL_RESPONSE")
			}
			for _, block := range output {
				item, err := object(block)
				if err != nil {
					return fail("INVALID_TERMINAL_RESPONSE")
				}
				if text(item, "type") == "function_call" && (namespace && text(item, "namespace") != "viber" || !namespace && text(item, "namespace") != "") {
					return fail("INVALID_TOOL_NAMESPACE")
				}
			}
			// Only the sealed terminal Response enters the shared result decoder.
			if err = decodeResult("openai", event["response"], result); err != nil {
				return fail("INVALID_TERMINAL_RESPONSE")
			}
			terminal = true
		}
		return nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) > 2<<20 {
			return fail("STREAM_LINE_QUOTA")
		}
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field = line
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			if eventName != "" {
				return fail("DUPLICATE_STREAM_EVENT_TYPE")
			}
			eventName = value
		case "data":
			data = append(data, value)
		case "id", "retry": // Transport hints cannot grant task/tool authority.
		default:
			return fail("INVALID_STREAM_FIELD")
		}
	}
	// An unterminated event is not a complete durable protocol boundary.
	if len(data) > 0 || eventName != "" || !terminal {
		return fail("STREAM_DISCONNECTED")
	}
	if err := validateResult(r, result); err != nil {
		return fail("INVALID_TERMINAL_RESPONSE")
	}
	return nil
}
