package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/learning"
)

func runRanking(args []string, out, errout io.Writer) int {
	action, args := promptFirst(args)
	f := flags("rank-policy", errout)
	directory := f.String("registry", "", "operator private registry outside repositories/stores")
	definitionFile := f.String("definition", "", "reviewed data-only ranking definition JSON")
	datasetFile := f.String("dataset", "", "reviewed frozen DEV or independent HOLDOUT source/label JSON")
	id := f.String("id", "", "data-only trained policy identity")
	version := f.Int("version", 1, "trained policy version")
	license := f.String("license", "", "explicit dataset/output usage license provenance")
	datasetDigest := f.String("dataset-sha", "", "exact canonical reviewed holdout digest")
	definitionDigest := f.String("definition-sha", "", "exact evaluated definition digest")
	evaluationDigest := f.String("evaluation-sha", "", "exact actual evaluation receipt digest")
	mode := f.String("mode", "SHADOW", "SHADOW, CANARY or ACTIVE; ordered explicit promotion")
	approval := f.String("approve", "", "explicit advisory-only heldout review acknowledgement")
	jsonMode := f.Bool("json", false, "structured bounded output")
	if f.Parse(args) != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" || action != "init" && action != "status" && action != "train" && action != "evaluate" && action != "select" && action != "disable" {
		return report(out, errout, c.Fail(c.InvalidArgument, "rank-policy init/status/train/evaluate/select/disable and registry required"), *jsonMode)
	}
	registry, err := learning.Open(*directory, action == "init")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer registry.Close()
	var result any
	switch action {
	case "init", "status":
		result, err = registry.Status()
	case "disable":
		result, err = registry.Disable()
	case "select":
		result, err = registry.Select(*definitionDigest, *evaluationDigest, *mode, *approval)
	case "train", "evaluate":
		read := func(name string, limit int64, dst any) error {
			absolute, e := filepath.Abs(name)
			if e != nil {
				return e
			}
			root, e := os.OpenRoot(filepath.Dir(absolute))
			if e != nil {
				return e
			}
			defer root.Close()
			raw, e := fileguard.ReadRegular(root, filepath.Base(absolute), limit)
			if e != nil {
				return e
			}
			return c.DecodeStrict(raw, dst)
		}
		var definition learning.Definition
		var dataset learning.Dataset
		if action == "evaluate" {
			err = read(*definitionFile, 16<<10, &definition)
		}
		if err == nil {
			err = read(*datasetFile, 8<<20, &dataset)
		}
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if action == "train" {
				result, err = registry.Train(ctx, *id, *version, *license, dataset, *datasetDigest)
				break
			}
			var evaluation learning.Evaluation
			evaluation, err = registry.Evaluate(ctx, definition, dataset, *datasetDigest)
			digest, _ := c.Digest(evaluation)
			result = struct {
				Receipt learning.Evaluation `json:"receipt"`
				Digest  string              `json:"evaluation_digest"`
			}{evaluation, digest}
		}
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if jsonWrite(out, result) != nil {
		return 4
	}
	return 0
}
