// Package context implements bounded packing without discarding required inputs.
package context

import (
	"encoding/json"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type Block struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type Limits struct {
	Context      int64
	Input        int64
	Output       int64
	SafetyMargin int64
}
type Manifest struct {
	Included      []string `json:"included"`
	Omitted       []string `json:"omitted"`
	InputTokens   int64    `json:"input_tokens"`
	OutputReserve int64    `json:"output_reserve"`
	Estimator     string   `json:"estimator"`
	Digest        string   `json:"digest"`
}
type CountTokens func([]byte) (int64, error)

// Pack keeps whole blocks. The count function must include the serialized
// protocol/tools/continuation in mandatory blocks; production adapter conformance
// owns that count. This function does not silently rely on provider truncation.
func Pack(mandatory, optional []Block, limits Limits, count CountTokens) ([]Block, Manifest, error) {
	m := Manifest{Included: []string{}, Omitted: []string{}, OutputReserve: limits.Output, Estimator: "adapter-supplied"}
	if len(mandatory) == 0 || count == nil || limits.Context <= 0 || limits.Output < 0 || limits.SafetyMargin < 0 || limits.Input < 0 {
		return nil, m, c.Fail(c.InvalidArgument, "invalid context contract")
	}
	if limits.Output > limits.Context || limits.SafetyMargin > limits.Context-limits.Output {
		return nil, m, c.Fail(c.ContextTooSmall, "reserves exceed context")
	}
	available := limits.Context - limits.Output - limits.SafetyMargin
	if limits.Input > 0 && limits.Input < available {
		available = limits.Input
	}
	selected := append([]Block(nil), mandatory...)
	seen := map[string]bool{}
	for _, b := range append(append([]Block(nil), mandatory...), optional...) {
		if b.ID == "" || seen[b.ID] {
			return nil, m, c.Fail(c.InvalidArgument, "empty or duplicate block ID")
		}
		seen[b.ID] = true
	}
	measure := func(blocks []Block) (int64, error) {
		raw, err := json.Marshal(blocks)
		if err != nil {
			return 0, err
		}
		n, err := count(raw)
		if n < 0 {
			return 0, c.Fail(c.InvalidArgument, "negative token count")
		}
		return n, err
	}
	n, err := measure(selected)
	if err != nil {
		return nil, m, err
	}
	if n > available {
		return nil, m, c.Fail(c.ContextTooSmall, "mandatory runtime, intent and constraints do not fit")
	}
	for _, b := range mandatory {
		m.Included = append(m.Included, b.ID)
	}
	for _, b := range optional {
		proposed := append(append([]Block(nil), selected...), b)
		next, err := measure(proposed)
		if err != nil {
			return nil, m, err
		}
		if next > available {
			m.Omitted = append(m.Omitted, b.ID)
			continue
		}
		selected = proposed
		n = next
		m.Included = append(m.Included, b.ID)
	}
	m.InputTokens = n
	m.Digest, err = c.Digest(selected)
	return selected, m, err
}
