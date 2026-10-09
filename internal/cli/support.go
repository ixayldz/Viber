package cli

import (
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"io"
)

type supportExport struct {
	Output string `json:"output"`
}

func runSupport(args []string, out, errout io.Writer) int {
	f := flags("support", errout)
	directory := f.String("store", "", "existing private store")
	output := f.String("output", "", "fresh private support directory")
	jsonMode := f.Bool("json", false, "numeric allowlist report")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "support requires an existing store"), *jsonMode)
	}
	action := "support"
	var payload any
	if *output != "" {
		action = "support-export"
		payload = supportExport{*output}
	}
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	ctx := context.Background()
	raw, routed, err := ownerCall(ctx, *directory, "", action, id, payload)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if routed {
		if err = jsonWrite(out, json.RawMessage(raw)); err != nil {
			return 4
		}
		return 0
	}
	s, err := agent.OpenExisting(ctx, *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer s.Close()
	var result agent.SupportReport
	if *output != "" {
		result, err = s.ExportSupport(ctx, *output)
	} else {
		result, err = s.Support(ctx)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, result); err != nil {
		return 4
	}
	return 0
}
