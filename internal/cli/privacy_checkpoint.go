package cli

import (
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"io"
)

func runPrivacyCheckpoint(args []string, out, errout io.Writer) int {
	f := flags("privacy-checkpoint", errout)
	directory := f.String("store", "", "existing store; stop all family owners before checkpoint maintenance")
	jsonMode := f.Bool("json", false, "bounded numeric checkpoint status")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if *directory == "" || f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one existing private store required"), *jsonMode)
	}
	ctx := context.Background()
	session, err := agent.OpenExisting(ctx, *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	status, err := session.CheckpointPrivacyMetadata(ctx)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, status); err != nil {
		return 4
	}
	return 0
}
