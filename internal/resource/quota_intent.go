package resource

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const (
	maxQuotaIntentBindingBytes = 32 << 10
	maxQuotaIntentPayloadBytes = 2 << 20
	maxQuotaIntentRecords      = 4096
	maxQuotaIntentTotalBytes   = 16 << 20
)

const quotaReservationIntentsDDL = `CREATE TABLE quota_reservation_intents (
	allocation_id TEXT NOT NULL PRIMARY KEY CHECK(length(CAST(allocation_id AS BLOB))=32) REFERENCES quota_allocations(id),
	schema_version INTEGER NOT NULL CHECK(schema_version=1),
	binding_digest TEXT NOT NULL CHECK(length(CAST(binding_digest AS BLOB))=64),
	binding BLOB NOT NULL CHECK(length(CAST(binding AS BLOB)) BETWEEN 1 AND 32768),
	payload_digest TEXT NOT NULL CHECK(length(CAST(payload_digest AS BLOB))=64),
	payload BLOB NOT NULL CHECK(length(CAST(payload AS BLOB))<=2097152)
)`

type reservationIntentInput struct {
	Binding       []byte
	BindingDigest string
	Payload       []byte
	PayloadDigest string
}

type ReservationIntent struct {
	Match   ReservationMatch
	Binding []byte
}

// ReserveOrMatchWithIntent binds a small stable interpretation key and the
// exact first payload to the physical reservation in one SQLite transaction.
// When an exact RESERVED/WRITING reservation already exists, it returns the
// payload that was admitted originally. Once committed, the payload bytes are
// erased and the stable binding remains available for receipt validation.
func (l *Ledger) ReserveOrMatchWithIntent(ctx context.Context, request Request, binding, payload []byte) (ReservationMatch, []byte, error) {
	if len(binding) == 0 || len(binding) > maxQuotaIntentBindingBytes || len(payload) == 0 || len(payload) > maxQuotaIntentPayloadBytes {
		return ReservationMatch{}, nil, c.Fail(c.InvalidArgument, "invalid bounded physical quota reservation intent")
	}
	intent := reservationIntentInput{
		Binding:       append([]byte(nil), binding...),
		BindingDigest: c.HashBytes(binding),
		Payload:       append([]byte(nil), payload...),
		PayloadDigest: c.HashBytes(payload),
	}
	request.RequestDigest = intent.PayloadDigest
	match, saved, err := l.reserveOrMatchIntent(ctx, request, true, &intent)
	return match, saved, err
}

// FindReservationIntent reads the existing exact intent for a target without
// changing its lifecycle. It is useful to validate a narrowly recognized
// writer-owned suffix before attempting a retry with its original binding.
func (l *Ledger) FindReservationIntent(ctx context.Context, target, writer, family string) (ReservationIntent, bool, error) {
	if target == "" || !filepath.IsAbs(target) || len(writer) == 0 || len(writer) > 64 || len(family) == 0 || len(family) > 128 {
		return ReservationIntent{}, false, c.Fail(c.InvalidArgument, "invalid physical quota intent lookup")
	}
	target = filepath.Clean(target)
	var result ReservationIntent
	found := false
	err := l.withReadLock(ctx, func() error {
		rows, err := l.db.QueryContext(ctx, `SELECT id,writer,family,request_digest,target_root,volume_identity,parent_path,parent_identity,target_identity,target_existed,class,status,upper_bytes,upper_entries
			FROM quota_allocations WHERE target_root=? AND writer=? AND family=? AND class=? AND status IN ('RESERVED','WRITING','RETAINED','FREE_PENDING','RELEASED')`, target, writer, family, string(Payload))
		if err != nil {
			return err
		}
		var candidate reservationCandidate
		count := 0
		for rows.Next() {
			if err = rows.Scan(&candidate.id, &candidate.writer, &candidate.family, &candidate.digest, &candidate.target, &candidate.volume, &candidate.parentPath, &candidate.parentIdentity, &candidate.targetIdentity, &candidate.targetExisted, &candidate.class, &candidate.status, &candidate.upperBytes, &candidate.upperEntries); err != nil {
				rows.Close()
				return err
			}
			count++
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		if count != 1 {
			return c.Fail(c.StoreIntegrityError, "physical quota target has ambiguous reservations")
		}
		if !validID(candidate.id) || !c.ValidDigest(candidate.digest) || candidate.target != target || candidate.writer != writer || candidate.family != family || candidate.class != string(Payload) || candidate.upperBytes < 0 || candidate.upperEntries <= 0 || candidate.targetExisted < 0 || candidate.targetExisted > 1 {
			return c.Fail(c.StoreIntegrityError, "physical quota intent reservation binding is corrupt")
		}
		if err = verifyMatchedReservation(candidate, true); err != nil {
			return err
		}
		var schemaVersion int
		var bindingDigest, payloadDigest string
		var bindingBytes, payloadBytes int
		err = l.db.QueryRowContext(ctx, "SELECT schema_version,binding_digest,length(binding),payload_digest,length(payload) FROM quota_reservation_intents WHERE allocation_id=?", candidate.id).Scan(&schemaVersion, &bindingDigest, &bindingBytes, &payloadDigest, &payloadBytes)
		if errors.Is(err, sql.ErrNoRows) {
			return c.Fail(c.StaleAuthority, "existing physical quota allocation has no saved retry intent")
		}
		if err != nil {
			return err
		}
		if schemaVersion != 1 || bindingBytes < 1 || bindingBytes > maxQuotaIntentBindingBytes || payloadBytes < 0 || payloadBytes > maxQuotaIntentPayloadBytes || !c.ValidDigest(bindingDigest) || !c.ValidDigest(payloadDigest) || payloadDigest != candidate.digest || (payloadBytes == 0 && candidate.status != "RETAINED" && candidate.status != "FREE_PENDING" && candidate.status != "RELEASED") || (payloadBytes > 0 && (candidate.status == "RETAINED" || candidate.status == "FREE_PENDING" || candidate.status == "RELEASED")) {
			return c.Fail(c.StoreIntegrityError, "physical quota saved retry intent is corrupt")
		}
		var binding, payload []byte
		if err = l.db.QueryRowContext(ctx, "SELECT binding,payload FROM quota_reservation_intents WHERE allocation_id=?", candidate.id).Scan(&binding, &payload); err != nil {
			return err
		}
		if len(binding) != bindingBytes || len(payload) != payloadBytes || bindingDigest != c.HashBytes(binding) || (len(payload) > 0 && payloadDigest != c.HashBytes(payload)) {
			return c.Fail(c.StoreIntegrityError, "physical quota saved retry intent is corrupt")
		}
		result.Match = ReservationMatch{Allocation: Allocation{ledger: l, ID: candidate.id, targetRoot: candidate.target}, Scope: ExactReservationScope, Status: candidate.status, UpperBytes: uint64(candidate.upperBytes), UpperEntries: uint64(candidate.upperEntries), RequestDigest: candidate.digest}
		result.Binding = append([]byte(nil), binding...)
		found = true
		return nil
	})
	if err != nil {
		return ReservationIntent{}, false, err
	}
	return result, found, nil
}

func quotaIntentAdmissionBytes(bindingBytes, payloadBytes int) int64 {
	return int64(bindingBytes+payloadBytes)*2 + metadataAdmission
}

func insertReservationIntentTx(ctx context.Context, tx *sql.Tx, allocationID string, intent reservationIntentInput) error {
	var count int
	var total int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(binding)+length(payload)),0) FROM quota_reservation_intents").Scan(&count, &total); err != nil {
		return err
	}
	newBytes := int64(len(intent.Binding) + len(intent.Payload))
	if count < 0 || count >= maxQuotaIntentRecords || total < 0 || newBytes > maxQuotaIntentTotalBytes-total {
		return c.Fail(c.BudgetLimitReached, "physical quota reservation intent catalog is full")
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO quota_reservation_intents(allocation_id,schema_version,binding_digest,binding,payload_digest,payload) VALUES(?,1,?,?,?,?)", allocationID, intent.BindingDigest, intent.Binding, intent.PayloadDigest, intent.Payload)
	return err
}

func readMatchedReservationIntentTx(ctx context.Context, tx *sql.Tx, candidate reservationCandidate, request reservationIntentInput) ([]byte, error) {
	var schemaVersion int
	var bindingDigest, payloadDigest string
	var bindingBytes, payloadBytes int
	err := tx.QueryRowContext(ctx, "SELECT schema_version,binding_digest,length(binding),payload_digest,length(payload) FROM quota_reservation_intents WHERE allocation_id=?", candidate.id).Scan(&schemaVersion, &bindingDigest, &bindingBytes, &payloadDigest, &payloadBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, c.Fail(c.StaleAuthority, "existing physical quota allocation has no saved retry intent")
	}
	if err != nil {
		return nil, err
	}
	if schemaVersion != 1 || bindingBytes < 1 || bindingBytes > maxQuotaIntentBindingBytes || payloadBytes < 0 || payloadBytes > maxQuotaIntentPayloadBytes || !c.ValidDigest(bindingDigest) || !c.ValidDigest(payloadDigest) || payloadDigest != candidate.digest {
		return nil, c.Fail(c.StoreIntegrityError, "physical quota reservation intent is corrupt")
	}
	if (payloadBytes == 0 && candidate.status != "RETAINED" && candidate.status != "FREE_PENDING" && candidate.status != "RELEASED") || (payloadBytes > 0 && (candidate.status == "RETAINED" || candidate.status == "FREE_PENDING" || candidate.status == "RELEASED")) {
		return nil, c.Fail(c.StoreIntegrityError, "physical quota reservation intent payload lifecycle is invalid")
	}
	var binding []byte
	if err = tx.QueryRowContext(ctx, "SELECT binding FROM quota_reservation_intents WHERE allocation_id=?", candidate.id).Scan(&binding); err != nil {
		return nil, err
	}
	if len(binding) != bindingBytes || bindingDigest != c.HashBytes(binding) {
		return nil, c.Fail(c.StoreIntegrityError, "physical quota reservation intent is corrupt")
	}
	if bindingDigest != request.BindingDigest || !c.ValidDigest(request.BindingDigest) || !bytesEqual(binding, request.Binding) {
		return nil, c.Fail(c.StaleAuthority, "physical quota retry binding differs from its saved intent")
	}
	if payloadBytes == 0 {
		return nil, nil
	}
	var payload []byte
	if err = tx.QueryRowContext(ctx, "SELECT payload FROM quota_reservation_intents WHERE allocation_id=?", candidate.id).Scan(&payload); err != nil {
		return nil, err
	}
	if len(payload) != payloadBytes || payloadDigest != c.HashBytes(payload) {
		return nil, c.Fail(c.StoreIntegrityError, "physical quota saved retry payload is corrupt")
	}
	return append([]byte(nil), payload...), nil
}

func eraseReservationIntentPayloadTx(ctx context.Context, tx *sql.Tx, allocationID string) error {
	_, err := tx.ExecContext(ctx, "UPDATE quota_reservation_intents SET payload=X'' WHERE allocation_id=? AND length(payload)>0", allocationID)
	return err
}

func ensureReservationIntentSchema(db *sql.DB) error {
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='quota_reservation_intents'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		if _, err := db.Exec(quotaReservationIntentsDDL); err != nil {
			return err
		}
	} else if exists != 1 {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent table is ambiguous")
	}
	var schemaBytes int64
	if err := db.QueryRow("SELECT length(CAST(sql AS BLOB)) FROM sqlite_master WHERE type='table' AND name='quota_reservation_intents'").Scan(&schemaBytes); err != nil {
		return err
	}
	if schemaBytes <= 0 || schemaBytes > 4096 {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent schema is invalid")
	}
	var schemaSQL string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='quota_reservation_intents'").Scan(&schemaSQL); err != nil {
		return err
	}
	storedDDL := strings.ToUpper(strings.Join(strings.Fields(schemaSQL), " "))
	expectedDDL := strings.ToUpper(strings.Join(strings.Fields(quotaReservationIntentsDDL), " "))
	if storedDDL != expectedDDL {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent schema is invalid")
	}
	rows, err := db.Query("PRAGMA table_info(quota_reservation_intents)")
	if err != nil {
		return err
	}
	want := []struct{ name, kind string }{{"allocation_id", "TEXT"}, {"schema_version", "INTEGER"}, {"binding_digest", "TEXT"}, {"binding", "BLOB"}, {"payload_digest", "TEXT"}, {"payload", "BLOB"}}
	columns := make([]string, 0, len(want))
	kinds := make([]string, 0, len(want))
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, name)
		kinds = append(kinds, strings.ToUpper(kind))
		if notNull != 1 || (name == "allocation_id" && primaryKey != 1) || (name != "allocation_id" && primaryKey != 0) {
			rows.Close()
			return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent schema is invalid")
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(columns) != len(want) {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent schema is invalid")
	}
	for i := range want {
		if columns[i] != want[i].name || kinds[i] != want[i].kind {
			return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent schema is invalid")
		}
	}
	foreignKeys, err := db.Query("PRAGMA foreign_key_list(quota_reservation_intents)")
	if err != nil {
		return err
	}
	type foreignKey struct{ table, from, to string }
	keys := []foreignKey{}
	for foreignKeys.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err = foreignKeys.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			foreignKeys.Close()
			return err
		}
		keys = append(keys, foreignKey{table, from, to})
	}
	if err = errors.Join(foreignKeys.Err(), foreignKeys.Close()); err != nil {
		return err
	}
	if len(keys) != 1 || keys[0] != (foreignKey{"quota_allocations", "allocation_id", "id"}) {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent allocation binding is invalid")
	}
	var triggers int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='quota_reservation_intents'").Scan(&triggers); err != nil {
		return err
	}
	if triggers != 0 {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent triggers are not permitted")
	}
	var invalidRows int
	if err = db.QueryRow(`SELECT COUNT(*)
		FROM quota_reservation_intents
		WHERE typeof(allocation_id)!='text' OR typeof(schema_version)!='integer' OR typeof(binding_digest)!='text' OR
			typeof(binding)!='blob' OR typeof(payload_digest)!='text' OR typeof(payload)!='blob' OR
		length(binding)<1 OR length(binding)>? OR length(payload)>?`, maxQuotaIntentBindingBytes, maxQuotaIntentPayloadBytes).Scan(&invalidRows); err != nil {
		return err
	}
	if invalidRows != 0 {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent catalog contains oversized or invalid rows")
	}
	var count int
	var total int64
	if err = db.QueryRow("SELECT COUNT(*),COALESCE(SUM(length(binding)+length(payload)),0) FROM quota_reservation_intents").Scan(&count, &total); err != nil {
		return err
	}
	if count < 0 || count > maxQuotaIntentRecords || total < 0 || total > maxQuotaIntentTotalBytes {
		return c.Fail(c.StoreIntegrityError, "physical quota reservation-intent catalog exceeds its bounded profile")
	}
	rows, err = db.Query(`SELECT intent.allocation_id,intent.schema_version,intent.binding_digest,intent.binding,intent.payload_digest,intent.payload,allocation.request_digest,allocation.status
		FROM quota_reservation_intents AS intent LEFT JOIN quota_allocations AS allocation ON allocation.id=intent.allocation_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, bindingDigest, payloadDigest, requestDigest, status string
		var schemaVersion int
		var binding, payload []byte
		if err = rows.Scan(&id, &schemaVersion, &bindingDigest, &binding, &payloadDigest, &payload, &requestDigest, &status); err != nil {
			rows.Close()
			return err
		}
		if !validID(id) || schemaVersion != 1 || len(binding) == 0 || len(binding) > maxQuotaIntentBindingBytes || !c.ValidDigest(bindingDigest) || bindingDigest != c.HashBytes(binding) || !c.ValidDigest(payloadDigest) || payloadDigest != requestDigest || len(payload) > maxQuotaIntentPayloadBytes || (len(payload) == 0 && status != "RETAINED" && status != "FREE_PENDING" && status != "RELEASED") || (len(payload) > 0 && (status == "RETAINED" || status == "FREE_PENDING" || status == "RELEASED" || payloadDigest != c.HashBytes(payload))) {
			rows.Close()
			return c.Fail(c.StoreIntegrityError, "physical quota reservation intent is detached or corrupt")
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	return bytes.Equal(a, b)
}
