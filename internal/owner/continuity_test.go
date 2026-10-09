package owner

import (
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"testing"
	"time"
)

func TestContinuityRPCDurableDedupAndBoundAuthority(t *testing.T) {
	host, _, _ := ownerFixture(t, []agent.Turn{{Text: "done", UsageKnown: true}}, false)
	state, doc, err := host.Session.Load(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	command := agent.ContinuityCommand{CommandID: "pin-rpc", TaskID: "task", ExpectedTaskSeq: state.TaskSeq, Action: "pin", Pin: &agent.ContextPin{ID: "source", Path: "a.txt", Candidate: doc.Candidate.SnapshotDigest, SourceDigest: c.HashBytes([]byte("base")), Start: 0, End: 4}}
	raw := rpc(t, host, "context-edit", "pin-rpc", command)
	var after c.TaskState
	if err = c.DecodeStrict(raw, &after); err != nil {
		t.Fatal(err)
	}
	var retry c.TaskState
	if err = c.DecodeStrict(rpc(t, host, "context-edit", "pin-rpc", command), &retry); err != nil || retry.TaskSeq != after.TaskSeq {
		t.Fatal("dedup", err)
	}
	command.Pin.End = 3
	payload, _ := c.CanonicalV1(command)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = ipc.Call(ctx, host.Server.Info, ipc.Request{ID: "pin-rpc", Command: "context-edit", TaskID: "task", Payload: payload}); err == nil {
		t.Fatal("command changed under same ID")
	}
	_, loaded, err := host.Session.Load(context.Background(), "task")
	if err != nil || len(loaded.Pins) != 1 || string(loaded.Pins[0].Data) != "base" {
		t.Fatal("RPC not wired", err)
	}
}
