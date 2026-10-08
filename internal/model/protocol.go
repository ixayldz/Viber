package model

import (
	"encoding/json"
	"fmt"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func encodeRequest(provider string, r Request) (any, string, error) {
	switch provider {
	case "chatgpt":
		return encodeChatGPT(r)
	case "openai":
		input := []any{}
		for _, message := range r.Messages {
			if message.Role == "tool" {
				for _, reply := range message.Replies {
					input = append(input, map[string]any{"type": "function_call_output", "call_id": reply.CallID, "output": reply.Content})
				}
				continue
			}
			if message.Role == "assistant" && len(message.Continuation) > 0 {
				if err := checkContinuation("openai", message); err != nil {
					return nil, "", err
				}
				for _, item := range message.Continuation {
					input = append(input, item)
				}
				continue
			}
			input = append(input, map[string]any{"role": message.Role, "content": message.Text})
			for _, call := range message.Calls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": string(call.Arguments)})
			}
		}
		tools := []any{}
		for _, tool := range r.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters, "strict": true})
		}
		return map[string]any{"model": r.Model, "instructions": r.Instructions, "input": input, "tools": tools, "store": false, "stream": r.Stream, "parallel_tool_calls": false, "truncation": "disabled", "max_output_tokens": r.MaxOutputTokens, "include": []string{"reasoning.encrypted_content"}}, "/v1/responses", nil
	case "anthropic":
		messages := []any{}
		for _, message := range r.Messages {
			content := []any{}
			role := message.Role
			if message.Role == "tool" {
				role = "user"
				for _, reply := range message.Replies {
					content = append(content, map[string]any{"type": "tool_result", "tool_use_id": reply.CallID, "content": reply.Content, "is_error": reply.IsError})
				}
			} else if message.Role == "assistant" && len(message.Continuation) > 0 {
				if err := checkContinuation(provider, message); err != nil {
					return nil, "", err
				}
				for _, block := range message.Continuation {
					content = append(content, block)
				}
			} else {
				if message.Text != "" {
					content = append(content, map[string]any{"type": "text", "text": message.Text})
				}
				for _, call := range message.Calls {
					content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": call.Arguments})
				}
			}
			if len(content) == 0 {
				return nil, "", c.Fail(c.InvalidArgument, "empty Anthropic content boundary")
			}
			messages = append(messages, map[string]any{"role": role, "content": content})
		}
		tools := []any{}
		for _, tool := range r.Tools {
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters})
		}
		return map[string]any{"model": r.Model, "system": r.Instructions, "messages": messages, "tools": tools, "max_tokens": r.MaxOutputTokens, "stream": r.Stream, "tool_choice": map[string]any{"type": "auto", "disable_parallel_tool_use": true}}, "/v1/messages", nil
	case "ollama":
		messages := []any{map[string]any{"role": "system", "content": r.Instructions}}
		callNames := map[string]string{}
		for _, message := range r.Messages {
			if len(message.Continuation) > 0 {
				return nil, "", c.Fail(c.UnsupportedCapability, "Ollama opaque continuation unsupported")
			}
			if message.Role == "tool" {
				for _, reply := range message.Replies {
					messages = append(messages, map[string]any{"role": "tool", "tool_name": callNames[reply.CallID], "content": reply.Content})
				}
				continue
			}
			wire := map[string]any{"role": message.Role, "content": message.Text}
			calls := []any{}
			for _, call := range message.Calls {
				callNames[call.ID] = call.Name
				calls = append(calls, map[string]any{"function": map[string]any{"name": call.Name, "arguments": call.Arguments}})
			}
			if len(calls) > 0 {
				wire["tool_calls"] = calls
			}
			messages = append(messages, wire)
		}
		tools := []any{}
		for _, tool := range r.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters}})
		}
		// A local endpoint may have model-specific tool support; no endpoint or model
		// is marked supported until its recorded/real profile conformance passes.
		options := map[string]any{"num_predict": r.MaxOutputTokens}
		if r.ContextWindow > 0 {
			options["num_ctx"] = r.ContextWindow
		}
		return map[string]any{"model": r.Model, "messages": messages, "tools": tools, "stream": r.Stream, "think": false, "options": options}, "/api/chat", nil
	}
	return nil, "", c.Fail(c.UnsupportedCapability, "unknown protocol")
}

func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, &Failure{Kind: "INVALID_RESPONSE", UsageUnknown: true}
	}
	return value, nil
}
func text(value map[string]json.RawMessage, key string) string {
	var out string
	_ = json.Unmarshal(value[key], &out)
	return out
}
func flag(value map[string]json.RawMessage, key string) bool {
	var out bool
	_ = json.Unmarshal(value[key], &out)
	return out
}
func number(value map[string]json.RawMessage, key string) (int64, bool) {
	var out int64
	raw, ok := value[key]
	if !ok || string(raw) == "null" {
		return 0, false
	}
	return out, json.Unmarshal(raw, &out) == nil
}
func list(value map[string]json.RawMessage, key string) ([]json.RawMessage, error) {
	raw, ok := value[key]
	if !ok || string(raw) == "null" {
		return nil, &Failure{Kind: "INVALID_RESPONSE", UsageUnknown: true}
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Failure{Kind: "INVALID_RESPONSE", UsageUnknown: true}
	}
	return out, nil
}
func usage(value map[string]json.RawMessage, input, output, cached string) Usage {
	in, okIn := number(value, input)
	out, okOut := number(value, output)
	cache, okCache := number(value, cached)
	_, cachePresent := value[cached]
	return Usage{Known: okIn && okOut && (!cachePresent || okCache), Input: in, Output: out, CachedInput: cache}
}
func decodeResult(provider string, raw []byte, result *Result) error {
	value, err := object(raw)
	if err != nil {
		return err
	}
	switch provider {

	case "openai":
		if result.ProviderRequestID == "" {
			result.ProviderRequestID = text(value, "id")
		}
		status := text(value, "status")
		result.StopReason = status
		result.CompletionStatus = "INCOMPLETE"
		if status == "completed" {
			result.CompletionStatus = "COMPLETE"
		}
		if status == "" {
			return &Failure{Kind: "INVALID_STOP", UsageUnknown: true}
		}
		if wireUsage, err := object(value["usage"]); err == nil {
			result.Usage = usage(wireUsage, "input_tokens", "output_tokens", "")
			if details, err := object(wireUsage["input_tokens_details"]); err == nil {
				result.Usage.CachedInput, _ = number(details, "cached_tokens")
			}
		}
		items, err := list(value, "output")
		if err != nil {
			return err
		}
		if err = decodeBlocks(provider, items, result); err != nil {
			return err
		}
		result.Continuation = items
	case "anthropic":
		if result.ProviderRequestID == "" {
			result.ProviderRequestID = text(value, "id")
		}
		if text(value, "role") != "assistant" {
			return &Failure{Kind: "INVALID_ROLE", UsageUnknown: true}
		}
		stop := text(value, "stop_reason")
		result.StopReason = stop
		result.CompletionStatus = "INCOMPLETE"
		if stop == "end_turn" || stop == "tool_use" || stop == "stop_sequence" {
			result.CompletionStatus = "COMPLETE"
		}
		if stop == "" {
			return &Failure{Kind: "INVALID_STOP", UsageUnknown: true}
		}
		if wireUsage, err := object(value["usage"]); err == nil {
			result.Usage = usage(wireUsage, "input_tokens", "output_tokens", "cache_read_input_tokens")
			// Anthropic input_tokens excludes cache read/write categories. Canonical
			// input counts all categories and cached input remains a subset of it.
			write, validWrite := number(wireUsage, "cache_creation_input_tokens")
			_, writePresent := wireUsage["cache_creation_input_tokens"]
			if !result.Usage.Known || writePresent && !validWrite || result.Usage.Input < 0 || result.Usage.Output < 0 || result.Usage.CachedInput < 0 || write < 0 || result.Usage.Input > 1<<60 || result.Usage.CachedInput > 1<<60 || write > 1<<60 {
				return &Failure{Kind: "INVALID_USAGE", UsageUnknown: true}
			}
			result.Usage.Input += result.Usage.CachedInput + write
		}
		blocks, err := list(value, "content")
		if err != nil {
			return err
		}
		if err = decodeBlocks(provider, blocks, result); err != nil {
			return err
		}
		result.Continuation = blocks
		if stop == "tool_use" && len(result.Calls) == 0 || stop != "tool_use" && len(result.Calls) > 0 {
			return &Failure{Kind: "INVALID_TOOL_STOP", UsageUnknown: !result.Usage.Known}
		}
	case "ollama":
		result.StopReason = text(value, "done_reason")
		result.CompletionStatus = "INCOMPLETE"
		if flag(value, "done") && result.StopReason == "stop" {
			result.CompletionStatus = "COMPLETE"
		}
		message, err := object(value["message"])
		if err != nil {
			return err
		}
		if text(message, "role") != "assistant" {
			return &Failure{Kind: "INVALID_ROLE", UsageUnknown: true}
		}
		result.Text = text(message, "content")
		result.Usage = usage(value, "prompt_eval_count", "eval_count", "prompt_eval_cached_count")
		if text(message, "thinking") != "" {
			return &Failure{Kind: "UNSUPPORTED_THINKING_PROFILE", UsageUnknown: !result.Usage.Known}
		}
		if _, ok := message["tool_calls"]; ok {
			calls, err := list(message, "tool_calls")
			if err != nil {
				return err
			}
			for n, raw := range calls {
				call, err := object(raw)
				if err != nil {
					return err
				}
				function, err := object(call["function"])
				if err != nil {
					return err
				}
				result.Calls = append(result.Calls, Call{ID: fmt.Sprintf("%s/local/%d", result.RequestID, n), Name: text(function, "name"), Arguments: function["arguments"]})
			}
		}
	default:
		return c.Fail(c.UnsupportedCapability, "unknown protocol")
	}
	return nil
}
func decodeBlocks(provider string, blocks []json.RawMessage, result *Result) error {
	for _, raw := range blocks {
		block, err := object(raw)
		if err != nil {
			return err
		}
		kind := text(block, "type")
		if provider == "openai" {
			switch kind {
			case "function_call":
				if text(block, "status") != "completed" {
					result.CompletionStatus = "INCOMPLETE"
				}
				result.Calls = append(result.Calls, Call{ID: text(block, "call_id"), Name: text(block, "name"), Arguments: json.RawMessage(text(block, "arguments"))})
			case "message":
				if text(block, "role") != "assistant" {
					return &Failure{Kind: "INVALID_ROLE", UsageUnknown: !result.Usage.Known}
				}
				contents, err := list(block, "content")
				if err != nil {
					return err
				}
				for _, raw := range contents {
					content, err := object(raw)
					if err != nil {
						return err
					}
					switch text(content, "type") {
					case "output_text":
						result.Text += text(content, "text")
					case "refusal":
						result.Text += text(content, "refusal")
						result.CompletionStatus = "REFUSED"
					default:
						return &Failure{Kind: "UNSUPPORTED_CONTENT", UsageUnknown: !result.Usage.Known}
					}
				}
			case "reasoning":
				// All returned reasoning/phase fields remain opaque and are replayed only
				// to the same provider/model; they never become user/system instructions.
				if text(block, "encrypted_content") == "" {
					return &Failure{Kind: "MISSING_STATELESS_REASONING", UsageUnknown: !result.Usage.Known}
				}
			default:
				return &Failure{Kind: "SERVER_TOOL_OR_UNKNOWN_ITEM", UsageUnknown: !result.Usage.Known}
			}
		} else {
			switch kind {
			case "tool_use":
				result.Calls = append(result.Calls, Call{ID: text(block, "id"), Name: text(block, "name"), Arguments: block["input"]})
			case "text":
				result.Text += text(block, "text")
			case "thinking":
				if text(block, "signature") == "" {
					return &Failure{Kind: "MISSING_THINKING_SIGNATURE", UsageUnknown: !result.Usage.Known}
				}
			case "redacted_thinking":
				if text(block, "data") == "" {
					return &Failure{Kind: "INVALID_REDACTED_THINKING", UsageUnknown: !result.Usage.Known}
				}
			default:
				return &Failure{Kind: "SERVER_TOOL_OR_UNKNOWN_ITEM", UsageUnknown: !result.Usage.Known}
			}
		}
	}
	return nil
}
func checkContinuation(provider string, message Message) error {
	result := Result{CompletionStatus: "COMPLETE", Calls: []Call{}}
	for _, raw := range message.Continuation {
		if err := validateWire(raw); err != nil {
			return c.Fail(c.InvalidArgument, "invalid continuation bytes")
		}
	}
	if err := decodeBlocks(provider, message.Continuation, &result); err != nil {
		return err
	}
	a, err := c.Digest(result.Calls)
	if err != nil {
		return err
	}
	b, err := c.Digest(message.Calls)
	if err != nil {
		return err
	}
	if a != b || result.Text != message.Text || result.CompletionStatus != "COMPLETE" {
		return c.Fail(c.InvalidArgument, "canonical/opaque continuation mismatch")
	}
	return nil
}
