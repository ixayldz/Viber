package cli

import (
	"encoding/json"
	"io"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runCheckConfig(args []string, out, errout io.Writer) int {
	f := flags("check-config", errout)
	runtimeFile := f.String("runtime", "", "operator check runtime JSON")
	planFile := f.String("plan", "", "optional operator check plan JSON")
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
	if *planFile != "" {
		var plan struct {
			SchemaVersion int               `json:"schema_version"`
			Checks        []json.RawMessage `json:"checks"`
		}
		if err := readJSON(*planFile, &plan); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		raw, _ := json.Marshal(plan)
		parsed, err := agent.ParseCheckPlan(raw)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		for _, check := range parsed.Checks {
			if check.RunnerDigest != digest {
				return report(out, errout, c.Fail(c.PolicyDenied, "check profile digest mismatch"), *jsonMode)
			}
		}
		count = len(parsed.Checks)
	}
	value := struct {
		SchemaVersion      int    `json:"schema_version"`
		ProfileDigest      string `json:"runner_digest"`
		Checks             int    `json:"registered_checks"`
		Scope              string `json:"validation_scope"`
		StrongVerification bool   `json:"strong_verification_available"`
	}{1, digest, count, "OPERATOR_CONFIG_ONLY; SOURCE_CLOSURE_AND_ENV_ACCEPTANCE_NOT_CHECKED", false}
	if err = jsonWrite(out, value); err != nil {
		return 4
	}
	return 0
}
