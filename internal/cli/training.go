package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func runTrainingExport(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("training-export", errout)
	store := f.String("store", "", "existing private task store")
	output := f.String("output", "", "fresh local output directory")
	consentFile := f.String("consent", "", "explicit exact-content task-bound training consent JSON")
	samplesFile := f.String("samples", "", "operator-reviewed sample JSON; no automatic gold labels")
	jsonMode := f.Bool("json", false, "structured local export receipt")
	if f.Parse(args) != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task required"), *jsonMode)
	}
	if task == "" || *store == "" || *output == "" || *consentFile == "" || *samplesFile == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "task, store, output, consent and reviewed samples required"), *jsonMode)
	}
	read := func(name string, limit int64) ([]byte, error) {
		abs, e := filepath.Abs(name)
		if e != nil {
			return nil, e
		}
		root, e := os.OpenRoot(filepath.Dir(abs))
		if e != nil {
			return nil, e
		}
		defer root.Close()
		return fileguard.ReadRegular(root, filepath.Base(abs), limit)
	}
	raw, err := read(*consentFile, 64<<10)
	var consent agent.TrainingConsent
	if err == nil {
		err = c.DecodeStrict(raw, &consent)
	}
	if err != nil {
		return report(out, errout, c.Fail(c.InvalidArgument, "invalid bounded training consent; content omitted"), *jsonMode)
	}
	samples, err := read(*samplesFile, 1<<20)
	if err != nil {
		return report(out, errout, c.Fail(c.InvalidArgument, "reviewed training samples unavailable; content omitted"), *jsonMode)
	}
	session, err := agent.OpenExisting(context.Background(), *store)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer session.Close()
	manifest, err := session.ExportTraining(context.Background(), task, *output, consent, samples)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if jsonWrite(out, manifest) != nil {
		return 4
	}
	return 0
}
