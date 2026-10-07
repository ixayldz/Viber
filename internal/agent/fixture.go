package agent

import (
	"encoding/json"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

type Turn struct {
	Text         string       `json:"text,omitempty"`
	Calls        []model.Call `json:"tool_calls,omitempty"`
	UsageKnown   bool         `json:"usage_known"`
	InputTokens  int64        `json:"input_tokens,omitempty"`
	OutputTokens int64        `json:"output_tokens,omitempty"`
	Incomplete   bool         `json:"incomplete,omitempty"`
}
type Fixture struct {
	SchemaVersion int    `json:"schema_version"`
	Turns         []Turn `json:"turns"`
}

func ParseFixture(raw []byte) (Fixture, error) {
	var fixture Fixture
	if err := c.DecodeStrict(raw, &fixture); err != nil {
		return fixture, err
	}
	if fixture.SchemaVersion != 1 || len(fixture.Turns) == 0 || len(fixture.Turns) > 256 {
		return fixture, c.Fail(c.InvalidArgument, "invalid offline model fixture")
	}
	return fixture, nil
}
func (fixture Fixture) Next(request model.Request, cursor int64) (model.Result, error) {
	result := model.Result{SchemaVersion: 1, RequestID: request.ID, Provider: "fixture", Model: request.Model, Calls: []model.Call{}, CompletionStatus: "UNKNOWN"}
	if err := model.ValidateRequest(request, "fixture"); err != nil {
		return result, err
	}
	if cursor < 0 || cursor >= int64(len(fixture.Turns)) {
		return result, c.Fail(c.InvalidArgument, "fixture exhausted without a final response")
	}
	turn := fixture.Turns[cursor]
	result.Text = turn.Text
	result.Calls = turn.Calls
	result.Usage = model.Usage{Known: turn.UsageKnown, Input: turn.InputTokens, Output: turn.OutputTokens}
	result.StopReason = "stop"
	result.CompletionStatus = "COMPLETE"
	if turn.Incomplete {
		result.CompletionStatus = "INCOMPLETE"
	}
	result.Raw, _ = json.Marshal(turn)
	if err := model.ValidateResult(request, &result); err != nil {
		return result, err
	}
	return result, nil
}
