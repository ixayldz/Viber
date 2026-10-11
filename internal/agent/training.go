package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

// Consent binds the exact operator-reviewed payload to the current task. It
// does not manufacture a gold label from a successful task or a passed check.
type TrainingConsent struct {
	SchemaVersion    int       `json:"schema_version"`
	TaskID           string    `json:"task_id"`
	TaskSeq          int64     `json:"task_seq"`
	DocumentDigest   string    `json:"document_digest"`
	CandidateDigest  string    `json:"candidate_digest"`
	SamplesDigest    string    `json:"samples_digest"`
	License          string    `json:"license"`
	ProvenanceDigest string    `json:"provenance_digest"`
	ExpiresAt        time.Time `json:"expires_at"`
	Acknowledgment   string    `json:"acknowledgment"`
}

type TrainingSample struct {
	Kind     string          `json:"kind"`
	Input    json.RawMessage `json:"input"`
	Target   json.RawMessage `json:"target"`
	Rejected json.RawMessage `json:"rejected,omitempty"`
}

type TrainingSamples struct {
	SchemaVersion int              `json:"schema_version"`
	Samples       []TrainingSample `json:"samples"`
}

type TrainingManifest struct {
	SchemaVersion  int             `json:"schema_version"`
	Consent        TrainingConsent `json:"consent"`
	ConsentDigest  string          `json:"consent_digest"`
	ConfigDigest   string          `json:"config_digest"`
	SourceQuality  c.Quality       `json:"source_quality"`
	LabelAuthority string          `json:"label_authority"`
	Files          []BackupFile    `json:"files"`
	NetworkUsed    bool            `json:"network_used"`
	GoldLabels     bool            `json:"gold_labels"`
}

func (s *Session) ExportTraining(ctx context.Context, task, output string, consent TrainingConsent, samplesRaw []byte) (TrainingManifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := TrainingManifest{SchemaVersion: 1, LabelAuthority: "EXPLICIT_OPERATOR_REVIEW_NOT_INDEPENDENT_GOLD", Files: []BackupFile{}}
	state, doc, err := s.Load(ctx, task)
	if err != nil {
		return result, err
	}
	if doc.Config == nil {
		return result, c.Fail(c.PolicyDenied, "training export is off by default")
	}
	if err = doc.Config.AdmitTrainingExport(); err != nil {
		return result, err
	}
	if consent.SchemaVersion != 1 || consent.TaskID != task || consent.TaskSeq != state.TaskSeq || consent.DocumentDigest != state.DocumentDigest || consent.CandidateDigest != state.CandidateDigest || consent.SamplesDigest != c.HashBytes(samplesRaw) || !c.ValidDigest(consent.ProvenanceDigest) || len(consent.License) < 1 || len(consent.License) > 80 || strings.ContainsAny(consent.License, "\x00\r\n") || !utf8.ValidString(consent.License) || !consent.ExpiresAt.After(time.Now()) || consent.ExpiresAt.After(time.Now().Add(24*time.Hour)) || consent.Acknowledgment != "I_REVIEWED_EXACT_PAYLOAD_AND_LICENSE_FOR_LOCAL_TRAINING_EXPORT" {
		return result, c.Fail(c.StaleAuthority, "training consent is missing, expired, or bound to different content")
	}
	var samples TrainingSamples
	if len(samplesRaw) < 1 || len(samplesRaw) > 1<<20 || c.DecodeStrict(samplesRaw, &samples) != nil || samples.SchemaVersion != 1 || len(samples.Samples) < 1 || len(samples.Samples) > 256 {
		return result, c.Fail(c.InvalidArgument, "invalid bounded reviewed training samples")
	}
	for _, sample := range samples.Samples {
		switch sample.Kind {
		case "SFT", "PREFERENCE", "PROCESS", "CONTEXT", "PLANNER", "ROUTER":
		default:
			return result, c.Fail(c.InvalidArgument, "unknown training record type")
		}
		if !json.Valid(sample.Input) || !json.Valid(sample.Target) || string(sample.Input) == "null" || string(sample.Target) == "null" || len(sample.Input) > 64<<10 || len(sample.Target) > 64<<10 || len(sample.Rejected) > 64<<10 || (sample.Kind == "PREFERENCE") != (len(sample.Rejected) > 0) || len(sample.Rejected) > 0 && (!json.Valid(sample.Rejected) || string(sample.Rejected) == "null") {
			return result, c.Fail(c.InvalidArgument, "invalid reviewed training pair")
		}
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
	result.Consent = consent
	result.ConsentDigest, err = c.Digest(consent)
	if err != nil {
		return result, err
	}
	result.ConfigDigest, err = c.Digest(*doc.Config)
	if err != nil {
		return result, err
	}
	result.SourceQuality = state.Quality
	result.Files = append(result.Files, BackupFile{Path: "samples.json", Digest: c.HashBytes(samplesRaw), Size: int64(len(samplesRaw))})
	raw, err := c.CanonicalV1(result)
	if err != nil {
		return result, err
	}
	target, err := fileguard.ResolveProspective(output)
	if err != nil {
		return result, err
	}
	var allocation resource.Allocation
	if s.PhysicalQuota != nil {
		bytes, entries, e := exportPhysicalEstimate([]exportPayload{{path: "samples.json", raw: samplesRaw}}, raw)
		if e != nil {
			return result, e
		}
		allocation, err = s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "agent", Family: "TRAINING_EXPORT", RequestDigest: result.ConsentDigest, TargetRoot: target, UpperBytes: bytes, UpperEntries: entries, Class: resource.Payload})
		if err != nil {
			return result, err
		}
		if err = allocation.Begin(ctx); err != nil {
			return result, err
		}
	}
	root, err := freshPrivate(target)
	if err != nil {
		return result, err
	}
	defer root.Close()
	if err = s.allocateManagedCopy(ctx, root, "TRAINING_EXPORT", []string{task}); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = doc.Config.AdmitTrainingExport(); err != nil {
		return result, err
	}
	if err = fileguard.Publish(root, filepath.FromSlash("samples.json"), samplesRaw); err != nil {
		return result, err
	}
	if err = fileguard.Publish(root, "manifest.json", raw); err != nil {
		return result, err
	}
	if s.PhysicalQuota != nil {
		inventory, e := resource.Measure(root, true)
		if e != nil {
			return result, e
		}
		if err = allocation.Commit(ctx, inventory); err != nil {
			return result, err
		}
	}
	return result, nil
}
