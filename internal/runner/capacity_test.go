package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestEngineCapacitySlotsAreSharedAcrossBrokersAndUseExactEngineScope(t *testing.T) {
	d1, engine := newLeaseEngine(t, 1)
	d2 := &Docker{controlHook: engine.control}
	ctx := context.Background()
	if _, err := d1.BackendInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d2.BackendInfo(ctx); err != nil {
		t.Fatal(err)
	}
	owner := engine.targets[0].Owner
	profile := engine.targets[0].Profile
	capsule := engine.capsules[0]

	// A lost marker acknowledgement is accepted only after the exact slot and
	// complete owner binding are inspected. A second broker reuses that slot.
	engine.capacityLostAck = true
	if err := d1.admitCapacity(ctx, capsule, profile); err != nil {
		t.Fatal(err)
	}
	if err := d2.admitCapacity(ctx, capsule, profile); err != nil {
		t.Fatal(err)
	}

	owner2 := owner
	owner2.Lease.ID = strings.Repeat("d", 32)
	owner2.Lease.InstanceID = strings.Repeat("e", 32)
	owner3 := owner
	owner3.Lease.ID = strings.Repeat("f", 32)
	owner3.Lease.InstanceID = strings.Repeat("1", 32)
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for _, item := range []struct {
		broker *Docker
		owner  Ownership
	}{{d1, owner2}, {d2, owner3}} {
		wait.Add(1)
		go func(broker *Docker, owner Ownership) {
			defer wait.Done()
			admitted := capsule
			admitted.Owner = owner
			errs <- broker.admitCapacity(ctx, admitted, profile)
		}(item.broker, item.owner)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	changedInvocation := capsule
	changedInvocation.InvocationDigest = c.HashBytes([]byte("different-invocation"))
	if err := d2.admitCapacity(ctx, changedInvocation, profile); err != nil {
		t.Fatal(err)
	}
	view, err := d1.Capacity(ctx, owner.Lease.InstanceID)
	if err != nil || view.CapacityMarkerSlots != 4 || view.Subjects != 2 || view.Available != MaxInstanceSubjects-2 || view.EngineLimit != MaxEngineCapacitySlots || view.EngineAvailable != MaxEngineCapacitySlots-4 || view.PhysicalBytesKnown {
		t.Fatal(view, err)
	}

	otherEngine, other := newLeaseEngine(t, 1)
	other.engineID = "replacement-engine"
	if _, err = otherEngine.BackendInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if err = otherEngine.admitCapacity(ctx, other.capsules[0], profile); err != nil {
		t.Fatal(err)
	}
	otherView, err := otherEngine.Capacity(ctx, owner.Lease.InstanceID)
	if err != nil || otherView.EngineID != "replacement-engine" || otherView.CapacityMarkerSlots != 1 {
		t.Fatal(otherView, err)
	}
	if engineCapacityNamespace("fixture-engine") == engineCapacityNamespace("replacement-engine") {
		t.Fatal("engine namespace did not bind the exact engine identity")
	}
	if other.capacityEntries[fmt.Sprintf("%064x", 1)].Name == engine.capacityEntries[fmt.Sprintf("%064x", 1)].Name {
		t.Fatal("different engines reused a marker name namespace")
	}
}

func TestFullEngineCapacityStopsRunBeforeWorkloadCreate(t *testing.T) {
	broker, engine := newLeaseEngine(t, 1)
	if _, err := broker.BackendInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	profile := engine.targets[0].Profile
	for slot := 0; slot < MaxEngineCapacitySlots; slot++ {
		capsule := engine.capsules[0]
		owner := engine.targets[0].Owner
		owner.Lease.ID = fmt.Sprintf("%032x", slot+1000)
		if slot == 0 {
			owner.Lease = engine.targets[0].Owner.Lease
		}
		capsule.Owner = owner
		engine.addCapacity(slot, capsule, profile.Image)
	}
	newOwner := engine.targets[0].Owner
	newOwner.Lease.ID = fmt.Sprintf("%032x", MaxEngineCapacitySlots+1000)
	ctx, err := WithOwnership(context.Background(), newOwner, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, err = broker.Run(ctx, t.TempDir(), profile, Invocation{CandidateDigest: c.HashBytes([]byte("new-work")), Argv: []string{"/bin/subject"}})
	if err == nil || engine.creates != 0 || engine.capacityCreates != 0 {
		t.Fatal("full engine capacity reached workload create", err, engine.creates, engine.capacityCreates)
	}
	view, viewErr := broker.Capacity(context.Background(), newOwner.Lease.InstanceID)
	if viewErr != nil || view.CapacityMarkerSlots != MaxEngineCapacitySlots || view.EngineAvailable != 0 || view.PhysicalBytesKnown {
		t.Fatal(view, viewErr)
	}
}
