package store

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
)

type ResourceLedgerView struct {
	SchemaVersion int               `json:"schema_version"`
	Coverage      string            `json:"coverage"`
	Policy        *c.ResourcePolicy `json:"immutable_store_policy,omitempty"`
	Used          c.ResourceVector  `json:"charged"`
	Reserved      c.ResourceVector  `json:"reserved"`
	Tasks         int               `json:"enrolled_tasks"`
	Unknown       int               `json:"unknown_reservations"`
}

func (s *Store) ResourceLedger(ctx context.Context) (ResourceLedgerView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	view := ResourceLedgerView{SchemaVersion: 1, Coverage: "EMPTY"}
	if err := s.verifyProjectionsLocked(ctx); err != nil {
		return view, err
	}
	states, err := s.replayLocked(ctx, "")
	if err != nil {
		return view, err
	}
	for _, state := range states {
		if state.Resources == nil {
			view.Coverage = "LEGACY_UNTRACKED"
			continue
		}
		policy := state.Resources.Policy
		view.Policy = &policy
		view.Used = view.Used.Add(state.Resources.Used)
		view.Reserved = view.Reserved.Add(state.Resources.Reserved)
		view.Tasks++
		for _, r := range state.Resources.Reservations {
			if r.Status == "UNKNOWN" {
				view.Unknown++
			}
		}
	}
	if view.Tasks > 0 && view.Coverage != "LEGACY_UNTRACKED" {
		view.Coverage = "ALL_TASKS_CONSERVATIVE_RESOURCE_V1"
	}
	return view, nil
}
