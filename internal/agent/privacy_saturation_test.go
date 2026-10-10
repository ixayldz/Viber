package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// These are registry metadata fixtures, not successful kernel task deletions.
// Every seeded record still has the production schema, chain and admission.
func privacyRegistryFixture(tb testing.TB) (*os.Root, PrivacyAuthority, ManagedCopy) {
	tb.Helper()
	root, err := freshPrivate(filepath.Join(tb.TempDir(), "authority"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { root.Close() })
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		tb.Fatal(err)
	}
	authority := PrivacyAuthority{1, newID(""), root.Name(), physical}
	raw, err := c.CanonicalV1(authority)
	if err != nil {
		tb.Fatal(err)
	}
	if err = fileguard.Publish(root, "authority.json", raw); err != nil {
		tb.Fatal(err)
	}
	if err = root.Mkdir("records", 0700); err != nil {
		tb.Fatal(err)
	}
	output, err := os.OpenRoot(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	copyID, err := fileguard.DirectoryIdentity(output)
	directory := output.Name()
	output.Close()
	if err != nil {
		tb.Fatal(err)
	}
	return root, authority, ManagedCopy{Kind: "SUPPORT", Directory: directory, PhysicalRoot: copyID, Tasks: []string{"capacity-fixture"}, Files: []BackupFile{}}
}

func privacyFixtureRecord(tb testing.TB, view privacyView, copy ManagedCopy) privacyRecord {
	tb.Helper()
	if view.Sequence < privacyWorkRecordLimit {
		return privacyRecord{Type: "MANAGED_COPY", Copy: &copy}
	}
	return privacyControlFixtureRecord(tb, view)
}

func privacyControlFixtureRecord(tb testing.TB, view privacyView) privacyRecord {
	tb.Helper()
	plan := DeletionPlan{
		SchemaVersion: 1, TaskID: fmt.Sprintf("capacity-control-%04d", view.Watermark+1), TaskSequence: 1,
		DocumentDigest: c.HashBytes(nil), AuthorityDigest: c.HashBytes([]byte("capacity-fixture")),
		OwnerScopesDigest: c.HashBytes(nil), RegistrySequence: view.Sequence, RegistryTail: view.Tail,
		Watermark: view.Watermark + 1, Scope: "TASK_CONTENT", Objects: []artifact.Object{},
		Copies: []ManagedCopy{}, Stores: []DeletionStore{},
	}
	plan.ObjectsDigest = deletionObjectsDigest(plan)
	plan.Digest = deletionDigest(plan)
	command := DeletionCommand{CommandID: "control-fixture-" + plan.TaskID, Plan: plan}
	if err := validateDeletionCommand(command); err != nil {
		tb.Fatal(err)
	}
	return privacyRecord{Type: "DELETE_INTENT", Command: &command}
}

func advancePrivacyFixture(tb testing.TB, view *privacyView, record privacyRecord) []byte {
	tb.Helper()
	record.SchemaVersion, record.Sequence, record.Previous = 1, view.Sequence+1, view.Tail
	raw, err := c.CanonicalV1(record)
	if err != nil {
		tb.Fatal(err)
	}
	view.Sequence, view.Tail = record.Sequence, c.HashBytes(raw)
	if record.Command != nil {
		view.Watermark++
	}
	return raw
}

func TestPrivacyFullJournalSaturationPreservesControlWatermarkOnTenReopens(t *testing.T) {
	root, authority, copy := privacyRegistryFixture(t)
	lock, err := privacyLock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	view := privacyView{}
	for view.Sequence < privacyRecordLimit {
		record := privacyFixtureRecord(t, view, copy)
		if err = appendPrivacy(root, view, record); err != nil {
			lock.Close()
			t.Fatal("physical admission failed before expected limit", view.Sequence, err)
		}
		advancePrivacyFixture(t, &view, record)
	}
	before, err := readPrivacy(root)
	if err != nil || before.Sequence != privacyRecordLimit || before.Watermark != privacyRecordLimit-privacyWorkRecordLimit || len(before.Deletions) != 512 || len(before.Copies) != 1 {
		lock.Close()
		t.Fatal("physical saturation replay mismatch", before.Sequence, before.Watermark, err)
	}
	bytesBefore, err := privacyMetadataBytes(root)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	if err = appendPrivacy(root, before, privacyFixtureRecord(t, before, copy)); err == nil {
		lock.Close()
		t.Fatal("record 2049 was published")
	}
	bytesAfter, err := privacyMetadataBytes(root)
	if err != nil || bytesAfter != bytesBefore {
		lock.Close()
		t.Fatal("rejected control write changed journal bytes", err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 10; reopen++ {
		opened, err := openPrivacyRoot(authority)
		if err != nil {
			t.Fatal(err)
		}
		after, readErr := readPrivacy(opened)
		err = opened.Close()
		if readErr != nil || err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("saturated registry replay changed", reopen, readErr, err)
		}
	}
}

func TestPrivacyPhysicalMetadataBytePressureRejectsBeforeIntentPublication(t *testing.T) {
	root, authority, _ := privacyRegistryFixture(t)
	lock, err := privacyLock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	view := privacyView{}
	for {
		record := privacyControlFixtureRecord(t, view)
		plan := &record.Command.Plan
		for i := 0; i < 10000; i++ {
			path, err := artifact.ScopedBlobPath(plan.TaskID, fmt.Sprintf("%064x", i))
			if err != nil {
				t.Fatal(err)
			}
			plan.Objects = append(plan.Objects, artifact.Object{Path: path, Digest: c.HashBytes(nil), Size: 0})
		}
		plan.ObjectsDigest, plan.Digest = deletionObjectsDigest(*plan), ""
		plan.Digest = deletionDigest(*plan)
		if err = validateDeletionCommand(*record.Command); err != nil {
			t.Fatal(err)
		}
		candidate := view
		raw := advancePrivacyFixture(t, &candidate, record)
		retained, err := privacyMetadataBytes(root)
		if err != nil {
			t.Fatal(err)
		}
		if privacyMetadataAdmission(view.Sequence, retained, int64(len(raw))*2, true) != nil {
			if retained < privacyMetadataLimit-int64(len(raw))*2 || retained <= privacyWorkMetadataLimit || view.Sequence >= 100 {
				t.Fatal("fixture did not reach the physical byte boundary", retained, view.Sequence)
			}
			t.Logf("METADATA_BYTE_PRESSURE records=%d retained_bytes=%d temporary_bytes=%d denied_watermark=%d", view.Sequence, retained, int64(len(raw))*2, candidate.Watermark)
			if err = appendPrivacy(root, view, record); err == nil {
				t.Fatal("over-budget metadata intent published")
			}
			if after, err := privacyMetadataBytes(root); err != nil || after != retained {
				t.Fatal("rejected record changed retained bytes", err)
			}
			if privacyMetadataAdmission(view.Sequence, retained, 4096, false) == nil {
				t.Fatal("ordinary writer used deletion's byte class")
			}
			opened, err := openPrivacyRoot(authority)
			if err != nil {
				t.Fatal(err)
			}
			recovered, readErr := readPrivacy(opened)
			opened.Close()
			if readErr != nil || recovered.Sequence != view.Sequence || recovered.Tail != view.Tail || recovered.Watermark != view.Watermark || len(recovered.Deletions) != int(view.Watermark) {
				t.Fatal("byte-pressure reopen changed deletion state", readErr)
			}
			break
		}
		if err = appendPrivacy(root, view, record); err != nil {
			t.Fatal("control write failed before byte boundary", view.Sequence, err)
		}
		view = candidate
	}
}

func BenchmarkPrivacyRegistryReplayAndInventory(b *testing.B) {
	for _, count := range []int{0, 256, 1024, 1536, 2048} {
		b.Run(fmt.Sprintf("records-%d", count), func(b *testing.B) {
			root, _, copy := privacyRegistryFixture(b)
			view := privacyView{}
			total := int64(0)
			for i := 0; i < count; i++ {
				record := privacyFixtureRecord(b, view, copy)
				raw := advancePrivacyFixture(b, &view, record)
				if err := fileguard.Publish(root, filepath.Join("records", fmt.Sprintf("%010d.json", view.Sequence)), raw); err != nil {
					b.Fatal(err)
				}
				total += int64(len(raw))
			}
			b.Run("replay", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					actual, err := readPrivacy(root)
					if err != nil || actual.Sequence != int64(count) {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(total), "logical-B/op")
			})
			b.Run("inventory", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					actual, err := privacyMetadataBytes(root)
					if err != nil || actual != total {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
