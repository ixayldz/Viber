package cli

import (
	"context"
	"strconv"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

const prdInstruction = `

--- Viber PRD workflow ---
The user's task above is preserved verbatim and remains authoritative. Work from this task's immutable captured project candidate. First read the exact project-relative PRD path shown below using the admitted fs_read tool; do not infer its contents or read the live filesystem. If the file cannot be read from the captured candidate, stop and report that blocker.

Read every byte of that exact file by following fs_read coverage cursors (the default page is bounded at 16 KiB). The kernel records only successful exact-byte pages bound to the captured candidate; saying that the PRD was read is not sufficient. The complete raw PRD input remains authoritative. Markdown section requirement IDs, when present, are deterministic source spans and are not semantic claims; a bounded or unsupported extraction is reported as review-required.

Before proposing implementation changes, call plan_propose with a source-bound typed dependency plan that covers the PRD requirements, implementation steps, and applicable registered checks. Follow the native plan workflow before making candidate changes.

Implement only within the authorized candidate and plan scope. Use only checks already registered for this task through the registered check tool. Never invent a build or test command, and never claim a check succeeded unless the registered tool reports that result. Report exactly what was verified and leave unsupported checks unknown.

Exact captured project-relative PRD path: `

func validatePRDPath(path string) error {
	if len(path) > 256 || !policy.SafePath(path) {
		return c.Fail(c.InvalidArgument, "--prd requires a safe project-relative path of at most 256 bytes")
	}
	return nil
}

func promptWithPRDWorkflow(rawTask, path string) (string, error) {
	if err := validatePRDPath(path); err != nil {
		return "", err
	}
	var prompt strings.Builder
	prompt.Grow(len(rawTask) + len(prdInstruction) + len(path) + 8)
	prompt.WriteString(rawTask)
	prompt.WriteString(prdInstruction)
	prompt.WriteString(strconv.Quote(path))
	if prompt.Len() > 64<<10 {
		return "", c.Fail(c.InvalidArgument, "raw task plus PRD workflow exceeds the task prompt limit")
	}
	return prompt.String(), nil
}

type capturedPRDBinding struct {
	Candidate string
	Entry     c.Entry
}

// requireCapturedPRD resolves the CLI path against the just-created immutable
// candidate under the same current snapshot.read policy used by model tools.
// source-list is fuzzy, so selection succeeds only when it returns one exact
// path entry; fuzzy neighbors do not substitute for the requested path.
func requireCapturedPRD(ctx context.Context, session *agent.Session, task, path string) (capturedPRDBinding, error) {
	if err := validatePRDPath(path); err != nil {
		return capturedPRDBinding{}, err
	}
	value, err := session.Observe(ctx, task, agent.Observation{Kind: "source-list", Query: path, Limit: 64})
	if err != nil {
		return capturedPRDBinding{}, err
	}
	matches, ok := value.(agent.SourceMatches)
	if !ok || matches.SchemaVersion != 1 || !c.ValidDigest(matches.Candidate) {
		return capturedPRDBinding{}, c.Fail(c.StoreIntegrityError, "captured PRD source listing is invalid")
	}
	var exact *c.Entry
	for i := range matches.Entries {
		if matches.Entries[i].Path != path {
			continue
		}
		if exact != nil {
			return capturedPRDBinding{}, c.Fail(c.StoreIntegrityError, "captured PRD path is duplicated")
		}
		exact = &matches.Entries[i]
	}
	if exact == nil || !c.ValidDigest(exact.Hash) || exact.Size < 0 {
		return capturedPRDBinding{}, c.Fail(c.PolicyDenied, "--prd must name one exact policy-admitted file in the captured project")
	}
	state, doc, err := session.Load(ctx, task)
	if err != nil {
		return capturedPRDBinding{}, err
	}
	if state.CandidateDigest != matches.Candidate || doc.Candidate.SnapshotDigest != matches.Candidate {
		return capturedPRDBinding{}, c.Fail(c.StaleBase, "captured PRD candidate changed before dispatch")
	}
	return capturedPRDBinding{Candidate: matches.Candidate, Entry: *exact}, nil
}
