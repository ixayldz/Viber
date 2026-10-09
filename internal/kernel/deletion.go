package kernel

import c "github.com/ixayldz/Viber/internal/contracts"

func deletionAllowed(state c.TaskState, event c.Event, p c.EventPayload) bool {
	if p.Deletion == nil || p.Deletion.Validate() != nil || state.Execution != c.Terminated || p.DocumentDigest != "" || p.SnapshotDigest != "" || p.Verification != nil || p.Tokens != nil || p.Resources != nil || p.State != "" || p.Outcome != "" || p.Quality != "" || p.Fulfillment != "" || p.SpecVersion != 0 || p.PolicyEpoch != 0 || p.InputID != "" || p.InputDigest != "" || p.QueueID != "" || p.InputBytes != 0 || p.RequiredObligations != 0 || p.CommandError != nil || p.Reason != p.Deletion.PlanDigest {
		return false
	}
	if state.Tokens != nil {
		for _, reservation := range state.Tokens.Reservations {
			if reservation.Status != "SETTLED" {
				return false
			}
		}
	}
	if state.Resources != nil {
		for _, reservation := range state.Resources.Reservations {
			if reservation.Status != "SETTLED" {
				return false
			}
		}
	}
	if event.Type == "TaskContentDeleted" {
		return event.Actor == "user" && state.Deletion == nil && p.Deletion.Status == "PENDING" && p.Deletion.OriginalDocument == state.DocumentDigest
	}
	if event.Type == "TaskDeletionPurged" && event.Actor == "kernel" && state.Deletion != nil && state.Deletion.Status == "PENDING" && p.Deletion.Status == "PURGED" {
		expected := *state.Deletion
		expected.Status = "PURGED"
		return expected == *p.Deletion
	}
	return false
}
