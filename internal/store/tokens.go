package store

import (
	"context"
	"database/sql"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func tokenStates(ctx context.Context, tx *sql.Tx) (map[string]c.TaskState, error) {
	rows, err := tx.QueryContext(ctx, "SELECT task_id,state FROM tasks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]c.TaskState{}
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var state c.TaskState
		if err = c.DecodeStrict(raw, &state); err != nil {
			return nil, err
		}
		states[id] = state
	}
	return states, rows.Err()
}

type TokenLedgerView struct {
	SchemaVersion int           `json:"schema_version"`
	Coverage      string        `json:"coverage"`
	Limits        c.TokenLimits `json:"limits"`
	Used          c.TokenLimits `json:"charged"`
	Reserved      c.TokenLimits `json:"reserved"`
	Tasks         int           `json:"enrolled_tasks"`
	Unknown       int           `json:"unknown_reservations"`
}

func (s *Store) TokenLedger(ctx context.Context) (TokenLedgerView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	view := TokenLedgerView{SchemaVersion: 1, Coverage: "EMPTY"}
	if err := s.verifyProjectionsLocked(ctx); err != nil {
		return view, err
	}
	states, err := s.replayLocked(ctx, "")
	if err != nil {
		return view, err
	}
	for _, state := range states {
		if state.Tokens == nil {
			view.Coverage = "LEGACY_UNTRACKED"
			continue
		}
		view.Limits = state.Tokens.Limits
		view.Used.Input += state.Tokens.Used.Input
		view.Used.Output += state.Tokens.Used.Output
		view.Reserved.Input += state.Tokens.Reserved.Input
		view.Reserved.Output += state.Tokens.Reserved.Output
		view.Tasks++
		for _, r := range state.Tokens.Reservations {
			if r.Status == "UNKNOWN" {
				view.Unknown++
			}
		}
	}
	if view.Tasks > 0 && view.Coverage != "LEGACY_UNTRACKED" {
		view.Coverage = "ALL_TASKS_TOKEN_ONLY_V1"
	}
	return view, nil
}
