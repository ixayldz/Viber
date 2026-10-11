package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestPrivacyPendingDeletionNamespaceCannotAuthorizeAnotherTask(t *testing.T) {
	for _, kind := range []string{"command", "purged-receipt"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, options, _ := deletionFixture(t)
			second := options
			second.TaskID = "second-task"
			if _, err := s.Create(ctx, second); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Run(ctx, second.TaskID); err != nil {
				t.Fatal(err)
			}
			plan, err := s.DeletionPreview(ctx, options.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			first := DeletionCommand{CommandID: "pending-first-delete", Plan: plan}
			otherPlan, err := s.DeletionPreview(ctx, second.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			root, err := openPrivacyRoot(*s.privacy)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := privacyLock(ctx, root)
			if err != nil {
				root.Close()
				t.Fatal(err)
			}
			view, err := readPrivacy(root)
			if err == nil {
				err = appendPrivacy(root, view, privacyRecord{Type: "DELETE_INTENT", Command: &first})
			}
			closeErr := lock.Close()
			root.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			before, err := s.Journal.ResourceLedger(ctx)
			if err != nil {
				t.Fatal(err)
			}
			currentRegistry, err := s.retentionView(ctx)
			if err != nil {
				t.Fatal(err)
			}
			otherPlan.RegistrySequence, otherPlan.RegistryTail, otherPlan.Watermark = currentRegistry.Sequence, currentRegistry.Tail, currentRegistry.Watermark+1
			otherPlan.Digest = deletionDigest(otherPlan)
			id := first.CommandID
			if kind == "purged-receipt" {
				id = "privacy-purge-" + c.HashBytes([]byte(id))
			}
			_, err = s.DeleteContent(ctx, DeletionCommand{CommandID: id, Plan: otherPlan})
			var typed *c.Error
			if !errors.As(err, &typed) || typed.Code != c.CommandIDConflict {
				t.Fatalf("pending family namespace reused: %v", err)
			}
			current, err := s.retentionView(ctx)
			if err != nil || current.Sequence != plan.RegistrySequence+1 || current.Watermark != plan.Watermark || len(current.Deletions) != 1 {
				t.Fatal("denial changed external intent", current.Sequence, current.Watermark, err)
			}
			if _, _, err := s.Load(ctx, second.TaskID); err != nil {
				t.Fatal("denial erased second task", err)
			}
			after, err := s.Journal.ResourceLedger(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("denial changed risk accounting", err)
			}
			result, err := s.DeleteContent(ctx, first)
			if err != nil || result.Status != "PURGED_MANAGED_LOCAL_CONTENT" {
				t.Fatal("original exact intent could not reconcile", result, err)
			}
		})
	}
}

func TestPrivacyReplayRejectsRehashedDeletionNamespaceCollision(t *testing.T) {
	for _, kind := range []string{"command", "purged-receipt", "restored-receipt", "restored-purged"} {
		t.Run(kind, func(t *testing.T) {
			root, authority, _ := privacyRegistryFixture(t)
			view := privacyView{}
			first := privacyControlFixtureRecord(t, view)
			first.Command.Plan.Stores = []DeletionStore{{Scope: privacyScope{authority.ID, root.Name(), authority.PhysicalRoot}, TaskSequence: 1, DocumentDigest: c.HashBytes(nil)}}
			first.Command.Plan.ObjectsDigest = deletionObjectsDigest(first.Command.Plan)
			first.Command.Plan.Digest = deletionDigest(first.Command.Plan)
			raw := advancePrivacyFixture(t, &view, first)
			if err := fileguard.Publish(root, filepath.Join("records", "0000000001.json"), raw); err != nil {
				t.Fatal(err)
			}
			second := privacyControlFixtureRecord(t, view)
			second.Command.CommandID = first.Command.CommandID
			if kind == "purged-receipt" {
				second.Command.CommandID = "privacy-purge-" + c.HashBytes([]byte(first.Command.CommandID))
			}
			if kind == "restored-receipt" {
				second.Command.CommandID = deletionCommandIDs(*first.Command)[2]
			}
			if kind == "restored-purged" {
				second.Command.CommandID = deletionCommandIDs(*first.Command)[3]
			}
			raw = advancePrivacyFixture(t, &view, second)
			if err := fileguard.Publish(root, filepath.Join("records", "0000000002.json"), raw); err != nil {
				t.Fatal(err)
			}
			if _, err := readPrivacy(root); err == nil {
				t.Fatal("rehashed cross-task namespace accepted")
			}
		})
	}
}

func retentionReplayFixture(tb testing.TB, root *os.Root, authority PrivacyAuthority, count int) {
	tb.Helper()
	authorityDigest, _ := c.Digest(authority)
	view := privacyView{}
	for i := 0; i < count; i++ {
		options := RetentionOptions{CommandID: fmt.Sprintf("replay-consent-%04d", i), ExpectedRevision: int64(i), Mode: "KEEP"}
		input, _ := retentionInputDigest("replay-task", options)
		policy := c.RetentionPolicy{SchemaVersion: 1, TaskID: "replay-task", Revision: int64(i + 1), Actor: "USER", Scope: "MANAGED_FAMILY_TASK_CONTENT", Mode: "KEEP", Clock: "SYSTEM_UTC", ChangedAt: "2026-10-11T00:00:00Z", AuthorityDigest: authorityDigest, SourcePhysicalRoot: authority.PhysicalRoot, SourceTaskSequence: 1, SourceDocumentDigest: c.HashBytes(nil)}
		record := privacyRetentionRecord{CommandID: options.CommandID, InputDigest: input, Policy: policy}
		if err := record.validate(); err != nil {
			tb.Fatal(err)
		}
		raw := advancePrivacyFixture(tb, &view, privacyRecord{Type: "RETENTION_POLICY", Retention: &record})
		if err := fileguard.Publish(root, filepath.Join("records", fmt.Sprintf("%010d.json", view.Sequence)), raw); err != nil {
			tb.Fatal(err)
		}
	}
}

func TestPrivacyReplayRetainsEveryConsentRetryAndRefreshesAuthority(t *testing.T) {
	root, authority, _ := privacyRegistryFixture(t)
	retentionReplayFixture(t, root, authority, 256)
	first, err := readPrivacy(root)
	if err != nil || first.Sequence != 256 || len(first.RetentionCommands) != 256 || first.Retentions["replay-task"].Policy.Revision != 256 {
		t.Fatal(first.Sequence, err)
	}
	for i := 0; i < 3; i++ {
		current, err := readPrivacy(root)
		if err != nil || !reflect.DeepEqual(first, current) {
			t.Fatal("replay discarded historical dedup facts", err)
		}
	}
	// No cache survives a replay call; a replaced genesis cannot reuse a digest.
	authority.ID = "00000000000000000000000000000000"
	raw, _ := c.CanonicalV1(authority)
	if err := root.WriteFile("authority.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivacy(root); err == nil {
		t.Fatal("cached authority survived a replacement")
	}
}

// Synthetic valid registry consents measure replay, not real task permission,
// physical I/O, cold cache, p95 or a production performance threshold.
func BenchmarkPrivacyRetentionReplay(b *testing.B) {
	for _, count := range []int{256, 1024} {
		b.Run(fmt.Sprintf("records-%d", count), func(b *testing.B) {
			root, authority, _ := privacyRegistryFixture(b)
			retentionReplayFixture(b, root, authority, count)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				view, err := readPrivacy(root)
				if err != nil || view.Sequence != int64(count) || len(view.RetentionCommands) != count {
					b.Fatal(err)
				}
			}
		})
	}
}
