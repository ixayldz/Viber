package kernel

import (
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestProjectMemoryMetadataRebindDoesNotResumeOrGrantAuthority(t *testing.T) {
	for _, execution := range []c.ExecutionState{c.Terminated, c.Ready, c.Paused} {
		t.Run(string(execution), func(t *testing.T) {
			state := c.TaskState{SchemaVersion: 1, TaskID: "memory-task", SpecVersion: 1, Execution: execution,
				Quality: c.Verified, PolicyEpoch: 1, KernelGeneration: 1, TaskSeq: 1, StoreSeq: 1,
				DocumentDigest: c.HashBytes([]byte("original document")), CandidateDigest: c.HashBytes([]byte("candidate")),
				WorkBudget: &c.TaskWorkBudget{Limits: c.BudgetAllocation{MaxSteps: 16, MaxToolCalls: 64, MaxInputTokens: 524288, MaxOutputTokens: 32768, MaxActiveMillis: 120000}}}
			event := c.Event{SchemaVersion: 1, ID: "memory-command", CausationID: "memory-command", TaskID: state.TaskID,
				TaskSeq: 2, StoreSeq: 2, KernelGeneration: 2, Actor: "user", Type: "ProjectMemoryRecorded"}
			mutation := c.ProjectMemoryMutation{SourceDocumentDigest: state.DocumentDigest, EventDigest: c.HashBytes([]byte("assertion")),
				CommandID: event.ID, EventBytes: 100, SourceTaskSeq: state.TaskSeq, SpecVersion: 1, PolicyEpoch: 1, KernelGeneration: 2, Action: "ASSERT"}
			payload := c.EventPayload{DocumentDigest: c.HashBytes([]byte("memory document")), ProjectMemoryMutation: &mutation}
			next, err := Reduce(&state, event, payload)
			if err != nil || next.Execution != execution || next.Quality != state.Quality || next.CandidateDigest != state.CandidateDigest ||
				next.SpecVersion != state.SpecVersion || next.PolicyEpoch != state.PolicyEpoch || next.ProjectMemoryRevision != 1 ||
				next.KernelGeneration != 2 || !reflect.DeepEqual(next.WorkBudget, state.WorkBudget) {
				t.Fatal("metadata rebind changed task authority", next, err)
			}
			for _, scenario := range []string{"wrong actor", "stale source", "quality grant", "budget grant", "pending effect", "input barrier", "deletion"} {
				t.Run(scenario, func(t *testing.T) {
					before, attempt, data, source := state, event, payload, mutation
					data.ProjectMemoryMutation = &source
					switch scenario {
					case "wrong actor":
						attempt.Actor = "kernel"
					case "stale source":
						source.SourceDocumentDigest = c.HashBytes([]byte("other document"))
					case "quality grant":
						data.Quality = c.Verified
					case "budget grant":
						data.WorkBudget = state.WorkBudget
					case "pending effect":
						budget := *state.WorkBudget
						budget.EffectPending = true
						before.WorkBudget = &budget
					case "input barrier":
						before.InputBarrier = true
					case "deletion":
						before.Deletion = &c.TaskDeletion{Status: "PENDING"}
					}
					if _, err := Reduce(&before, attempt, data); err == nil {
						t.Fatal("invalid memory metadata accepted")
					}
				})
			}
		})
	}
}
