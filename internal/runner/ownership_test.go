package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func testLease() Lease {
	return Lease{1, strings.Repeat("a", 32), strings.Repeat("b", 32), c.HashBytes([]byte("task")), c.HashBytes([]byte("operation")), 1, 1, 1, strings.Repeat("c", 32), 120000}
}
func testInspected(target Target, capsule Capsule, id string, fence bool) inspected {
	inv := target.Invocation
	labels, _ := capsule.labels()
	if fence {
		inv = fenceInvocation(inv.CandidateDigest)
		labels["io.viber.runtime"] = fenceRuntime
		labels["io.viber.fence"] = "1"
	}
	p := target.Profile
	var info inspected
	info.ID = id
	info.Name = "/" + capsule.name()
	info.Config.Labels = labels
	info.Config.OpenStdin = inv.Stdin != nil
	info.Config.User = "65532:65532"
	info.Config.Image = p.Image
	info.Config.WorkingDir = "/workspace"
	info.Config.Entrypoint = inv.Argv[:1]
	info.Config.Cmd = inv.Argv[1:]
	info.HostConfig.ReadonlyRootfs = true
	info.HostConfig.NetworkMode = "none"
	info.HostConfig.CapDrop = []string{"ALL"}
	info.HostConfig.SecurityOpt = []string{"no-new-privileges=true", "seccomp=builtin"}
	info.HostConfig.Memory = p.MemoryBytes
	info.HostConfig.MemorySwap = p.MemoryBytes
	info.HostConfig.PidsLimit = p.Pids
	info.HostConfig.NanoCpus = p.CPUs * 1e9
	init := true
	info.HostConfig.Init = &init
	info.HostConfig.IpcMode = "private"
	info.HostConfig.Tmpfs = map[string]string{"/tmp": scratchOptions(p)}
	info.Mounts = []inspectedMount{{Destination: "/workspace", Source: target.Source, Type: "bind", Propagation: "rprivate"}}
	info.State.Status = "created"
	if !fence {
		info.State.Status = "running"
		info.State.Running = true
	}
	return info
}

type leaseEngine struct {
	targets  []Target
	capsules []Capsule
	entries  map[string]inspected
	next     int
	removals int
	creates  int
	race     bool
	lostAck  bool
}

func newLeaseEngine(t *testing.T, count int) (*Docker, *leaseEngine) {
	t.Helper()
	engine := &leaseEngine{entries: map[string]inspected{}, next: 1}
	for i := 1; i <= count; i++ {
		target, capsule, err := PrepareTarget(Target{Owner: Ownership{testLease(), "CHECK", i}, Source: t.TempDir(), Profile: DefaultProfile("golang@sha256:" + strings.Repeat("a", 64)), Invocation: Invocation{CandidateDigest: c.HashBytes([]byte("candidate")), Argv: []string{"/bin/subject"}}})
		if err != nil {
			t.Fatal(err)
		}
		engine.targets = append(engine.targets, target)
		engine.capsules = append(engine.capsules, capsule)
	}
	return &Docker{controlHook: engine.control}, engine
}
func (e *leaseEngine) add(position int, fence bool) string {
	id := fmt.Sprintf("%064x", e.next)
	e.next++
	e.entries[id] = testInspected(e.targets[position], e.capsules[position], id, fence)
	return id
}
func (e *leaseEngine) control(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch args[0] {
	case "info":
		return []byte(`{"OSType":"linux","SecurityOptions":["name=seccomp,profile=builtin"]}`), nil
	case "ps":
		ids := []string{}
		for id := range e.entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return []byte(strings.Join(ids, "\n")), nil
	case "inspect":
		identity := args[len(args)-1]
		for id, entry := range e.entries {
			if id == identity || entry.Name == "/"+identity {
				if len(args) > 4 && args[3] == "--format" {
					return []byte(id), nil
				}
				return json.Marshal([]inspected{entry})
			}
		}
		return nil, fmt.Errorf("container absent")
	case "rm":
		id := args[len(args)-1]
		if _, ok := e.entries[id]; !ok {
			return nil, fmt.Errorf("already absent")
		}
		delete(e.entries, id)
		e.removals++
		return []byte(id), nil
	case "create":
		e.creates++
		name := ""
		for i, arg := range args {
			if arg == "--name" {
				name = args[i+1]
			}
		}
		position := -1
		for i, capsule := range e.capsules {
			if capsule.name() == name {
				position = i
			}
		}
		if position < 0 {
			return nil, fmt.Errorf("unexpected name")
		}
		for _, entry := range e.entries {
			if entry.Name == "/"+name {
				return nil, fmt.Errorf("name already held")
			}
		}
		if e.race {
			e.race = false
			e.add(position, false)
			return nil, fmt.Errorf("delayed old-owner create won the name race")
		}
		id := e.add(position, true)
		if e.lostAck {
			e.lostAck = false
			return nil, fmt.Errorf("fence create response lost")
		}
		return []byte(id), nil
	}
	return nil, fmt.Errorf("unexpected engine operation %s", args[0])
}
func TestNativeLeaseFencesDelayedCreateAndLostFenceAck(t *testing.T) {
	for _, mode := range []string{"normal", "delayed-create", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			d, e := newLeaseEngine(t, 2)
			e.add(0, false)
			e.race = mode == "delayed-create"
			e.lostAck = mode == "lost-ack"
			receipt, err := d.Reconcile(context.Background(), testLease(), 2, e.targets)
			if err != nil || ValidateCleanupCapsules(receipt, testLease(), 2, e.capsules) != nil {
				t.Fatal(receipt, err)
			}
			if len(e.entries) != 2 || e.removals < 1 || !receipt.FencesHeld || !receipt.NoLiveSubjects {
				t.Fatal("cleanup not fenced", receipt)
			}
			for i, entry := range e.entries {
				if entry.State.Running || entry.State.Status != "created" || entry.Config.Labels["io.viber.runtime"] != fenceRuntime {
					t.Fatal("live subject", i)
				}
			}
			before := e.removals
			again, err := d.Reconcile(context.Background(), testLease(), 3, e.targets)
			if err != nil || e.removals != before || ValidateCleanupCapsules(again, testLease(), 3, e.capsules) != nil {
				t.Fatal("repeat did not retain fences", err)
			}
			if _, err = e.control(context.Background(), createArguments(e.capsules[0].name(), e.targets[0].Source, e.targets[0].Profile, e.targets[0].Invocation, nil)...); err == nil {
				t.Fatal("late create revived code")
			}
		})
	}
}
func TestNativeLeasePreflightRejectsForeignOrAlteredGroupBeforeAnyRemoval(t *testing.T) {
	cases := map[string]func(*inspected){
		"other generation": func(i *inspected) { i.Config.Labels["io.viber.generation"] = "2" },
		"wrong image":      func(i *inspected) { i.Config.Image = "untrusted:latest" },
		"wrong argv":       func(i *inspected) { i.Config.Entrypoint = []string{"/other"} },
		"wrong mount":      func(i *inspected) { i.Mounts[0].Source = "/secret" },
		"started fence": func(i *inspected) {
			i.State.Status = "exited"
			i.Config.Labels["io.viber.runtime"] = fenceRuntime
			i.Config.Labels["io.viber.fence"] = "1"
		},
		"unexpected same-label name": func(i *inspected) { i.Name = "/another" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d, e := newLeaseEngine(t, 2)
			e.add(0, false)
			id := e.add(1, false)
			info := e.entries[id]
			mutate(&info)
			e.entries[id] = info
			if _, err := d.Reconcile(context.Background(), testLease(), 2, e.targets); err == nil {
				t.Fatal("altered group accepted")
			}
			if e.removals != 0 || e.creates != 0 {
				t.Fatal("preflight mutated another subject")
			}
		})
	}
	d, e := newLeaseEngine(t, 1)
	if _, err := d.Reconcile(context.Background(), testLease(), 1, e.targets); err == nil || e.creates != 0 {
		t.Fatal("old owner allowed cleanup")
	}
}
func TestNativeLeaseExpiredOrCancelledNeverDispatches(t *testing.T) {
	owner := Ownership{testLease(), "CHECK", 1}
	if _, err := WithOwnership(context.Background(), owner, time.Now().Add(-time.Second)); err == nil {
		t.Fatal("expired lease admitted")
	}
	ctx := context.WithValue(context.Background(), ownerContextKey{}, ownershipContext{owner, time.Now().Add(-time.Second)})
	d := &Docker{}
	_, err := d.Run(ctx, t.TempDir(), DefaultProfile("golang@sha256:"+strings.Repeat("a", 64)), Invocation{CandidateDigest: c.HashBytes(nil), Argv: []string{"/bin/subject"}})
	failure, ok := err.(*DispatchFailure)
	if !ok || failure.EffectPossible {
		t.Fatal("expired lease possibly dispatched", err)
	}
}
func TestActualDockerNativeOrphanFencingPreventsLateCreateAndStart(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in real pinned Docker engine required")
	}
	d, err := OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	source := t.TempDir()
	if err = os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	lease := testLease()
	lease.InstanceID = fmt.Sprintf("%032x", time.Now().UnixNano())
	lease.ID = lease.InstanceID
	target, capsule, err := PrepareTarget(Target{Owner: Ownership{lease, "CHECK", 1}, Source: source, Profile: DefaultProfile(image), Invocation: Invocation{CandidateDigest: c.HashBytes([]byte("actual")), Argv: []string{"/bin/sh", "-c", "sleep 300 & wait"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	labels, _ := capsule.labels()
	args := createArguments(capsule.name(), target.Source, target.Profile, target.Invocation, labels)
	raw, err := d.control(ctx, args...)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.TrimSpace(string(raw))
	defer d.control(context.Background(), "rm", "--force", old)
	if _, err = d.control(ctx, "start", old); err != nil {
		t.Fatal(err)
	}
	receipt, err := d.Reconcile(ctx, lease, 2, []Target{target})
	if err != nil {
		t.Fatal(err)
	}
	defer d.control(context.Background(), "rm", "--force", receipt.Items[0].FenceContainerID)
	if err = ValidateCleanupCapsules(receipt, lease, 2, []Capsule{capsule}); err != nil {
		t.Fatal(err)
	}
	if _, err = d.control(ctx, "start", old); err == nil {
		t.Fatal("old process identity restarted")
	}
	if _, err = d.control(ctx, args...); err == nil {
		t.Fatal("late old create obtained fenced name")
	}
	again, err := d.Reconcile(ctx, lease, 3, []Target{target})
	if err != nil || again.Items[0].FenceContainerID != receipt.Items[0].FenceContainerID {
		t.Fatal("fence repeat changed identity", err)
	}
}
