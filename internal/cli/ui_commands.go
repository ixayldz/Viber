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
	"github.com/ixayldz/Viber/internal/tui"
	"regexp"
	"strconv"
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
	case agent.RetentionOptions:
		id = value.CommandID
	case agent.GoalCommand:
		id = value.CommandID
	case agent.LLMReadWorkerCommand:
		id = value.CommandID
	case agent.ReadWorkersRequest:
		id = value.OperationID
	case agent.ContinuityCommand:
		id = value.CommandID
	case projectMemoryMutationRequest:
		id = value.CommandID
	case projectMemoryCopyRequest:
		id = value.CommandID
	}
	task := u.task
	if action == "retention-run" || action == "privacy-capacity" || action == "privacy-reserve-replenish" || action == "privacy-operation-retire" {
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
		if err != nil {
			return "", false, err
		}
		return "Raw input recorded; scope revision is pending. No automatic resume.", false, nil
	}
	command, arg, _ := strings.Cut(text, " ")
	arg = strings.TrimSpace(arg)
	switch command {
	case "/clear":
		if arg != "" {
			return "", false, c.Fail(c.InvalidArgument, "/clear takes no arguments")
		}
		return "Displayed output cleared; task history and source files are retained.", false, nil
	case "/compact":
		keep := 4
		if arg != "" {
			var err error
			keep, err = strconv.Atoi(arg)
			if err != nil || keep < 2 || keep > 64 {
				return "", false, c.Fail(c.InvalidArgument, "/compact [KEEP_RECENT] requires 2..64")
			}
		}
		view, err := u.status(ctx)
		if err != nil {
			return "", false, err
		}
		id, err := resolveCommandID("")
		if err != nil {
			return "", false, err
		}
		raw, err := u.call(ctx, "context-edit", agent.ContinuityCommand{CommandID: id, TaskID: u.task, ExpectedTaskSeq: view.State.TaskSeq, Action: "compact", Keep: keep})
		return string(raw), false, err
	case "/memory":
		return u.memoryCommand(ctx, arg)
	case "/explore", "/review", "/worker":
		return u.llmReadWorkerCommand(ctx, command, arg)
	case "/workers":
		view, err := u.status(ctx)
		if err != nil {
			return "", false, err
		}
		kinds, query, _ := strings.Cut(arg, " ")
		if kinds == "" {
			kinds = "inventory,requirements"
		}
		id, err := resolveCommandID("")
		if err != nil {
			return "", false, err
		}
		workers := []agent.ReadWorkerKind{}
		for _, kind := range strings.Split(kinds, ",") {
			workers = append(workers, agent.ReadWorkerKind(strings.TrimSpace(kind)))
		}
		raw, err := u.call(ctx, "read-workers", agent.ReadWorkersRequest{OperationID: id, CandidateDigest: view.State.CandidateDigest, ExpectedDocumentDigest: view.State.DocumentDigest, Workers: workers, Query: query, TimeoutMillis: 2000})
		if err != nil {
			return "", false, err
		}
		var result agent.ReadWorkersResult
		if err = c.DecodeStrict(raw, &result); err != nil {
			return "", false, err
		}
		var summary strings.Builder
		fmt.Fprintf(&summary, "Local read-only analysis: %s\nParent charge: %d steps, %d calls, %d ms ceiling\n", result.Status, result.Charge.Admission.ChargedSteps, result.Charge.Admission.ChargedToolCalls, result.Charge.Admission.ChargedActiveMillis)
		if result.Failure != nil {
			fmt.Fprintf(&summary, "Analysis stopped: %s\nThe charge remains in the task journal; inspect /budget and /status before retrying.", result.Failure.Message)
			return summary.String(), false, result.Failure
		}
		for _, worker := range result.Workers {
			switch {
			case worker.Inventory != nil:
				fmt.Fprintf(&summary, "Inventory: %d files, %d bytes\n", worker.Inventory.Files, worker.Inventory.TotalBytes)
			case worker.GoGraph != nil:
				fmt.Fprintf(&summary, "Go graph: %d parsed files, %d nodes, %d edges; %s\n", worker.GoGraph.ParsedFiles, worker.GoGraph.Nodes, worker.GoGraph.Edges, worker.GoGraph.Coverage)
			case worker.Requirements != nil:
				fmt.Fprintf(&summary, "Requirements: %d total, %d required\n", worker.Requirements.Total, worker.Requirements.Required)
			case worker.Search != nil:
				fmt.Fprintf(&summary, "Search: %d displayed hits\n", len(worker.Search.Hits))
				for _, hit := range worker.Search.Hits[:min(5, len(worker.Search.Hits))] {
					fmt.Fprintf(&summary, "  %s:%d\n", hit.Path, hit.Line)
				}
			}
		}
		summary.WriteString("Source hints are advisory; required checks remain in force.")
		return summary.String(), false, nil
	case "/retention":
		if arg == "" {
			raw, err := u.call(ctx, "retention-status", nil)
			return string(raw), false, err
		}
		if arg == "run-due" {
			raw, err := u.call(ctx, "retention-run", nil)
			return string(raw), false, err
		}
		parts := strings.Fields(arg)
		if len(parts) != 2 && len(parts) != 4 {
			return "", false, c.Fail(c.InvalidArgument, "retention accepts keep REVISION or expires UTC REVISION CONSENT or run-due")
		}
		options := agent.RetentionOptions{Mode: "KEEP"}
		revisionIndex := 1
		if len(parts) == 4 && parts[0] == "expires" {
			options.Mode = "EXPIRE"
			options.Deadline = parts[1]
			options.Acknowledgement = parts[3]
			revisionIndex = 2
		} else if len(parts) != 2 || parts[0] != "keep" {
			return "", false, c.Fail(c.InvalidArgument, "invalid retention action")
		}
		revision, err := strconv.ParseInt(parts[revisionIndex], 10, 64)
		if err != nil || revision < 0 {
			return "", false, c.Fail(c.InvalidArgument, "current retention revision required")
		}
		options.ExpectedRevision = revision
		if options.Mode == "EXPIRE" && options.Acknowledgement != c.RetentionAcknowledgement {
			return "", false, c.Fail(c.PolicyDenied, retentionConsentNotice+" Explicit managed-content expiry consent required.")
		}
		options.CommandID, err = resolveCommandID("")
		if err != nil {
			return "", false, err
		}
		raw, err := u.call(ctx, "retention-set", options)
		if options.Mode == "EXPIRE" && err == nil {
			return retentionConsentNotice + "\n" + string(raw), false, nil
		}
		return string(raw), false, err
	case "/capacity":
		if arg != "" && arg != "replenish" && arg != "retire-operations" {
			return "", false, c.Fail(c.InvalidArgument, "capacity accepts optional replenish or retire-operations")
		}
		action := "privacy-capacity"
		if arg == "replenish" {
			action = "privacy-reserve-replenish"
		}
		if arg == "retire-operations" {
			action = "privacy-operation-retire"
		}
		raw, err := u.call(ctx, action, nil)
		return string(raw), false, err
	case "/quit", "/detach", "/exit":
		return "UI detached; background task continues.", true, nil
	case "/", "/help":
		return "Start here: /plan → /requests → /approve → /resume → /diff → /export\nReview each proposal before approving. /approve needs an ID if several proposals are pending.\n\n" + tui.CommandHelp() + "\nPlain text records steering and pauses; /revise applies that scope change. @file selects captured source. /resume starts background work. /quit detaches without cancellation. Retention defaults to KEEP; expiry requires explicit deletion consent and the current revision. Backups shared with other tasks can also be removed by consented expiry. Cancel does not undo source files.", false, nil
	case "/pause", "/cancel":
		if arg != "" {
			return "", false, c.Fail(c.InvalidArgument, "control takes no extra arguments")
		}
		raw, err := u.call(ctx, command[1:], nil)
		if err != nil {
			return "", false, err
		}
		var view owner.View
		if err = c.DecodeStrict(raw, &view); err != nil {
			return "", false, err
		}
		return renderTaskStatus(view), false, nil
	case "/resume":
		if arg != "" {
			return "", false, c.Fail(c.InvalidArgument, "/resume takes no arguments")
		}
		raw, err := u.call(ctx, "detach", nil)
		var view owner.View
		if err == nil {
			err = c.DecodeStrict(raw, &view)
		}
		return "Work admitted in the background.\n" + renderTaskStatus(view), false, err
	case "/status":
		view, err := u.status(ctx)
		if err != nil {
			return "", false, err
		}
		return u.renderStatus(ctx, view), false, nil
	case "/plan":
		if arg != "" {
			return "", false, c.Fail(c.InvalidArgument, "/plan shows the current plan; start a PRD task with run --prd FILE")
		}
		view, err := u.status(ctx)
		return renderWorkPlan(view), false, err
	case "/export", "/preview":
		if arg == "" {
			var err error
			arg, err = u.deliveryOutput(command[1:])
			if err != nil {
				return "", false, err
			}
		}
		if strings.HasPrefix(arg, "\"") {
			unquoted, err := strconv.Unquote(arg)
			if err != nil {
				return "", false, c.Fail(c.InvalidArgument, "invalid quoted output directory")
			}
			arg = unquoted
		}
		action := "delivery-export"
		if command == "/preview" {
			action = "delivery-preview"
		}
		raw, err := u.call(ctx, action, struct {
			Output string `json:"output"`
		}{arg})
		if err != nil {
			return "", false, err
		}
		if command == "/preview" {
			var result agent.DeliveryPreviewManifest
			if err = c.DecodeStrict(raw, &result); err != nil {
				return "", false, err
			}
			return fmt.Sprintf("Merge preview: %s\nSaved to: %s\nInspect preview files before applying changes.", result.Preview.Status, arg), false, nil
		}
		var result agent.ExportManifest
		if err = c.DecodeStrict(raw, &result); err != nil {
			return "", false, err
		}
		return fmt.Sprintf("Exported %d changes to %s\nchanges.patch, manifest.json and exact before/after files are ready.\nQuality: %s. Original project files are unchanged.", len(result.Changeset.Changes), arg, result.State.Quality), false, nil
	case "/diff":
		if arg != "" {
			return "", false, c.Fail(c.InvalidArgument, "/diff takes no arguments; use /export for the complete patch")
		}
		raw, err := u.call(ctx, "diff-text", nil)
		var result owner.DiffText
		if err == nil {
			err = c.DecodeStrict(raw, &result)
		}
		return renderDiff(result), false, err
	case "/requests":
		if arg != "" && arg != "json" {
			return "", false, c.Fail(c.InvalidArgument, "/requests accepts optional json")
		}
		raw, err := u.call(ctx, "requests", nil)
		if err != nil || arg == "json" {
			return string(raw), false, err
		}
		var requests []agent.UserRequest
		if err = c.DecodeStrict(raw, &requests); err != nil {
			return "", false, err
		}
		view, statusErr := u.status(ctx)
		if statusErr != nil {
			return renderRequests(requests) + "\nCurrent task status is unavailable; do not treat this proposal as verified.", false, nil
		}
		return renderRequests(requests, view), false, nil
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
	case "/approve", "/reject":
		if strings.ContainsAny(arg, " \t\r\n") {
			return "", false, c.Fail(c.InvalidArgument, command+" accepts one optional request ID")
		}
		return u.respond(ctx, arg, command[1:])
	case "/respond":
		parts := strings.Fields(arg)
		if len(parts) != 2 || parts[1] != "approve" && parts[1] != "reject" {
			return "", false, c.Fail(c.InvalidArgument, "respond requires request ID and approve/reject")
		}
		return u.respond(ctx, parts[0], parts[1])
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
	case "/goal":
		return u.goal(ctx, arg)
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

func (u uiSession) goal(ctx context.Context, arg string) (string, bool, error) {
	if arg == "" {
		raw, err := u.call(ctx, "goal-list", nil)
		return string(raw), false, err
	}
	action, value, _ := strings.Cut(arg, " ")
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, c.Fail(c.InvalidArgument, "goal action needs a child ID, goal ID, or text")
	}
	id, err := resolveCommandID("")
	if err != nil {
		return "", false, err
	}
	command := agent.GoalCommand{CommandID: id, TaskID: u.task, Action: action}
	switch action {
	case "add":
		child, text, found := strings.Cut(value, " ")
		text = strings.TrimSpace(text)
		if !found || child == "" || text == "" {
			return "", false, c.Fail(c.InvalidArgument, "goal add syntax is /goal add CHILD_ID TEXT")
		}
		command.ChildTaskID = child
		command.Text, err = u.bindMentions(ctx, text)
		if err != nil {
			return "", false, err
		}
	case "cancel", "activate", "recover":
		if strings.ContainsAny(value, " \t\r\n") {
			return "", false, c.Fail(c.InvalidArgument, "goal action accepts one goal ID")
		}
		command.GoalID = value
	default:
		return "", false, c.Fail(c.InvalidArgument, "goal action must add/cancel/activate/recover")
	}
	raw, err := u.call(ctx, "goal-control", command)
	return "Goal command " + id + ": " + string(raw), false, err
}
