package firstconfirm

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestConfirmAtomicAndIdempotent(t *testing.T) {
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "confirm.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED',?,?)", []any{now, now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1','identity-1','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','AWAITING_CONFIRMATION','STARTED','POSSIBLE_EFFECT',1,100,?)", []any{now}},
		{"INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-1','attempt-1','target-1',100,1,'ACTIVE')", nil},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','req','target','STARTED',1,1,'before','ABSENT',?,?)", []any{now, now}},
		{"UPDATE race_jobs SET job_state='AWAITING_CONFIRMATION', current_attempt_id='attempt-1' WHERE job_id='job-1'", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	request := Request{BindingID: "binding-1", AttemptID: "attempt-1", SubmitIntentID: "intent-1", ReservationID: "reservation-1", JobID: "job-1", StorageTargetID: "target-1", IdentityKey: "identity-1", QBTorrentID: "torrent-1", SyncGeneration: 2, SnapshotRevision: 1, SnapshotDigest: "after", PreAddSyncGeneration: 1, PreAddSnapshotRevision: 1, PreAddSnapshotDigest: "before", EvidenceDigest: "evidence", Now: time.Date(2026, 8, 26, 1, 1, 0, 0, time.UTC)}
	result, err := Confirm(context.Background(), database, request)
	if err != nil || result.Idempotent {
		t.Fatalf("first confirm = %+v, %v", result, err)
	}
	result, err = Confirm(context.Background(), database, request)
	if err != nil || !result.Idempotent {
		t.Fatalf("repeat confirm = %+v, %v", result, err)
	}
	var state string
	if err := database.QueryRow("SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "HASH_SEEN" {
		t.Fatalf("attempt state = %s", state)
	}
}

func TestConfirmRejectsStaleSnapshotBeforeWrite(t *testing.T) {
	request := Request{BindingID: "b", AttemptID: "a", SubmitIntentID: "i", ReservationID: "r", JobID: "j", StorageTargetID: "s", IdentityKey: "id", QBTorrentID: "q", SyncGeneration: 1, SnapshotRevision: 1, SnapshotDigest: "same", PreAddSyncGeneration: 1, PreAddSnapshotRevision: 1, PreAddSnapshotDigest: "same", EvidenceDigest: "e", Now: time.Now()}
	if _, err := Confirm(context.Background(), nil, request); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("error = %v", err)
	}
}

func TestConfirmUnknownAttemptReachesReconciledConfirmed(t *testing.T) {
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "confirm-unknown.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED',?,?)", []any{now, now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1','identity-1','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','UNKNOWN','STARTED','POSSIBLE_EFFECT',1,100,?)", []any{now}},
		{"INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-1','attempt-1','target-1',100,1,'ACTIVE')", nil},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','req','target','STARTED',1,1,'before','ABSENT',?,?)", []any{now, now}},
		{"UPDATE race_jobs SET job_state='UNKNOWN', current_attempt_id='attempt-1' WHERE job_id='job-1'", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	request := Request{BindingID: "binding-1", AttemptID: "attempt-1", SubmitIntentID: "intent-1", ReservationID: "reservation-1", JobID: "job-1", StorageTargetID: "target-1", IdentityKey: "identity-1", QBTorrentID: "torrent-7", SyncGeneration: 2, SnapshotRevision: 1, SnapshotDigest: "after", PreAddSyncGeneration: 1, PreAddSnapshotRevision: 1, PreAddSnapshotDigest: "before", EvidenceDigest: "evidence", Now: time.Date(2026, 8, 29, 1, 1, 0, 0, time.UTC)}
	if _, err := Confirm(context.Background(), database, request); err != nil {
		t.Fatal(err)
	}
	var attemptState, jobState string
	if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT job_state FROM race_jobs WHERE job_id='job-1'`).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "RECONCILED_CONFIRMED" || jobState != "RECONCILED_CONFIRMED" {
		t.Fatalf("states: %s/%s", attemptState, jobState)
	}
	if result, err := Confirm(context.Background(), database, request); err != nil || !result.Idempotent {
		t.Fatalf("replay = %+v, %v", result, err)
	}
}

var _ *sql.DB
