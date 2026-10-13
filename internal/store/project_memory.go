package store

import (
	"context"
	"database/sql"
	"errors"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const (
	ProjectMemoryConflictLegacyWitness   = "LEGACY_WITNESS_MISSING"
	ProjectMemoryConflictRecordLimit     = "RECORD_LIMIT"
	ProjectMemoryConflictByteLimit       = "BYTE_LIMIT"
	ProjectMemoryConflictJournalLimit    = "JOURNAL_LIMIT"
	ProjectMemoryConflictStateLimit      = "STATE_LIMIT"
	projectMemoryConflictMaxJournalRows  = 262144
	projectMemoryConflictMaxJournalBytes = 256 << 20
	projectMemoryConflictMaxStateBytes   = 64 << 20
)

// ProjectMemoryConflictRecord contains only the event envelope and typed
// metadata witness. It never contains an archived assertion or task document.
type ProjectMemoryConflictRecord struct {
	StoreSeq int64                   `json:"store_seq"`
	TaskID   string                  `json:"task_id"`
	TaskSeq  int64                   `json:"task_seq"`
	EventID  string                  `json:"event_id"`
	Mutation c.ProjectMemoryMutation `json:"mutation"`
}

// ProjectMemoryConflictIndex is complete only when every project memory
// mutation can be classified by its metadata witness. In particular, one
// legacy event makes project-wide conflict status unknown because it cannot
// be safely assigned to a project without opening task documents.
type ProjectMemoryConflictIndex struct {
	Complete      bool                          `json:"complete"`
	Reason        string                        `json:"reason,omitempty"`
	Records       []ProjectMemoryConflictRecord `json:"records"`
	CurrentStates map[string]c.TaskState        `json:"-"`
}

// ProjectMemoryConflictMetadata returns a bounded metadata-only view for one
// project. It performs SQL counts and byte checks before scanning any payload
// rows, then validates the journal projections before returning selected
// metadata. Archive blobs are never opened here.
func (s *Store) ProjectMemoryConflictMetadata(ctx context.Context, projectDigest string, maxRecords int, maxBytes int64) (ProjectMemoryConflictIndex, error) {
	result := ProjectMemoryConflictIndex{Records: []ProjectMemoryConflictRecord{}, CurrentStates: map[string]c.TaskState{}}
	if !c.ValidDigest(projectDigest) || maxRecords < 1 || maxRecords > 4096 || maxBytes < 1 || maxBytes > 32<<20 {
		return result, c.Fail(c.InvalidArgument, "valid project digest and bounded conflict-index limits required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return result, c.Fail(c.StoreIntegrityError, "store closed")
	}
	withinJournal, err := s.projectMemoryConflictJournalBoundLocked(ctx)
	if err != nil {
		return result, err
	}
	if !withinJournal {
		result.Reason = ProjectMemoryConflictJournalLimit
		return result, nil
	}
	legacy, malformed, err := s.projectMemoryConflictWitnessCountsLocked(ctx)
	if err != nil {
		return result, err
	}
	if malformed != 0 {
		return result, c.Fail(c.StoreIntegrityError, "project memory conflict metadata shape is invalid")
	}
	var recordCount, payloadBytes int64
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(LENGTH(e.envelope)+LENGTH(p.body)),0)
		FROM events e JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest
		WHERE json_extract(CAST(e.envelope AS TEXT),'$.type')='ProjectMemoryRecorded'
		  AND json_extract(CAST(p.body AS TEXT),'$.project_memory_mutation.conflict_metadata.project_digest')=?`, projectDigest).Scan(&recordCount, &payloadBytes)
	if err != nil {
		return result, err
	}
	if recordCount > int64(maxRecords) {
		result.Reason = ProjectMemoryConflictRecordLimit
		return result, nil
	}
	if payloadBytes > maxBytes {
		result.Reason = ProjectMemoryConflictByteLimit
		return result, nil
	}
	var taskCount, missingTasks, stateBytes int64
	err = s.db.QueryRowContext(ctx, `WITH relevant_tasks AS (
		SELECT DISTINCT e.task_id FROM events e JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest
		WHERE json_extract(CAST(e.envelope AS TEXT),'$.type')='ProjectMemoryRecorded'
		  AND json_extract(CAST(p.body AS TEXT),'$.project_memory_mutation.conflict_metadata.project_digest')=?
		)
		SELECT COUNT(*),COALESCE(SUM(CASE WHEN t.state IS NULL THEN 1 ELSE 0 END),0),COALESCE(SUM(LENGTH(t.state)),0)
		FROM relevant_tasks r LEFT JOIN tasks t ON t.task_id=r.task_id`, projectDigest).Scan(&taskCount, &missingTasks, &stateBytes)
	if err != nil {
		return result, err
	}
	if taskCount > int64(maxRecords) || missingTasks != 0 {
		result.Reason = ProjectMemoryConflictRecordLimit
		return result, nil
	}
	if stateBytes > maxBytes-payloadBytes || stateBytes > projectMemoryConflictMaxStateBytes {
		result.Reason = ProjectMemoryConflictStateLimit
		return result, nil
	}
	if legacy != 0 {
		result.Reason = ProjectMemoryConflictLegacyWitness
		return result, nil
	}
	// The whole-history replay is now bounded by the SQL-only journal pre-scan
	// above; selected memory records and current task projections have also
	// passed their tighter limits before any payload/state bytes are scanned.
	if err = s.verifyProjectionsLocked(ctx); err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.store_seq,e.task_id,e.task_seq,e.event_id,e.envelope,e.payload_digest,p.body
		FROM events e JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest
		WHERE json_extract(CAST(e.envelope AS TEXT),'$.type')='ProjectMemoryRecorded'
		  AND json_extract(CAST(p.body AS TEXT),'$.project_memory_mutation.conflict_metadata.project_digest')=?
		ORDER BY e.store_seq`, projectDigest)
	if err != nil {
		return result, err
	}
	seenTasks := map[string]bool{}
	for rows.Next() {
		var record ProjectMemoryConflictRecord
		var envelope, body []byte
		var payloadDigest string
		if err = rows.Scan(&record.StoreSeq, &record.TaskID, &record.TaskSeq, &record.EventID, &envelope, &payloadDigest, &body); err != nil {
			rows.Close()
			return result, err
		}
		var event c.Event
		var payload c.EventPayload
		if c.DecodeStrict(envelope, &event) != nil || c.DecodeStrict(body, &payload) != nil ||
			event.Type != "ProjectMemoryRecorded" || event.StoreSeq != record.StoreSeq || event.TaskID != record.TaskID ||
			event.TaskSeq != record.TaskSeq || event.ID != record.EventID || event.PayloadDigest != payloadDigest ||
			payload.ProjectMemoryMutation == nil || payload.ProjectMemoryMutation.ConflictMetadata == nil {
			rows.Close()
			return result, c.Fail(c.StoreIntegrityError, "project memory conflict journal binding is invalid")
		}
		payloadHash, hashErr := c.Digest(payload)
		if hashErr != nil || payloadHash != payloadDigest {
			rows.Close()
			return result, c.Fail(c.StoreIntegrityError, "project memory conflict payload digest mismatch")
		}
		if validateErr := payload.ProjectMemoryMutation.Validate(); validateErr != nil ||
			payload.ProjectMemoryMutation.ConflictMetadata.ProjectDigest != projectDigest {
			rows.Close()
			return result, c.Fail(c.StoreIntegrityError, "project memory conflict witness is invalid")
		}
		record.Mutation = *payload.ProjectMemoryMutation
		result.Records = append(result.Records, record)
		seenTasks[record.TaskID] = true
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	for task := range seenTasks {
		var raw []byte
		err = s.db.QueryRowContext(ctx, "SELECT state FROM tasks WHERE task_id=?", task).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return result, err
		}
		var state c.TaskState
		if c.DecodeStrict(raw, &state) != nil || state.TaskID != task {
			return result, c.Fail(c.StoreIntegrityError, "project memory conflict task projection is invalid")
		}
		result.CurrentStates[task] = state
	}
	result.Complete = true
	return result, nil
}

// ProjectMemoryConflictMetadataUsed reports durable use of the new witness
// wire format. It scans journal history, so use remains detectable even after
// a task document stops referencing every memory event.
func (s *Store) ProjectMemoryConflictMetadataUsed(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return false, c.Fail(c.StoreIntegrityError, "store closed")
	}
	withinJournal, err := s.projectMemoryConflictJournalBoundLocked(ctx)
	if err != nil {
		return false, err
	}
	if !withinJournal {
		return false, c.Fail(c.BudgetLimitReached, "project memory metadata capability scan exceeds its journal bound")
	}
	_, malformed, err := s.projectMemoryConflictWitnessCountsLocked(ctx)
	if err != nil {
		return false, err
	}
	if malformed != 0 {
		return false, c.Fail(c.StoreIntegrityError, "project memory conflict metadata shape is invalid")
	}
	if err = s.verifyProjectionsLocked(ctx); err != nil {
		return false, err
	}
	var count int64
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest
		WHERE json_extract(CAST(e.envelope AS TEXT),'$.type')='ProjectMemoryRecorded'
		  AND json_type(CAST(p.body AS TEXT),'$.project_memory_mutation.conflict_metadata')='object'`).Scan(&count)
	return count > 0, err
}

func (s *Store) projectMemoryConflictWitnessCountsLocked(ctx context.Context) (legacy, malformed int64, err error) {
	// JSON1 runs inside SQLite so the pre-scan does not hydrate payload bodies
	// into Go. Missing payload rows count as malformed and are caught before
	// any selected metadata is returned.
	var missingPayload, missingWitness, malformedWitness int64
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN p.body IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN p.body IS NOT NULL AND json_type(CAST(p.body AS TEXT),'$.project_memory_mutation.conflict_metadata') IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN p.body IS NOT NULL AND json_type(CAST(p.body AS TEXT),'$.project_memory_mutation.conflict_metadata') IS NOT NULL AND json_type(CAST(p.body AS TEXT),'$.project_memory_mutation.conflict_metadata')!='object' THEN 1 ELSE 0 END),0)
		FROM events e LEFT JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest
		WHERE json_extract(CAST(e.envelope AS TEXT),'$.type')='ProjectMemoryRecorded'`).Scan(&missingPayload, &missingWitness, &malformedWitness)
	legacy = missingWitness
	malformed = missingPayload + malformedWitness
	return legacy, malformed, err
}

func (s *Store) projectMemoryConflictJournalBoundLocked(ctx context.Context) (bool, error) {
	var eventCount, missingPayloads, journalBytes, taskCount, stateBytes int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN p.body IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(LENGTH(e.envelope)+LENGTH(p.body)),0)
		FROM events e LEFT JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest`).Scan(&eventCount, &missingPayloads, &journalBytes)
	if err != nil {
		return false, err
	}
	if missingPayloads != 0 {
		return false, c.Fail(c.StoreIntegrityError, "project memory metadata scan found a missing journal payload")
	}
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(LENGTH(state)),0) FROM tasks").Scan(&taskCount, &stateBytes); err != nil {
		return false, err
	}
	return eventCount <= projectMemoryConflictMaxJournalRows && journalBytes <= projectMemoryConflictMaxJournalBytes &&
		taskCount <= projectMemoryConflictMaxJournalRows && stateBytes <= projectMemoryConflictMaxStateBytes, nil
}
