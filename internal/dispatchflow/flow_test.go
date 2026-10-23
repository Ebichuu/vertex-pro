package dispatchflow

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
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
	}), flowFixtureNow)
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
	}), flowFixtureNow)
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
	}), flowFixtureNow)
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

func TestExternalProcessKillAfterSendBeforeRecordRecoversWithoutReplay(t *testing.T) {
	if os.Getenv("VERTEX_PRO_DISPATCH_CRASH_CHILD") == "1" {
		runDispatchCrashChild(t)
		return
	}

	directory := t.TempDir()
	databasePath := filepath.Join(directory, "crash.db")
	database, err := db.Open(context.Background(), db.Config{Path: databasePath, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	migrationRunner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationRunner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	_ = prepareReservedFlow(t, database)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	var networkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil || request.Method != http.MethodPost || request.URL.Path != "/api/v2/torrents/add" || len(body) == 0 {
			http.Error(response, "invalid add", http.StatusBadRequest)
			return
		}
		networkCalls.Add(1)
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("Ok."))
	}))
	defer server.Close()

	signalPath := filepath.Join(directory, "response-received.signal")
	command := exec.Command(os.Args[0], "-test.run=^TestExternalProcessKillAfterSendBeforeRecordRecoversWithoutReplay$")
	command.Env = append(os.Environ(),
		"VERTEX_PRO_DISPATCH_CRASH_CHILD=1",
		"VERTEX_PRO_DISPATCH_CRASH_DB="+databasePath,
		"VERTEX_PRO_DISPATCH_CRASH_URL="+server.URL,
		"VERTEX_PRO_DISPATCH_CRASH_SIGNAL="+signalPath,
	)
	var childOutput bytes.Buffer
	command.Stdout = &childOutput
	command.Stderr = &childOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- command.Wait() }()
	childRunning := true
	t.Cleanup(func() {
		if childRunning && command.Process != nil {
			_ = command.Process.Kill()
			<-childDone
		}
	})

	// 60s 只约束等待边界信号的时长：子进程提前退出仍会立即经 childDone
	// 报错，真实失败不会被延长掩盖；全量套件高负载下 15s 曾偶发不足。
	deadline := time.NewTimer(60 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer poll.Stop()
waitForSignal:
	for {
		select {
		case err := <-childDone:
			childRunning = false
			t.Fatalf("crash child exited before kill boundary: %v\n%s", err, childOutput.String())
		case <-deadline.C:
			t.Fatalf("timed out waiting for post-send/pre-Record boundary\n%s", childOutput.String())
		case <-poll.C:
			if _, err := os.Stat(signalPath); err == nil {
				break waitForSignal
			}
		}
	}
	if networkCalls.Load() != 1 {
		t.Fatalf("network calls before kill=%d want=1", networkCalls.Load())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-childDone; err == nil {
		t.Fatal("crash child exited cleanly after an abrupt kill")
	}
	childRunning = false

	recoveredDatabase, err := db.Open(context.Background(), db.Config{Path: databasePath, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredDatabase.Close()
	now := time.Date(2026, 9, 1, 16, 1, 0, 0, time.UTC)
	recovery, err := startuprecover.Recover(context.Background(), recoveredDatabase, startuprecover.Input{Now: now, ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.PromotedUnknown) != 1 || recovery.PromotedUnknown[0] != "attempt-1" || len(recovery.KeptWaiters) != 1 {
		t.Fatalf("recovery=%+v", recovery)
	}
	assertAttempt(t, recoveredDatabase, "UNKNOWN", "STARTED", "POSSIBLE_EFFECT", true)
	var responseFact sql.NullString
	if err := recoveredDatabase.QueryRow(`SELECT response_fact_at FROM submit_intents WHERE submit_intent_id='intent-1'`).Scan(&responseFact); err != nil {
		t.Fatal(err)
	}
	if responseFact.Valid {
		t.Fatalf("response_fact_at=%q, want NULL after killed process", responseFact.String)
	}
	var reason string
	if err := recoveredDatabase.QueryRow(`SELECT reason_code FROM race_events WHERE attempt_id='attempt-1' AND to_state='UNKNOWN' ORDER BY event_id DESC LIMIT 1`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "STARTUP_INTERRUPTED_DISPATCH" {
		t.Fatalf("recovery reason=%q", reason)
	}

	localReplayCalls := 0
	replayRunner, err := New(recoveredDatabase, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		localReplayCalls++
		return qbresponse.RawResponse{}, nil
	}), flowFixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = replayRunner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: addRequest(t, "2.11.2")})
	if !errors.Is(err, dispatchcommit.ErrDispatchAlreadyStarted) {
		t.Fatalf("replay error=%v", err)
	}
	if localReplayCalls != 0 || networkCalls.Load() != 1 {
		t.Fatalf("local replay calls=%d total network calls=%d", localReplayCalls, networkCalls.Load())
	}
}

func runDispatchCrashChild(t *testing.T) {
	databasePath := os.Getenv("VERTEX_PRO_DISPATCH_CRASH_DB")
	baseURL := os.Getenv("VERTEX_PRO_DISPATCH_CRASH_URL")
	signalPath := os.Getenv("VERTEX_PRO_DISPATCH_CRASH_SIGNAL")
	if databasePath == "" || baseURL == "" || signalPath == "" {
		t.Fatal("crash child environment is incomplete")
	}
	database, err := db.Open(context.Background(), db.Config{Path: databasePath, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, err := New(database, senderFunc(func(ctx context.Context, request *http.Request) (qbresponse.RawResponse, error) {
		target, parseErr := url.Parse(baseURL)
		if parseErr != nil {
			return qbresponse.RawResponse{}, parseErr
		}
		request.URL.Scheme = target.Scheme
		request.URL.Host = target.Host
		request.Host = target.Host
		response, sendErr := (&http.Client{Timeout: 10 * time.Second}).Do(request.WithContext(ctx))
		if sendErr != nil {
			return qbresponse.RawResponse{}, sendErr
		}
		closeErr := response.Body.Close()
		if closeErr != nil {
			return qbresponse.RawResponse{}, closeErr
		}
		if err := os.WriteFile(signalPath, []byte("response received; dispatchcommit.Record not entered\n"), 0o600); err != nil {
			return qbresponse.RawResponse{}, fmt.Errorf("write crash boundary signal: %w", err)
		}
		// The parent terminates this process at the boundary; block forever
		// without reaching Record. Sleep instead of a bare select{}: once the
		// transport goroutines exit, an empty select trips the deadlock
		// detector and the child self-exits before the parent's kill lands.
		for {
			time.Sleep(time.Hour)
		}
	}), func() time.Time { return time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: addRequest(t, "2.11.2")})
	t.Fatalf("dispatch returned before external kill: %v", err)
}

func TestRunnerDoesNotSendWhenDurableMarkerAlreadyExists(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	if _, err := dispatchcommit.Begin(context.Background(), database, dispatchcommit.BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: flowFixtureNow()}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		calls++
		return qbresponse.RawResponse{}, nil
	}), flowFixtureNow)
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
	}), flowFixtureNow)
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
			}), flowFixtureNow)
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

func flowFixtureNow() time.Time {
	return time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
}
