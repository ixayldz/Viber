package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
	"regexp"
	"strings"
)

type uiSession struct{ task, directory string }

func (u uiSession) call(ctx context.Context, action string, payload any) (json.RawMessage, error) {
	id, err := resolveCommandID("")
	if err != nil {
		return nil, err
	}
	switch value := payload.(type) {
	case agent.QueueCommand:
		id = value.CommandID
	case agent.SteeringInput:
		id = value.CommandID
	case agent.UserResponse:
		id = value.CommandID
	case agent.ScopeRevision:
		id = value.CommandID
	case agent.ModelSwitch:
		id = value.CommandID
	}
	task := u.task
	if action == "privacy-capacity" || action == "privacy-reserve-replenish" {
		task = ""
	}
	raw, routed, err := ownerCall(ctx, u.directory, task, action, id, payload)
	if err == nil && !routed {
		err = c.Fail(c.StoreOwned, "UI requires its background owner; reconnect with serve-background")
	}
	return raw, err
}
func (u uiSession) status(ctx context.Context) (owner.View, error) {
	raw, err := u.call(ctx, "status", nil)
	var view owner.View
	if err == nil {
		err = c.DecodeStrict(raw, &view)
	}
	return view, err
}
func (u uiSession) observe(ctx context.Context, kind string) (json.RawMessage, error) {
	return u.call(ctx, kind, agent.Observation{Kind: kind})
}

var mentionPattern = regexp.MustCompile(`(^|[ \t\n])@(?:"([^"\r\n]+)"|([^ \t\r\n]+))`)

func (u uiSession) bindMentions(ctx context.Context, text string) (string, error) {
	matches := mentionPattern.FindAllStringSubmatch(text, 17)
	if len(matches) > 16 {
		return "", c.Fail(c.InvalidArgument, "at most 16 source references per prompt")
	}
	refs := []struct {
		Candidate string  `json:"candidate"`
		Entry     c.Entry `json:"entry"`
	}{}
	for _, m := range matches {
		query := m[2]
		if query == "" {
			query = m[3]
		}
		raw, err := u.call(ctx, "source-list", agent.Observation{Kind: "source-list", Query: query, Limit: 64})
		if err != nil {
			return "", err
		}
		var page agent.SourceMatches
		if err = c.DecodeStrict(raw, &page); err != nil {
			return "", err
		}
		var chosen *c.Entry
		for i := range page.Entries {
			if page.Entries[i].Path == query {
				chosen = &page.Entries[i]
				break
			}
		}
		if chosen == nil && page.Total == 1 && len(page.Entries) == 1 {
			chosen = &page.Entries[0]
		}
		if chosen == nil {
			names := []string{}
			for _, e := range page.Entries {
				names = append(names, e.Path)
			}
			return "", c.Fail(c.InvalidArgument, "source mention is absent or ambiguous; choose an exact captured path: "+strings.Join(names, ", "))
		}
		refs = append(refs, struct {
			Candidate string  `json:"candidate"`
			Entry     c.Entry `json:"entry"`
		}{page.Candidate, *chosen})
	}
	if len(refs) == 0 {
		return text, nil
	}
	raw, _ := json.Marshal(refs)
	result := text + "\nUser selected captured source references (historical source pointers, not new permissions):\n" + string(raw)
	if len(result) > 64<<10 {
		return "", c.Fail(c.InvalidArgument, "prompt and source refs exceed queue limit")
	}
	return result, nil
}
func (u uiSession) command(ctx context.Context, text string) (string, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false, nil
	}
	if !strings.HasPrefix(text, "/") {
		bound, err := u.bindMentions(ctx, text)
		if err != nil {
			return "", false, err
		}
		id, err := resolveCommandID("")
		if err != nil {
			return "", false, err
		}
		_, err = u.call(ctx, "steer", agent.SteeringInput{CommandID: id, TaskID: u.task, Text: bound})
		return "Raw input recorded; scope revision is pending. No automatic resume.", false, err
	}
	command, arg, _ := strings.Cut(text, " ")
	arg = strings.TrimSpace(arg)
	switch command {
	case "/capacity":
		if arg != "" && arg != "replenish" {
			return "", false, c.Fail(c.InvalidArgument, "capacity accepts only optional replenish")
		}
		action := "privacy-capacity"
		if arg == "replenish" {
			action = "privacy-reserve-replenish"
		}
		raw, err := u.call(ctx, action, nil)
		return string(raw), false, err
	case "/quit", "/detach":
		return "UI detached; background task continues.", true, nil
	case "/help":
		return "/status /diff /plan /pause /resume /cancel /model ID /queue [add TEXT|remove ID|activate ID] /context /why /evidence /budget /capacity [replenish] /trace /requests /respond ID approve|reject /revise [FRESH_FIXTURE] /read PATH /quit\nPlain text records steering and pauses; queue add does not change scope. @file selects captured source. /resume starts a detached invocation. Capacity is an instant observation; replenish restores only bounded control reserve and never prunes metadata, owner identities or fences. Candidate-only delivery is available; live restore/apply needs its exclusive backend.", false, nil
	case "/pause", "/cancel":
		if arg != "" {
			return "", false, c.Fail(c.InvalidArgument, "control takes no extra arguments")
		}
		raw, err := u.call(ctx, command[1:], nil)
		return string(raw), false, err
	case "/resume":
		raw, err := u.call(ctx, "detach", nil)
		return string(raw), false, err
	case "/status":
		view, err := u.status(ctx)
		raw, _ := json.MarshalIndent(view, "", "  ")
		return string(raw), false, err
	case "/plan":
		view, err := u.status(ctx)
		raw, _ := json.MarshalIndent(view.Plan, "", "  ")
		return string(raw), false, err
	case "/diff", "/requests":
		raw, err := u.call(ctx, command[1:], nil)
		return string(raw), false, err
	case "/context", "/why", "/evidence", "/verification", "/runtime", "/budget", "/trace":
		kind := map[string]string{"/context": "continuity-info", "/why": "context-why", "/evidence": "checks", "/verification": "verification", "/runtime": "runtime-info", "/budget": "resources", "/trace": "events"}[command]
		var raw json.RawMessage
		var err error
		if kind == "events" {
			raw, err = u.call(ctx, kind, owner.Page{After: 0, Limit: 32})
		} else {
			raw, err = u.observe(ctx, kind)
		}
		return string(raw), false, err
	case "/model":
		if arg == "" {
			view, err := u.status(ctx)
			raw, _ := json.Marshal(view.Runtime)
			return string(raw), false, err
		}
		raw, err := u.observe(ctx, "continuity-info")
		if err != nil {
			return "", false, err
		}
		var info struct {
			SchemaVersion int                   `json:"schema_version"`
			State         c.TaskState           `json:"state"`
			Profile       string                `json:"profile_digest"`
			Compactions   []agent.CompactionRef `json:"compactions"`
			Pins          []agent.ContextPin    `json:"pins"`
			Runtime       *agent.Runtime        `json:"runtime,omitempty"`
		}
		if err = c.DecodeStrict(raw, &info); err != nil {
			return "", false, err
		}
		if info.Runtime == nil {
			return "", false, c.Fail(c.UnsupportedCapability, "fixture tasks have no switchable runtime")
		}
		id, err := resolveCommandID("")
		if err != nil {
			return "", false, err
		}
		next := *info.Runtime
		next.Model = arg
		result, err := u.call(ctx, "model-switch", agent.ModelSwitch{CommandID: id, TaskID: u.task, ExpectedTaskSeq: info.State.TaskSeq, ExpectedProfile: info.Profile, Runtime: next})
		return string(result), false, err
	case "/respond":
		parts := strings.Fields(arg)
		if len(parts) != 2 || parts[1] != "approve" && parts[1] != "reject" {
			return "", false, c.Fail(c.InvalidArgument, "respond requires request ID and approve/reject")
		}
		raw, err := u.call(ctx, "requests", nil)
		if err != nil {
			return "", false, err
		}
		var requests []agent.UserRequest
		if err = c.DecodeStrict(raw, &requests); err != nil {
			return "", false, err
		}
		for _, request := range requests {
			if request.ID != parts[0] {
				continue
			}
			if request.Status != "PENDING" {
				return "", false, c.Fail(c.StaleRequest, "request is no longer pending")
			}
			id, err := resolveCommandID("")
			if err != nil {
				return "", false, err
			}
			response := agent.UserResponse{CommandID: id, TaskID: u.task, RequestID: request.ID, Attempt: request.Attempt, ExpectedSpecVersion: request.SpecVersion, ExpectedPolicyDigest: request.PolicyDigest, ActionDigest: request.ActionDigest}
			response.Response.Decision = parts[1]
			result, err := u.call(ctx, "respond", response)
			return string(result), false, err
		}
		return "", false, c.Fail(c.StaleRequest, "request ID unavailable")
	case "/revise":
		view, err := u.status(ctx)
		if err != nil {
			return "", false, err
		}
		if !view.State.InputBarrier || len(view.State.PendingInputIDs) == 0 {
			return "", false, c.Fail(c.StaleRequest, "no pending scope input")
		}
		var fixture []byte
		if view.Runtime == nil {
			if arg == "" {
				return "", false, c.Fail(c.InvalidArgument, "offline revision requires an explicit fresh fixture file")
			}
			file, err := os.Open(arg)
			if err != nil {
				return "", false, c.Fail(c.InvalidArgument, "revision fixture unavailable")
			}
			fixture, err = io.ReadAll(io.LimitReader(file, (8<<20)+1))
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				return "", false, c.Fail(c.InvalidArgument, "revision fixture unavailable")
			}
			if _, err = agent.ParseFixture(fixture); err != nil {
				return "", false, c.Fail(c.InvalidArgument, "invalid bounded revision fixture")
			}
		} else if arg != "" {
			return "", false, c.Fail(c.InvalidArgument, "runtime revision cannot import a fixture")
		}
		id, err := resolveCommandID("")
		if err != nil {
			return "", false, err
		}
		result, err := u.call(ctx, "revise", agent.ScopeRevision{CommandID: id, TaskID: u.task, InputID: view.State.PendingInputIDs[0], ExpectedSpecVersion: view.State.SpecVersion, ExpectedPolicyEpoch: view.State.PolicyEpoch, ExpectedCandidate: view.State.CandidateDigest, Fixture: fixture})
		return string(result), false, err
	case "/queue":
		return u.queue(ctx, arg)
	case "/read":
		matches, err := u.call(ctx, "source-list", agent.Observation{Kind: "source-list", Query: arg, Limit: 64})
		if err != nil {
			return "", false, err
		}
		var page agent.SourceMatches
		if err = c.DecodeStrict(matches, &page); err != nil {
			return "", false, err
		}
		if page.Total != 1 || page.Entries[0].Path != arg {
			return "", false, c.Fail(c.InvalidArgument, "read requires exact unique candidate file path")
		}
		raw, err := u.call(ctx, "source-page", agent.Observation{Kind: "source-page", Candidate: page.Candidate, Path: arg, Limit: 16384})
		var data agent.SourcePage
		if err == nil {
			err = c.DecodeStrict(raw, &data)
		}
		return fmt.Sprintf("candidate %s | %s | digest %s | next %d | complete %t\n%s", data.Candidate, data.Entry.Path, data.Entry.Hash, data.Next, data.Complete, string(data.Bytes)), false, err
	default:
		return "", false, c.Fail(c.InvalidArgument, "unknown or unavailable UI command; use /help")
	}
}

func (u uiSession) queue(ctx context.Context, arg string) (string, bool, error) {
	if arg == "" {
		raw, err := u.call(ctx, "queue-list", nil)
		return string(raw), false, err
	}
	action, value, _ := strings.Cut(arg, " ")
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, c.Fail(c.InvalidArgument, "queue action needs text or ID")
	}
	id, err := resolveCommandID("")
	if err != nil {
		return "", false, err
	}
	command := agent.QueueCommand{CommandID: id, TaskID: u.task, Action: action}
	switch action {
	case "add":
		command.Text, err = u.bindMentions(ctx, value)
		if err != nil {
			return "", false, err
		}
	case "remove", "activate":
		command.QueueID = value
		if action == "activate" {
			raw, err := u.call(ctx, "queue-list", nil)
			if err != nil {
				return "", false, err
			}
			var prompts []agent.QueuedPrompt
			if err = c.DecodeStrict(raw, &prompts); err != nil {
				return "", false, err
			}
			for _, prompt := range prompts {
				if prompt.Ref.ID == value {
					command.Text = prompt.Text
				}
			}
			if command.Text == "" {
				return "", false, c.Fail(c.StaleRequest, "queued prompt unavailable")
			}
		}
	default:
		return "", false, c.Fail(c.InvalidArgument, "queue action must add/remove/activate")
	}
	raw, err := u.call(ctx, "queue-control", command)
	return "Queue command " + id + ": " + string(raw), false, err
}
