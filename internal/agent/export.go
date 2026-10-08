package agent

import (
	"context"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
	"github.com/ixayldz/Viber/internal/fileguard"
)

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
	target, err := fileguard.ResolveProspective(output)
	if err != nil {
		return result, err
	}
	root, err := freshPrivate(target)
	if err != nil {
		return result, err
	}
	defer root.Close()
	publish := func(name string, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fileguard.Publish(root, filepath.FromSlash(name), raw); err != nil {
			return err
		}
		result.Files = append(result.Files, BackupFile{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
		return nil
	}
	for _, change := range changeset.Changes {
		if change.BeforeDigest != "" {
			if err = publish("before/"+change.Path, before.Contents[change.Path]); err != nil {
				return result, err
			}
		}
		if change.AfterDigest != "" {
			if err = publish("after/"+change.Path, after.Contents[change.Path]); err != nil {
				return result, err
			}
		}
	}
	if err = publish("changes.patch", patch); err != nil {
		return result, err
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
	if err = publish("report.json", report); err != nil {
		return result, err
	}
	raw, err := c.CanonicalV1(result)
	if err != nil {
		return result, err
	}
	// This marker is the only complete-export indicator; partial output is not a
	// changeset delivery receipt and cannot change task quality or fulfillment.
	return result, fileguard.Publish(root, "manifest.json", raw)
}
