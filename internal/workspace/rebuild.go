package workspace

import (
	"bytes"
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"strings"
)

// Rebuild is a pure trusted-kernel preview primitive. It grants no active
// proposal, task, filesystem-write or verification authority. The original
// target metadata/index is retained, and every changed path is preconditioned.
func Rebuild(ctx context.Context, current Capture, changes []c.Change, modes map[string]uint32) (Capture, error) {
	if err := ctx.Err(); err != nil {
		return Capture{}, err
	}
	if err := VerifyCapture(current); err != nil {
		return Capture{}, err
	}
	if len(changes) > 10000 {
		return Capture{}, c.Fail(c.UnsupportedCapability, "merged file count exceeded")
	}
	result := Capture{Snapshot: current.Snapshot, Contents: map[string][]byte{}, IndexBytes: bytes.Clone(current.IndexBytes), IgnoreSources: map[string][]byte{}}
	result.Snapshot.Exclusions = append([]string{}, current.Snapshot.Exclusions...)
	result.Snapshot.Directories = append([]string{}, current.Snapshot.Directories...)
	if current.Snapshot.Git != nil {
		git := *current.Snapshot.Git
		git.Entries = append([]c.GitIndexEntry{}, git.Entries...)
		result.Snapshot.Git = &git
	}
	entries := entryMap(current.Snapshot)
	for name, raw := range current.Contents {
		if err := ctx.Err(); err != nil {
			return Capture{}, err
		}
		result.Contents[name] = bytes.Clone(raw)
	}
	for name, raw := range current.IgnoreSources {
		result.IgnoreSources[name] = bytes.Clone(raw)
	}
	seen := map[string]bool{}
	directories := map[string]bool{}
	for _, directory := range result.Snapshot.Directories {
		directories[directory] = true
	}
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return Capture{}, err
		}
		if !policy.SafePath(change.Path) || seen[change.Path] || coverageExcluded(current, change.Path) {
			return Capture{}, c.Fail(c.PolicyDenied, "unsafe, duplicate or excluded merged path")
		}
		seen[change.Path] = true
		before, present := entries[change.Path]
		if present && before.Hash != change.BeforeDigest || !present && change.BeforeDigest != "" {
			return Capture{}, c.Fail(c.StaleBase, "merged target preimage changed")
		}
		if !present && capturedPathExists(current.Snapshot, change.Path) {
			return Capture{}, c.Fail(c.Conflict, "merge target path is occupied")
		}
		if change.Delete {
			if !present || len(change.After) != 0 {
				return Capture{}, c.Fail(c.InvalidArgument, "invalid merged deletion")
			}
			delete(entries, change.Path)
			delete(result.Contents, change.Path)
			continue
		}
		mode, ok := modes[change.Path]
		if !ok || mode & ^uint32(0777) != 0 || len(change.After) > 2<<20 {
			return Capture{}, c.Fail(c.InvalidArgument, "bounded exact merge mode/content required")
		}
		result.Contents[change.Path] = bytes.Clone(change.After)
		entries[change.Path] = c.Entry{Path: change.Path, Hash: c.HashBytes(change.After), Size: int64(len(change.After)), Mode: mode}
		parts := strings.Split(change.Path, "/")
		for n := 1; n < len(parts); n++ {
			directory := strings.Join(parts[:n], "/")
			if !directories[directory] {
				result.Snapshot.Directories = append(result.Snapshot.Directories, directory)
				directories[directory] = true
			}
		}
	}
	result.Snapshot.Entries = []c.Entry{}
	var total int64
	for _, entry := range entries {
		result.Snapshot.Entries = append(result.Snapshot.Entries, entry)
		total += entry.Size
	}
	if len(entries) > 10000 || total > 32<<20 {
		return Capture{}, c.Fail(c.UnsupportedCapability, "merged capture quota exceeded")
	}
	if err := seal(&result.Snapshot); err != nil {
		return Capture{}, err
	}
	if err := VerifyCapture(result); err != nil {
		return Capture{}, c.Fail(c.Conflict, "merged namespace or source shape is incompatible")
	}
	return result, nil
}
