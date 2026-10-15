package model

import (
	"bytes"
	"encoding/json"
	"reflect"
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

const responseStreamOutputQuota = 32

// A bounded ChatGPT stream may carry its sealed output only in
// response.output_item.done while response.completed reports output: []. This
// is a decoder accommodation for that observed route shape, not an authority
// grant to execute partial deltas. Only completed done items are reconstructed.
type responseStreamOutputItem struct {
	id                 string
	kind               string
	callID             string
	name               string
	namespace          string
	namespacePresent   bool
	argumentPrefix     string
	argumentPrefixSeen bool
	done               bool
	doneRaw            json.RawMessage
	argumentDelta      strings.Builder
	argumentDeltaSeen  bool
	argumentDone       string
	argumentDoneSeen   bool
}

type responseStreamOutputs struct {
	items  []*responseStreamOutputItem
	byID   map[string]*responseStreamOutputItem
	done   []json.RawMessage
	events bool
}

func newResponseStreamOutputs() responseStreamOutputs {
	return responseStreamOutputs{byID: map[string]*responseStreamOutputItem{}}
}

func responseStreamOutputIndex(event map[string]json.RawMessage) (int64, bool) {
	index, ok := number(event, "output_index")
	return index, ok && index >= 0 && index < responseStreamOutputQuota
}

func responseStreamItemText(item map[string]json.RawMessage, key string, required bool) (string, bool) {
	raw, present := item[key]
	if !present {
		return "", !required
	}
	value, ok := rawString(raw)
	if !ok {
		return "", false
	}
	return value, true
}

func responseStreamItemIdentity(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}

func validateResponseStreamOutputItem(item map[string]json.RawMessage, namespace, done bool) (string, bool) {
	id, ok := responseStreamItemText(item, "id", true)
	if !ok || !responseStreamItemIdentity(id) {
		return "", false
	}
	kind, ok := responseStreamItemText(item, "type", true)
	if !ok {
		return "", false
	}
	switch kind {
	case "reasoning", "message", "function_call":
	default:
		return "", false
	}
	status, ok := responseStreamItemText(item, "status", false)
	if !ok || status != "" && status != "in_progress" && status != "completed" {
		return "", false
	}
	if done && ((kind != "reasoning" && status != "completed") || (kind == "reasoning" && status != "" && status != "completed")) {
		return "", false
	}
	if kind == "message" {
		role, ok := responseStreamItemText(item, "role", false)
		if !ok || role != "" && role != "assistant" {
			return "", false
		}
	}
	if kind == "reasoning" && done && text(item, "encrypted_content") == "" {
		return "", false
	}
	if kind != "function_call" {
		return kind, true
	}
	itemNamespace, present := item["namespace"]
	if present {
		value, ok := rawString(itemNamespace)
		if !ok || namespace && value != "viber" || !namespace && value != "" {
			return "", false
		}
	} else if namespace {
		return "", false
	}
	callID, ok := responseStreamItemText(item, "call_id", true)
	if !ok || !responseStreamItemIdentity(callID) {
		return "", false
	}
	name, ok := responseStreamItemText(item, "name", true)
	if !ok || !toolName.MatchString(name) {
		return "", false
	}
	arguments, ok := responseStreamItemText(item, "arguments", done)
	if !ok || len(arguments) > 1<<20 || done && validArguments([]byte(arguments)) != nil {
		return "", false
	}
	return kind, true
}

func responseStreamJSONEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func (s *responseStreamOutputs) add(event map[string]json.RawMessage, namespace bool) bool {
	index, ok := responseStreamOutputIndex(event)
	if !ok || index != int64(len(s.items)) || len(s.items) >= responseStreamOutputQuota {
		return false
	}
	item, err := object(event["item"])
	if err != nil {
		return false
	}
	kind, ok := validateResponseStreamOutputItem(item, namespace, false)
	if !ok {
		return false
	}
	id, _ := responseStreamItemText(item, "id", true)
	if _, exists := s.byID[id]; exists {
		return false
	}
	entry := &responseStreamOutputItem{id: id, kind: kind}
	if kind == "function_call" {
		entry.callID, _ = responseStreamItemText(item, "call_id", true)
		entry.name, _ = responseStreamItemText(item, "name", true)
		if raw, present := item["namespace"]; present {
			entry.namespace, _ = rawString(raw)
			entry.namespacePresent = true
		}
		if raw, present := item["arguments"]; present {
			entry.argumentPrefix, _ = rawString(raw)
			entry.argumentPrefixSeen = true
		}
	}
	s.items = append(s.items, entry)
	s.byID[id] = entry
	s.events = true
	return true
}

func (s *responseStreamOutputs) doneItem(event map[string]json.RawMessage, namespace bool) bool {
	index, ok := responseStreamOutputIndex(event)
	if !ok || index != int64(len(s.done)) || index >= int64(len(s.items)) {
		return false
	}
	item, err := object(event["item"])
	if err != nil {
		return false
	}
	kind, ok := validateResponseStreamOutputItem(item, namespace, false)
	if !ok {
		return false
	}
	id, _ := responseStreamItemText(item, "id", true)
	entry := s.items[index]
	if entry.id != id || entry.kind != kind || entry.done {
		return false
	}
	if kind == "function_call" {
		callID, callIDOK := responseStreamItemText(item, "call_id", true)
		name, nameOK := responseStreamItemText(item, "name", true)
		if !callIDOK || !nameOK || callID != entry.callID || name != entry.name {
			return false
		}
		itemNamespace, present := item["namespace"]
		if present != entry.namespacePresent {
			return false
		}
		if present {
			value, ok := rawString(itemNamespace)
			if !ok || value != entry.namespace {
				return false
			}
		}
		if entry.argumentPrefixSeen {
			arguments, ok := responseStreamItemText(item, "arguments", true)
			if !ok || !strings.HasPrefix(arguments, entry.argumentPrefix) {
				return false
			}
		}
	}
	if entry.argumentDoneSeen {
		if arguments := text(item, "arguments"); arguments != entry.argumentDone {
			return false
		}
	}
	entry.done = true
	entry.doneRaw = append(json.RawMessage(nil), event["item"]...)
	s.done = append(s.done, entry.doneRaw)
	s.events = true
	return true
}

func (s *responseStreamOutputs) functionArgumentsDelta(event map[string]json.RawMessage) bool {
	itemID, ok := responseStreamItemText(event, "item_id", true)
	if !ok || !responseStreamItemIdentity(itemID) {
		return false
	}
	index, ok := responseStreamOutputIndex(event)
	if !ok || index >= int64(len(s.items)) {
		return false
	}
	entry := s.items[index]
	if entry.id != itemID || entry.kind != "function_call" || entry.done || entry.argumentDoneSeen {
		return false
	}
	delta, ok := responseStreamItemText(event, "delta", true)
	if !ok || len(delta) > (1<<20)-entry.argumentDelta.Len() {
		return false
	}
	entry.argumentDelta.WriteString(delta)
	entry.argumentDeltaSeen = true
	s.events = true
	return true
}

func (s *responseStreamOutputs) functionArgumentsDone(event map[string]json.RawMessage) bool {
	itemID, ok := responseStreamItemText(event, "item_id", true)
	if !ok || !responseStreamItemIdentity(itemID) {
		return false
	}
	index, ok := responseStreamOutputIndex(event)
	if !ok || index >= int64(len(s.items)) {
		return false
	}
	entry := s.items[index]
	if entry.id != itemID || entry.kind != "function_call" || entry.done || entry.argumentDoneSeen {
		return false
	}
	arguments, ok := responseStreamItemText(event, "arguments", true)
	if !ok || len(arguments) > 1<<20 {
		return false
	}
	if entry.argumentDeltaSeen && entry.argumentDelta.String() != arguments {
		return false
	}
	entry.argumentDone = arguments
	entry.argumentDoneSeen = true
	s.events = true
	return true
}

func (s *responseStreamOutputs) sealed(namespace bool) ([]json.RawMessage, bool) {
	if !s.events || len(s.items) == 0 || len(s.done) != len(s.items) {
		return nil, false
	}
	for _, item := range s.items {
		if !item.done {
			return nil, false
		}
		value, err := object(item.doneRaw)
		if err != nil {
			return nil, false
		}
		if _, ok := validateResponseStreamOutputItem(value, namespace, true); !ok {
			return nil, false
		}
		if item.kind == "function_call" {
			if item.argumentDeltaSeen && !item.argumentDoneSeen {
				return nil, false
			}
			if item.argumentDoneSeen && text(value, "arguments") != item.argumentDone {
				return nil, false
			}
		}
	}
	return s.done, true
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
	createdModel := ""
	events := 0
	lastSeq := int64(-1)
	outputs := newResponseStreamOutputs()
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
			if err != nil || createdID != "" || !responseStreamItemIdentity(text(response, "id")) {
				return fail("INVALID_STREAM_IDENTITY")
			}
			createdID = text(response, "id")
			createdModel = text(response, "model")
			if createdModel != "" && createdModel != r.Model {
				return fail("STREAM_PROFILE_OR_STATUS_MISMATCH")
			}
		}
		switch kind {
		case "response.output_item.added":
			if !outputs.add(event, namespace) {
				return fail("INVALID_OUTPUT_ITEM_ADDED")
			}
		case "response.output_item.done":
			if !outputs.doneItem(event, namespace) {
				return fail("INVALID_OUTPUT_ITEM_DONE")
			}
		case "response.function_call_arguments.delta":
			if !outputs.functionArgumentsDelta(event) {
				return fail("INVALID_FUNCTION_ARGUMENT_DELTA")
			}
		case "response.function_call_arguments.done":
			if !outputs.functionArgumentsDone(event) {
				return fail("INVALID_FUNCTION_ARGUMENT_DONE")
			}
		}
		switch kind {
		case "response.completed", "response.incomplete", "response.failed":
			response, err := object(event["response"])
			if err != nil {
				return fail("INVALID_TERMINAL_RESPONSE")
			}
			sealedResponse := event["response"]
			if !responseStreamItemIdentity(text(response, "id")) || createdID != "" && createdID != text(response, "id") || createdModel != "" && createdModel != text(response, "model") || text(response, "model") != r.Model || text(response, "status") != strings.TrimPrefix(kind, "response.") {
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
			if outputs.events {
				if kind == "response.completed" {
					if createdID == "" {
						return fail("INVALID_OUTPUT_ITEM_TERMINAL")
					}
					sealed, ok := outputs.sealed(namespace)
					if !ok {
						return fail("INCOMPLETE_OUTPUT_ITEM")
					}
					if len(output) == 0 {
						response["output"], _ = json.Marshal(sealed)
						sealedResponse, _ = json.Marshal(response)
					} else {
						if len(output) != len(sealed) {
							return fail("CONFLICTING_TERMINAL_OUTPUT")
						}
						for index := range output {
							if !responseStreamJSONEqual(output[index], sealed[index]) {
								return fail("CONFLICTING_TERMINAL_OUTPUT")
							}
						}
					}
				} else {
					if namespace {
						return fail("INVALID_OUTPUT_ITEM_TERMINAL")
					}
					// OpenAI's ordinary stream decoder historically accepts an
					// incomplete/failed terminal receipt with known usage. Keep
					// that accounting behavior when lifecycle events are present;
					// only a completed response may publish reconstructed items.
				}
			}
			// Only the sealed terminal Response enters the shared result decoder.
			if err = decodeResult("openai", sealedResponse, result); err != nil {
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
