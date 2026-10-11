package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

type deltaObserverKey struct{}

// WithDeltaObserver installs an ephemeral, best-effort observer for visible
// assistant text. It is intentionally carried in context instead of Request,
// so provider progress never becomes part of a durable model request receipt.
func WithDeltaObserver(ctx context.Context, observe func(string)) context.Context {
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, deltaObserverKey{}, observe)
}

func observeDelta(ctx context.Context, provider, raw string) {
	observe, _ := ctx.Value(deltaObserverKey{}).(func(string))
	if observe == nil || raw == "" || len(raw) > 1<<20 || !utf8.ValidString(raw) {
		return
	}
	var event map[string]json.RawMessage
	if validateWire([]byte(raw)) != nil {
		return
	}
	var err error
	event, err = object(json.RawMessage(raw))
	if err != nil {
		return
	}
	textDelta := ""
	switch provider {
	case "openai":
		if text(event, "type") != "response.output_text.delta" {
			return
		}
		textDelta, _ = rawString(event["delta"])
	case "anthropic":
		if text(event, "type") != "content_block_delta" {
			return
		}
		block, blockErr := object(event["delta"])
		if blockErr != nil || text(block, "type") != "text_delta" {
			return
		}
		textDelta, _ = rawString(block["text"])
	case "ollama":
		message, messageErr := object(event["message"])
		if messageErr != nil || text(message, "role") != "assistant" || text(message, "thinking") != "" {
			return
		}
		textDelta, _ = rawString(message["content"])
	default:
		return
	}
	// Keep individual IPC records small and preserve rune boundaries.
	for len(textDelta) > 0 {
		end := len(textDelta)
		if end > 4096 {
			end = 4096
			for end > 0 && !utf8.RuneStart(textDelta[end]) {
				end--
			}
		}
		if end == 0 {
			return
		}
		observe(textDelta[:end])
		textDelta = textDelta[end:]
	}
}

// readProviderStream keeps the terminal wire receipt intact while projecting
// only complete, recognized visible-text deltas into an ephemeral observer.
// The total body quota is enforced before returning any bytes to the receipt
// decoder, and a stalled consumer cannot block the producer callback itself.
func readProviderStream(ctx context.Context, body io.Reader, max int64, provider string) ([]byte, error) {
	if max < 1 || max > 8<<20 {
		return nil, io.ErrUnexpectedEOF
	}
	reader := bufio.NewReaderSize(body, 32<<10)
	var raw bytes.Buffer
	var line []byte
	var data []string
	eventName := ""
	events := 0
	flushSSE := func() {
		if len(data) == 0 {
			eventName = ""
			return
		}
		payload := strings.Join(data, "\n")
		data = nil
		var event map[string]json.RawMessage
		if validateWire([]byte(payload)) == nil {
			if parsed, err := object(json.RawMessage(payload)); err == nil && (eventName == "" || eventName == text(parsed, "type")) {
				event = parsed
			}
		}
		eventName = ""
		if event != nil {
			observeDelta(ctx, provider, payload)
		}
	}
	processLine := func(next []byte) {
		trimmed := bytes.TrimSuffix(bytes.TrimSuffix(next, []byte{'\n'}), []byte{'\r'})
		if len(trimmed) > 2<<20 {
			return
		}
		if provider == "ollama" {
			if len(trimmed) != 0 {
				observeDelta(ctx, provider, string(trimmed))
			}
			return
		}
		if len(trimmed) == 0 {
			flushSSE()
			return
		}
		if trimmed[0] == ':' {
			return
		}
		field, value, found := bytes.Cut(trimmed, []byte{':'})
		if !found {
			field = trimmed
		}
		value = bytes.TrimPrefix(value, []byte{' '})
		switch string(field) {
		case "event":
			if eventName != "" {
				eventName = "invalid"
			} else {
				eventName = string(value)
			}
		case "data":
			if len(data) < 128 {
				data = append(data, string(value))
			}
		}
	}
	for {
		part, err := reader.ReadBytes('\n')
		if len(part) != 0 {
			if int64(raw.Len())+int64(len(part)) > max {
				return raw.Bytes(), io.ErrShortBuffer
			}
			raw.Write(part)
			line = append(line[:0], part...)
			if len(line) > 2<<20 {
				line = nil
			} else {
				processLine(line)
			}
		}
		if err != nil {
			if err == io.EOF {
				return raw.Bytes(), nil
			}
			return raw.Bytes(), err
		}
		events++
		if events > 32768 {
			return raw.Bytes(), io.ErrShortBuffer
		}
	}
}
