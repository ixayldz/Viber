package model

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

func streamFailure(result *Result, kind string) error {
	result.Calls = nil
	result.Continuation = nil
	result.Text = ""
	result.Usage = Usage{}
	result.CompletionStatus = "UNKNOWN"
	return &Failure{Kind: kind, UsageUnknown: true}
}

func decodeStreamReceipt(r Request, provider string, raw []byte, result *Result) error {
	var err error
	switch provider {
	case "openai":
		err = decodeResponsesStream(r, raw, result, false)
	case "anthropic":
		err = decodeAnthropicStream(r, raw, result)
	case "ollama":
		err = decodeOllamaStream(r, raw, result)
	default:
		err = streamFailure(result, "UNSUPPORTED_STREAM_PROFILE")
	}
	if err != nil {
		result.Calls = nil
		result.Continuation = nil
	}
	return err
}

// walkSSE accepts complete UTF-8 event boundaries only. Transport hints carry
// no authority. The whole retained response has a fixed memory/event quota.
func walkSSE(raw []byte, consume func(string, map[string]json.RawMessage) error) error {
	if len(raw) == 0 || len(raw) > 8<<20 || !utf8.Valid(raw) {
		return &Failure{Kind: "INVALID_STREAM", UsageUnknown: true}
	}
	name := ""
	data := []string{}
	events := 0
	flush := func() error {
		if len(data) == 0 {
			if name != "" {
				return &Failure{Kind: "EMPTY_STREAM_EVENT", UsageUnknown: true}
			}
			return nil
		}
		payload := []byte(strings.Join(data, "\n"))
		if validateWire(payload) != nil {
			return &Failure{Kind: "INVALID_STREAM_EVENT", UsageUnknown: true}
		}
		event, err := object(payload)
		if err != nil || text(event, "type") == "" || name != "" && name != text(event, "type") {
			return &Failure{Kind: "INVALID_STREAM_EVENT_TYPE", UsageUnknown: true}
		}
		events++
		if events > 32768 {
			return &Failure{Kind: "STREAM_EVENT_QUOTA", UsageUnknown: true}
		}
		if err = consume(text(event, "type"), event); err != nil {
			return err
		}
		name, data = "", nil
		return nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if len(line) > 2<<20 {
			return &Failure{Kind: "STREAM_LINE_QUOTA", UsageUnknown: true}
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
			if name != "" {
				return &Failure{Kind: "DUPLICATE_STREAM_EVENT_TYPE", UsageUnknown: true}
			}
			name = value
		case "data":
			data = append(data, value)
		case "id", "retry":
		default:
			return &Failure{Kind: "INVALID_STREAM_FIELD", UsageUnknown: true}
		}
	}
	if len(data) > 0 || name != "" {
		return &Failure{Kind: "STREAM_DISCONNECTED", UsageUnknown: true}
	}
	return nil
}

type streamedBlock struct {
	value      map[string]json.RawMessage
	kind       string
	closed     bool
	text       strings.Builder
	thinking   strings.Builder
	signature  strings.Builder
	input      strings.Builder
	inputDelta bool
}

func rawString(raw json.RawMessage) (string, bool) {
	var value string
	ok := raw != nil && string(raw) != "null" && json.Unmarshal(raw, &value) == nil
	return value, ok
}
func jsonString(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
func decodeAnthropicStream(r Request, raw []byte, result *Result) error {
	fail := func(kind string) error { return streamFailure(result, kind) }
	var message map[string]json.RawMessage
	var wireUsage map[string]json.RawMessage
	blocks := []*streamedBlock{}
	phase := "INITIAL"
	output := int64(-1)
	terminal := false
	err := walkSSE(raw, func(kind string, event map[string]json.RawMessage) error {
		if terminal {
			return fail("EVENT_AFTER_TERMINAL")
		}
		if kind == "ping" {
			return nil
		}
		switch kind {
		case "message_start":
			if phase != "INITIAL" {
				return fail("DUPLICATE_MESSAGE_START")
			}
			var err error
			message, err = object(event["message"])
			if err != nil || text(message, "id") == "" || text(message, "model") != r.Model || text(message, "role") != "assistant" || text(message, "type") != "message" {
				return fail("STREAM_PROFILE_OR_ROLE_MISMATCH")
			}
			content, err := list(message, "content")
			if err != nil || len(content) != 0 || text(message, "stop_reason") != "" {
				return fail("INVALID_MESSAGE_START")
			}
			wireUsage, err = object(message["usage"])
			if err != nil {
				return fail("INVALID_USAGE")
			}
			if n, exists := wireUsage["output_tokens"]; exists {
				var ok bool
				output, ok = number(map[string]json.RawMessage{"n": n}, "n")
				if !ok || output < 0 || output > 1<<40 {
					return fail("INVALID_USAGE")
				}
			}
			phase = "CONTENT"
		case "content_block_start":
			index, ok := number(event, "index")
			if phase != "CONTENT" || !ok || index != int64(len(blocks)) || len(blocks) >= 256 || len(blocks) > 0 && !blocks[len(blocks)-1].closed {
				return fail("INVALID_BLOCK_BOUNDARY")
			}
			value, err := object(event["content_block"])
			if err != nil {
				return fail("INVALID_CONTENT_BLOCK")
			}
			block := &streamedBlock{value: value, kind: text(value, "type")}
			switch block.kind {
			case "text", "thinking":
				key := "text"
				if block.kind == "thinking" {
					key = "thinking"
				}
				initial, ok := rawString(value[key])
				if !ok {
					return fail("INVALID_CONTENT_BLOCK")
				}
				if block.kind == "text" {
					block.text.WriteString(initial)
				} else {
					block.thinking.WriteString(initial)
					if signature, present := value["signature"]; present {
						initial, ok := rawString(signature)
						if !ok {
							return fail("INVALID_THINKING_SIGNATURE")
						}
						block.signature.WriteString(initial)
					}
				}
			case "tool_use":
				if text(value, "id") == "" || !toolName.MatchString(text(value, "name")) || validArguments(value["input"]) != nil {
					return fail("INVALID_TOOL_BLOCK")
				}
			case "redacted_thinking":
				if text(value, "data") == "" {
					return fail("INVALID_REDACTED_THINKING")
				}
			default:
				return fail("SERVER_TOOL_FALLBACK_OR_UNKNOWN_BLOCK")
			}
			blocks = append(blocks, block)
		case "content_block_delta":
			index, ok := number(event, "index")
			if phase != "CONTENT" || !ok || index < 0 || index != int64(len(blocks))-1 || blocks[index].closed {
				return fail("INVALID_BLOCK_BOUNDARY")
			}
			block := blocks[index]
			delta, err := object(event["delta"])
			if err != nil {
				return fail("INVALID_BLOCK_DELTA")
			}
			appendDelta := func(key string, builder *strings.Builder) error {
				value, ok := rawString(delta[key])
				if !ok || len(value) > (1<<20)-builder.Len() {
					return fail("INVALID_OR_OVERSIZED_DELTA")
				}
				builder.WriteString(value)
				return nil
			}
			switch text(delta, "type") {
			case "text_delta":
				if block.kind != "text" {
					return fail("BLOCK_DELTA_KIND_MISMATCH")
				}
				return appendDelta("text", &block.text)
			case "thinking_delta":
				if block.kind != "thinking" || block.signature.Len() != 0 {
					return fail("BLOCK_DELTA_KIND_MISMATCH")
				}
				return appendDelta("thinking", &block.thinking)
			case "signature_delta":
				if block.kind != "thinking" {
					return fail("BLOCK_DELTA_KIND_MISMATCH")
				}
				return appendDelta("signature", &block.signature)
			case "input_json_delta":
				if block.kind != "tool_use" {
					return fail("BLOCK_DELTA_KIND_MISMATCH")
				}
				initial, err := object(block.value["input"])
				if err != nil || len(initial) != 0 {
					return fail("AMBIGUOUS_TOOL_INPUT")
				}
				block.inputDelta = true
				return appendDelta("partial_json", &block.input)
			default:
				return fail("UNSUPPORTED_BLOCK_DELTA")
			}
		case "content_block_stop":
			index, ok := number(event, "index")
			if phase != "CONTENT" || !ok || index < 0 || index != int64(len(blocks))-1 || blocks[index].closed {
				return fail("INVALID_BLOCK_BOUNDARY")
			}
			block := blocks[index]
			switch block.kind {
			case "text":
				block.value["text"] = jsonString(block.text.String())
			case "thinking":
				block.value["thinking"] = jsonString(block.thinking.String())
				block.value["signature"] = jsonString(block.signature.String())
			case "tool_use":
				if block.inputDelta {
					input := json.RawMessage(block.input.String())
					if validArguments(input) != nil {
						return fail("INVALID_TOOL_ARGUMENTS")
					}
					block.value["input"] = input
				}
			}
			block.closed = true
		case "message_delta":
			if phase != "CONTENT" && phase != "FINALIZING" || len(blocks) > 0 && !blocks[len(blocks)-1].closed {
				return fail("INVALID_MESSAGE_DELTA_BOUNDARY")
			}
			delta, err := object(event["delta"])
			if err != nil || text(delta, "stop_reason") == "" || delta["model"] != nil {
				return fail("INVALID_MESSAGE_DELTA")
			}
			message["stop_reason"] = delta["stop_reason"]
			if sequence, exists := delta["stop_sequence"]; exists {
				message["stop_sequence"] = sequence
			}
			u, err := object(event["usage"])
			if err != nil {
				return fail("INVALID_USAGE")
			}
			n, ok := number(u, "output_tokens")
			if !ok || n < output || n > 1<<40 {
				return fail("INVALID_CUMULATIVE_USAGE")
			}
			for key, value := range u {
				if key == "input_tokens" || key == "cache_read_input_tokens" || key == "cache_creation_input_tokens" {
					previous, present := wireUsage[key]
					if present && !bytes.Equal(previous, value) {
						return fail("STREAM_INPUT_USAGE_CHANGED")
					}
				}
				wireUsage[key] = value
			}
			output = n
			phase = "FINALIZING"
		case "message_stop":
			if phase != "FINALIZING" {
				return fail("STREAM_ENDED_WITHOUT_TERMINAL")
			}
			terminal = true
		default:
			return fail("STREAM_ERROR_OR_UNKNOWN_EVENT")
		}
		return nil
	})
	if err != nil {
		return fail("INVALID_ANTHROPIC_STREAM")
	}
	if !terminal {
		return fail("STREAM_DISCONNECTED")
	}
	content := []json.RawMessage{}
	for _, block := range blocks {
		encoded, err := json.Marshal(block.value)
		if err != nil {
			return fail("INVALID_CONTENT_BLOCK")
		}
		content = append(content, encoded)
	}
	message["content"], _ = json.Marshal(content)
	message["usage"], _ = json.Marshal(wireUsage)
	sealed, _ := json.Marshal(message)
	if err := decodeResult("anthropic", sealed, result); err != nil {
		return fail("INVALID_TERMINAL_RESPONSE")
	}
	if err := validateResult(r, result); err != nil {
		return fail("INVALID_TERMINAL_RESPONSE")
	}
	return nil
}

func decodeOllamaStream(r Request, raw []byte, result *Result) error {
	fail := func(kind string) error { return streamFailure(result, kind) }
	if len(raw) == 0 || len(raw) > 8<<20 || !utf8.Valid(raw) || raw[len(raw)-1] != '\n' {
		return fail("INVALID_OR_DISCONNECTED_NDJSON")
	}
	var complete map[string]json.RawMessage
	var content strings.Builder
	calls := []json.RawMessage{}
	events := 0
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 {
			continue
		}
		if complete != nil || len(line) > 2<<20 || validateWire(line) != nil {
			return fail("INVALID_NDJSON_BOUNDARY")
		}
		events++
		if events > 32768 {
			return fail("STREAM_EVENT_QUOTA")
		}
		event, err := object(line)
		if err != nil || text(event, "model") != r.Model || event["error"] != nil {
			return fail("STREAM_PROFILE_OR_ERROR_MISMATCH")
		}
		var done bool
		if event["done"] == nil || string(event["done"]) == "null" || json.Unmarshal(event["done"], &done) != nil {
			return fail("INVALID_DONE_MARKER")
		}
		message, err := object(event["message"])
		if err != nil || text(message, "role") != "assistant" || text(message, "thinking") != "" {
			return fail("UNSUPPORTED_STREAM_ROLE_OR_THINKING")
		}
		part, ok := rawString(message["content"])
		if !ok || len(part) > (1<<20)-content.Len() {
			return fail("INVALID_OR_OVERSIZED_DELTA")
		}
		content.WriteString(part)
		if message["tool_calls"] != nil {
			chunk, err := list(message, "tool_calls")
			if err != nil || len(chunk) > 32-len(calls) {
				return fail("INVALID_STREAM_TOOL_CALLS")
			}
			for _, rawCall := range chunk {
				call, err := object(rawCall)
				if err != nil {
					return fail("INVALID_STREAM_TOOL_CALL")
				}
				function, err := object(call["function"])
				if err != nil || !toolName.MatchString(text(function, "name")) || validArguments(function["arguments"]) != nil {
					return fail("INVALID_STREAM_TOOL_ARGUMENTS")
				}
				if _, present := function["index"]; present {
					index, ok := number(function, "index")
					if !ok || index != int64(len(calls)) {
						return fail("DUPLICATE_OR_OUT_OF_ORDER_TOOL_INDEX")
					}
				}
				calls = append(calls, rawCall)
			}
		}
		if done {
			if text(event, "done_reason") == "" {
				return fail("INVALID_STOP")
			}
			complete = event
		}
	}
	if complete == nil {
		return fail("STREAM_DISCONNECTED")
	}
	complete["message"], _ = json.Marshal(map[string]any{"role": "assistant", "content": content.String(), "tool_calls": calls})
	sealed, _ := json.Marshal(complete)
	if err := decodeResult("ollama", sealed, result); err != nil {
		return fail("INVALID_TERMINAL_RESPONSE")
	}
	if err := validateResult(r, result); err != nil {
		return fail("INVALID_TERMINAL_RESPONSE")
	}
	return nil
}
