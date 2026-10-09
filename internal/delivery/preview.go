package delivery

import (
	"bytes"
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
)

type MergeDecision struct {
	Path          string `json:"path"`
	Action        string `json:"action"`
	CurrentDigest string `json:"current_digest,omitempty"`
	ResultDigest  string `json:"result_digest,omitempty"`
	Reason        string `json:"reason,omitempty"`
}
type MergePreview struct {
	SchemaVersion          int             `json:"schema_version"`
	Algorithm              string          `json:"algorithm"`
	Baseline               string          `json:"baseline"`
	Candidate              string          `json:"candidate"`
	Target                 string          `json:"target"`
	Result                 string          `json:"result,omitempty"`
	Status                 string          `json:"status"`
	Decisions              []MergeDecision `json:"decisions"`
	Verification           string          `json:"result_verification"`
	RequiresReverification bool            `json:"requires_reverification"`
	LiveWorkspaceWritten   bool            `json:"live_workspace_written"`
	Backend                string          `json:"live_backend"`
	Digest                 string          `json:"preview_digest"`
}

func entries(capture workspace.Capture) map[string]c.Entry {
	result := map[string]c.Entry{}
	for _, entry := range capture.Snapshot.Entries {
		result[entry.Path] = entry
	}
	return result
}
func sameFile(a, b map[string]c.Entry, path string) bool {
	left, leftOK := a[path]
	right, rightOK := b[path]
	return leftOK == rightOK && (!leftOK || left.Hash == right.Hash && left.Mode == right.Mode)
}
func mergeMode(base, candidate, current uint32) (uint32, bool) {
	if candidate == base {
		return current, true
	}
	if current == base || current == candidate {
		return candidate, true
	}
	return 0, false
}
func sealPreview(preview *MergePreview) error {
	preview.Digest = ""
	digest, err := c.Digest(*preview)
	preview.Digest = digest
	return err
}

// Preview compares B/C/U without filesystem effects. A conflicted preview
// never returns a partially merged capture. Even READY is an unverified
// candidate/patch proposal, not admission or an auto-apply guarantee.
func Preview(ctx context.Context, baseline, candidate, current workspace.Capture) (MergePreview, workspace.Capture, error) {
	result := MergePreview{SchemaVersion: 1, Algorithm: MergeVersion, Baseline: baseline.Snapshot.Digest, Candidate: candidate.Snapshot.Digest, Target: current.Snapshot.Digest, Status: "READY", Decisions: []MergeDecision{}, Verification: "UNVERIFIED", RequiresReverification: true, Backend: "UNSUPPORTED_STRONG_EXCLUSIVITY; CANDIDATE_PATCH_ONLY"}
	if err := ctx.Err(); err != nil {
		return result, workspace.Capture{}, err
	}
	for _, source := range []workspace.Capture{baseline, candidate, current} {
		if err := workspace.VerifyCapture(source); err != nil {
			return result, workspace.Capture{}, err
		}
	}
	if baseline.Snapshot.Root != candidate.Snapshot.Root || baseline.Snapshot.Root != current.Snapshot.Root {
		return result, workspace.Capture{}, c.Fail(c.StaleBase, "delivery target identity changed")
	}
	if err := workspace.GitPrecondition(baseline, current); err != nil {
		return result, workspace.Capture{}, err
	}
	if err := workspace.GitPrecondition(baseline, candidate); err != nil {
		return result, workspace.Capture{}, err
	}
	changed, _, err := Build(baseline, candidate)
	if err != nil {
		return result, workspace.Capture{}, err
	}
	before, after, target := entries(baseline), entries(candidate), entries(current)
	changes := []c.Change{}
	modes := map[string]uint32{}
	for _, change := range changed.Changes {
		if err := ctx.Err(); err != nil {
			return result, workspace.Capture{}, err
		}
		path := change.Path
		decision := MergeDecision{Path: path, Action: "APPLY_CANDIDATE", CurrentDigest: target[path].Hash}
		if sameFile(after, target, path) {
			decision.Action = "ALREADY_PRESENT"
			decision.ResultDigest = after[path].Hash
			result.Decisions = append(result.Decisions, decision)
			continue
		}
		raw, exists := candidate.Contents[path]
		mode := after[path].Mode
		if !sameFile(before, target, path) {
			_, baseOK := baseline.Contents[path]
			_, currentOK := current.Contents[path]
			if !baseOK || !exists || !currentOK {
				decision.Action = "CONFLICT"
				decision.Reason = "DIVERGENT_PRESENCE_OR_DELETE_EDIT"
			} else {
				merged, _, mergeErr := MergeBytes(ctx, baseline.Contents[path], raw, current.Contents[path])
				if mergeErr != nil {
					var failure *c.Error
					if !errors.As(mergeErr, &failure) || failure.Code != c.Conflict && failure.Code != c.UnsupportedCapability {
						return result, workspace.Capture{}, mergeErr
					}
					decision.Action = "CONFLICT"
					decision.Reason = string(failure.Code)
				} else if selected, compatible := mergeMode(before[path].Mode, after[path].Mode, target[path].Mode); !compatible {
					decision.Action = "CONFLICT"
					decision.Reason = "DIVERGENT_MODE"
				} else {
					raw = merged
					mode = selected
					decision.Action = "MERGED_REVERIFY"
				}
			}
		}
		if decision.Action == "CONFLICT" {
			result.Status = "CONFLICT"
			result.Decisions = append(result.Decisions, decision)
			continue
		}
		if exists {
			decision.ResultDigest = c.HashBytes(raw)
			modes[path] = mode
		}
		result.Decisions = append(result.Decisions, decision)
		if exists && bytes.Equal(raw, current.Contents[path]) && mode == target[path].Mode {
			continue
		}
		changes = append(changes, c.Change{Path: path, BeforeDigest: target[path].Hash, After: bytes.Clone(raw), Delete: !exists})
	}
	if result.Status == "CONFLICT" {
		return result, workspace.Capture{}, sealPreview(&result)
	}
	merged, err := workspace.Rebuild(ctx, current, changes, modes)
	if err != nil {
		var failure *c.Error
		if errors.As(err, &failure) && (failure.Code == c.Conflict || failure.Code == c.PolicyDenied || failure.Code == c.UnsupportedCapability) {
			result.Status = "CONFLICT"
			result.Decisions = append(result.Decisions, MergeDecision{Action: "CONFLICT", Reason: string(failure.Code)})
			return result, workspace.Capture{}, sealPreview(&result)
		}
		return result, workspace.Capture{}, err
	}
	result.Result = merged.Snapshot.Digest
	return result, merged, sealPreview(&result)
}
