package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type restartFixture struct {
	EngineID string
	Lease    Lease
	Target   Target
	Capsule  Capsule
	OldID    string
}

func TestDisposableEnginePhase(t *testing.T) {
	phase := os.Getenv("VIBER_ENGINE_MATRIX_PHASE")
	directory := os.Getenv("VIBER_ENGINE_MATRIX_DIR")
	if (phase != "before" && phase != "after") || directory == "" {
		t.Skip("explicit disposable daemon phase required")
	}
	broker, err := OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	backend, err := broker.BackendInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "restart.json")
	if phase == "before" {
		source := filepath.Join(directory, "source")
		if err = os.Mkdir(source, 0755); err != nil {
			t.Fatal(err)
		}
		lease := testLease()
		lease.InstanceID = strings.Repeat("d", 32)
		lease.ID = strings.Repeat("e", 32)
		target, capsule, err := PrepareTarget(Target{Owner: Ownership{lease, "CHECK", 1}, Source: source, Profile: DefaultProfile(os.Getenv("VIBER_DOCKER_TEST_IMAGE")), Invocation: Invocation{CandidateDigest: c.HashBytes(nil), Argv: []string{"/bin/sh", "-c", "sleep 300 & wait"}}})
		if err != nil {
			t.Fatal(err)
		}
		labels, _ := capsule.labels()
		raw, err := broker.control(ctx, createArguments(capsule.name(), target.Source, target.Profile, target.Invocation, labels)...)
		if err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(string(raw))
		if !validContainerID(id) {
			t.Fatal("unbound identity")
		}
		if _, err = broker.control(ctx, "start", id); err != nil {
			t.Fatal(err)
		}
		inspected, err := broker.inspect(ctx, id)
		if err != nil || !inspected.State.Running {
			t.Fatal("subject not running before restart", err)
		}
		fixture := restartFixture{backend.EngineID, lease, target, capsule, id}
		raw, err = c.CanonicalV1(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("BEFORE_RESTART_OWNED_SUBJECT_RUNNING", id)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture restartFixture
	if c.DecodeStrict(raw, &fixture) != nil || fixture.EngineID != backend.EngineID || !validContainerID(fixture.OldID) {
		t.Fatal("engine or fixture identity changed")
	}
	receipt, err := broker.Reconcile(ctx, fixture.Lease, 2, []Target{fixture.Target})
	if err != nil || ValidateCleanupCapsules(receipt, fixture.Lease, 2, []Capsule{fixture.Capsule}) != nil {
		t.Fatal("restart cleanup unproven", receipt, err)
	}
	if _, err = broker.control(ctx, "start", fixture.OldID); err == nil {
		t.Fatal("old identity restarted")
	}
	labels, _ := fixture.Capsule.labels()
	if _, err = broker.control(ctx, createArguments(fixture.Capsule.name(), fixture.Target.Source, fixture.Target.Profile, fixture.Target.Invocation, labels)...); err == nil {
		t.Fatal("late create admitted after restart")
	}
	info, err := broker.Capacity(ctx, fixture.Lease.InstanceID)
	if err != nil || info.Subjects != 1 {
		t.Fatal(info, err)
	}
	proof, _ := json.Marshal(struct {
		Backend  BackendInfo
		Receipt  CleanupReceipt
		Capacity Capacity
	}{backend, receipt, info})
	t.Log("AFTER_RESTART_FENCED", string(proof))
}
func TestDisposableRootlessCapability(t *testing.T) {
	if os.Getenv("VIBER_ENGINE_MATRIX_PHASE") != "rootless-probe" {
		t.Skip("explicit disposable rootless daemon required")
	}
	broker, err := OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := broker.BackendInfo(ctx)
	if !info.Rootless {
		t.Fatal("rootless engine was not observed", info, err)
	}
	if err == nil {
		t.Log("ROOTLESS_SUPPORTED", info)
		return
	}
	failure, ok := err.(*c.Error)
	if !ok || failure.Code != c.UnsupportedCapability {
		t.Fatal("untyped rootless rejection", err)
	}
	t.Log("ROOTLESS_DENIED_UNSUPPORTED_RESOURCE_CONTROLLERS", info, err)
}
