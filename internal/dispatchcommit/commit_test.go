package dispatchcommit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
)

func TestBeginPersistsDispatchMarkerBeforeSendAndRejectsReplay(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	result, err := Begin(context.Background(), database, BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != AttemptAddSent {
		t.Fatalf("result=%+v", result)
	}
	assertStates(t, database, AttemptAddSent, "STARTED", "POSSIBLE_EFFECT", true)
	var startedAt string
	var responseFactAt sql.NullString
	if err := database.QueryRow(`SELECT dispatch_started_at, response_fact_at FROM submit_intents WHERE submit_intent_id='intent-1'`).Scan(&startedAt, &responseFactAt); err != nil {
		t.Fatal(err)
	}
	if startedAt == "" || responseFactAt.Valid {
		t.Fatalf("started_at=%q response_fact_at=%v", startedAt, responseFactAt)
	}
	if _, err := Begin(context.Background(), database, BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now}); !errors.Is(err, ErrDispatchAlreadyStarted) {
		t.Fatalf("replay error=%v", err)
	}
	assertEventCount(t, database, 1)
}

func TestBeginConcurrentCallersAllowExactlyOneDispatchOwner(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	errorsByCaller := make(chan error, 2)
	for caller := 0; caller < 2; caller++ {
		go func() {
			<-start
			_, err := Begin(context.Background(), database, BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now})
			errorsByCaller <- err
		}()
	}
	close(start)
	succeeded, rejected := 0, 0
	for caller := 0; caller < 2; caller++ {
		err := <-errorsByCaller
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrDispatchAlreadyStarted):
			rejected++
		default:
			t.Fatalf("unexpected begin error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
	assertStates(t, database, AttemptAddSent, "STARTED", "POSSIBLE_EFFECT", true)
	assertEventCount(t, database, 1)
}

func TestRecordCommitsAwaitingConfirmationAndReplaysIdempotently(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	beginDispatch(t, database, now)
	request := Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now.Add(time.Second), Result: dispatchworker.Result{AttemptID: "attempt-1", DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT", Response: qbresponse.Result{APIVersion: "2.11.2", ResponseFormat: "legacy_text", SuccessCount: 1, DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT"}}}
	result, err := Record(context.Background(), database, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != AttemptAwaitingConfirmation || result.Idempotent {
		t.Fatalf("result=%+v", result)
	}
	assertStates(t, database, AttemptAwaitingConfirmation, "STARTED", "POSSIBLE_EFFECT", true)
	replay, err := Record(context.Background(), database, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Idempotent || replay.State != AttemptAwaitingConfirmation {
		t.Fatalf("replay=%+v", replay)
	}
	assertEventCount(t, database, 2)
}

func TestRecordTransportLossCommitsUnknown(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	beginDispatch(t, database, now)
	result, err := Record(context.Background(), database, Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now.Add(time.Second), DispatchErr: errors.New("response lost"), Result: dispatchworker.Result{AttemptID: "attempt-1", DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT", Response: qbresponse.Result{APIVersion: "2.15.1", ResponseFormat: "transport_error", DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != AttemptUnknown {
		t.Fatalf("result=%+v", result)
	}
	assertStates(t, database, AttemptUnknown, "STARTED", "POSSIBLE_EFFECT", true)
}

func TestRecordKnownNotSentClosesAttemptWithoutMakingItRetryable(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	beginDispatch(t, database, now)
	result, err := Record(context.Background(), database, Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now.Add(time.Second), Result: dispatchworker.Result{AttemptID: "attempt-1", DispatchState: "NOT_STARTED", SideEffectFact: "FAILED_NO_EFFECT", Response: qbresponse.Result{APIVersion: "2.11.2", ResponseFormat: "transport_error", DispatchState: "NOT_STARTED", SideEffectFact: "FAILED_NO_EFFECT"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != AttemptAddFailed || result.Idempotent {
		t.Fatalf("result=%+v", result)
	}
	assertStates(t, database, AttemptAddFailed, "STARTED", "FAILED_NO_EFFECT", false)
	replay, err := Record(context.Background(), database, Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now.Add(time.Second), Result: dispatchworker.Result{AttemptID: "attempt-1", DispatchState: "NOT_STARTED", SideEffectFact: "FAILED_NO_EFFECT", Response: qbresponse.Result{APIVersion: "2.11.2", ResponseFormat: "transport_error", DispatchState: "NOT_STARTED", SideEffectFact: "FAILED_NO_EFFECT"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Idempotent {
		t.Fatalf("replay=%+v", replay)
	}
}

func TestRecordRejectsResponseBeforeDurableBegin(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	_, err := Record(context.Background(), database, Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: time.Now(), Result: dispatchworker.Result{AttemptID: "attempt-1", DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT", Response: qbresponse.Result{APIVersion: "2.11.2", ResponseFormat: "legacy_text", SuccessCount: 1, DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT"}}})
	if err == nil || !strings.Contains(err.Error(), "DISPATCH_CHAIN_FACT_MISMATCH") {
		t.Fatalf("error=%v", err)
	}
	assertStates(t, database, "RESERVED", "NOT_STARTED", "", true)
	assertEventCount(t, database, 0)
}

func TestBeginRollsBackWhenWaiterIsMissing(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	if _, err := database.Exec(`DELETE FROM sync_waiters WHERE waiter_id='waiter-1'`); err != nil {
		t.Fatal(err)
	}
	_, err := Begin(context.Background(), database, BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "SYNC_WAITER_NOT_FOUND") {
		t.Fatalf("error=%v", err)
	}
	assertStates(t, database, "RESERVED", "NOT_STARTED", "", true)
	assertEventCount(t, database, 0)
}

func openDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "dispatch.db")})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func seedReserved(t *testing.T, database *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED','2026-09-01T15:00:00Z','2026-09-01T15:00:00Z')`,
		`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1','READY',3,0,7,'baseline','2026-09-01T15:00:00Z')`,
		`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1',3,7,0,'baseline','/snapshots/baseline.json',1,'2026-09-01T15:00:00Z')`,
		`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1','identity-1','READY','MATCHED','READY','PREPARED','2026-09-02T00:00:00Z','2026-09-02T00:00:00Z','2026-09-01T15:00:00Z','2026-09-01T15:00:00Z')`,
		`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','RESERVED','NOT_STARTED',1,100,'2026-09-01T15:00:00Z')`,
		`INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state, reflected_physical_bytes) VALUES ('reservation-1','attempt-1','target-1',100,1,'ACTIVE',0)`,
		`INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','request','target','NOT_STARTED',3,7,'baseline','ABSENT','2026-09-01T15:00:00Z')`,
		`INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state, created_at) VALUES ('waiter-1','attempt-1','qb-1',3,7,'identity-1','2026-09-01T16:10:00Z','2026-09-01T17:00:00Z','ACTIVE','2026-09-01T15:00:00Z')`,
		`UPDATE race_jobs SET job_state='RESERVED', current_attempt_id='attempt-1' WHERE job_id='job-1'`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func beginDispatch(t *testing.T, database *sql.DB, now time.Time) {
	t.Helper()
	if _, err := Begin(context.Background(), database, BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now}); err != nil {
		t.Fatal(err)
	}
}

func assertStates(t *testing.T, database *sql.DB, state, dispatch, sideEffect string, wantPointer bool) {
	t.Helper()
	var attemptState, attemptDispatch, actualSideEffect, jobState, intentDispatch string
	var currentAttempt sql.NullString
	if err := database.QueryRow(`SELECT a.attempt_state, a.dispatch_state, COALESCE(a.side_effect_fact,''), j.job_state, j.current_attempt_id, s.dispatch_state FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id JOIN submit_intents s ON s.attempt_id=a.attempt_id WHERE a.attempt_id='attempt-1'`).Scan(&attemptState, &attemptDispatch, &actualSideEffect, &jobState, &currentAttempt, &intentDispatch); err != nil {
		t.Fatal(err)
	}
	if attemptState != state || jobState != state || attemptDispatch != dispatch || intentDispatch != dispatch || actualSideEffect != sideEffect || currentAttempt.Valid != wantPointer {
		t.Fatalf("states=%s/%s/%s/%s/%s pointer=%v", attemptState, attemptDispatch, actualSideEffect, jobState, intentDispatch, currentAttempt)
	}
	if wantPointer && currentAttempt.String != "attempt-1" {
		t.Fatalf("current attempt=%v", currentAttempt)
	}
}

func assertEventCount(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var events int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE attempt_id='attempt-1'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != want {
		t.Fatalf("events=%d want=%d", events, want)
	}
}
