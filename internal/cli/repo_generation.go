package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/intelligence"
	"github.com/ixayldz/Viber/internal/workspace"
)

func runRepoGeneration(command string, args []string, out, errout io.Writer) int {
	f := flags(command, errout)
	sourcePath := f.String("root", "", "captured blueprint source directory")
	git := f.Bool("git", false, "bind native Git HEAD/index capture")
	blueprintFile := f.String("blueprint", "", "reviewed blueprint specification/manifest JSON")
	blueprintDigest := f.String("blueprint-digest", "", "exact reviewed manifest digest for publication")
	filesFile := f.String("files", "", "exact file JSON array: path, mode, data_base64")
	output := f.String("output", "", "fresh generated repository directory outside source")
	checksFile := f.String("check-config", "", "optional separately protected bound check configuration")
	task := f.String("task", "", "explicit task identity for protected origin binding")
	inputDigest := f.String("input-sha", "", "exact task input digest for protected origin binding")
	jsonMode := f.Bool("json", false, "structured source-bound result")
	if f.Parse(args) != nil {
		return 4
	}
	if f.NArg() != 0 || *sourcePath == "" || *blueprintFile == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "source root and blueprint required"), *jsonMode)
	}
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
	var source workspace.Capture
	var err error
	if *git {
		source, err = workspace.CaptureRepository(*sourcePath, workspace.DefaultLimits())
	} else {
		source, err = workspace.CaptureDirectory(*sourcePath, workspace.DefaultLimits())
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if command == "repo-blueprint" {
		if *output != "" || *filesFile != "" || *checksFile != "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "repo-blueprint emits a manifest; publication requires repo-generate"), *jsonMode)
		}
		var spec struct {
			Decisions    []intelligence.BlueprintDecision   `json:"architecture_decisions"`
			Dependencies []intelligence.BlueprintDependency `json:"dependencies"`
			Paths        []string                           `json:"planned_generated_paths"`
			Checks       []string                           `json:"required_independent_checks"`
		}
		if err = read(*blueprintFile, 1<<20, &spec); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		manifest, e := intelligence.BuildBlueprint(source, spec.Decisions, spec.Dependencies, spec.Paths, spec.Checks)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		if jsonWrite(out, manifest) != nil {
			return 4
		}
		return 0
	}
	if *output == "" || *filesFile == "" || !c.ValidDigest(*blueprintDigest) {
		return report(out, errout, c.Fail(c.InvalidArgument, "fresh output, exact generated files and reviewed blueprint digest required"), *jsonMode)
	}
	var blueprint intelligence.BlueprintManifest
	var files []intelligence.GenerationFile
	if err = read(*blueprintFile, 1<<20, &blueprint); err == nil {
		err = read(*filesFile, 48<<20, &files)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	plan, err := intelligence.BuildGenerationPlan(source, blueprint, *blueprintDigest, files)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	// Validate protected check IDs before publication. Source closure is bound
	// against the published candidate below. No check is executed
	// by this publisher; the normal task/evaluator runner remains the authority.
	var checks *operatorChecks
	if *checksFile != "" {
		absolute, e := filepath.Abs(*checksFile)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		target, e := fileguard.ResolveProspective(*output)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		if !fileguard.Disjoint(absolute, source.Snapshot.Root) || !fileguard.Disjoint(absolute, target) || *task == "" || !c.ValidDigest(*inputDigest) {
			return report(out, errout, c.Fail(c.PolicyDenied, "protected recipe must be outside source/output and bind exact task/input"), *jsonMode)
		}
		configured, e := readOperatorChecks(absolute)
		if e != nil {
			return report(out, errout, e, *jsonMode)
		}
		checks = &configured
		if e = intelligence.ValidateGenerationCheckPlan(blueprint, checks.Plan); e != nil {
			return report(out, errout, e, *jsonMode)
		}
	}
	receipt, err := intelligence.PublishGeneration(source, blueprint, plan, *output)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var validation *intelligence.GenerationValidationBinding
	var validationError *c.Error
	if checks != nil {
		candidate, e := workspace.CaptureDirectory(receipt.OutputDirectory, workspace.DefaultLimits())
		if e == nil {
			var bound intelligence.GenerationValidationBinding
			bound, e = intelligence.BindGenerationValidation(source, blueprint, receipt, candidate, *task, *inputDigest, checks.Plan, checks.SourceDiscovery)
			if e == nil {
				validation = &bound
			}
		}
		if e != nil && !errors.As(e, &validationError) {
			validationError = &c.Error{Code: c.StaleAuthority, Message: "repository published; protected validation binding unavailable"}
		}
	}
	result := struct {
		Receipt         intelligence.GenerationReceipt            `json:"receipt"`
		Validation      *intelligence.GenerationValidationBinding `json:"validation,omitempty"`
		ValidationError *c.Error                                  `json:"validation_error,omitempty"`
	}{receipt, validation, validationError}
	if jsonWrite(out, result) != nil {
		return 4
	}
	if validationError != nil {
		// The publication receipt must remain visible even when the later
		// validation binding fails. Exit 4 must never imply no output was created.
		return 4
	}
	return 0
}
