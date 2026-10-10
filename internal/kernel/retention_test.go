package kernel

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
	"time"
)

func TestRetentionExpiryRequiresDistinctActorElapsedDeadlineAndNoUnknownRisk(t *testing.T) {
	for _, name := range []string{"valid", "early", "user-forgery", "no-proof", "bad-time", "unknown-tokens", "unknown-native", "active", "quality"} {
		t.Run(name, func(t *testing.T) {
			state, event, payload := deletionState()
			event.Actor = "retention"
			event.Timestamp = "2026-10-10T12:00:00Z"
			payload.Deletion.RetentionDigest = c.HashBytes([]byte("consent"))
			payload.Deletion.RetentionRevision = 2
			payload.Deletion.RetentionDeadline = "2026-10-10T11:00:00Z"
			switch name {
			case "early":
				payload.Deletion.RetentionDeadline = "2026-10-10T13:00:00Z"
			case "user-forgery":
				event.Actor = "user"
			case "no-proof":
				payload.Deletion.RetentionDigest = ""
			case "bad-time":
				event.Timestamp = time.Time{}.Format(time.RFC3339Nano)
			case "unknown-tokens":
				state.Tokens = &c.TokenAccount{Reservations: []c.TokenReservation{{Status: "UNKNOWN"}}}
			case "unknown-native":
				state.Resources = &c.ResourceAccount{Reservations: []c.ResourceReservation{{Status: "UNKNOWN"}}}
			case "active":
				state.Execution = c.Running
			case "quality":
				payload.Quality = c.Verified
			}
			next, err := Reduce(&state, event, payload)
			if name != "valid" {
				if err == nil {
					t.Fatal("unsafe expiry admitted")
				}
				return
			}
			if err != nil || next.Deletion == nil || next.Deletion.RetentionDigest != payload.Deletion.RetentionDigest || next.Quality != state.Quality || next.Outcome != state.Outcome || StrictSuccess(next) {
				t.Fatal(next, err)
			}
			event.Actor = "kernel"
			event.Type = "TaskDeletionPurged"
			event.TaskSeq++
			event.StoreSeq++
			payload.Deletion.Status = "PURGED"
			purged, err := Reduce(&next, event, payload)
			if err != nil || purged.Deletion.Status != "PURGED" || purged.Deletion.RetentionRevision != 2 {
				t.Fatal(purged, err)
			}
		})
	}
}
