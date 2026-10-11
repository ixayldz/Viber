package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestTrainingExportExactConsentAndPrivacyDeletion(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer s.Close()
	samples := []byte(`{"schema_version":1,"samples":[{"kind":"CONTEXT","input":{"selected":[]},"target":{"review":"unverified"}}]}`)
	output := filepath.Join(t.TempDir(), "training")
	if _, err := s.ExportTraining(ctx, options.TaskID, output, TrainingConsent{}, samples); err == nil {
		t.Fatal("default training permission granted")
	}
	yes := true
	user := config.File{SchemaVersion: 1, Restrictions: config.Privacy{TrainingExport: &yes}}
	resolved, err := config.Resolve(&user, nil)
	if err != nil {
		t.Fatal(err)
	}
	options.TaskID = "training-optin"
	options.Config = &resolved
	if _, err = s.Create(ctx, options); err != nil {
		t.Fatal(err)
	}
	state, _, err := s.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	consent := TrainingConsent{SchemaVersion: 1, TaskID: options.TaskID, TaskSeq: state.TaskSeq, DocumentDigest: state.DocumentDigest, CandidateDigest: state.CandidateDigest, SamplesDigest: c.HashBytes(samples), License: "MIT", ProvenanceDigest: c.HashBytes([]byte("operator provenance")), ExpiresAt: time.Now().UTC().Add(time.Hour), Acknowledgment: "I_REVIEWED_EXACT_PAYLOAD_AND_LICENSE_FOR_LOCAL_TRAINING_EXPORT"}
	if _, err = s.ExportTraining(ctx, options.TaskID, output, consent, append(samples, ' ')); err == nil {
		t.Fatal("changed payload admitted")
	}
	if _, err = os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("failed consent published output", err)
	}
	manifest, err := s.ExportTraining(ctx, options.TaskID, output, consent, samples)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.GoldLabels || manifest.NetworkUsed || manifest.Consent.SamplesDigest != c.HashBytes(samples) {
		t.Fatal("training authority forged")
	}
	if _, err = s.ControlsWithID(ctx, options.TaskID, "cancel", "training-cancel"); err != nil {
		t.Fatal(err)
	}
	plan, err := s.DeletionPreview(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteContent(ctx, DeletionCommand{CommandID: "training-delete", Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(output, "samples.json")); !os.IsNotExist(err) {
		t.Fatal("training derivative survived task deletion", err)
	}
}
