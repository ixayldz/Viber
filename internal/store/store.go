// Package store is the initial single-owner SQLite journal foundation.
// Full power-loss conformance and privacy deletion are not
// shipped yet; this package is not a supported production metadata service.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/kernel"
	_ "modernc.org/sqlite"
)

const storeVersion = 2

type Store struct {
	mu             sync.Mutex
	db             *sql.DB
	lock           *os.File
	generation     int64
	directory      string
	schema         int
	readOnlyReason string
	migrationFault func(string) error
	migrationSpace func() uint64
}
type Command struct {
	ID              string         `json:"command_id"`
	TaskID          string         `json:"task_id"`
	Actor           string         `json:"actor"`
	ExpectedTaskSeq int64          `json:"expected_task_seq"`
	Type            string         `json:"type"`
	Payload         c.EventPayload `json:"payload"`
}

func Open(ctx context.Context, directory string) (result *Store, resultErr error) {
	root, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	privateRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	if err = fileguard.Private(privateRoot); err != nil {
		privateRoot.Close()
		return nil, err
	}
	defer privateRoot.Close()
	for _, name := range []string{"owner.lock", "state.sqlite", "state.sqlite-wal", "state.sqlite-shm"} {
		if err = fileguard.RegularPath(privateRoot, name, true); err != nil {
			return nil, err
		}
	}
	lock, err := fileguard.Lock(privateRoot, "owner.lock")
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			lock.Close()
		}
	}()
	dbpath := filepath.Join(root, "state.sqlite")
	uriPath := filepath.ToSlash(dbpath)
	if filepath.VolumeName(dbpath) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			db.Close()
		}
	}()
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version < 0 || version > storeVersion {
		return nil, c.Fail(c.UnsupportedCapability, "newer store schema; downgrade forbidden")
	}
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL"} {
		if _, err = db.ExecContext(ctx, pragma); err != nil {
			return nil, err
		}
	}
	for _, setting := range []struct{ name, expected string }{{"journal_mode", "wal"}, {"synchronous", "2"}, {"foreign_keys", "1"}} {
		var value string
		if err = db.QueryRowContext(ctx, "PRAGMA "+setting.name).Scan(&value); err != nil || value != setting.expected {
			return nil, c.Fail(c.StoreIntegrityError, "SQLite durability profile unavailable")
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if version == 0 {
		statements := []string{
			"CREATE TABLE meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL)",
			"INSERT INTO meta VALUES ('generation',0),('store_seq',0)",
			"CREATE TABLE tasks (task_id TEXT PRIMARY KEY, state BLOB NOT NULL)",
			"CREATE TABLE payloads (task_id TEXT NOT NULL,digest TEXT NOT NULL,body BLOB NOT NULL, PRIMARY KEY(task_id,digest))",
			"CREATE TABLE events (store_seq INTEGER PRIMARY KEY,task_id TEXT NOT NULL,task_seq INTEGER NOT NULL,event_id TEXT NOT NULL UNIQUE,envelope BLOB NOT NULL,payload_digest TEXT NOT NULL,previous_hash TEXT NOT NULL,event_hash TEXT NOT NULL,UNIQUE(task_id,task_seq),FOREIGN KEY(task_id,payload_digest) REFERENCES payloads(task_id,digest))",
			"CREATE TABLE commands (command_id TEXT PRIMARY KEY,payload_digest TEXT NOT NULL,result BLOB NOT NULL)",
			"CREATE TABLE checkpoints (task_id TEXT PRIMARY KEY,store_seq INTEGER NOT NULL,task_seq INTEGER NOT NULL,reducer_version INTEGER NOT NULL,state_digest TEXT NOT NULL,state BLOB NOT NULL)",
			formatTable,
			"PRAGMA user_version=2",
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return nil, err
			}
		}
	}
	if version == 0 {
		format, formatErr := newFormat(nil)
		if formatErr != nil {
			return nil, formatErr
		}
		if err = insertFormat(ctx, tx, format); err != nil {
			return nil, err
		}
		version = storeVersion
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s := &Store{db: db, lock: lock, directory: root, schema: version}
	if err = s.verifyProjectionsLocked(ctx); err != nil {
		return nil, err
	}
	if err = s.checkMigrationLocked(ctx); err != nil {
		return nil, err
	}
	if s.readOnlyReason != "" {
		if err = db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='generation'").Scan(&s.generation); err != nil {
			return nil, err
		}
		return s, nil
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE meta SET value=value+1 WHERE key='generation'"); err != nil {
		return nil, err
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='generation'").Scan(&generation); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.generation = generation
	return s, nil
}
func (s *Store) Generation() int64 { return s.generation }
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	lockErr := s.lock.Close()
	s.db = nil
	return errors.Join(err, lockErr)
}
func (s *Store) Execute(ctx context.Context, command Command) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return c.TaskState{}, err
	}
	if command.ID == "" || command.TaskID == "" || command.Actor == "" || command.ExpectedTaskSeq < 0 {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "invalid command")
	}
	if err := s.verifyProjectionsLocked(ctx); err != nil {
		return c.TaskState{}, err
	}
	if s.schema == 2 {
		format, _, err := s.formatLocked(ctx)
		if err != nil {
			return c.TaskState{}, err
		}
		if format.Migration != nil && format.Migration.ID == command.ID {
			return c.TaskState{}, c.Fail(c.CommandIDConflict, "command ID belongs to schema migration")
		}
	}
	digest, err := c.Digest(command)
	if err != nil {
		return c.TaskState{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.TaskState{}, err
	}
	defer tx.Rollback()
	var existingDigest string
	var result []byte
	err = tx.QueryRowContext(ctx, "SELECT payload_digest,result FROM commands WHERE command_id=?", command.ID).Scan(&existingDigest, &result)
	if err == nil {
		if existingDigest != digest {
			return c.TaskState{}, c.Fail(c.CommandIDConflict, "command ID reused with different immutable payload")
		}
		var state c.TaskState
		if err = c.DecodeStrict(result, &state); err != nil {
			return state, c.Fail(c.StoreIntegrityError, "corrupt command result")
		}
		if err = tx.Rollback(); err != nil {
			return state, err
		}
		replayed, err := s.replayToLocked(ctx, command.TaskID, state.TaskSeq)
		if err != nil {
			return state, err
		}
		actual, _ := c.Digest(state)
		expected, _ := c.Digest(replayed[command.TaskID])
		if actual != expected {
			return state, c.Fail(c.StoreIntegrityError, "command receipt differs from historical journal")
		}
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return c.TaskState{}, err
	}
	var previous *c.TaskState
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT state FROM tasks WHERE task_id=?", command.TaskID).Scan(&raw)
	if err == nil {
		previous = &c.TaskState{}
		if err = c.DecodeStrict(raw, previous); err != nil {
			return c.TaskState{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return c.TaskState{}, err
	}
	taskSeq := int64(1)
	if previous != nil {
		taskSeq = previous.TaskSeq + 1
	}
	if command.ExpectedTaskSeq != taskSeq-1 {
		return c.TaskState{}, c.Fail(c.StaleBase, "command task cursor changed")
	}
	var sequence int64
	if err = tx.QueryRowContext(ctx, "SELECT value+1 FROM meta WHERE key='store_seq'").Scan(&sequence); err != nil {
		return c.TaskState{}, err
	}
	payload, err := c.CanonicalV1(command.Payload)
	if err != nil {
		return c.TaskState{}, err
	}
	payloadDigest, err := c.Digest(command.Payload)
	if err != nil {
		return c.TaskState{}, err
	}
	event := c.Event{SchemaVersion: c.SchemaVersion, ID: command.ID, StoreSeq: sequence, TaskID: command.TaskID, TaskSeq: taskSeq, KernelGeneration: s.generation, Actor: command.Actor, CausationID: command.ID, Type: command.Type, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), PayloadDigest: payloadDigest, PayloadRef: "ctx://event/" + command.TaskID + "/" + payloadDigest}
	state, err := kernel.Reduce(previous, event, command.Payload)
	if err != nil {
		return state, err
	}
	if command.Payload.Tokens != nil || command.Type == "TaskCreated" {
		all, loadErr := tokenStates(ctx, tx)
		if loadErr != nil {
			return state, loadErr
		}
		if err = c.ValidateTokenAdmission(all, state, command.Payload.Tokens); err != nil {
			return c.TaskState{}, err
		}
	}
	envelope, err := c.CanonicalV1(event)
	if err != nil {
		return state, err
	}
	var previousHash string
	err = tx.QueryRowContext(ctx, "SELECT event_hash FROM events ORDER BY store_seq DESC LIMIT 1").Scan(&previousHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	eventHash, err := chainDigest(event, previousHash)
	if err != nil {
		return state, err
	}
	stateRaw, err := c.CanonicalV1(state)
	if err != nil {
		return state, err
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO payloads(task_id,digest,body) VALUES(?,?,?) ON CONFLICT(task_id,digest) DO NOTHING", []any{command.TaskID, payloadDigest, payload}},
		{"INSERT INTO events VALUES(?,?,?,?,?,?,?,?)", []any{sequence, command.TaskID, taskSeq, event.ID, envelope, payloadDigest, previousHash, eventHash}},
		{"INSERT INTO tasks VALUES(?,?) ON CONFLICT(task_id) DO UPDATE SET state=excluded.state", []any{command.TaskID, stateRaw}},
		{"INSERT INTO commands VALUES(?,?,?)", []any{command.ID, digest, stateRaw}},
		{"UPDATE meta SET value=? WHERE key='store_seq'", []any{sequence}},
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return state, err
		}
	}
	if err = tx.Commit(); err != nil {
		return c.TaskState{}, err
	}
	return state, nil
}
func chainDigest(event c.Event, previous string) (string, error) {
	return c.Digest(struct {
		Event    c.Event `json:"event"`
		Previous string  `json:"previous"`
	}{event, previous})
}
func (s *Store) Replay(ctx context.Context, taskID string) (map[string]c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, c.Fail(c.StoreIntegrityError, "store closed")
	}
	return s.replayLocked(ctx, taskID)
}
func (s *Store) replayLocked(ctx context.Context, taskID string) (map[string]c.TaskState, error) {
	return s.replayToLocked(ctx, taskID, 0)
}
func (s *Store) replayToLocked(ctx context.Context, taskID string, until int64) (map[string]c.TaskState, error) {
	var historical *c.TaskState
	rows, err := s.db.QueryContext(ctx, "SELECT e.store_seq,e.task_id,e.task_seq,e.event_id,e.envelope,e.payload_digest,e.previous_hash,e.event_hash,p.body FROM events e LEFT JOIN payloads p ON p.task_id=e.task_id AND p.digest=e.payload_digest ORDER BY e.store_seq")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]c.TaskState{}
	sequence := int64(0)
	previousHash := ""
	for rows.Next() {
		var seq, taskSeq int64
		var task, eventID, payloadDigest, prev, hash string
		var raw, body []byte
		if err = rows.Scan(&seq, &task, &taskSeq, &eventID, &raw, &payloadDigest, &prev, &hash, &body); err != nil {
			return nil, err
		}
		var event c.Event
		var payload c.EventPayload
		if err = c.DecodeStrict(raw, &event); err != nil {
			return nil, c.Fail(c.StoreIntegrityError, "corrupt event envelope")
		}
		if err = c.DecodeStrict(body, &payload); err != nil {
			return nil, c.Fail(c.StoreIntegrityError, "required event payload unavailable or corrupt")
		}
		pd, err := c.Digest(payload)
		if err != nil {
			return nil, err
		}
		eh, err := chainDigest(event, prev)
		if err != nil {
			return nil, err
		}
		if seq != sequence+1 || event.StoreSeq != seq || event.TaskID != task || event.TaskSeq != taskSeq || event.ID != eventID || event.PayloadDigest != payloadDigest || pd != payloadDigest || prev != previousHash || eh != hash {
			return nil, c.Fail(c.StoreIntegrityError, "journal sequence/payload/hash chain mismatch")
		}
		var old *c.TaskState
		if st, exists := states[task]; exists {
			old = &st
		}
		next, err := kernel.Reduce(old, event, payload)
		if err != nil {
			return nil, c.Fail(c.StoreIntegrityError, "journal reducer rejected event")
		}
		if err := c.ValidateTokenAdmission(states, next, payload.Tokens); err != nil {
			return nil, c.Fail(c.StoreIntegrityError, "journal global token admission is invalid")
		}
		states[task] = next
		if task == taskID && next.TaskSeq == until {
			saved := next
			historical = &saved
		}
		sequence = seq
		previousHash = hash
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	var expected int64
	if err = s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='store_seq'").Scan(&expected); err != nil {
		return nil, err
	}
	if sequence != expected {
		return nil, c.Fail(c.StoreIntegrityError, "journal tail missing")
	}
	if taskID != "" {
		if until > 0 {
			if historical == nil {
				return nil, c.Fail(c.InvalidArgument, "historical task cursor unavailable")
			}
			return map[string]c.TaskState{taskID: *historical}, nil
		}
		st, ok := states[taskID]
		if !ok {
			return nil, c.Fail(c.InvalidArgument, "task not found")
		}
		return map[string]c.TaskState{taskID: st}, nil
	}
	return states, nil
}
func (s *Store) Checkpoint(ctx context.Context, taskID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return "", err
	}
	if err := s.verifyProjectionsLocked(ctx); err != nil {
		return "", err
	}
	states, err := s.replayLocked(ctx, taskID)
	if err != nil {
		return "", err
	}
	state := states[taskID]
	raw, err := c.CanonicalV1(state)
	if err != nil {
		return "", err
	}
	digest, err := c.Digest(state)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO checkpoints VALUES(?,?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET store_seq=excluded.store_seq,task_seq=excluded.task_seq,reducer_version=excluded.reducer_version,state_digest=excluded.state_digest,state=excluded.state", taskID, state.StoreSeq, state.TaskSeq, c.ReducerVersion, digest, raw)
	return digest, err
}
func (s *Store) InspectCheckpoint(ctx context.Context, taskID string) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state c.TaskState
	if s.db == nil {
		return state, c.Fail(c.StoreIntegrityError, "store closed")
	}
	if err := s.verifyProjectionsLocked(ctx); err != nil {
		return state, err
	}
	var seq, taskSeq int64
	var version int
	var digest string
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT store_seq,task_seq,reducer_version,state_digest,state FROM checkpoints WHERE task_id=?", taskID).Scan(&seq, &taskSeq, &version, &digest, &raw); err != nil {
		return state, err
	}
	if err := c.DecodeStrict(raw, &state); err != nil {
		return state, err
	}
	actual, err := c.Digest(state)
	if err != nil {
		return state, err
	}
	if version != c.ReducerVersion || seq != state.StoreSeq || taskSeq != state.TaskSeq || actual != digest {
		return c.TaskState{}, c.Fail(c.StoreIntegrityError, "checkpoint integrity mismatch")
	}
	return state, nil
}
func (s *Store) String() string { return fmt.Sprintf("store-generation-%d", s.generation) }

func (s *Store) verifyProjectionsLocked(ctx context.Context) error {
	if _, _, err := s.formatLocked(ctx); err != nil {
		return err
	}
	states, err := s.replayLocked(ctx, "")
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT task_id,state FROM tasks")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return err
		}
		var actual c.TaskState
		if err = c.DecodeStrict(raw, &actual); err != nil {
			return c.Fail(c.StoreIntegrityError, "corrupt task projection")
		}
		expected, ok := states[id]
		if !ok {
			return c.Fail(c.StoreIntegrityError, "projection lacks journal source")
		}
		a, err := c.Digest(actual)
		if err != nil {
			return err
		}
		b, err := c.Digest(expected)
		if err != nil {
			return err
		}
		if a != b {
			return c.Fail(c.StoreIntegrityError, "projection differs from journal")
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != len(states) {
		return c.Fail(c.StoreIntegrityError, "task projection missing")
	}
	if err = rows.Close(); err != nil {
		return err
	}
	return s.verifyCheckpointsLocked(ctx)
}
