package cli

import (
	"io"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/evaluation"
)

func runEvaluationReport(args []string, out, errout io.Writer) int {
	f := flags("eval-report", errout)
	protocol := f.String("protocol", "", "preregistered fixed assignment JSON")
	observations := f.String("observations", "", "bounded imported measurement JSONL")
	output := f.String("output", "", "fresh private report bundle directory")
	jsonMode := f.Bool("json", false, "structured administrative result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *protocol == "" || *observations == "" || *output == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "protocol, observations and fresh output required"), *jsonMode)
	}
	input, err := evaluation.ReadImportedInputs(*protocol, *observations)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	manifest, err := evaluation.PublishImportedReport(input, *output)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, manifest); err != nil {
		return 4
	}
	return 0
}
