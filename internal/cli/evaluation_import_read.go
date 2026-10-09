package cli

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/evaluation"
	"io"
)

func runEvaluationReportInspect(args []string, out, errout io.Writer) int {
	f := flags("eval-report-inspect", errout)
	bundle := f.String("bundle", "", "existing imported report directory")
	jsonMode := f.Bool("json", false, "structured imported result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *bundle == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "one imported bundle required"), *jsonMode)
	}
	result, err := evaluation.ReadImportedReport(*bundle)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, result); err != nil {
		return 4
	}
	return 0
}
