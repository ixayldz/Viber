package agent

import (
	"context"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestGCCollectsOnlyOrphansAndRetainsHistoricalAndStandaloneScopes(t *testing.T) {
	ctx := context.Background()
	s, opts, source := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	defer s.Close()
	state, err := s.Run(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := s.Archive.Put("standalone", base)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.Archive.PutBytes(opts.TaskID, []byte("unreferenced interrupted write"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := artifact.ScopedBlobPath(opts.TaskID, orphan)
	if len(plan.Objects) != 1 || plan.Objects[0].Path != name {
		t.Fatal("unexpected reachability", plan.Objects)
	}
	command := GCCommand{CommandID: "gc-one", Plan: plan}
	first, err := s.CollectGarbage(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CollectGarbage(ctx, command)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("GC not idempotent", err)
	}
	if first.BytesRemoved != int64(len("unreferenced interrupted write")) {
		t.Fatal(first)
	}
	if _, err = os.Stat(filepath.Join(s.directory, "artifacts", filepath.FromSlash(name))); !os.IsNotExist(err) {
		t.Fatal("orphan retained", err)
	}
	if _, err = s.Archive.Get(standalone); err != nil {
		t.Fatal("standalone scope collected", err)
	}
	actual, _, err := s.Load(ctx, opts.TaskID)
	if err != nil || !reflect.DeepEqual(state, actual) {
		t.Fatal("GC changed task state", err)
	}
	if _, _, err = s.Inspect(ctx, opts.TaskID, 1); err != nil {
		t.Fatal("historical evidence lost", err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(ctx, backup, restored); err != nil {
		t.Fatal("GC invalidated backup closure", err)
	}
	raw, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(raw) != "before\r\n" {
		t.Fatal("GC touched source")
	}
}
func TestGCRejectsReachableObjectsForgedDigestsAndStalePlans(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer s.Close()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	_, doc, err := s.Load(ctx, opts.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.Archive.PutBytes(opts.TaskID, []byte("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	attacked := plan
	attacked.Objects = append([]artifact.Object{}, plan.Objects...)
	attacked.Objects[0].Digest = c.HashBytes([]byte("forged"))
	attacked.Digest = planDigest(attacked)
	if _, err = s.CollectGarbage(ctx, GCCommand{"bad-digest", attacked}); err == nil {
		t.Fatal("forged object accepted")
	}
	raw, err := s.Archive.GetBytes(opts.TaskID, doc.Spec.Inputs[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := artifact.ScopedBlobPath(opts.TaskID, doc.Spec.Inputs[0].Digest)
	attacked = plan
	attacked.Objects = append(append([]artifact.Object{}, plan.Objects...), artifact.Object{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
	sort.Slice(attacked.Objects, func(i, j int) bool { return attacked.Objects[i].Path < attacked.Objects[j].Path })
	attacked.Digest = planDigest(attacked)
	if _, err = s.CollectGarbage(ctx, GCCommand{"reachable", attacked}); err == nil {
		t.Fatal("reachable source accepted")
	}
	if _, err = s.Archive.GetBytes(opts.TaskID, orphan); err != nil {
		t.Fatal("invalid plan partially collected", err)
	}
	if _, err = s.RecordSteering(ctx, SteeringInput{CommandID: "new-input", TaskID: opts.TaskID, Text: "retain this raw input"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CollectGarbage(ctx, GCCommand{"stale", plan}); err == nil {
		t.Fatal("stale reference graph accepted")
	}
}
func TestGCReconcilesMissingFileOnlyAfterBoundIntent(t *testing.T) {
	ctx := context.Background()
	s, opts, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, false)
	defer s.Close()
	if _, err := s.Run(ctx, opts.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive.PutBytes(opts.TaskID, []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	plan, err := s.GCPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command := GCCommand{"interrupted", plan}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	raw, _ := c.CanonicalV1(command)
	base := filepath.Join("maintenance", "gc", c.HashBytes([]byte(command.CommandID)))
	if err = fileguard.Publish(root, filepath.Join(base, "intent.json"), raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Archive.RemoveOrphan(plan.Objects[0]); err != nil {
		t.Fatal(err)
	}
	result, err := s.CollectGarbage(ctx, command)
	if err != nil || len(result.Objects) != 1 || result.Objects[0].Status != "ALREADY_ABSENT_AFTER_INTENT" || result.BytesRemoved != 0 {
		t.Fatal(result, err)
	}
	conflicting := command
	conflicting.Plan.HasMore = !plan.HasMore
	conflicting.Plan.Digest = planDigest(conflicting.Plan)
	if _, err = s.CollectGarbage(ctx, conflicting); err == nil {
		t.Fatal("command ID conflict accepted")
	}
}
func TestGCUnknownEffectsPinWholeArchive(t *testing.T) {
	s, opts, _ := startFixture(t, []Turn{{Text: "lost", UsageKnown: false}}, true)
	defer s.Close()
	if _, err := s.Run(context.Background(), opts.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GCPreview(context.Background()); err == nil {
		t.Fatal("unknown risk collected")
	}
}
