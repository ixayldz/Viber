package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

func TestPrivacyCheckpointWaitsForLiveRestoreOperation(t *testing.T) {
	ctx := context.Background()
	s, _, _ := startFixture(t, []Turn{{Text: "checkpoint gate", UsageKnown: true}}, false)
	defer s.Close()
	if err := s.ensurePrivacy(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err := privacyOperation(ctx, *s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operation.Close() })
	_, checkpointErr := s.CheckpointPrivacyMetadata(ctx)
	var typed *c.Error
	if !errors.As(checkpointErr, &typed) || typed.Code != c.StoreOwned {
		t.Fatalf("checkpoint advanced under a live restore lease: %v", checkpointErr)
	}
	if err = operation.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckpointPrivacyMetadata(ctx); err != nil {
		t.Fatalf("checkpoint did not resume after the operation lease closed: %v", err)
	}
}

func TestPrivacyCheckpointBlocksAnUnfinishedRestoreStage(t *testing.T) {
	ctx := context.Background()
	s, options, _ := startFixture(t, []Turn{{Text: "checkpoint stage gate", UsageKnown: true}}, false)
	defer s.Close()
	if err := s.ensurePrivacy(ctx); err != nil {
		t.Fatal(err)
	}
	stageRoot, err := freshPrivate(filepath.Join(t.TempDir(), "restore-stage"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.allocateManagedCopy(ctx, stageRoot, "RESTORE_STAGE", []string{options.TaskID}); err != nil {
		stageRoot.Close()
		t.Fatal(err)
	}
	if err = stageRoot.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = s.CheckpointPrivacyMetadata(ctx)
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != c.StaleAuthority {
		t.Fatalf("checkpoint did not block an unfinished restore allocation: %v", err)
	}
}

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
