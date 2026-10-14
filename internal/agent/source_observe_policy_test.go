package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestSourceObservationListsMetadataAndReadsOnlyAdmittedFile(t *testing.T) {
	ctx := context.Background()
	session, options := retrievalReportFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer session.Close()
	_, doc, err := session.Load(ctx, options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := session.Archive.ReadManifest(doc.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Snapshot.Entries {
		if entry.Path == "secret.txt" {
			if err = os.Remove(filepath.Join(session.directory, "artifacts", "tasks", doc.TaskID, "blobs", entry.Hash[:2], entry.Hash)); err != nil {
				t.Fatal(err)
			}
		}
	}
	value, err := session.Observe(ctx, options.TaskID, Observation{Kind: "source-list", Query: "a.txt", Limit: 64})
	if err != nil {
		t.Fatal("metadata listing hydrated the unrelated missing blob:", err)
	}
	listing := value.(SourceMatches)
	if len(listing.Entries) != 1 || listing.Entries[0].Path != "a.txt" {
		t.Fatal("listing did not preserve the exact readable source:", listing)
	}
	value, err = session.Observe(ctx, options.TaskID, Observation{Kind: "source-page", Candidate: doc.Candidate.SnapshotDigest, Path: "a.txt", Limit: 2})
	if err != nil || string(value.(SourcePage).Bytes) != "be" {
		t.Fatal("selected file read hydrated unrelated source:", err)
	}
	restrictions, err := c.CanonicalV1(config.Authority{SchemaVersion: 1, Revision: 2, Restrictions: config.Privacy{SensitivePaths: []string{"secret.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(doc.Config.Authority.Path, restrictions, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = session.Observe(ctx, options.TaskID, Observation{Kind: "source-page", Candidate: doc.Candidate.SnapshotDigest, Path: "secret.txt", Limit: 2})
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != c.PolicyDenied {
		t.Fatal("denied read touched the missing source instead of refusing at admission:", err)
	}
}
