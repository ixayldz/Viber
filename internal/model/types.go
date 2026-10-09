// Package model adapts client-controlled model protocols. Adapters never execute
// tools or infer permission from model output. Production endpoint acceptance is
// distinct from recorded/offline protocol conformance.
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type Call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Reply struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}
type Message struct {
	HistoryDigest string  `json:"historical_archive_ref,omitempty"`
	Role          string  `json:"role"`
	Text          string  `json:"text"`
	Calls         []Call  `json:"calls"`
	Replies       []Reply `json:"replies"`
	// Continuation is opaque, scoped to this exact provider/model profile.
	Continuation []json.RawMessage `json:"continuation"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model"`
}
type Request struct {
	Stream          bool      `json:"stream,omitempty"`
	ContextWindow   int64     `json:"context_window,omitempty"`
	SchemaVersion   int       `json:"schema_version"`
	ID              string    `json:"request_id"`
	Model           string    `json:"model"`
	Instructions    string    `json:"instructions"`
	Messages        []Message `json:"messages"`
	Tools           []Tool    `json:"tools"`
	MaxOutputTokens int64     `json:"max_output_tokens"`
}
type Usage struct {
	Known       bool  `json:"known"`
	Input       int64 `json:"input_tokens"`
	Output      int64 `json:"output_tokens"`
	CachedInput int64 `json:"cached_input_tokens"`
}
type Result struct {
	SchemaVersion     int               `json:"schema_version"`
	RequestID         string            `json:"request_id"`
	ProviderRequestID string            `json:"provider_request_id"`
	Provider          string            `json:"provider"`
	Model             string            `json:"model"`
	Text              string            `json:"text"`
	Calls             []Call            `json:"tool_calls"`
	Usage             Usage             `json:"usage"`
	StopReason        string            `json:"stop_reason"`
	CompletionStatus  string            `json:"completion_status"`
	Continuation      []json.RawMessage `json:"continuation"`
	// Raw bytes are private artifacts, never an instruction or task receipt.
	Raw []byte `json:"-"`
}
type Failure struct {
	Kind              string `json:"kind"`
	StatusCode        int    `json:"status_code"`
	ProviderRequestID string `json:"provider_request_id"`
	UsageUnknown      bool   `json:"usage_unknown"`
	Retryable         bool   `json:"retryable"`
}

func (f *Failure) Error() string { return fmt.Sprintf("model %s (HTTP %d)", f.Kind, f.StatusCode) }

type Adapter interface {
	Complete(context.Context, Request) (Result, error)
}

var toolName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func validateRequest(r Request, provider string) error {
	if r.ContextWindow < 0 || r.ContextWindow > 1<<20 || r.ContextWindow > 0 && provider != "ollama" || r.SchemaVersion != c.SchemaVersion || r.ID == "" || r.Model == "" || len(r.Model) > 256 || strings.ContainsAny(r.Model, "\r\n\x00") || r.MaxOutputTokens < 1 || r.MaxOutputTokens > 65536 && provider != "chatgpt" || len(r.Messages) == 0 || len(r.Messages) > 10000 || len(r.Tools) > 32 {
		return c.Fail(c.InvalidArgument, "invalid model request")
	}
	if r.Stream && provider != "openai" && provider != "anthropic" && provider != "ollama" {
		return c.Fail(c.PolicyDenied, "explicit streaming is supported only for registered API/local protocols")
	}
	if provider == "chatgpt" && (r.Model != ChatGPTModel || r.MaxOutputTokens != ChatGPTOutputCeiling || r.ContextWindow != 0) {
		return c.Fail(c.PolicyDenied, "ChatGPT model requires a registered published output ceiling")
	}
	names := map[string]bool{}
	for _, tool := range r.Tools {
		if !toolName.MatchString(tool.Name) || names[tool.Name] || len(tool.Description) > 8192 {
			return c.Fail(c.InvalidArgument, "invalid or duplicate native tool")
		}
		names[tool.Name] = true
		var shape map[string]any
		if err := c.DecodeStrict(tool.Parameters, &shape); err != nil {
			return err
		}
		if shape["type"] != "object" || shape["additionalProperties"] != false {
			return c.Fail(c.InvalidArgument, "strict object tool schema required")
		}
	}
	pending := map[string]string{}
	allIDs := map[string]bool{}
	for _, message := range r.Messages {
		if message.HistoryDigest != "" && (message.Role != "user" || !c.ValidDigest(message.HistoryDigest)) {
			return c.Fail(c.InvalidArgument, "invalid historical archive reference")
		}
		if len(pending) > 0 && message.Role != "tool" {
			return c.Fail(c.InvalidArgument, "unresolved tool protocol boundary")
		}
		if len(message.Continuation) > 0 && (message.Provider != provider || message.Model != r.Model || message.Role != "assistant") {
			return c.Fail(c.PolicyDenied, "continuation profile mismatch")
		}
		switch message.Role {
		case "user":
			if len(message.Calls) > 0 || len(message.Replies) > 0 || len(message.Continuation) > 0 {
				return c.Fail(c.InvalidArgument, "user message cannot carry protocol authority")
			}
		case "assistant":
			if len(message.Calls) > 32 || len(message.Replies) > 0 {
				return c.Fail(c.InvalidArgument, "assistant replies forbidden")
			}
			for _, call := range message.Calls {
				if call.ID == "" || allIDs[call.ID] || !names[call.Name] {
					return c.Fail(c.InvalidArgument, "unknown/duplicate historical tool call")
				}
				if err := validArguments(call.Arguments); err != nil {
					return err
				}
				pending[call.ID] = call.Name
				allIDs[call.ID] = true
			}
		case "tool":
			if len(message.Calls) > 0 || len(message.Continuation) > 0 || len(message.Replies) == 0 || message.Text != "" {
				return c.Fail(c.InvalidArgument, "invalid tool result message")
			}
			if len(message.Replies) != len(pending) {
				return c.Fail(c.InvalidArgument, "complete serial tool reply boundary required")
			}
			for _, reply := range message.Replies {
				if _, ok := pending[reply.CallID]; !ok {
					return c.Fail(c.InvalidArgument, "unmatched/duplicate tool result")
				}
				delete(pending, reply.CallID)
			}
		default:
			return c.Fail(c.InvalidArgument, "unknown canonical message role")
		}
	}
	if len(pending) > 0 {
		return c.Fail(c.InvalidArgument, "request cannot contain unresolved tool calls")
	}
	return nil
}
func validArguments(raw []byte) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return c.Fail(c.InvalidArgument, "invalid tool argument size")
	}
	var value map[string]any
	if err := c.DecodeStrict(raw, &value); err != nil {
		return err
	}
	if value == nil {
		return c.Fail(c.InvalidArgument, "tool arguments must be an object")
	}
	return nil
}
func validateResult(r Request, result *Result) error {
	if len(result.Calls) > 32 {
		result.Calls = nil
		result.Continuation = nil
		result.CompletionStatus = "UNKNOWN"
		return &Failure{Kind: "TOOL_CALL_QUOTA", UsageUnknown: true}
	}
	selected := map[string]bool{}
	for _, tool := range r.Tools {
		selected[tool.Name] = true
	}
	ids := map[string]bool{}
	for _, message := range r.Messages {
		if message.HistoryDigest != "" && (message.Role != "user" || !c.ValidDigest(message.HistoryDigest)) {
			return c.Fail(c.InvalidArgument, "invalid historical archive reference")
		}
		for _, call := range message.Calls {
			ids[call.ID] = true
		}
	}
	for _, call := range result.Calls {
		if call.ID == "" || ids[call.ID] || !selected[call.Name] {
			return &Failure{Kind: "INVALID_TOOL_CALL", UsageUnknown: !result.Usage.Known}
		}
		ids[call.ID] = true
		if err := validArguments(call.Arguments); err != nil {
			return &Failure{Kind: "INVALID_TOOL_ARGUMENTS", UsageUnknown: !result.Usage.Known}
		}
	}
	if result.Usage.Input < 0 || result.Usage.Output < 0 || result.Usage.CachedInput < 0 || result.Usage.CachedInput > result.Usage.Input {
		return &Failure{Kind: "INVALID_USAGE", UsageUnknown: true}
	}
	if result.CompletionStatus != "COMPLETE" {
		result.Calls = []Call{}
		result.Continuation = nil
	}
	return nil
}
func (r Result) AssistantMessage() (Message, error) {
	if r.CompletionStatus != "COMPLETE" {
		return Message{}, c.Fail(c.InvalidArgument, "incomplete response cannot continue tool protocol")
	}
	return Message{Role: "assistant", Text: r.Text, Calls: r.Calls, Continuation: r.Continuation, Provider: r.Provider, Model: r.Model}, nil
}

func ValidateRequest(request Request, provider string) error {
	return validateRequest(request, provider)
}
func ValidateResult(request Request, result *Result) error { return validateResult(request, result) }

// PreflightFailure can only be emitted before the inference transport is called.
// Credential/catalog renewal is distinct from possibly billed inference.
type PreflightFailure struct{ Cause error }

func (f *PreflightFailure) Error() string { return "model preflight declined" }
func (f *PreflightFailure) Unwrap() error { return f.Cause }

type NoDispatchReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	RequestDigest string `json:"request_digest"`
	ProfileDigest string `json:"profile_digest"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Decision      string `json:"decision"`
}
