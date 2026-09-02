package dispatchflow

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
)

type senderFunc func(context.Context, *http.Request) (qbresponse.RawResponse, error)

func (send senderFunc) Send(ctx context.Context, request *http.Request) (qbresponse.RawResponse, error) {
	return send(ctx, request)
}

func TestRunnerPersistsDispatchMarkerBeforeSenderAndCommitsFacts(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	calls := 0
	runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		calls++
		assertDurableMarker(t, database)
		return qbresponse.RawResponse{APIVersion: "2.11.2", StatusCode: 200, Body: []byte("Ok.")}, nil
	}), func() time.Time { return time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: request})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Begin.State != "ADD_SENT" || result.Commit.State != "AWAITING_CONFIRMATION" || result.Dispatch.Response.SuccessCount != 1 {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
	assertAttempt(t, database, "AWAITING_CONFIRMATION", "STARTED", "POSSIBLE_EFFECT", true)
}

func TestRunnerPersistsUnknownAfterNetworkDeadline(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	runner, err := New(database, senderFunc(func(ctx context.Context, _ *http.Request) (qbresponse.RawResponse, error) {
		assertDurableMarker(t, database)
		<-ctx.Done()
		return qbresponse.RawResponse{APIVersion: "2.11.2"}, ctx.Err()
	}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	deadlineContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := runner.Run(deadlineContext, Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: request})
	if err != nil || result.Commit.State != "UNKNOWN" || result.Dispatch.Response.ResponseFormat != "transport_error" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertAttempt(t, database, "UNKNOWN", "STARTED", "POSSIBLE_EFFECT", true)
}

func TestRunnerKnownNotSentClosesAttemptAndNeverReusesIt(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	calls := 0
	runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		calls++
		assertDurableMarker(t, database)
		return qbresponse.RawResponse{}, &dispatchworker.NotSentError{Err: io.ErrClosedPipe}
	}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: request})
	if err != nil {
		t.Fatal(err)
	}
	if result.Commit.State != "ADD_FAILED" || calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
	assertAttempt(t, database, "ADD_FAILED", "STARTED", "FAILED_NO_EFFECT", false)

	secondRequest := addRequest(t, "2.11.2")
	if _, err := runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: secondRequest}); !errors.Is(err, dispatchcommit.ErrDispatchAlreadyStarted) {
		t.Fatalf("second dispatch error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want=1", calls)
	}
}

func TestCrashAfterSendBeforeRecordRecoversUnknownWithoutReplay(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	if _, err := dispatchcommit.Begin(context.Background(), database, dispatchcommit.BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	worker, err := dispatchworker.New(senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		calls++
		assertDurableMarker(t, database)
		return qbresponse.RawResponse{APIVersion: "2.11.2", StatusCode: 200, Body: []byte("Ok.")}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Dispatch(context.Background(), dispatchworker.Request{
		AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: request,
		AttemptState: "ADD_SENT", AttemptDispatchState: "STARTED", AttemptSideEffectFact: "POSSIBLE_EFFECT",
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate kill -9 here: the response was received but Record never ran.
	recovery, err := startuprecover.Recover(context.Background(), database, startuprecover.Input{Now: now.Add(time.Minute), ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.PromotedUnknown) != 1 || recovery.PromotedUnknown[0] != "attempt-1" {
		t.Fatalf("recovery=%+v", recovery)
	}
	assertAttempt(t, database, "UNKNOWN", "STARTED", "POSSIBLE_EFFECT", true)

	runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		calls++
		return qbresponse.RawResponse{}, nil
	}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: addRequest(t, "2.11.2")})
	if !errors.Is(err, dispatchcommit.ErrDispatchAlreadyStarted) {
		t.Fatalf("replay error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("sender calls=%d want=1", calls)
	}
}

func TestRunnerDoesNotSendWhenDurableMarkerAlreadyExists(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	if _, err := dispatchcommit.Begin(context.Background(), database, dispatchcommit.BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		calls++
		return qbresponse.RawResponse{}, nil
	}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: request})
	if !errors.Is(err, dispatchcommit.ErrDispatchAlreadyStarted) {
		t.Fatalf("error=%v", err)
	}
	if calls != 0 {
		t.Fatalf("calls=%d want=0", calls)
	}
	assertAttempt(t, database, "ADD_SENT", "STARTED", "POSSIBLE_EFFECT", true)
}

func TestRunnerRejectsMalformedRequestBeforeDurableMarker(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	request.URL.Path = "/api/v2/app/version"
	calls := 0
	runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		calls++
		return qbresponse.RawResponse{}, nil
	}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: request}); !errors.Is(err, dispatchworker.ErrInvalidDispatchRequest) {
		t.Fatalf("error=%v", err)
	}
	if calls != 0 {
		t.Fatalf("calls=%d want=0", calls)
	}
	assertAttempt(t, database, "RESERVED", "NOT_STARTED", "", true)
}

func TestRunnerCommitsSyntheticResponseFacts(t *testing.T) {
	cases := []struct {
		name           string
		raw            qbresponse.RawResponse
		expectedFormat string
		expectedState  string
	}{
		{name: "202 pending", raw: qbresponse.RawResponse{APIVersion: "2.15.1", StatusCode: http.StatusAccepted, Body: []byte(`{"pending_count":1}`)}, expectedFormat: "json", expectedState: "AWAITING_CONFIRMATION"},
		{name: "204 empty", raw: qbresponse.RawResponse{APIVersion: "2.15.1", StatusCode: http.StatusNoContent}, expectedFormat: "empty", expectedState: "UNKNOWN"},
		{name: "409 conflict", raw: qbresponse.RawResponse{APIVersion: "2.15.1", StatusCode: http.StatusConflict, Body: []byte(`{"failure_count":1}`)}, expectedFormat: "json", expectedState: "UNKNOWN"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database := flowDatabase(t)
			request := prepareReservedFlow(t, database)
			runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
				assertDurableMarker(t, database)
				return testCase.raw, nil
			}), time.Now)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.15.1", HTTPRequest: request})
			if err != nil {
				t.Fatal(err)
			}
			if result.Dispatch.Response.ResponseFormat != testCase.expectedFormat || result.Commit.State != testCase.expectedState {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func prepareReservedFlow(t *testing.T, database *sql.DB) *http.Request {
	t.Helper()
	seedReadyJob(t, database)
	ctx := context.Background()
	if _, err := reservation.ReserveAtomic(ctx, database, reservation.Request{
		JobID: "job-1", AttemptID: "attempt-1", SubmitIntentID: "intent-1", ReservationID: "reservation-1",
		IdentityKey: "identity-1", InstanceID: "qb-1", StorageTargetID: "target-1", RequiredBytes: 100,
		CapacityEpochID: "epoch-1", AllocationEpoch: 1, CapacityRevision: 1, SnapshotGeneration: 3,
		SnapshotRevision: 7, SnapshotDigest: "baseline", RequestFingerprint: "request", TargetFingerprint: "target",
		PreAddIdentityFact: reservation.IdentityAbsent, FinalCandidateRecheckRevision: 1, MaxInflight: 2,
		Now: "2026-09-01T15:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := syncwaiter.Register(ctx, database, syncwaiter.Request{
		WaiterID: "waiter-1", AttemptID: "attempt-1", InstanceID: "qb-1", RegisteredGeneration: 3,
		SourceSnapshotRevision: 7, IdentityKey: "identity-1", ConfirmationDeadline: "2026-09-01T16:10:00Z",
		ReconciliationDeadline: "2026-09-01T17:00:00Z", CreatedAt: "2026-09-01T15:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	return addRequest(t, "2.15.1")
}

func addRequest(t *testing.T, apiVersion string) *http.Request {
	t.Helper()
	profile := qbprofile.Profile{ID: "QB_522", WebAPIVersion: apiVersion, AddStartField: "stopped"}
	if apiVersion == "2.11.2" {
		profile.ID = "QB_504"
	}
	request, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: "test.torrent", Content: []byte("torrent")}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func flowDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "flow.db")})
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

func seedReadyJob(t *testing.T, database *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED','2026-09-01T15:00:00Z','2026-09-01T15:00:00Z')`,
		`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1','target-1',1,'TRUSTED',1000,1000,3,7,'baseline','2026-09-01T15:00:00Z')`,
		`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1','READY',3,0,7,'baseline','2026-09-01T15:00:00Z')`,
		`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1',3,7,0,'baseline','/snapshots/baseline.json',1,'2026-09-01T15:00:00Z')`,
		`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1','identity-1','READY','MATCHED','READY','PREPARED','2026-09-02T00:00:00Z','2026-09-02T00:00:00Z','2026-09-01T15:00:00Z','2026-09-01T15:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func assertDurableMarker(t *testing.T, database *sql.DB) {
	t.Helper()
	assertAttempt(t, database, "ADD_SENT", "STARTED", "POSSIBLE_EFFECT", true)
	var startedAt string
	if err := database.QueryRow(`SELECT dispatch_started_at FROM submit_intents WHERE submit_intent_id='intent-1'`).Scan(&startedAt); err != nil {
		t.Fatal(err)
	}
	if startedAt == "" {
		t.Fatal("dispatch_started_at is empty inside sender")
	}
}

func assertAttempt(t *testing.T, database *sql.DB, wantState, wantDispatch, wantSideEffect string, wantPointer bool) {
	t.Helper()
	var attemptState, dispatchState, sideEffect, jobState string
	var currentAttempt sql.NullString
	if err := database.QueryRow(`SELECT a.attempt_state, a.dispatch_state, COALESCE(a.side_effect_fact,''), j.job_state, j.current_attempt_id FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id WHERE a.attempt_id='attempt-1'`).Scan(&attemptState, &dispatchState, &sideEffect, &jobState, &currentAttempt); err != nil {
		t.Fatal(err)
	}
	if attemptState != wantState || jobState != wantState || dispatchState != wantDispatch || sideEffect != wantSideEffect || currentAttempt.Valid != wantPointer {
		t.Fatalf("attempt=%s/%s/%s job=%s pointer=%v", attemptState, dispatchState, sideEffect, jobState, currentAttempt)
	}
}
