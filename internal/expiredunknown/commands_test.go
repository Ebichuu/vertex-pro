package expiredunknown

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestConfirmNoEffectAndExpireClosesChainWithAudit(t *testing.T) {
	database := openMigrated(t)
	seedExpiredUnknown(t, database, "1")
	request := baseRequest("cmd-1")
	request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: false}
	result, err := ConfirmNoEffectAndExpire(context.Background(), database, request)
	if err != nil || result.Idempotent {
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
	var jobState string
	var currentAttempt sql.NullString
	if err := database.QueryRow(`SELECT job_state, current_attempt_id FROM race_jobs WHERE job_id='job-1'`).Scan(&jobState, &currentAttempt); err != nil {
		t.Fatal(err)
	}
	if jobState != "EXPIRED" || currentAttempt.Valid {
		t.Fatalf("job facts: %s/%v", jobState, currentAttempt)
	}
	var reservationState, releaseReason string
	var unconsumed int64
	if err := database.QueryRow(`SELECT reservation_state, release_reason, released_unconsumed_bytes FROM capacity_reservations WHERE reservation_id='reservation-1'`).Scan(&reservationState, &releaseReason, &unconsumed); err != nil {
		t.Fatal(err)
	}
	if reservationState != "RELEASED" || releaseReason != "NO_EFFECT" || unconsumed != 100 {
		t.Fatalf("reservation facts: %s/%s/%d", reservationState, releaseReason, unconsumed)
	}
	var events int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE reason_code='MANUAL_CONFIRM_NO_EFFECT'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 3 {
		t.Fatalf("audit events = %d", events)
	}
	assertCommand(t, database, "cmd-1", "CONFIRM_NO_EFFECT_AND_EXPIRE", "SUCCEEDED", "")
	replay, err := ConfirmNoEffectAndExpire(context.Background(), database, request)
	if err != nil || !replay.Idempotent {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestConfirmEffectBindsAndReflects(t *testing.T) {
	database := openMigrated(t)
	seedExpiredUnknown(t, database, "1")
	request := baseRequest("cmd-2")
	request.BindingID = "binding-1"
	request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: true, QBTorrentID: "torrent-9"}
	result, err := ConfirmEffect(context.Background(), database, request)
	if err != nil || result.Idempotent {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var attemptState, torrentID string
	if err := database.QueryRow(`SELECT attempt_state, qb_torrent_id FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState, &torrentID); err != nil {
		t.Fatal(err)
	}
	if attemptState != "RECONCILED_CONFIRMED" || torrentID != "torrent-9" {
		t.Fatalf("attempt facts: %s/%s", attemptState, torrentID)
	}
	var jobState, observation string
	var currentAttempt sql.NullString
	if err := database.QueryRow(`SELECT job_state, current_attempt_id, observation_state FROM race_jobs WHERE job_id='job-1'`).Scan(&jobState, &currentAttempt, &observation); err != nil {
		t.Fatal(err)
	}
	if jobState != "RECONCILED_CONFIRMED" || !currentAttempt.Valid || observation != "ACTIVE" {
		t.Fatalf("job facts: %s/%v/%s", jobState, currentAttempt, observation)
	}
	var ownerKind, bindingReservation string
	if err := database.QueryRow(`SELECT owner_kind, reservation_id FROM capacity_task_bindings WHERE binding_id='binding-1'`).Scan(&ownerKind, &bindingReservation); err != nil {
		t.Fatal(err)
	}
	if ownerKind != "RESERVATION" || bindingReservation != "reservation-1" {
		t.Fatalf("binding facts: %s/%s", ownerKind, bindingReservation)
	}
	var reservationState string
	if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations WHERE reservation_id='reservation-1'`).Scan(&reservationState); err != nil {
		t.Fatal(err)
	}
	if reservationState != "REFLECTING" {
		t.Fatalf("reservation state = %s", reservationState)
	}
	assertCommand(t, database, "cmd-2", "CONFIRM_EFFECT", "SUCCEEDED", "")
	replay, err := ConfirmEffect(context.Background(), database, request)
	if err != nil || !replay.Idempotent {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestManualClosureRejectionsLeaveChainUntouched(t *testing.T) {
	tests := []struct {
		name          string
		seed          func(t *testing.T, database *sql.DB)
		command       func(ctx context.Context, database *sql.DB, request Request) (Result, error)
		kind          string
		mutate        func(request *Request)
		wantErr       error
		expectAttempt string
	}{
		{
			name:    "stale evidence no-effect",
			seed:    func(t *testing.T, database *sql.DB) { seedExpiredUnknown(t, database, "1") },
			command: ConfirmNoEffectAndExpire,
			kind:    CommandConfirmNoEffectAndExpire,
			mutate: func(request *Request) {
				request.Evidence = Evidence{SyncGeneration: 3, SnapshotRevision: 7, SnapshotDigest: "baseline"}
			},
			wantErr: ErrInvalidEvidence,
		},
		{
			name:    "present evidence for no-effect",
			seed:    func(t *testing.T, database *sql.DB) { seedExpiredUnknown(t, database, "1") },
			command: ConfirmNoEffectAndExpire,
			kind:    CommandConfirmNoEffectAndExpire,
			mutate: func(request *Request) {
				request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: true, QBTorrentID: "torrent-9"}
			},
			wantErr: ErrInvalidEvidence,
		},
		{
			name:    "absent evidence for confirm",
			seed:    func(t *testing.T, database *sql.DB) { seedExpiredUnknown(t, database, "1") },
			command: ConfirmEffect,
			kind:    CommandConfirmEffect,
			mutate: func(request *Request) {
				request.BindingID = "binding-1"
				request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after"}
			},
			wantErr: ErrInvalidEvidence,
		},
		{
			name: "owner ambiguous no-effect",
			seed: func(t *testing.T, database *sql.DB) {
				seedExpiredUnknown(t, database, "1")
				seedForeignBinding(t, database)
			},
			command: ConfirmNoEffectAndExpire,
			kind:    CommandConfirmNoEffectAndExpire,
			mutate: func(request *Request) {
				request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after"}
			},
			wantErr: ErrOwnerAmbiguous,
		},
		{
			name: "owner ambiguous confirm",
			seed: func(t *testing.T, database *sql.DB) {
				seedExpiredUnknown(t, database, "1")
				seedForeignBinding(t, database)
			},
			command: ConfirmEffect,
			kind:    CommandConfirmEffect,
			mutate: func(request *Request) {
				request.BindingID = "binding-1"
				request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: true, QBTorrentID: "torrent-9"}
			},
			wantErr: ErrOwnerAmbiguous,
		},
		{
			name: "attempt not expired unknown",
			seed: func(t *testing.T, database *sql.DB) {
				seedExpiredUnknown(t, database, "1")
				if _, err := database.Exec(`UPDATE placement_attempts SET attempt_state='UNKNOWN' WHERE attempt_id='attempt-1'`); err != nil {
					t.Fatal(err)
				}
			},
			command: ConfirmNoEffectAndExpire,
			kind:    CommandConfirmNoEffectAndExpire,
			mutate: func(request *Request) {
				request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after"}
			},
			wantErr:       ErrNotExpirable,
			expectAttempt: "UNKNOWN",
		},
		{
			name: "reservation already released",
			seed: func(t *testing.T, database *sql.DB) {
				seedExpiredUnknown(t, database, "1")
				if _, err := database.Exec(`UPDATE capacity_reservations SET reservation_state='RELEASED', release_reason='NO_EFFECT', released_unconsumed_bytes=100, released_at='2026-08-29T01:30:00Z' WHERE reservation_id='reservation-1'`); err != nil {
					t.Fatal(err)
				}
			},
			command: ConfirmNoEffectAndExpire,
			kind:    CommandConfirmNoEffectAndExpire,
			mutate: func(request *Request) {
				request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after"}
			},
			wantErr: ErrNotExpirable,
		},
		{
			name:    "attempt facts mismatch",
			seed:    func(t *testing.T, database *sql.DB) { seedExpiredUnknown(t, database, "1") },
			command: ConfirmNoEffectAndExpire,
			kind:    CommandConfirmNoEffectAndExpire,
			mutate: func(request *Request) {
				request.IdentityKey = "identity-other"
				request.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after"}
			},
			wantErr: ErrNotExpirable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openMigrated(t)
			test.seed(t, database)
			request := baseRequest("cmd-reject")
			test.mutate(&request)
			if _, err := test.command(context.Background(), database, request); !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v", err)
			}
			var attemptState, reservationState string
			if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations WHERE reservation_id='reservation-1'`).Scan(&reservationState); err != nil {
				t.Fatal(err)
			}
			expected := test.expectAttempt
			if expected == "" {
				expected = "EXPIRED_UNKNOWN"
			}
			expectedReservation := "ACTIVE"
			if test.name == "reservation already released" {
				expectedReservation = "RELEASED"
			}
			if attemptState != expected || reservationState != expectedReservation {
				t.Fatalf("chain changed after rejection: %s/%s want %s/%s", attemptState, reservationState, expected, expectedReservation)
			}
			assertCommand(t, database, "cmd-reject", test.kind, "FAILED", "EXPIRED_UNKNOWN_COMMAND_REJECTED")
		})
	}
}

func TestValidateBaseRejectsIncompleteRequests(t *testing.T) {
	valid := baseRequest("cmd-v")
	valid.Evidence = Evidence{SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after"}
	if err := validateBase(valid); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(request *Request){
		"missing command":   func(request *Request) { request.CommandID = "" },
		"missing job":       func(request *Request) { request.JobID = "" },
		"zero time":         func(request *Request) { request.Now = time.Time{} },
		"incomplete digest": func(request *Request) { request.Evidence.SnapshotDigest = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if err := validateBase(request); err == nil {
				t.Fatal("validation error = nil")
			}
		})
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "expired-unknown.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func baseRequest(commandID string) Request {
	return Request{
		CommandID:       commandID,
		JobID:           "job-1",
		AttemptID:       "attempt-1",
		SubmitIntentID:  "intent-1",
		ReservationID:   "reservation-1",
		StorageTargetID: "target-1",
		InstanceID:      "qb-1",
		IdentityKey:     "identity-1",
		Now:             time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC),
	}
}

func seedExpiredUnknown(t *testing.T, database *sql.DB, suffix string) {
	t.Helper()
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	attemptID := "attempt-" + suffix
	jobID := "job-" + suffix
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-" + suffix + "','qb-1','/data-" + suffix + "','VERIFIED',?,?)", []any{now, now}},
		{"INSERT OR IGNORE INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1','READY',3,0,7,'baseline',?)", []any{now}},
		{"INSERT OR IGNORE INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1',3,7,0,'baseline','/snapshots/baseline.json',1,?)", []any{now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('" + jobID + "','source-1','identity-" + suffix + "','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at, finished_at) VALUES ('" + attemptID + "','" + jobID + "','identity-" + suffix + "'," + suffix + ",'qb-1','target-" + suffix + "','EXPIRED_UNKNOWN','STARTED','POSSIBLE_EFFECT',1,100,?,?)", []any{now, now}},
		{"INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-" + suffix + "','" + attemptID + "','target-" + suffix + "',100,1,'ACTIVE')", nil},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-" + suffix + "','" + attemptID + "','qb-1','identity-" + suffix + "','req','target','STARTED',3,7,'baseline','ABSENT',?,?)", []any{now, now}},
		{"INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state, created_at, closed_at) VALUES ('waiter-" + suffix + "','" + attemptID + "','qb-1',3,7,'identity-" + suffix + "','2026-08-29T00:10:00Z','2026-08-29T01:00:00Z','CLOSED',?,?)", []any{now, now}},
		{"UPDATE race_jobs SET job_state='EXPIRED_UNKNOWN', current_attempt_id='" + attemptID + "' WHERE job_id='" + jobID + "'", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func seedForeignBinding(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-other','qb-1','/data-other','VERIFIED','2026-08-29T01:00:00Z','2026-08-29T01:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, identity_key, owner_kind, amount_left_state, amount_left, uncertainty_debit_kind, uncertainty_debit_bytes, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('binding-foreign','target-other','torrent-other','identity-1','UNMANAGED','KNOWN',10,'NONE',0,3,7,'baseline','2026-08-29T01:00:00Z')`); err != nil {
		t.Fatal(err)
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
