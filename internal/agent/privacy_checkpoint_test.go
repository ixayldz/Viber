package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ixayldz/Viber/internal/store"
)

func TestPrivacyCheckpointRetainsFullStateAcrossJournalRetirementAndReopen(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Text: "checkpoint", UsageKnown: true}}, false)
	defer s.Close()
	if _, err := s.SetRetention(ctx, options.TaskID, RetentionOptions{CommandID: "cp-retention", Mode: "KEEP"}); err != nil {
		t.Fatal(err)
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := readPrivacy(root)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.CheckpointPrivacyMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, err := readPrivacy(root)
	if err != nil {
		t.Fatal(err)
	}
	before.RecordCount = 0
	after.RecordCount = 0
	if !reflect.DeepEqual(before, after) {
		t.Fatal("checkpoint changed full replayed privacy state")
	}
	if !checkpoint.Supported || checkpoint.Sequence < 1 {
		t.Fatal("checkpoint source state missing", checkpoint)
	}
	capacity, err := s.PrivacyMetadataCapacity(ctx)
	if err != nil || capacity.Records != 0 || capacity.Sequence != before.Sequence {
		t.Fatal("capacity confused monotonic lineage with retained journal records", capacity, err)
	}
	if _, err = root.Lstat(filepath.Join("records", "0000000001.json")); !os.IsNotExist(err) {
		t.Fatal("exact receipt did not retire source record", err)
	}
	if _, err = s.SetRetention(ctx, options.TaskID, RetentionOptions{CommandID: "cp-retention-next", Mode: "KEEP", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckpointPrivacyMetadata(ctx); err != nil {
		t.Fatal("second checkpoint failed", err)
	}
	directory := s.directory
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, _, err = reopened.Load(ctx, options.TaskID); err != nil {
		t.Fatal(err)
	}
	// Losing a retirement receipt cannot turn missing source records into proof.
	anchor, digest, err := currentPrivacyCheckpointAnchor(root)
	if err != nil || anchor == nil {
		t.Fatal(err)
	}
	receipt := privacyCheckpointRetirementPath(digest)
	if err = root.Rename(receipt, receipt+".held"); err != nil {
		t.Fatal(err)
	}
	if _, err = readPrivacy(root); err == nil {
		t.Fatal("missing retirement receipt accepted absent source records")
	}
	if err = root.Rename(receipt+".held", receipt); err != nil {
		t.Fatal(err)
	}
}

func TestPrivacyCheckpointReusesOwnerThroughEquivalentDirectorySpelling(t *testing.T) {
	ctx := context.Background()
	s, _, _ := startFixture(t, []Turn{{Text: "alias", UsageKnown: true}}, false)
	defer s.Close()
	canonical, err := filepath.EvalSymlinks(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, s.directory)
	if err != nil {
		// Windows hosted checkout and temporary fixtures can be on separate
		// volumes. An absolute /./ spelling still exercises owner canonicalization.
		relative = filepath.Dir(s.directory) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(s.directory)
	}
	scope := privacyScope{InstanceID: s.instance.ID, Directory: canonical, PhysicalRoot: s.instance.PhysicalRoot}
	called := false
	err = withPrivacyScopeStore(ctx, *s.privacy, scope, s.Journal, s.instance, relative, func(journal *store.Store) error {
		if journal != s.Journal {
			t.Fatal("equivalent owner spelling opened another writer")
		}
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatal("canonical scope did not reuse equivalent current owner", err)
	}
}
