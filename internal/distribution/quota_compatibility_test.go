package distribution

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/resource"
)

func TestInstallAndRollbackRespectPhysicalQuotaProtocols(t *testing.T) {
	for _, protocol := range []string{RuntimeCapabilityPhysicalQuotaIntentV1, RuntimeCapabilityPhysicalQuotaHandoffV1, RuntimeCapabilityPhysicalQuotaPartitionV1} {
		t.Run(protocol, func(t *testing.T) {
			ctx := context.Background()
			platform := nativePlatform(t)
			parent := t.TempDir()
			manager, err := Open(filepath.Join(parent, "program"))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			directory := filepath.Join(parent, "store")
			session, err := agent.Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			if err = session.Close(); err != nil {
				t.Fatal(err)
			}
			if err = manager.RegisterStore(directory); err != nil {
				t.Fatal(err)
			}
			legacyRevision := strings.Repeat("7", 40)
			legacy, err := ParseArchive(fixtureArchiveCapabilities(t, legacyRevision, fixtureCapabilitiesWithout(protocol), nil), "zip", legacyRevision, platform)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Install(ctx, legacy); err != nil {
				t.Fatal("unused tables blocked compatible legacy package", err)
			}
			session, err = agent.OpenExisting(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			request := resource.Request{Writer: "compatibility", Family: "QUOTA_PROTOCOL", RequestDigest: c.HashBytes([]byte("intent")), TargetRoot: filepath.Join(parent, ".stage"), UpperBytes: 1 << 20, UpperEntries: 32, Class: resource.Payload}
			if protocol == RuntimeCapabilityPhysicalQuotaIntentV1 {
				_, _, err = session.PhysicalQuota.ReserveOrMatchWithIntent(ctx, request, []byte("stable-binding"), []byte("{}"))
			} else if protocol == RuntimeCapabilityPhysicalQuotaHandoffV1 {
				var allocation resource.Allocation
				allocation, err = session.PhysicalQuota.Reserve(ctx, request)
				if err == nil {
					err = allocation.PrepareHandoff(ctx, filepath.Join(parent, "final"))
				}
			} else {
				var allocation resource.Allocation
				allocation, err = session.PhysicalQuota.Reserve(ctx, request)
				if err == nil {
					err = allocation.PreparePartition(ctx, resource.IndependentOwnerPartition)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = session.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Install(ctx, legacy); err == nil {
				t.Fatal("older package ignored a durable quota protocol")
			}
			currentRevision := strings.Repeat("8", 40)
			current, err := ParseArchive(fixtureArchiveCapabilities(t, currentRevision, supportedRuntimeCapabilities, nil), "zip", currentRevision, platform)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Install(ctx, current); err != nil {
				t.Fatal("protocol-aware package was rejected", err)
			}
			if _, err = manager.RollbackContext(ctx, legacyRevision); err == nil {
				t.Fatal("rollback ignored durable quota protocol records")
			}
			state, err := manager.Status()
			if err != nil || state.Active.Revision != currentRevision {
				t.Fatal("refused rollback changed activation", state, err)
			}
		})
	}
}
