package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/workspace"
	"path/filepath"
	"sort"
)

type DeliveryPreviewManifest struct {
	SchemaVersion              int                   `json:"schema_version"`
	TaskID                     string                `json:"task_id"`
	TaskSeq                    int64                 `json:"task_seq"`
	SpecVersion                int64                 `json:"spec_version"`
	PolicyEpoch                int64                 `json:"policy_epoch"`
	Document                   string                `json:"document_digest"`
	HistoricalCandidateQuality c.Quality             `json:"historical_candidate_quality"`
	Preview                    delivery.MergePreview `json:"preview"`
	Changeset                  *delivery.Changeset   `json:"changeset,omitempty"`
	Files                      []BackupFile          `json:"files"`
	Digest                     string                `json:"manifest_digest"`
}

// DeliveryPreview is an operator export. It binds a fresh BEST_EFFORT B/C/U
// capture and emits exact merged bytes without modifying source/index/task.
// It does not open a live-write capability or copy historical verification.
func (s *Session) DeliveryPreview(ctx context.Context, task, output string) (DeliveryPreviewManifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := DeliveryPreviewManifest{SchemaVersion: 1, TaskID: task, Files: []BackupFile{}}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return result, err
	}
	if state.Execution != c.Terminated || doc.Pending != nil || doc.UnknownEffect {
		return result, c.Fail(c.PolicyDenied, "delivery preview requires a terminal quiescent candidate")
	}
	baseline, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		return result, err
	}
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return result, err
	}
	if err = disjointRoots(output, []string{s.directory, baseline.Snapshot.Root}); err != nil {
		return result, err
	}
	var current workspace.Capture
	if baseline.Snapshot.Git != nil {
		current, err = workspace.CaptureRepository(baseline.Snapshot.Root, configuredCaptureLimits(doc.Config))
	} else {
		current, err = workspace.CaptureDirectory(baseline.Snapshot.Root, configuredCaptureLimits(doc.Config))
	}
	if err != nil {
		return result, err
	}
	preview, merged, err := delivery.Preview(ctx, baseline, candidate, current)
	if err != nil {
		return result, err
	}
	result.TaskSeq = state.TaskSeq
	result.SpecVersion = state.SpecVersion
	result.PolicyEpoch = state.PolicyEpoch
	result.Document = state.DocumentDigest
	result.HistoricalCandidateQuality = state.Quality
	result.Preview = preview
	var patch []byte
	if preview.Status == "READY" {
		changeset, raw, buildErr := delivery.Build(current, merged)
		if buildErr != nil {
			return result, buildErr
		}
		result.Changeset = &changeset
		patch = raw
	}
	target, err := fileguard.ResolveProspective(output)
	if err != nil {
		return result, err
	}
	root, err := freshPrivate(target)
	if err != nil {
		return result, err
	}
	defer root.Close()
	if err = s.allocateManagedCopy(ctx, root, "DELIVERY_PREVIEW", []string{task}); err != nil {
		return result, err
	}
	publish := func(name string, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(result.Files) >= backupMaxFiles {
			return c.Fail(c.UnsupportedCapability, "delivery preview file quota exceeded")
		}
		if err := fileguard.Publish(root, filepath.FromSlash(name), raw); err != nil {
			return err
		}
		result.Files = append(result.Files, BackupFile{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
		return nil
	}
	if preview.Status == "READY" {
		for _, entry := range merged.Snapshot.Entries {
			if err = publish("merged/"+entry.Path, merged.Contents[entry.Path]); err != nil {
				return result, err
			}
		}
		snapshot, err := c.CanonicalV1(merged.Snapshot)
		if err != nil {
			return result, err
		}
		if err = publish("merged-snapshot.json", snapshot); err != nil {
			return result, err
		}
		if err = publish("changes.patch", patch); err != nil {
			return result, err
		}
		for _, change := range result.Changeset.Changes {
			if change.BeforeDigest != "" {
				if err = publish("current/"+change.Path, current.Contents[change.Path]); err != nil {
					return result, err
				}
			}
		}
	}
	raw, err := c.CanonicalV1(preview)
	if err != nil {
		return result, err
	}
	if err = publish("preview.json", raw); err != nil {
		return result, err
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	result.Digest, err = c.Digest(result)
	if err != nil {
		return result, err
	}
	raw, err = c.CanonicalV1(result)
	if err != nil {
		return result, err
	}
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return result, err
	}
	files := append([]BackupFile{}, result.Files...)
	files = append(files, BackupFile{Path: "manifest.json", Digest: c.HashBytes(raw), Size: int64(len(raw))})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if err = s.registerManagedCopy(ctx, ManagedCopy{Kind: "DELIVERY_PREVIEW", Directory: root.Name(), PhysicalRoot: physical, Files: files, Tasks: []string{task}}); err != nil {
		return result, err
	}
	return result, fileguard.Publish(root, "manifest.json", raw)
}
