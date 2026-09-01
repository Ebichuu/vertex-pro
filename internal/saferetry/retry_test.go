package saferetry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestCloseChainAndRequeueReturnsReadyWithoutHalfCreatedAttempt(t *testing.T) {
	database := openMigrated(t)
	seedClosedChain(t, database, "ADD_FAILED", "1")
	request := baseRequest("cmd-1", time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC))
	result, err := CloseChainAndRequeue(context.Background(), database, request)
	if err != nil || result.JobState != "READY" || result.Idempotent {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertJob(t, database, "job-1", "READY", false)
	assertReleased(t, database, "reservation-1", "NO_EFFECT", 100)
	assertNoHalfCreatedAttempt(t, database, "job-1")
	var attempts int
	if err := database.QueryRow(`SELECT COUNT(*) FROM submit_intents`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("submit intents = %d", attempts)
	}
	assertCommand(t, database, "cmd-1", CommandCloseChainAndRequeue, "SUCCEEDED", "")
}

func TestCloseChainExpiresPastWindowOrBudget(t *testing.T) {
	database := openMigrated(t)
	seedClosedChain(t, database, "RECONCILED_NO_EFFECT", "1")
	pastWindow := baseRequest("cmd-2", time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC))
	result, err := CloseChainAndRequeue(context.Background(), database, pastWindow)
	if err != nil || result.JobState != "EXPIRED" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertJob(t, database, "job-1", "EXPIRED", false)
	assertCommand(t, database, "cmd-2", CommandCloseChainAndRequeue, "SUCCEEDED", "")

	other := openMigrated(t)
	seedClosedChain(t, other, "ADD_FAILED", "1")
	overBudget := baseRequest("cmd-3", time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC))
	overBudget.MaxAttempts = 1
	result, err = CloseChainAndRequeue(context.Background(), other, overBudget)
	if err != nil || result.JobState != "EXPIRED" {
		t.Fatalf("budget result=%+v err=%v", result, err)
	}
	assertJob(t, other, "job-1", "EXPIRED", false)
}

func TestConfirmNoEffectAndRetryReconcilesUnknownChain(t *testing.T) {
	database := openMigrated(t)
	seedUnknownChain(t, database, "1")
	request := baseRequest("cmd-4", time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC))
	request.SubmitIntentID = "intent-1"
	request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: false}
	result, err := ConfirmNoEffectAndRetry(context.Background(), database, request)
	if err != nil || result.JobState != "READY" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var attemptState string
	var finishedAt sql.NullString
	if err := database.QueryRow(`SELECT attempt_state, finished_at FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState, &finishedAt); err != nil {
		t.Fatal(err)
	}
	if attemptState != "RECONCILED_NO_EFFECT" || !finishedAt.Valid {
		t.Fatalf("attempt facts: %s/%v", attemptState, finishedAt)
	}
	assertJob(t, database, "job-1", "READY", false)
	assertReleased(t, database, "reservation-1", "NO_EFFECT", 100)
	assertNoHalfCreatedAttempt(t, database, "job-1")
	var events int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE to_state='READY'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("requeue events = %d", events)
	}
	assertCommand(t, database, "cmd-4", CommandConfirmNoEffectRetry, "SUCCEEDED", "")
}

func TestConfirmNoEffectAndRetryRejectsStaleEvidenceAndAmbiguousOwner(t *testing.T) {
	database := openMigrated(t)
	seedUnknownChain(t, database, "1")
	stale := baseRequest("cmd-5", time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC))
	stale.SubmitIntentID = "intent-1"
	stale.Evidence = Evidence{SyncGeneration: 3, SnapshotRevision: 7, SnapshotDigest: "baseline"}
	if _, err := ConfirmNoEffectAndRetry(context.Background(), database, stale); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("stale error = %v", err)
	}
	assertJob(t, database, "job-1", "UNKNOWN", true)

	other := openMigrated(t)
	seedUnknownChain(t, other, "1")
	if _, err := other.Exec(`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-other','qb-1','/data-other','VERIFIED','2026-08-29T01:00:00Z','2026-08-29T01:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, identity_key, owner_kind, amount_left_state, amount_left, uncertainty_debit_kind, uncertainty_debit_bytes, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('binding-foreign','target-other','torrent-other','identity-1','UNMANAGED','KNOWN',10,'NONE',0,3,7,'baseline','2026-08-29T01:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ambiguous := baseRequest("cmd-6", time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC))
	ambiguous.SubmitIntentID = "intent-1"
	ambiguous.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after"}
	if _, err := ConfirmNoEffectAndRetry(context.Background(), other, ambiguous); !errors.Is(err, ErrOwnerAmbiguous) {
		t.Fatalf("ambiguous error = %v", err)
	}
	assertJob(t, other, "job-1", "UNKNOWN", true)
	assertCommand(t, other, "cmd-6", CommandConfirmNoEffectRetry, "FAILED", "RETRY_COMMAND_REJECTED")
}

func TestCloseChainRejectsUnclosableStates(t *testing.T) {
	database := openMigrated(t)
	seedUnknownChain(t, database, "1")
	request := baseRequest("cmd-7", time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC))
	if _, err := CloseChainAndRequeue(context.Background(), database, request); !errors.Is(err, ErrNotRetryable) {
		t.Fatalf("unknown chain error = %v", err)
	}
	var attempts, reservations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_reservations WHERE reservation_state='RELEASED'`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_jobs WHERE job_state IN ('READY','EXPIRED')`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 || attempts != 0 {
		t.Fatalf("rejected close mutated state: reservations=%d jobs=%d", reservations, attempts)
	}
	assertCommand(t, database, "cmd-7", CommandCloseChainAndRequeue, "FAILED", "RETRY_COMMAND_REJECTED")
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "safe-retry.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func baseRequest(commandID string, now time.Time) Request {
	return Request{
		CommandID:             commandID,
		JobID:                 "job-1",
		AttemptID:             "attempt-1",
		ReservationID:         "reservation-1",
		StorageTargetID:       "target-1",
		InstanceID:            "qb-1",
		IdentityKey:           "identity-1",
		MaxAttempts:           2,
		ParticipationDeadline: "2026-08-29T01:00:00Z",
		Now:                   now,
	}
}

func seedClosedChain(t *testing.T, database *sql.DB, closedState, suffix string) {
	t.Helper()
	seedChain(t, database, closedState, closedState, suffix, false, "NULL")
}

func seedUnknownChain(t *testing.T, database *sql.DB, suffix string) {
	t.Helper()
	seedChain(t, database, "UNKNOWN", "UNKNOWN", suffix, true, "'attempt-"+suffix+"'")
}

func seedChain(t *testing.T, database *sql.DB, attemptState, jobState, suffix string, withWaiter bool, pointerSQL string) {
	t.Helper()
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	attemptID := "attempt-" + suffix
	jobID := "job-" + suffix
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-" + suffix + "','qb-1','/data-" + suffix + "','VERIFIED',?,?)", []any{now, now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('" + jobID + "','source-1','identity-" + suffix + "','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at, finished_at) VALUES ('" + attemptID + "','" + jobID + "','identity-" + suffix + "'," + suffix + ",'qb-1','target-" + suffix + "','" + attemptState + "','STARTED','POSSIBLE_EFFECT',1,100,?,?)", []any{now, now}},
		{"INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-" + suffix + "','" + attemptID + "','target-" + suffix + "',100,1,'ACTIVE')", nil},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-" + suffix + "','" + attemptID + "','qb-1','identity-" + suffix + "','req','target','STARTED',3,7,'baseline','ABSENT',?,?)", []any{now, now}},
		{"UPDATE race_jobs SET job_state='" + jobState + "', current_attempt_id=" + pointerSQL + " WHERE job_id='" + jobID + "'", nil},
	}
	if withWaiter {
		statements = append(statements, struct {
			query string
			args  []any
		}{"INSERT OR IGNORE INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1','READY',3,0,7,'baseline',?)", []any{now}})
		statements = append(statements, struct {
			query string
			args  []any
		}{"INSERT OR IGNORE INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1',3,7,0,'baseline','/snapshots/baseline.json',1,?)", []any{now}})
		statements = append(statements, struct {
			query string
			args  []any
		}{"INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state, created_at) VALUES ('waiter-" + suffix + "','" + attemptID + "','qb-1',3,7,'identity-" + suffix + "','2026-08-29T00:10:00Z','2026-08-29T01:00:00Z','ACTIVE',?)", []any{now}})
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func assertJob(t *testing.T, database *sql.DB, jobID, wantState string, wantPointer bool) {
	t.Helper()
	var jobState string
	var currentAttempt sql.NullString
	if err := database.QueryRow(`SELECT job_state, current_attempt_id FROM race_jobs WHERE job_id=?`, jobID).Scan(&jobState, &currentAttempt); err != nil {
		t.Fatal(err)
	}
	if jobState != wantState || currentAttempt.Valid != wantPointer {
		t.Fatalf("job facts: %s/%v want %s pointer=%t", jobState, currentAttempt, wantState, wantPointer)
	}
}

func assertReleased(t *testing.T, database *sql.DB, reservationID, reason string, unconsumed int64) {
	t.Helper()
	var reservationState, releaseReason string
	var gotUnconsumed int64
	if err := database.QueryRow(`SELECT reservation_state, release_reason, released_unconsumed_bytes FROM capacity_reservations WHERE reservation_id=?`, reservationID).Scan(&reservationState, &releaseReason, &gotUnconsumed); err != nil {
		t.Fatal(err)
	}
	if reservationState != "RELEASED" || releaseReason != reason || gotUnconsumed != unconsumed {
		t.Fatalf("reservation facts: %s/%s/%d", reservationState, releaseReason, gotUnconsumed)
	}
}

func assertNoHalfCreatedAttempt(t *testing.T, database *sql.DB, jobID string) {
	t.Helper()
	var attempts int
	if err := database.QueryRow(`SELECT COUNT(*) FROM placement_attempts WHERE job_id=?`, jobID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempt count = %d, requeue must not create a new attempt", attempts)
	}
}

func assertCommand(t *testing.T, database *sql.DB, commandID, commandType, status, errorCode string) {
	t.Helper()
	var gotType, gotStatus string
	var gotError sql.NullString
	if err := database.QueryRow(`SELECT command_type, command_status, error_code FROM api_commands WHERE command_id=?`, commandID).Scan(&gotType, &gotStatus, &gotError); err != nil {
		t.Fatal(err)
	}
	if gotType != commandType || gotStatus != status {
		t.Fatalf("command audit: %s/%s want %s/%s", gotType, gotStatus, commandType, status)
	}
	if errorCode == "" && gotError.Valid {
		t.Fatalf("unexpected error code %s", gotError.String)
	}
	if errorCode != "" && (!gotError.Valid || gotError.String != errorCode) {
		t.Fatalf("error code = %v want %s", gotError, errorCode)
	}
}
