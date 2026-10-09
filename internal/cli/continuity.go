package cli

import (
	"context"
	"encoding/json"
	"io"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runContinuityEdit(action string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(action, errout)
	directory := f.String("store", "", "private existing task store")
	input := f.String("command-file", "", "source-bound JSON command; stable command ID and expected task sequence")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task required"), *jsonMode)
	}
	if task == "" || *directory == "" || *input == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task, store and bound command-file required; inspect continuity-info first"), *jsonMode)
	}
	var edit agent.ContinuityCommand
	var change agent.ModelSwitch
	var risk agent.ModelRiskCommand
	var payload any
	var id string
	if action == "reconcile-model-risk" {
		if err := readJSON(*input, &risk); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if risk.TaskID != task {
			return report(out, errout, c.Fail(c.InvalidArgument, "risk command task mismatch"), *jsonMode)
		}
		payload = risk
		id = risk.CommandID
	} else if action == "model-switch" {
		if err := readJSON(*input, &change); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if change.TaskID != task {
			return report(out, errout, c.Fail(c.InvalidArgument, "switch command task mismatch"), *jsonMode)
		}
		payload = change
		id = change.CommandID
	} else {
		if err := readJSON(*input, &edit); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if edit.TaskID != task {
			return report(out, errout, c.Fail(c.InvalidArgument, "context command task mismatch"), *jsonMode)
		}
		payload = edit
		id = edit.CommandID
	}
	raw, routed, err := ownerCall(context.Background(), *directory, task, action, id, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if routed {
		if err := jsonWrite(out, json.RawMessage(raw)); err != nil {
			return 4
		}
		return 0
	}
	s, err := agent.OpenExisting(context.Background(), *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer s.Close()
	var state c.TaskState
	if action == "reconcile-model-risk" {
		state, err = s.ReconcileModelRisk(context.Background(), risk)
	} else if action == "model-switch" {
		state, err = s.SwitchModel(context.Background(), change)
	} else {
		state, err = s.Continuity(context.Background(), edit)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err := jsonWrite(out, state); err != nil {
		return 4
	}
	return 0
}
