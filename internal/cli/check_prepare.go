package cli

import (
	"flag"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/verify"
	"io"
	"os"
	"path/filepath"
)

type operatorChecks struct {
	SchemaVersion int                `json:"schema_version"`
	Plan          verify.CheckPlan   `json:"plan"`
	Runtime       agent.CheckRuntime `json:"runtime"`
}

func bindOperatorChecks(input operatorChecks) (operatorChecks, error) {
	if input.SchemaVersion != 1 || input.Runtime.SchemaVersion != 1 || input.Runtime.Profile.Validate() != nil || input.Plan.SchemaVersion != 1 || len(input.Plan.Checks) == 0 || len(input.Plan.Checks) > 128 || len(input.Runtime.ObserverSuites) > 128 {
		return input, c.Fail(c.InvalidArgument, "bounded pinned operator checks required")
	}
	profile, _ := c.Digest(input.Runtime.Profile)
	seen := map[string]bool{}
	for i := range input.Plan.Checks {
		check := &input.Plan.Checks[i]
		if check.RunnerDigest != "" && check.RunnerDigest != profile {
			return input, c.Fail(c.PolicyDenied, "existing runner binding differs")
		}
		check.RunnerDigest = profile
		found := false
		for _, suite := range input.Runtime.ObserverSuites {
			if suite.CheckID != check.ID {
				continue
			}
			if found || seen[suite.CheckID] {
				return input, c.Fail(c.InvalidArgument, "duplicate oracle")
			}
			seen[suite.CheckID] = true
			found = true
			digest, _ := c.Digest(suite)
			if check.ObserverDigest != "" && check.ObserverDigest != digest {
				return input, c.Fail(c.PolicyDenied, "existing oracle binding differs")
			}
			check.ObserverDigest = digest
			if err := suite.Validate(*check, input.Runtime.Profile); err != nil {
				return input, err
			}
		}
		if !found && check.ObserverDigest != "" {
			return input, c.Fail(c.PolicyDenied, "protected oracle unavailable")
		}
	}
	if len(seen) != len(input.Runtime.ObserverSuites) {
		return input, c.Fail(c.PolicyDenied, "unregistered oracle")
	}
	return input, input.Plan.Validate()
}
func readOperatorChecks(path string) (operatorChecks, error) {
	var input operatorChecks
	absolute, err := fileguard.ResolveProspective(path)
	if err != nil {
		return input, c.Fail(c.InvalidArgument, "operator check file unavailable")
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return input, c.Fail(c.InvalidArgument, "operator check directory unavailable")
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(absolute), 1<<20)
	if err != nil || c.DecodeStrict(raw, &input) != nil {
		return input, c.Fail(c.InvalidArgument, "invalid bounded operator check configuration")
	}
	return bindOperatorChecks(input)
}
func runCheckPrepare(args []string, out, errout io.Writer) int {
	f := flag.NewFlagSet("check-prepare", flag.ContinueOnError)
	f.SetOutput(errout)
	inputFile := f.String("file", "", "explicit operator check recipe JSON")
	outputFile := f.String("output", "", "fresh private bound configuration file")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *inputFile == "" || *outputFile == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "file and fresh output required"), true)
	}
	input, err := readOperatorChecks(*inputFile)
	if err != nil {
		return report(out, errout, err, true)
	}
	absolute, err := fileguard.ResolveProspective(*outputFile)
	if err != nil {
		return report(out, errout, c.Fail(c.InvalidArgument, "output path unavailable"), true)
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return report(out, errout, c.Fail(c.InvalidArgument, "output parent must exist"), true)
	}
	defer root.Close()
	if _, err = root.Lstat(filepath.Base(absolute)); !os.IsNotExist(err) {
		return report(out, errout, c.Fail(c.Conflict, "fresh output file required"), true)
	}
	raw, err := c.CanonicalV1(input)
	if err != nil {
		return report(out, errout, err, true)
	}
	if err = fileguard.Publish(root, filepath.Base(absolute), raw); err != nil {
		return report(out, errout, err, true)
	}
	digest := c.HashBytes(raw)
	if err = jsonWrite(out, struct {
		SchemaVersion int    `json:"schema_version"`
		Digest        string `json:"digest"`
		Checks        int    `json:"checks"`
		Observers     int    `json:"observer_suites"`
	}{1, digest, len(input.Plan.Checks), len(input.Runtime.ObserverSuites)}); err != nil {
		return 4
	}
	return 0
}
