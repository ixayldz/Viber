package verify

import (
	"bytes"
	"context"
	"fmt"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/runner"
	"sort"
)

// StdioSuite is a trusted operator oracle. Neither expected outputs nor this
// object is mounted in the subject container or exposed as model tool data.
// The kernel observer executes no candidate code: it compares bounded bytes
// returned by isolated subject computations on its own protected result path.
type StdioSuite struct {
	SchemaVersion int         `json:"schema_version"`
	CheckID       string      `json:"check_id"`
	Level         string      `json:"level"`
	Protocol      string      `json:"protocol"`
	Repeats       int         `json:"repeats"`
	Cases         []StdioCase `json:"cases"`
}
type StdioCase struct {
	ID       string `json:"id"`
	Input    []byte `json:"input"`
	Stdout   []byte `json:"expected_stdout"`
	Stderr   []byte `json:"expected_stderr"`
	ExitCode int    `json:"expected_exit_code"`
}

const ObserverProtocol = "KERNEL_STDIO_EXACT_V1"

func (s StdioSuite) Validate(check CheckDefinition, profile runner.Profile) error {
	if s.SchemaVersion != 1 || s.CheckID != check.ID || s.Protocol != ObserverProtocol || s.Repeats < 2 || s.Repeats > 4 || len(s.Cases) == 0 || len(s.Cases) > 32 || check.Kind != "TEST" || profile.Validate() != nil {
		return c.Fail(c.InvalidArgument, "bounded repeated TEST observer suite required")
	}
	digest, _ := c.Digest(s)
	if check.ObserverDigest != digest || profile.MaxOutputBytes*int64(len(s.Cases)*s.Repeats*4) > 4<<20 {
		return c.Fail(c.PolicyDenied, "protected oracle identity or retained output quota mismatch")
	}
	if s.Level != "V4" {
		return c.Fail(c.UnsupportedCapability, "this observer verifies external STDIO behavior only; no framework self-report/discovery authority")
	}
	if len(check.ExpectedTests) != len(s.Cases) {
		return c.Fail(c.PolicyDenied, "observer discovery differs from protected selection")
	}
	expected := map[string]bool{}
	for _, id := range check.ExpectedTests {
		expected[id] = true
	}
	total := 0
	for _, item := range s.Cases {
		if !validCheckID(item.ID) || !expected[item.ID] || item.ExitCode < 0 || item.ExitCode > 255 || len(item.Input) > 64<<10 || int64(len(item.Stdout)) > profile.MaxOutputBytes || int64(len(item.Stderr)) > profile.MaxOutputBytes {
			return c.Fail(c.InvalidArgument, "invalid bounded observer case")
		}
		delete(expected, item.ID)
		total += len(item.Input) + len(item.Stdout) + len(item.Stderr)
	}
	if len(expected) != 0 || total > 256<<10 {
		return c.Fail(c.InvalidArgument, "observer case selection or byte quota invalid")
	}
	return nil
}

type SubjectRunner interface {
	Run(context.Context, string, runner.Profile, runner.Invocation) (runner.Result, error)
}
type ObservedAttempt struct {
	BrokerError bool          `json:"broker_error,omitempty"`
	CaseID      string        `json:"case_id"`
	Repeat      int           `json:"repeat"`
	Verdict     c.Verdict     `json:"verdict"`
	Result      runner.Result `json:"subject"`
}
type ObservedRun struct {
	SchemaVersion int               `json:"schema_version"`
	Protocol      string            `json:"protocol"`
	SuiteDigest   string            `json:"suite_digest"`
	Candidate     string            `json:"candidate"`
	ProfileDigest string            `json:"profile_digest"`
	Discovered    []string          `json:"discovered"`
	Attempts      []ObservedAttempt `json:"attempts"`
	Verdict       c.Verdict         `json:"verdict"`
	Complete      bool              `json:"trusted_completion"`
	Flaky         bool              `json:"flaky"`
}

func subjectAdmissible(result runner.Result) bool {
	return result.SchemaVersion == 1 && result.SourceReadOnly && result.ProtectedResultChannel && result.ProcessTreeQuiescent && c.ValidDigest(result.ContainerID) && result.ExitCode >= 0 && !result.TimedOut && !result.Cancelled && !result.OOMKilled && !result.OutputTruncated && result.DurationMillis >= 0
}
func caseVerdict(item StdioCase, result runner.Result) c.Verdict {
	if !subjectAdmissible(result) {
		return c.Unknown
	}
	if result.ExitCode == item.ExitCode && bytes.Equal(result.Stdout, item.Stdout) && bytes.Equal(result.Stderr, item.Stderr) {
		return c.Pass
	}
	return c.FailVerdict
}

// Observe always uses fresh isolated subject scratch per case/repeat. There is
// no pass-only rerun: every attempted result, including FAIL/UNKNOWN, is retained.
func Observe(ctx context.Context, broker SubjectRunner, source string, profile runner.Profile, candidate string, check CheckDefinition, suite StdioSuite) (ObservedRun, error) {
	observed := ObservedRun{SchemaVersion: 1, Protocol: ObserverProtocol, Candidate: candidate, Attempts: []ObservedAttempt{}, Discovered: []string{}, Verdict: c.Unknown}
	if suite.Validate(check, profile) != nil || !c.ValidDigest(candidate) || broker == nil {
		return observed, c.Fail(c.InvalidArgument, "valid immutable observer invocation required")
	}
	observed.SuiteDigest, _ = c.Digest(suite)
	observed.ProfileDigest, _ = c.Digest(profile)
	for _, item := range suite.Cases {
		observed.Discovered = append(observed.Discovered, item.ID)
	}
	for _, item := range suite.Cases {
		for repeat := 1; repeat <= suite.Repeats; repeat++ {
			if err := ctx.Err(); err != nil {
				return observed, &runner.DispatchFailure{Cause: err, EffectPossible: false}
			}
			input := bytes.Clone(item.Input)
			if input == nil {
				input = []byte{}
			}
			inv := runner.Invocation{CandidateDigest: candidate, Argv: append([]string{}, check.Argv...), Stdin: &input}
			result, err := broker.Run(ctx, source, profile, inv)
			verdict := caseVerdict(item, result)
			if err != nil {
				verdict = c.Unknown
			}
			observed.Attempts = append(observed.Attempts, ObservedAttempt{CaseID: item.ID, Repeat: repeat, Verdict: verdict, Result: result, BrokerError: err != nil})
			if err != nil {
				return observed, err
			}
		}
	}
	if err := ValidateObservedRun(observed, profile, candidate, check, suite); err != nil {
		return observed, err
	}
	observed.Complete = true
	observed.Verdict, observed.Flaky = ObservedVerdict(observed)
	if err := ValidateObservedRun(observed, profile, candidate, check, suite); err != nil {
		return observed, err
	}
	return observed, nil
}
func ObservedVerdict(run ObservedRun) (c.Verdict, bool) {
	verdict := c.Pass
	flaky := false
	first := map[string]c.Verdict{}
	for _, attempt := range run.Attempts {
		if old, exists := first[attempt.CaseID]; exists && old != attempt.Verdict {
			flaky = true
		}
		first[attempt.CaseID] = attempt.Verdict
		if attempt.Verdict == c.Unknown {
			verdict = c.Unknown
		} else if attempt.Verdict == c.FailVerdict && verdict != c.Unknown {
			verdict = c.FailVerdict
		}
	}
	if len(run.Attempts) == 0 || !run.Complete {
		verdict = c.Unknown
	}
	return verdict, flaky
}
func ValidateObservedRun(run ObservedRun, profile runner.Profile, candidate string, check CheckDefinition, suite StdioSuite) error {
	suiteDigest, _ := c.Digest(suite)
	profileDigest, _ := c.Digest(profile)
	if suite.Validate(check, profile) != nil || run.SchemaVersion != 1 || run.Protocol != ObserverProtocol || run.SuiteDigest != suiteDigest || run.ProfileDigest != profileDigest || run.Candidate != candidate || !c.ValidDigest(candidate) || len(run.Discovered) != len(suite.Cases) || len(run.Attempts) > len(suite.Cases)*suite.Repeats {
		return c.Fail(c.StoreIntegrityError, "observer binding/selection mismatch")
	}
	for i, item := range suite.Cases {
		if run.Discovered[i] != item.ID {
			return c.Fail(c.StoreIntegrityError, "observer discovery changed")
		}
	}
	containers := map[string]bool{}
	for i, attempt := range run.Attempts {
		item := suite.Cases[i/suite.Repeats]
		repeat := i%suite.Repeats + 1
		input := bytes.Clone(item.Input)
		if input == nil {
			input = []byte{}
		}
		invDigest, _ := c.Digest(runner.Invocation{CandidateDigest: candidate, Argv: check.Argv, Stdin: &input})
		r := attempt.Result
		if attempt.CaseID != item.ID || attempt.Repeat != repeat || int64(len(r.Stdout)) > profile.MaxOutputBytes || int64(len(r.Stderr)) > profile.MaxOutputBytes {
			return c.Fail(c.StoreIntegrityError, "observer case/attempt/output binding mismatch")
		}
		if attempt.BrokerError {
			if run.Complete || i != len(run.Attempts)-1 || attempt.Verdict != c.Unknown || (r.CandidateDigest != "" && r.CandidateDigest != candidate) || (r.ProfileDigest != "" && r.ProfileDigest != profileDigest) || (r.InvocationDigest != "" && r.InvocationDigest != invDigest) {
				return c.Fail(c.StoreIntegrityError, "invalid incomplete broker observation")
			}
		} else if r.CandidateDigest != candidate || r.ProfileDigest != profileDigest || r.InvocationDigest != invDigest || attempt.Verdict != caseVerdict(item, r) {
			return c.Fail(c.StoreIntegrityError, "observer subject authority mismatch")
		}
		if c.ValidDigest(r.ContainerID) {
			if containers[r.ContainerID] {
				return c.Fail(c.StoreIntegrityError, "observer reused subject identity")
			}
			containers[r.ContainerID] = true
		}
	}
	if !run.Complete && (run.Verdict != c.Unknown || run.Flaky) {
		return c.Fail(c.StoreIntegrityError, "partial observer cannot publish verdict")
	}
	if run.Complete {
		if len(run.Attempts) != len(suite.Cases)*suite.Repeats {
			return c.Fail(c.StoreIntegrityError, "incomplete observer cannot complete")
		}
		verdict, flaky := ObservedVerdict(run)
		if run.Verdict != verdict || run.Flaky != flaky {
			return c.Fail(c.StoreIntegrityError, "observer verdict differs from protected byte comparisons")
		}
	}
	return nil
}
func SuiteCaseIDs(s StdioSuite) []string {
	ids := make([]string, 0, len(s.Cases))
	for _, item := range s.Cases {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}
func ObserverSummary(run ObservedRun) string {
	return fmt.Sprintf("%s: %d discovered; %d attempts; complete=%t; flaky=%t", run.Protocol, len(run.Discovered), len(run.Attempts), run.Complete, run.Flaky)
}
