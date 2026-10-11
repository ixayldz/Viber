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
	registration := f.String("registration", "", "frozen evaluation registration directory")
	arm := f.String("arm", "", "predeclared arm ID for registered evaluation")
	repeat := f.Int("repeat", 0, "predeclared repeat number for registered evaluation")
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
		if task != "" || *bundle == "" || *store != "" || *recipe != "" || *output != "" || *arm != "" || *repeat != 0 {
			return report(out, errout, c.Fail(c.InvalidArgument, "eval-inspect requires bundle and optionally registration"), *jsonMode)
		}
		if *registration == "" {
			result, err = evaluation.ReadNativeResult(context.Background(), *bundle)
		} else {
			result, err = evaluation.ReadRegisteredNativeResult(context.Background(), *bundle, *registration)
		}
	} else {
		if task == "" || *store == "" || *output == "" || *bundle != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "task/store/fresh output required"), *jsonMode)
		}
		if *registration == "" && (*recipe == "" || *arm != "" || *repeat != 0) || *registration != "" && (*recipe != "" || *arm == "" || *repeat < 1 || *repeat > 10) {
			return report(out, errout, c.Fail(c.InvalidArgument, "choose either a post-hoc recipe or a registered arm and repeat"), *jsonMode)
		}
		original, openErr := agent.OpenExisting(context.Background(), *store)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer original.Close()
		if *registration == "" {
			result, err = evaluation.RunCandidate(context.Background(), original, task, *recipe, *output)
		} else {
			result, err = evaluation.RunRegisteredCandidate(context.Background(), original, task, *registration, *arm, *repeat, *output)
		}
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
