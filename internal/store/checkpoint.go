package store

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
)

type checkpointRecord struct {
	task         string
	seq, taskSeq int64
	version      int
	digest       string
	raw          []byte
}

// Checkpoints are historical projections, not independent authority. Verify the
// complete checkpoint set against its original journal prefix, even when an
// attacker has recomputed the checkpoint's local state digest.
func (s *Store) verifyCheckpointsLocked(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT task_id,store_seq,task_seq,reducer_version,state_digest,state FROM checkpoints ORDER BY task_id")
	if err != nil {
		return err
	}
	records := []checkpointRecord{}
	for rows.Next() {
		var r checkpointRecord
		if err = rows.Scan(&r.task, &r.seq, &r.taskSeq, &r.version, &r.digest, &r.raw); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, r := range records {
		var state c.TaskState
		if err = c.DecodeStrict(r.raw, &state); err != nil {
			return c.Fail(c.StoreIntegrityError, "corrupt historical checkpoint")
		}
		digest, err := c.Digest(state)
		if err != nil {
			return err
		}
		if r.version != c.ReducerVersion || state.TaskID != r.task || state.StoreSeq != r.seq || state.TaskSeq != r.taskSeq || digest != r.digest {
			return c.Fail(c.StoreIntegrityError, "checkpoint envelope mismatch")
		}
		historical, err := s.replayToLocked(ctx, r.task, r.taskSeq)
		if err != nil {
			return err
		}
		expected, exists := historical[r.task]
		canonical, err := c.Digest(expected)
		if err != nil {
			return err
		}
		if !exists || canonical != digest {
			return c.Fail(c.StoreIntegrityError, "checkpoint differs from historical journal")
		}
	}
	return nil
}
