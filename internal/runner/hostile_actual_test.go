package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestActualDockerHostileProcessMatrix(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned real engine required")
	}
	broker, err := OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	source := t.TempDir()
	if err = os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	scenarios := []struct {
		name, script                string
		timeout, truncated, nonzero bool
	}{
		{"descendants-ignore-term", `trap '' TERM; sh -c 'trap "" TERM; sleep 300 & wait' & wait`, true, false, true},
		{"output-flood", `while :; do printf 'hostile-output-012345678901234567890123456789\n'; done`, false, true, true},
		{"scratch-exhaustion", `dd if=/dev/zero of=/tmp/fill bs=1M count=16; code=$?; rm -f /tmp/fill; exit "$code"`, false, false, true},
		{"pid-exhaustion", `i=0; while [ "$i" -lt 64 ]; do sleep 2 & i=$((i+1)); done; wait`, false, false, true},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			p := DefaultProfile(image)
			p.TimeoutSeconds = 3
			p.MaxOutputBytes = 2048
			p.ScratchBytes = 1 << 20
			p.Pids = 16
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			result, err := broker.Run(ctx, source, p, Invocation{CandidateDigest: c.HashBytes([]byte(scenario.name)), Argv: []string{"/bin/sh", "-c", scenario.script}})
			if err != nil || !result.ProcessTreeQuiescent || !result.ProtectedResultChannel {
				t.Fatal("unproved hostile cleanup", result, err)
			}
			if scenario.timeout && !result.TimedOut || scenario.truncated && !result.OutputTruncated || scenario.nonzero && result.ExitCode == 0 {
				t.Fatal("hostile bound not observed", result)
			}
			if len(result.Stdout) > 2048 || len(result.Stderr) > 2048 {
				t.Fatal("output quota escaped")
			}
			raw, _ := json.Marshal(struct {
				Name                           string
				DurationMillis                 int64
				TimedOut, Truncated, Quiescent bool
				Exit                           int
			}{scenario.name, result.DurationMillis, result.TimedOut, result.OutputTruncated, result.ProcessTreeQuiescent, result.ExitCode})
			t.Log(string(raw))
		})
	}
}
func TestActualDockerPersistentFenceInventoryAndReopen(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned real engine required")
	}
	broker, err := OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err = os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	lease := testLease()
	lease.InstanceID = fmt.Sprintf("%032x", time.Now().UnixNano())
	lease.ID = lease.InstanceID
	targets := []Target{}
	capsules := []Capsule{}
	for ordinal := 1; ordinal <= 16; ordinal++ {
		target, capsule, err := PrepareTarget(Target{Owner: Ownership{lease, "CHECK", ordinal}, Source: source, Profile: DefaultProfile(image), Invocation: Invocation{CandidateDigest: c.HashBytes(nil), Argv: []string{"/bin/sh", "-c", "exit 0"}}})
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target)
		capsules = append(capsules, capsule)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	started := time.Now()
	receipt, err := broker.Reconcile(ctx, lease, 2, targets)
	if err != nil {
		broker.Close()
		t.Fatal(receipt, err)
	}
	// Test fixture never dispatched an old worker. Only these exact, proven IDs
	// are reclaimed at test end; production never prunes name fences.
	defer func() {
		cleanup, _ := OpenDocker()
		if cleanup == nil {
			return
		}
		defer cleanup.Close()
		for _, item := range receipt.Items {
			cleanup.control(context.Background(), "rm", "--force", item.FenceContainerID)
		}
	}()
	if err = ValidateCleanupCapsules(receipt, lease, 2, capsules); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(started).Milliseconds()
	info, err := broker.BackendInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	broker.Close()
	reopened, err := OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	started = time.Now()
	again, err := reopened.Reconcile(ctx, lease, 3, targets)
	if err != nil || ValidateCleanupCapsules(again, lease, 3, capsules) != nil {
		t.Fatal("reopen lost fences", again, err)
	}
	for i, item := range again.Items {
		if item.FenceContainerID != receipt.Items[i].FenceContainerID || len(item.RemovedIDs) != 0 {
			t.Fatal("durable name identity replaced")
		}
	}
	view, err := reopened.Capacity(ctx, lease.InstanceID)
	if err != nil || view.Subjects != 16 || view.Available != MaxInstanceSubjects-16 {
		t.Fatal(view, err)
	}
	// An arbitrarily late old create cannot acquire any held deterministic name.
	labels, _ := capsules[0].labels()
	if _, err = reopened.control(ctx, createArguments(capsules[0].name(), targets[0].Source, targets[0].Profile, targets[0].Invocation, labels)...); err == nil {
		t.Fatal("delayed create resurrected code")
	}
	raw, _ := json.Marshal(struct {
		Backend                  BackendInfo
		Capacity                 Capacity
		ColdMillis, ReopenMillis int64
		Identity                 string
	}{info, view, cold, time.Since(started).Milliseconds(), "SAME_ENGINE_DURABLE_NAMES"})
	t.Log(strings.TrimSpace(string(raw)))
}
