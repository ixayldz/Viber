package cli

import (
	"context"
	"io"
	"path/filepath"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/evaluation"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func runEvaluationRegister(args []string, out, errout io.Writer) int {
	f := flags("eval-register", errout)
	store := f.String("store", "", "existing task store whose privacy authority owns the registration")
	protocol := f.String("protocol", "", "frozen VIBER_PAIRED_EVAL_V1 protocol JSON")
	dataset := f.String("dataset", "", "dataset root containing the declared assets and dataset manifest")
	assets := f.String("assets", "", "asset-plan JSON binding protocol digests to dataset paths")
	output := f.String("output", "", "fresh evaluation registration directory")
	jsonMode := f.Bool("json", false, "structured administrative result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *store == "" || *protocol == "" || *dataset == "" || *assets == "" || *output == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "store, protocol, dataset, assets and fresh output required"), *jsonMode)
	}
	ctx := context.Background()
	session, err := agent.OpenExisting(ctx, *store)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	storeDirectory, err := filepath.Abs(*store)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if session.PhysicalQuota == nil || !fileguard.Disjoint(*output, storeDirectory) || !fileguard.Disjoint(*output, session.PhysicalQuota.Authority.Directory) || !fileguard.Disjoint(*dataset, storeDirectory) || !fileguard.Disjoint(*dataset, session.PhysicalQuota.Authority.Directory) {
		return report(out, errout, c.Fail(c.PolicyDenied, "registration dataset and output must stay outside the owner store and privacy authority"), *jsonMode)
	}
	manifest, err := evaluation.RegisterDatasetWithQuota(ctx, *protocol, *dataset, *assets, *output, session.PhysicalQuota)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, manifest); err != nil {
		return 4
	}
	return 0
}

func runEvaluationRegistrationInspect(args []string, out, errout io.Writer) int {
	f := flags("eval-registration-inspect", errout)
	registration := f.String("registration", "", "existing frozen evaluation registration directory")
	jsonMode := f.Bool("json", false, "structured registration status")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *registration == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "one evaluation registration directory required"), *jsonMode)
	}
	status, err := evaluation.InspectRegistration(*registration)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, status); err != nil {
		return 4
	}
	return 0
}
