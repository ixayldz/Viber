package owner

import (
	"context"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
)

func TestNativeRuntimeObservationRPCAndEnvelopeCannotGrantCleanup(t *testing.T) {
	host, _, _ := ownerFixture(t, []agent.Turn{{Text: "done", UsageKnown: true, InputTokens: 1, OutputTokens: 1}}, false)
	raw := rpc(t, host, "runtime-info", "native-info", agent.Observation{Kind: "runtime-info"})
	var info agent.NativeRiskView
	if c.DecodeStrict(raw, &info) != nil || info.TaskID != "task" || info.CanFence || info.Lease != nil {
		t.Fatal(string(raw))
	}
	command := agent.NativeRiskCommand{CommandID: "payload-id", TaskID: "task", ExpectedTaskSeq: info.TaskSeq, RequestDigest: c.HashBytes(nil), ProfileDigest: c.HashBytes(nil), Decision: "FENCE_OLD_SUBJECTS_AND_ACCOUNT_FULL_UPPER_BOUND"}
	payload, _ := c.CanonicalV1(command)
	if _, err := host.Handle(context.Background(), ipc.Request{ID: "different-envelope", TaskID: "task", Command: "reconcile-native-risk", Payload: payload}); err == nil {
		t.Fatal("mismatched control envelope accepted")
	}
}
