package cli

import (
	"io"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/discovery"
	"github.com/ixayldz/Viber/internal/verify"
	"github.com/ixayldz/Viber/internal/workspace"
)

func runCheckConfig(args []string, out, errout io.Writer) int {
	f := flags("check-config", errout)
	runtimeFile := f.String("runtime", "", "operator check runtime JSON")
	planFile := f.String("plan", "", "optional operator check plan JSON")
	rootPath := f.String("root", "", "source root for fresh closure validation")
	discoveryFile := f.String("discovery", "", "source discovery report JSON to revalidate against --root")
	jsonMode := f.Bool("json", false, "configuration result; does not run a process")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *runtimeFile == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "--runtime required; no task or process execution"), *jsonMode)
	}
	var config agent.CheckRuntime
	if err := readJSON(*runtimeFile, &config); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if config.SchemaVersion != 1 {
		return report(out, errout, c.Fail(c.InvalidArgument, "invalid check runtime schema"), *jsonMode)
	}
	if err := config.Profile.Validate(); err != nil {
		return report(out, errout, err, *jsonMode)
	}
	digest, err := c.Digest(config.Profile)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	count := 0
	var parsed verify.CheckPlan
	if *planFile != "" {
		if err := readJSON(*planFile, &parsed); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if err := parsed.Validate(); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		for _, check := range parsed.Checks {
			if check.RunnerDigest != digest {
				return report(out, errout, c.Fail(c.PolicyDenied, "check profile digest mismatch"), *jsonMode)
			}
		}
		count = len(parsed.Checks)
	}
	var sourceCapture workspace.Capture
	var sourceReport *discovery.Report
	if *discoveryFile != "" && *rootPath == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "--discovery requires --root freshness validation"), *jsonMode)
	}
	if *rootPath != "" {
		capture, err := workspace.CaptureDirectory(*rootPath, workspace.DefaultLimits())
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		sourceCapture = capture
		if *discoveryFile != "" {
			var value discovery.Report
			if err := readJSON(*discoveryFile, &value); err != nil {
				return report(out, errout, c.Fail(c.InvalidArgument, "invalid source discovery report"), *jsonMode)
			}
			sourceReport = &value
		} else {
			value, err := discovery.Discover(sourceCapture)
			if err != nil {
				return report(out, errout, err, *jsonMode)
			}
			sourceReport = &value
		}
		if verify.HasTestChecks(parsed) {
			if err := verify.RequireDiscoveredClosure(parsed, *sourceReport, sourceCapture); err != nil {
				return report(out, errout, err, *jsonMode)
			}
		} else if err := discovery.Validate(*sourceReport, sourceCapture); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	sourceDigest, sourceCoverage, scope := "", "", "OPERATOR_CONFIG_ONLY; SOURCE_CLOSURE_AND_ENV_ACCEPTANCE_NOT_CHECKED"
	if sourceReport != nil {
		sourceDigest = sourceReport.ReportDigest
		sourceCoverage = sourceReport.Coverage.Overall
		if verify.HasTestChecks(parsed) {
			scope = "SOURCE_CLOSURE_VALIDATED_FOR_CAPTURE; ENVIRONMENT_ACCEPTANCE_NOT_CHECKED"
		}
	}
	value := struct {
		SchemaVersion         int    `json:"schema_version"`
		ProfileDigest         string `json:"runner_digest"`
		Checks                int    `json:"registered_checks"`
		Scope                 string `json:"validation_scope"`
		StrongVerification    bool   `json:"strong_verification_available"`
		SourceDiscoveryDigest string `json:"source_discovery_digest,omitempty"`
		DiscoveryCoverage     string `json:"source_discovery_coverage,omitempty"`
	}{1, digest, count, scope, false, sourceDigest, sourceCoverage}
	if err = jsonWrite(out, value); err != nil {
		return 4
	}
	return 0
}
