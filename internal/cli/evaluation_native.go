package cli

import (
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/evaluation"
	"io"
)

func runEvaluationCandidate(command string, args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags(command, errout)
	store := f.String("store", "", "existing original private task store")
	recipe := f.String("recipe", "", "bound hidden operator STDIO recipe outside original root/store")
	output := f.String("output", "", "fresh independent evaluator directory")
	bundle := f.String("bundle", "", "existing native independent evaluator directory")
	jsonMode := f.Bool("json", false, "structured independent result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one original task required"), *jsonMode)
	}
	var result evaluation.NativeResult
	var err error
	if command == "eval-inspect" {
		if task != "" || *bundle == "" || *store != "" || *recipe != "" || *output != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "eval-inspect requires only bundle"), *jsonMode)
		}
		result, err = evaluation.ReadNativeResult(context.Background(), *bundle)
	} else {
		if task == "" || *store == "" || *recipe == "" || *output == "" || *bundle != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "task/store/hidden recipe/fresh output required"), *jsonMode)
		}
		original, openErr := agent.OpenExisting(context.Background(), *store)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer original.Close()
		result, err = evaluation.RunCandidate(context.Background(), original, task, *recipe, *output)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, result); err != nil {
		return 4
	}
	// Exit zero means the limited independent computation passed; it never
	// upgrades the original task, study adoption or release readiness.
	if result.IndependentVerdict == c.Pass {
		return 0
	}
	if result.IndependentVerdict == c.FailVerdict {
		return 1
	}
	return 2
}
