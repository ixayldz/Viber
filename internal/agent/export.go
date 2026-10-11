package agent

import (
	"context"
	"math"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

type exportPayload struct {
	path string
	raw  []byte
}

type ExportManifest struct {
	SchemaVersion        int                `json:"schema_version"`
	TaskID               string             `json:"task_id"`
	TaskSeq              int64              `json:"task_seq"`
	State                c.TaskState        `json:"state"`
	Changeset            delivery.Changeset `json:"changeset"`
	Files                []BackupFile       `json:"files"`
	LiveWorkspaceWritten bool               `json:"live_workspace_written"`
}

func (s *Session) Export(ctx context.Context, task, output string) (ExportManifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := ExportManifest{SchemaVersion: 1, TaskID: task, Files: []BackupFile{}}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return result, err
	}
	if err = currentOperatorEffect(doc, "export"); err != nil {
		return result, err
	}
	before, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		return result, err
	}
	after, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return result, err
	}
	if err = disjointRoots(output, []string{s.directory, before.Snapshot.Root, after.Snapshot.Root}); err != nil {
		return result, err
	}
	changeset, patch, err := delivery.Build(before, after)
	if err != nil {
		return result, err
	}
	result.Changeset = changeset
	result.State = state
	result.TaskSeq = state.TaskSeq
	files := make([]exportPayload, 0, len(changeset.Changes)*2+3)
	appendFile := func(name string, raw []byte) {
		files = append(files, exportPayload{path: name, raw: raw})
		result.Files = append(result.Files, BackupFile{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
	}
	for _, change := range changeset.Changes {
		if change.BeforeDigest != "" {
			appendFile("before/"+change.Path, before.Contents[change.Path])
		}
		if change.AfterDigest != "" {
			appendFile("after/"+change.Path, after.Contents[change.Path])
		}
	}
	appendFile("changes.patch", patch)
	if doc.FinalArtifactDigest != "" {
		raw, readErr := s.Archive.GetBytes(task, doc.FinalArtifactDigest)
		if readErr != nil {
			return result, readErr
		}
		appendFile("final-artifact.json", raw)
	}
	report, err := c.CanonicalV1(struct {
		Protection    *ProtectionInfo `json:"check_protection,omitempty"`
		State         c.TaskState     `json:"state"`
		Budget        Budget          `json:"budget"`
		Blocker       string          `json:"blocker"`
		ModelSummary  string          `json:"untrusted_model_summary"`
		PatchComplete bool            `json:"patch_complete"`
		ReleaseReady  bool            `json:"release_ready"`
	}{Protection: CheckProtection(doc), State: state, Budget: doc.Budget, Blocker: doc.Blocker, ModelSummary: doc.FinalSummary, PatchComplete: changeset.PatchComplete})
	if err != nil {
		return result, err
	}
	appendFile("report.json", report)
	raw, err := c.CanonicalV1(result)
	if err != nil {
		return result, err
	}
	target, err := fileguard.ResolveProspective(output)
	if err != nil {
		return result, err
	}
	var allocation resource.Allocation
	covered := false
	if s.PhysicalQuota != nil {
		_, covered, err = s.PhysicalQuota.EnclosingReservation(ctx, target)
		if err != nil {
			return result, err
		}
		if !covered {
			upperBytes, upperEntries, estimateErr := exportPhysicalEstimate(files, raw)
			if estimateErr != nil {
				return result, estimateErr
			}
			requestDigest := c.HashBytes(raw)
			allocation, err = s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "agent", Family: "EXPORT", RequestDigest: requestDigest, TargetRoot: target, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload})
			if err != nil {
				return result, err
			}
			if err = allocation.Begin(ctx); err != nil {
				return result, err
			}
		}
	}
	root, err := freshPrivate(target)
	if err != nil {
		return result, err
	}
	defer root.Close()
	if err = s.allocateManagedCopy(ctx, root, "EXPORT", []string{task}); err != nil {
		return result, err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := fileguard.Publish(root, filepath.FromSlash(file.path), file.raw); err != nil {
			return result, err
		}
	}
	// This marker is the only complete-export indicator; partial output is not a
	// changeset delivery receipt and cannot change task quality or fulfillment.
	if err = fileguard.Publish(root, "manifest.json", raw); err != nil {
		return result, err
	}
	if s.PhysicalQuota != nil && !covered {
		inventory, measureErr := resource.Measure(root, true)
		if measureErr != nil {
			return result, measureErr
		}
		if err = allocation.Commit(ctx, inventory); err != nil {
			return result, err
		}
	}
	return result, nil
}

func exportPhysicalEstimate(files []exportPayload, manifest []byte) (uint64, uint64, error) {
	if len(files) > 1<<20 || len(manifest) > 8<<20 {
		return 0, 0, c.Fail(c.UnsupportedCapability, "export physical receipt exceeds bounded profile")
	}
	fileCount := uint64(len(files) + 1) // includes the final manifest
	var total, maxTemp uint64
	for _, file := range files {
		size := uint64(len(file.raw))
		if size > math.MaxUint64-total {
			return 0, 0, c.Fail(c.UnsupportedCapability, "export byte estimate overflow")
		}
		total += size
		if size > maxTemp {
			maxTemp = size
		}
	}
	manifestSize := uint64(len(manifest))
	if manifestSize > math.MaxUint64-total {
		return 0, 0, c.Fail(c.UnsupportedCapability, "export byte estimate overflow")
	}
	total += manifestSize
	if manifestSize > maxTemp {
		maxTemp = manifestSize
	}
	if total > 1<<48 {
		return 0, 0, c.Fail(c.UnsupportedCapability, "export exceeds shared physical quota profile")
	}
	directories := map[string]bool{}
	for _, file := range files {
		for directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(file.path))); directory != "." && directory != ""; directory = filepath.ToSlash(filepath.Dir(directory)) {
			directories[directory] = true
		}
	}
	if len(directories) > 1<<20 || fileCount > 1<<20 {
		return 0, 0, c.Fail(c.UnsupportedCapability, "export entry estimate exceeds bounded profile")
	}
	// Publish keeps prior finals while one temp file is written. Bound per-file
	// allocation slack and all directory entries before creating the output.
	upperBytes := total
	addEstimate := func(value uint64) bool {
		if value > math.MaxUint64-upperBytes {
			return false
		}
		upperBytes += value
		return upperBytes <= 1<<48
	}
	if !addEstimate(maxTemp) || !addEstimate(fileCount*64<<10) || !addEstimate(uint64(len(directories)+1)*4096) || !addEstimate(64<<10) {
		return 0, 0, c.Fail(c.UnsupportedCapability, "export byte estimate exceeds bounded profile")
	}
	upperEntries := fileCount + uint64(len(directories)) + 2 // root and one live temporary file
	return upperBytes, upperEntries, nil
}
