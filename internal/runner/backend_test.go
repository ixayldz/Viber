package runner

import (
	"context"
	"encoding/json"
	"fmt"

	"testing"
)

func backendFixture(rootless bool) map[string]any {
	options := []string{"name=seccomp,profile=builtin"}
	if rootless {
		options = append(options, "name=rootless")
	}
	return map[string]any{"ID": "fixture-engine", "OSType": "linux", "ServerVersion": "test", "SecurityOptions": options, "CgroupVersion": "2", "CgroupDriver": "systemd", "MemoryLimit": true, "SwapLimit": true, "CpuCfsQuota": true, "CpuCfsPeriod": true, "PidsLimit": true}
}
func TestBackendRequiresActualRootlessResourceControllersAndStableEngine(t *testing.T) {
	for _, mode := range []string{"supported-rootless", "rootful", "missing-pids", "no-cpu", "no-swap", "no-memory", "no-pids", "rootless-no-cgroup", "rootless-v1", "rootless-no-systemd", "missing-identity", "no-seccomp"} {
		t.Run(mode, func(t *testing.T) {
			info := backendFixture(mode != "rootful")
			switch mode {
			case "missing-pids":
				delete(info, "PidsLimit")
			case "no-cpu":
				info["CpuCfsQuota"] = false
			case "no-swap":
				info["SwapLimit"] = false
			case "no-memory":
				info["MemoryLimit"] = false
			case "no-pids":
				info["PidsLimit"] = false
			case "rootless-no-cgroup":
				info["CgroupDriver"] = "none"
			case "rootless-v1":
				info["CgroupVersion"] = "1"
			case "rootless-no-systemd":
				info["CgroupDriver"] = "cgroupfs"
			case "missing-identity":
				info["ID"] = ""
			case "no-seccomp":
				info["SecurityOptions"] = []string{"name=rootless"}
			}
			raw, _ := json.Marshal(info)
			actual, err := decodeBackend(raw)
			if mode == "supported-rootless" || mode == "rootful" {
				if err != nil || !actual.ResourceLimits {
					t.Fatal(actual, err)
				}
			} else if err == nil {
				t.Fatal("unsupported resource controller admitted", actual)
			}
		})
	}
	info := backendFixture(false)
	broker := &Docker{controlHook: func(context.Context, ...string) ([]byte, error) { return json.Marshal(info) }}
	if _, err := broker.BackendInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Outage has no successful result. A same-ID restart/reconnect is admissible.
	broker.controlHook = func(context.Context, ...string) ([]byte, error) { return nil, fmt.Errorf("engine disconnected") }
	if _, err := broker.BackendInfo(context.Background()); err == nil {
		t.Fatal("outage accepted")
	}
	broker.controlHook = func(context.Context, ...string) ([]byte, error) { return json.Marshal(info) }
	if _, err := broker.BackendInfo(context.Background()); err != nil {
		t.Fatal("same engine restart rejected", err)
	}
	info["ID"] = "replacement-engine"
	if _, err := broker.BackendInfo(context.Background()); err == nil {
		t.Fatal("replacement engine inherited authority")
	}
}
func TestPersistentFenceCapacityStopsWorkAndPreservesCleanupReserve(t *testing.T) {
	broker, engine := newLeaseEngine(t, 1)
	engine.add(0, false)
	for i := 1; i < MaxInstanceSubjects; i++ {
		capsule := engine.capsules[0]
		capsule.Owner.Lease.ID = fmt.Sprintf("%032x", i+1000)
		id := fmt.Sprintf("%064x", i+100000)
		entry := testInspected(engine.targets[0], capsule, id, true)
		engine.entries[id] = entry
	}
	view, err := broker.Capacity(context.Background(), testLease().InstanceID)
	if err != nil || view.Subjects != MaxInstanceSubjects || view.Available != 0 || view.PhysicalBytesKnown {
		t.Fatal(view, err)
	}
	if err = broker.admitCapacity(context.Background(), engine.targets[0].Owner); err == nil {
		t.Fatal("full permanent fences admitted work")
	}
	receipt, err := broker.Reconcile(context.Background(), testLease(), 2, engine.targets)
	if err != nil || !receipt.FencesHeld || !receipt.NoLiveSubjects {
		t.Fatal("control cleanup blocked by full capacity", receipt, err)
	}
	if len(engine.entries) != MaxInstanceSubjects || engine.removals != 1 || engine.creates != 1 {
		t.Fatal("capacity pressure pruned another group's fence")
	}
}
func TestLocalBackendEndpointNeverInheritsRemoteAuthority(t *testing.T) {
	for _, value := range []string{"tcp://127.0.0.1:2375", "ssh://root@host", "unix://relative/socket", "unix:///tmp/../other", "unix:///tmp/socket\n", "named-context"} {
		if _, err := localEndpoint(value); err == nil {
			t.Fatal("unsafe endpoint accepted", value)
		}
	}
	if value, err := localEndpoint(""); err != nil || value != "" {
		t.Fatal(value, err)
	}
}
