package alreadypresent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestRecordClosesChainWithoutDispatchOrAttribution(t *testing.T) {
	database := openMigrated(t)
	seedBranch(t, database, "PRESENT", "torrent-1")
	request := baseRequest()
	result, err := Record(context.Background(), database, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Idempotent {
		t.Fatal("first record reported idempotent")
	}
	var attemptState, dispatchState, sideEffect, torrentID string
	var confirmationGeneration sql.NullInt64
	if err := database.QueryRow(`SELECT attempt_state, dispatch_state, side_effect_fact, qb_torrent_id, confirmation_sync_generation FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState, &dispatchState, &sideEffect, &torrentID, &confirmationGeneration); err != nil {
		t.Fatal(err)
	}
	if attemptState != "ALREADY_PRESENT" || dispatchState != "NOT_STARTED" || sideEffect != "OBSERVED_EFFECT" || torrentID != "torrent-1" || confirmationGeneration.Valid {
		t.Fatalf("attempt facts: %s/%s/%s/%s/%v", attemptState, dispatchState, sideEffect, torrentID, confirmationGeneration)
	}
	var jobState string
	var currentAttempt sql.NullString
	var observation string
	if err := database.QueryRow(`SELECT job_state, current_attempt_id, observation_state FROM race_jobs WHERE job_id='job-1'`).Scan(&jobState, &currentAttempt, &observation); err != nil {
		t.Fatal(err)
	}
	if jobState != "ALREADY_PRESENT" || currentAttempt.Valid || observation != "NOT_APPLICABLE" {
		t.Fatalf("job facts: %s/%v/%s", jobState, currentAttempt, observation)
	}
	var ownerKind string
	var bindingReservation sql.NullString
	var amountLeft int64
	var amountState, debitKind string
	var debitBytes int64
	var generation, revision int64
	var digest string
	if err := database.QueryRow(`SELECT owner_kind, reservation_id, amount_left, amount_left_state, uncertainty_debit_kind, uncertainty_debit_bytes, snapshot_generation, snapshot_revision, snapshot_digest FROM capacity_task_bindings WHERE binding_id='binding-1'`).Scan(&ownerKind, &bindingReservation, &amountLeft, &amountState, &debitKind, &debitBytes, &generation, &revision, &digest); err != nil {
		t.Fatal(err)
	}
	if ownerKind != "UNMANAGED" || bindingReservation.Valid || amountState != "KNOWN" || amountLeft != 2048 || debitKind != "NONE" || debitBytes != 0 || generation != 3 || revision != 7 || digest != "baseline-digest" {
		t.Fatalf("binding facts: %s/%v/%s/%d/%s/%d/%d/%d/%s", ownerKind, bindingReservation, amountState, amountLeft, debitKind, debitBytes, generation, revision, digest)
	}
	var reservationState, releaseReason string
	var unconsumed int64
	var reservationRevision int64
	if err := database.QueryRow(`SELECT reservation_state, release_reason, released_unconsumed_bytes, revision FROM capacity_reservations WHERE reservation_id='reservation-1'`).Scan(&reservationState, &releaseReason, &unconsumed, &reservationRevision); err != nil {
		t.Fatal(err)
	}
	if reservationState != "RELEASED" || releaseReason != "ALREADY_PRESENT" || unconsumed != 100 || reservationRevision != 1 {
		t.Fatalf("reservation facts: %s/%s/%d/%d", reservationState, releaseReason, unconsumed, reservationRevision)
	}
	var intentDispatch string
	if err := database.QueryRow(`SELECT dispatch_state FROM submit_intents WHERE submit_intent_id='intent-1'`).Scan(&intentDispatch); err != nil {
		t.Fatal(err)
	}
	if intentDispatch != "NOT_STARTED" {
		t.Fatalf("intent dispatch state = %s", intentDispatch)
	}
	var events int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE to_state='ALREADY_PRESENT'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("event count = %d", events)
	}
	replay, err := Record(context.Background(), database, request)
	if err != nil || !replay.Idempotent {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE to_state='ALREADY_PRESENT'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("event count after replay = %d", events)
	}
}

func TestRecordUnknownAmountFailsCapacityClosed(t *testing.T) {
	database := openMigrated(t)
	seedBranch(t, database, "PRESENT", "torrent-1")
	seedEpoch(t, database, "TRUSTED")
	request := baseRequest()
	request.AmountLeftKnown = false
	request.AmountLeftErrorCode = "MISSING_IN_FULL_SNAPSHOT"
	request.UncertaintyDebitKind = DebitKindBound
	request.UncertaintyDebitBytes = 512
	if _, err := Record(context.Background(), database, request); err != nil {
		t.Fatal(err)
	}
	var amountState, errorCode, debitKind string
	var debitBytes int64
	var amountLeft sql.NullInt64
	if err := database.QueryRow(`SELECT amount_left_state, amount_left_error_code, uncertainty_debit_kind, uncertainty_debit_bytes, amount_left FROM capacity_task_bindings WHERE binding_id='binding-1'`).Scan(&amountState, &errorCode, &debitKind, &debitBytes, &amountLeft); err != nil {
		t.Fatal(err)
	}
	if amountState != "UNKNOWN" || errorCode != "MISSING_IN_FULL_SNAPSHOT" || debitKind != DebitKindBound || debitBytes != 512 || amountLeft.Valid {
		t.Fatalf("unknown binding facts: %s/%s/%s/%d/%v", amountState, errorCode, debitKind, debitBytes, amountLeft)
	}
	var capacityState string
	if err := database.QueryRow(`SELECT capacity_state FROM capacity_epochs WHERE capacity_epoch_id='epoch-1'`).Scan(&capacityState); err != nil {
		t.Fatal(err)
	}
	if capacityState != "CAPACITY_UNCERTAIN" {
		t.Fatalf("capacity state = %s", capacityState)
	}
}

func TestRecordUnboundedDebitKeepsZeroBytes(t *testing.T) {
	database := openMigrated(t)
	seedBranch(t, database, "PRESENT", "torrent-1")
	request := baseRequest()
	request.AmountLeftKnown = false
	request.AmountLeftErrorCode = "NO_TRUSTED_PRIOR_VALUE"
	request.UncertaintyDebitKind = DebitKindUnbounded
	if _, err := Record(context.Background(), database, request); err != nil {
		t.Fatal(err)
	}
	var debitBytes int64
	if err := database.QueryRow(`SELECT uncertainty_debit_bytes FROM capacity_task_bindings WHERE binding_id='binding-1'`).Scan(&debitBytes); err != nil {
		t.Fatal(err)
	}
	if debitBytes != 0 {
		t.Fatalf("debit bytes = %d", debitBytes)
	}
}

func TestRecordFailureLeavesNoPartialWrites(t *testing.T) {
	database := openMigrated(t)
	seedBranch(t, database, "PRESENT", "torrent-1")
	if _, err := database.Exec(`UPDATE race_jobs SET job_state='READY', current_attempt_id=NULL WHERE job_id='job-1'`); err != nil {
		t.Fatal(err)
	}
	request := baseRequest()
	if _, err := Record(context.Background(), database, request); err == nil {
		t.Fatal("mismatched job recorded as already-present")
	}
	var attemptState string
	if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "RESERVED" {
		t.Fatalf("attempt state after rollback = %s", attemptState)
	}
	var bindings int
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_task_bindings`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 {
		t.Fatalf("binding count after rollback = %d", bindings)
	}
}

func TestRecordRejectsInvalidBranches(t *testing.T) {
	tests := []struct {
		name    string
		seed    func(t *testing.T, database *sql.DB)
		mutate  func(request *Request)
		message string
	}{
		{
			name:    "absent baseline",
			seed:    func(t *testing.T, database *sql.DB) { seedBranch(t, database, "ABSENT", "") },
			message: "did not freeze a pre-add present identity",
		},
		{
			name: "dispatched attempt",
			seed: func(t *testing.T, database *sql.DB) {
				seedBranch(t, database, "PRESENT", "torrent-1")
				if _, err := database.Exec(`UPDATE placement_attempts SET attempt_state='ADD_SENT', dispatch_state='STARTED', side_effect_fact='POSSIBLE_EFFECT' WHERE attempt_id='attempt-1'`); err != nil {
					t.Fatal(err)
				}
			},
			message: "attempt state ADD_SENT/STARTED",
		},
		{
			name:    "torrent mismatch",
			seed:    func(t *testing.T, database *sql.DB) { seedBranch(t, database, "PRESENT", "torrent-1") },
			mutate:  func(request *Request) { request.QBTorrentID = "torrent-2" },
			message: "did not freeze a pre-add present identity",
		},
		{
			name: "reservation-owned binding",
			seed: func(t *testing.T, database *sql.DB) {
				seedBranch(t, database, "PRESENT", "torrent-1")
				if _, err := database.Exec(`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, identity_key, owner_kind, reservation_id, amount_left_state, amount_left, uncertainty_debit_kind, uncertainty_debit_bytes, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('binding-conflict','target-1','torrent-1','identity-1','RESERVATION','reservation-1','KNOWN',10,'NONE',0,3,7,'baseline-digest','2026-08-26T01:00:00Z')`); err != nil {
					t.Fatal(err)
				}
			},
			message: "conflicting binding",
		},
		{
			name: "released reservation",
			seed: func(t *testing.T, database *sql.DB) {
				seedBranch(t, database, "PRESENT", "torrent-1")
				if _, err := database.Exec(`UPDATE capacity_reservations SET reservation_state='RELEASED', release_reason='NO_EFFECT', released_unconsumed_bytes=100, released_at='2026-08-26T01:00:30Z' WHERE reservation_id='reservation-1'`); err != nil {
					t.Fatal(err)
				}
			},
			message: "release reservation rows=0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := openMigrated(t)
			test.seed(t, database)
			request := baseRequest()
			if test.mutate != nil {
				test.mutate(&request)
			}
			if _, err := Record(context.Background(), database, request); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateRequestRejectsInvalidFacts(t *testing.T) {
	valid := baseRequest()
	if err := validateRequest(valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(request *Request)
	}{
		{"missing binding", func(request *Request) { request.BindingID = "" }},
		{"zero time", func(request *Request) { request.Now = time.Time{} }},
		{"negative amount", func(request *Request) { request.AmountLeft = -1 }},
		{"known with error code", func(request *Request) { request.AmountLeftErrorCode = "MISSING_IN_FULL_SNAPSHOT" }},
		{"known with debit", func(request *Request) {
			request.UncertaintyDebitKind = DebitKindBound
			request.UncertaintyDebitBytes = 5
		}},
		{"unknown without error code", func(request *Request) { request.AmountLeftKnown = false; request.AmountLeftErrorCode = "" }},
		{"unknown invalid code", func(request *Request) {
			request.AmountLeftKnown = false
			request.AmountLeftErrorCode = "SOMETHING_ELSE"
			request.UncertaintyDebitKind = DebitKindUnbounded
		}},
		{"unknown bound without bytes", func(request *Request) {
			request.AmountLeftKnown = false
			request.AmountLeftErrorCode = "TYPE_MISMATCH"
			request.UncertaintyDebitKind = DebitKindBound
		}},
		{"unknown unbounded with bytes", func(request *Request) {
			request.AmountLeftKnown = false
			request.AmountLeftErrorCode = "TYPE_MISMATCH"
			request.UncertaintyDebitKind = DebitKindUnbounded
			request.UncertaintyDebitBytes = 7
		}},
		{"unknown without debit kind", func(request *Request) { request.AmountLeftKnown = false; request.AmountLeftErrorCode = "TYPE_MISMATCH" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := validateRequest(request); err == nil {
				t.Fatal("validation error = nil")
			}
		})
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "already-present.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func baseRequest() Request {
	request := Request{
		BindingID:             "binding-1",
		AttemptID:             "attempt-1",
		SubmitIntentID:        "intent-1",
		ReservationID:         "reservation-1",
		JobID:                 "job-1",
		StorageTargetID:       "target-1",
		InstanceID:            "qb-1",
		IdentityKey:           "identity-1",
		QBTorrentID:           "torrent-1",
		AmountLeftKnown:       true,
		AmountLeft:            2048,
		UncertaintyDebitKind:  DebitKindNone,
		UncertaintyDebitBytes: 0,
		Now:                   time.Date(2026, 8, 26, 1, 1, 0, 0, time.UTC),
	}
	return request
}

func seedBranch(t *testing.T, database *sql.DB, preFact, preTorrent string) {
	t.Helper()
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED',?,?)", []any{now, now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1','identity-1','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','RESERVED','NOT_STARTED',1,100,?)", []any{now}},
		{"INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-1','attempt-1','target-1',100,1,'ACTIVE')", nil},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, pre_add_qb_torrent_id, created_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','req','target','NOT_STARTED',3,7,'baseline-digest',?, ?,?)", []any{preFact, nullableText(preTorrent), now}},
		{"UPDATE race_jobs SET job_state='RESERVED', current_attempt_id='attempt-1' WHERE job_id='job-1'", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func seedEpoch(t *testing.T, database *sql.DB, capacityState string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1','target-1',1,?,1000,900,3,7,'baseline-digest','2026-08-26T01:00:00Z')`, capacityState); err != nil {
		t.Fatal(err)
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
