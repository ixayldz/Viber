package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"io"
	"os"
	"os/signal"
)

const retentionConsentNotice = "Expiry can remove entire shared backups containing this task; other tasks may lose those restore paths. It uses the system UTC clock, so advancing that clock can trigger expiry early. Live source files are preserved."

func runRetention(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("retention", errout)
	directory := f.String("store", "", "existing private store")
	keep := f.Bool("keep", false, "disable automatic expiry; existing deletion intent remains immutable")
	deadline := f.String("expires-at", "", "canonical UTC managed family content deadline")
	ack := f.String("acknowledge", "", retentionConsentNotice+" Explicit consent: "+c.RetentionAcknowledgement)
	revision := f.Int64("revision", -1, "expected current retention revision; 0 for first policy")
	id := f.String("command-id", "", "stable configuration command ID")
	run := f.Bool("run-due", false, "bounded store-scoped expiry pass; does not cancel active work")
	jsonMode := f.Bool("json", false, "structured policy/status/maintenance result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "at most one task ID required"), *jsonMode)
	}
	setting := *keep || *deadline != ""
	if *directory == "" || *keep && *deadline != "" || *run && (task != "" || setting || *ack != "" || *revision != -1 || *id != "") || !*run && task == "" || !setting && (*ack != "" || *revision != -1 || *id != "") || setting && (*revision < 0 || *id == "") || *keep && *ack != "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "retention status needs task/store; configure needs mode, revision and stable command ID; run-due takes only store"), *jsonMode)
	}
	options := agent.RetentionOptions{CommandID: *id, ExpectedRevision: *revision, Mode: "KEEP", Deadline: *deadline, Acknowledgement: *ack}
	if *deadline != "" {
		options.Mode = "EXPIRE"
		if _, err := c.RetentionTime(*deadline); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if *ack != c.RetentionAcknowledgement {
			return report(out, errout, c.Fail(c.PolicyDenied, retentionConsentNotice+" Explicit managed-content expiry consent required."), *jsonMode)
		}
		// Preserve the JSON result protocol; disclosure goes to the human channel
		// before any owner dispatch or local configuration effect.
		if _, err := fmt.Fprintln(errout, retentionConsentNotice); err != nil {
			return 4
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	action := "retention-status"
	var payload any
	if *run {
		action = "retention-run"
	} else if setting {
		action = "retention-set"
		payload = options
	}
	requestID, err := resolveCommandID(*id)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	raw, routed, err := ownerCall(ctx, *directory, task, action, requestID, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result any
	if routed {
		result = json.RawMessage(raw)
	} else {
		s, openErr := agent.OpenExisting(ctx, *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer s.Close()
		switch action {
		case "retention-set":
			result, err = s.SetRetention(ctx, task, options)
		case "retention-run":
			result, err = s.RunRetention(ctx)
		default:
			result, err = s.RetentionStatus(ctx, task)
		}
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if jsonWrite(out, result) != nil {
			return 4
		}
		return 0
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return 4
	}
	switch action {
	case "retention-set":
		var policy c.RetentionPolicy
		if c.DecodeStrict(encoded, &policy) != nil {
			return 4
		}
		_, err = fmt.Fprintf(out, "Retention %s · revision %d · %s · deadline %s\n", policy.TaskID, policy.Revision, policy.Mode, policy.Deadline)
	case "retention-run":
		var run agent.RetentionRun
		if c.DecodeStrict(encoded, &run) != nil {
			return 4
		}
		_, err = fmt.Fprintf(out, "Retention maintenance: %d/%d attempts · more %t · cursor %s · %s\n", len(run.Items), run.Limit, run.HasMore, run.CursorPersistence, run.CursorError)
		for _, item := range run.Items {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(out, "%s · %s · %s\n", item.TaskID, item.Status, item.Reason)
		}
	default:
		var status agent.RetentionStatus
		if c.DecodeStrict(encoded, &status) != nil {
			return 4
		}
		revision := int64(0)
		if status.Policy != nil {
			revision = status.Policy.Revision
		}
		_, err = fmt.Fprintf(out, "Retention %s · revision %d · %s\n", status.TaskID, revision, status.Status)
	}
	if err != nil {
		return 4
	}
	return 0
}
