package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func TestDurableBindingsAndLimitedFinalCannotBeForged(t *testing.T) {
	digest := c.HashBytes([]byte("document"))
	candidate := c.HashBytes([]byte("candidate"))
	state := c.TaskState{SchemaVersion: 1, TaskID: "task", SpecVersion: 1, Execution: c.Running, TaskSeq: 4, StoreSeq: 4, KernelGeneration: 1, PolicyEpoch: 1, DocumentDigest: digest, BaselineDigest: candidate, CandidateDigest: candidate, Quality: c.Unverified, OpenRequiredObligations: 1}
	event := c.Event{SchemaVersion: 1, ID: "event", TaskID: "task", TaskSeq: 5, StoreSeq: 5, KernelGeneration: 1, Actor: "model"}
	for _, kind := range []string{"SessionRecorded", "CandidateRecorded", "UserResponseRecorded", "LimitedResultFinalized"} {
		event.Type = kind
		if _, err := Reduce(&state, event, c.EventPayload{DocumentDigest: digest, SnapshotDigest: candidate, InputID: "input", Reason: digest}); err == nil {
			t.Fatal("model crossed durable authority boundary", kind)
		}
	}
	state.Execution = c.Delivering
	event.Actor = "kernel"
	event.Type = "LimitedResultFinalized"
	good := c.EventPayload{DocumentDigest: digest, SnapshotDigest: candidate, Quality: c.Unverified, Fulfillment: c.Satisfied, Reason: "EXPLICIT_LIMITED_RESULT_POLICY"}
	final, err := Reduce(&state, event, good)
	if err != nil || final.Quality != c.Unverified || StrictSuccess(final) || InvocationExit(final, false) != 2 {
		t.Fatal("bad limited final", final, err)
	}
	for _, attack := range []string{"candidate", "verified", "reason", "barrier", "empty-candidate"} {
		changed := state
		p := good
		switch attack {
		case "candidate":
			p.SnapshotDigest = c.HashBytes([]byte("other"))
		case "verified":
			p.Quality = c.Verified
		case "reason":
			p.Reason = "model said done"
		case "barrier":
			changed.InputBarrier = true
			changed.PendingInputIDs = []string{"input"}
		case "empty-candidate":
			changed.CandidateDigest = ""
			p.SnapshotDigest = ""
		}
		if _, err := Reduce(&changed, event, p); err == nil {
			t.Fatal("limited final bypass", attack)
		}
	}
}
