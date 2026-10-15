package distribution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

func followupCapabilityTask(t *testing.T, directory string) *agent.Session {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "hello.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := agent.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	_, err = session.Create(context.Background(), agent.StartOptions{
		Root: source, Prompt: []byte("Inspect the private greeting."), TaskID: "compat-followup",
		Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true,
		Fixture: []byte(`{"schema_version":1,"turns":[{"text":"done","usage_known":true}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestRollbackRespectsDurableFollowupCapabilities(t *testing.T) {
	for _, capability := range []string{RuntimeCapabilityProjectMemoryConflictMetadataV1, RuntimeCapabilityGCReceiptCompactionV1, RuntimeCapabilityNativeEnginePartitionV1} {
		t.Run(capability, func(t *testing.T) {
			ctx := context.Background()
			parent := t.TempDir()
			manager, err := Open(filepath.Join(parent, "program"))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			directory := filepath.Join(parent, "store")
			session := followupCapabilityTask(t, directory)
			if err = session.Close(); err != nil {
				t.Fatal(err)
			}
			if err = manager.RegisterStore(directory); err != nil {
				t.Fatal(err)
			}
			legacyRevision := strings.Repeat("5", 40)
			legacyCapabilities := fixtureCapabilitiesWithout(capability)
			legacy, err := ParseArchive(fixtureArchiveCapabilities(t, legacyRevision, legacyCapabilities, nil), "zip", legacyRevision, nativePlatform(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Install(ctx, legacy); err != nil {
				t.Fatal("unused followup protocol blocked older package", err)
			}
			session, err = agent.OpenExisting(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			if capability == RuntimeCapabilityProjectMemoryConflictMetadataV1 {
				_, err = session.RecordProjectMemory(ctx, "compat-followup", "record-witness", "FACT", "greeting", "The greeting is hello.")
			} else if capability == RuntimeCapabilityNativeEnginePartitionV1 {
				bindNativeEnginePartitionForCompatibility(t, ctx, session, parent)
			} else {
				_, err = session.Run(ctx, "compat-followup")
				if err == nil {
					_, err = session.Archive.PutBytes("compat-followup", []byte("orphan for exact receipt compaction"))
				}
				if err == nil {
					var plan agent.GCPlan
					plan, err = session.GCPreview(ctx)
					if err == nil && len(plan.Objects) != 1 {
						t.Fatal("expected exactly one private orphan", plan)
					}
					if err == nil {
						_, err = session.CollectGarbage(ctx, agent.GCCommand{CommandID: "compact-compat", Plan: plan})
					}
				}
			}
			if err != nil {
				_ = session.Close()
				t.Fatal(err)
			}
			if err = session.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Install(ctx, legacy); err == nil || !strings.Contains(err.Error(), capability) {
				t.Fatal("older package did not refuse the exact durable protocol", capability, err)
			}
			currentRevision := strings.Repeat("6", 40)
			current, err := ParseArchive(fixtureArchiveCapabilities(t, currentRevision, supportedRuntimeCapabilities, nil), "zip", currentRevision, nativePlatform(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Install(ctx, current); err != nil {
				t.Fatal("protocol-aware package was rejected", err)
			}
			if _, err = manager.RollbackContext(ctx, legacyRevision); err == nil || !strings.Contains(err.Error(), capability) {
				t.Fatal("rollback ignored the exact durable protocol", capability, err)
			}
			status, err := manager.Status()
			if err != nil || status.Active.Revision != currentRevision {
				t.Fatal("refused rollback changed selected version", status, err)
			}
		})
	}
}

func bindNativeEnginePartitionForCompatibility(t *testing.T, ctx context.Context, session *agent.Session, parent string) {
	t.Helper()
	destination := filepath.Join(parent, "native-report")
	bytes, entries := resource.PartitionStartupEnvelope()
	allocation, err := session.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "evaluation", Family: "EVALUATION_REPORT", RequestDigest: c.HashBytes([]byte("native-partition-compatibility")), TargetRoot: destination, UpperBytes: bytes, UpperEntries: entries, Class: resource.Payload})
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.PreparePartition(ctx, resource.EvaluationReportPartition); err == nil {
		err = allocation.Begin(ctx)
	}
	if err == nil {
		err = os.Mkdir(destination, 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err = fileguard.Private(root); err == nil {
		err = allocation.BindPartitionRoot(ctx)
	}
	closeErr := root.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	ownerPath := filepath.Join(destination, "owner")
	child, err := session.OpenIndependentEvaluatorPartition(ctx, ownerPath, allocation)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	ownerRoot, err := os.OpenRoot(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerRoot.Close()
	raw, err := fileguard.ReadRegular(ownerRoot, "runtime-instance.json", 4096)
	var instance agent.RuntimeInstance
	if err != nil || c.DecodeStrict(raw, &instance) != nil {
		t.Fatal("fixture owner has no valid instance", err)
	}
	raw, err = c.CanonicalV1(agent.NativeEngineBinding{SchemaVersion: 1, InstanceID: instance.ID, EngineDigest: c.HashBytes([]byte("fixture-engine")), EndpointDigest: c.HashBytes([]byte("fixture-endpoint"))})
	if err == nil {
		err = fileguard.Publish(ownerRoot, "native-engine.json", raw)
	}
	if err == nil {
		err = ownerRoot.Close()
	}
	if err == nil {
		err = child.Close()
	}
	if err == nil {
		err = allocation.CommitPartition(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	used, err := session.PhysicalQuota.NativeEnginePartitionFeatureInUse(ctx)
	if err != nil || !used {
		t.Fatal("native bootstrap receipt capability was not detected", used, err)
	}
}
