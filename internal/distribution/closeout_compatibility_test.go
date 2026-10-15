package distribution

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestNewAuthorityBlocksBootstrapAndMetadataIncompatiblePackages(t *testing.T) {
	for _, capability := range []string{RuntimeCapabilityPrivacyBootstrapV1, RuntimeCapabilityPrivacyMetadataV1} {
		t.Run(capability, func(t *testing.T) {
			ctx := context.Background()
			parent := t.TempDir()
			directory := filepath.Join(parent, "store")
			session, err := agent.Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			if err = session.Close(); err != nil {
				t.Fatal(err)
			}
			manager, err := Open(filepath.Join(parent, "program"))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if err = manager.RegisterStore(directory); err != nil {
				t.Fatal(err)
			}
			currentRevision := strings.Repeat("1", 40)
			current, err := ParseArchive(fixtureArchiveCapabilities(t, currentRevision, supportedRuntimeCapabilities, nil), "zip", currentRevision, nativePlatform(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Install(ctx, current); err != nil {
				t.Fatal(err)
			}
			incompatibleRevision := strings.Repeat("2", 40)
			incompatible, err := ParseArchive(fixtureArchiveCapabilities(t, incompatibleRevision, fixtureCapabilitiesWithout(capability), nil), "zip", incompatibleRevision, nativePlatform(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Install(ctx, incompatible); err == nil || !strings.Contains(err.Error(), capability) {
				t.Fatal("durable authority protocol was ignored", capability, err)
			}
			status, err := manager.Status()
			if err != nil || status.Active.Revision != currentRevision {
				t.Fatal("incompatible package changed activation", status, err)
			}
		})
	}
}

func TestLLMWorkerCapabilityRetainedByDurableCancelledSchedule(t *testing.T) {
	digest := c.HashBytes([]byte("source"))
	lease := c.ReadWorkerLease{SchemaVersion: 1, ParentTask: "parent", ParentTaskSeq: 8, ParentDocumentDigest: digest, ParentCandidateDigest: digest, ChildTaskID: "child", Role: c.ReadWorkerRoleReview, SpecVersion: 2, PolicyEpoch: 3, KernelGeneration: 1, ContextProfileDigest: digest, OutputBudget: 4096, ClockDomain: strings.Repeat("a", 32), IssuedMillis: 0, DeadlineMillis: 1000}
	state := c.TaskState{TaskID: "parent", Deletion: &c.TaskDeletion{Status: "PURGED"}, GoalQueue: []c.GoalSchedule{{Role: c.ReadWorkerRoleReview, ReadWorkerLease: lease, Status: "CANCELLED"}}}
	used := map[string]bool{}
	if err := applyTaskRuntimeCapabilities(state, used); err != nil || !used[RuntimeCapabilityLLMReadWorkersV1] {
		t.Fatal("purge/cancellation lost required worker protocol", used, err)
	}
	state.GoalQueue[0].ReadWorkerLease.ClockDomain = "invalid"
	if err := applyTaskRuntimeCapabilities(state, map[string]bool{}); err == nil {
		t.Fatal("invalid worker protocol binding accepted")
	}
}
