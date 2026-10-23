package dispatchorchestrator

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/placement"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
)

func orchestratorDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "orchestrator.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func placementInput(profileState string) placement.DecisionInput {
	return placement.DecisionInput{
		IdentityKey: "identity-key", TargetMode: placement.TargetFixedInstance, TargetInstanceID: "instance-1", RequiredBytes: 1,
		Weights: placement.DecisionWeights{InstanceWeight: 1, FreeRatioWeight: 1, InflightPenaltyWeight: 1, CurrentDownloadLoadPenaltyWeight: 1, RecentAddLatencyPenaltyWeight: 1},
		Candidates: []placement.DecisionCandidate{{
			InstanceID: "instance-1", StorageTargetID: "target-1", CanPlace: true,
			HealthState: "AVAILABLE", ProfileState: profileState, StorageState: "VERIFIED", CapacityState: "TRUSTED",
			InstanceWeight: 1, FreeRatioPPM: 1_000_000, EffectiveFreeBytes: 1 << 30, MaxInflight: 1,
		}},
	}
}

func reservationRequest() reservation.Request {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	return reservation.Request{
		JobID: "job-1", AttemptID: "attempt-1", SubmitIntentID: "intent-1", ReservationID: "reservation-1",
		IdentityKey: "identity-key", InstanceID: "instance-1", StorageTargetID: "target-1", RequiredBytes: 1,
		CapacityEpochID: "epoch-1", AllocationEpoch: 1, CapacityRevision: 1, SnapshotGeneration: 1,
		SnapshotRevision: 1, SnapshotDigest: "baseline-digest", RequestFingerprint: "request-1", TargetFingerprint: "target-1",
		PreAddIdentityFact: reservation.IdentityAbsent, FinalCandidateRecheckRevision: 1, MaxInflight: 1,
		Now: now.Format(time.RFC3339),
	}
}

func waiterRequest() syncwaiter.Request {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	return syncwaiter.Request{
		WaiterID: "waiter-1", InstanceID: "instance-1", RegisteredGeneration: 1,
		SourceSnapshotRevision: 1, IdentityKey: "identity-key",
		ConfirmationDeadline:   now.Add(2 * time.Minute).Format(time.RFC3339),
		ReconciliationDeadline: now.Add(5 * time.Minute).Format(time.RFC3339),
		CreatedAt:              now.Format(time.RFC3339),
	}
}

// redirectSender mirrors the production qui proxy sender: it rewrites the
// relative qB Add request onto the lab endpoint and returns the raw response.
type redirectSender struct {
	target *url.URL
	calls  *int
}

func (sender redirectSender) Send(ctx context.Context, request *http.Request) (qbresponse.RawResponse, error) {
	outgoing, err := http.NewRequestWithContext(ctx, request.Method, sender.target.String(), request.Body)
	if err != nil {
		return qbresponse.RawResponse{}, err
	}
	outgoing.URL.Path = request.URL.Path
	outgoing.Header = request.Header
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(outgoing)
	if err != nil {
		return qbresponse.RawResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return qbresponse.RawResponse{StatusCode: response.StatusCode}, err
	}
	return qbresponse.RawResponse{StatusCode: response.StatusCode, Body: body}, nil
}

func newAddRequest(t *testing.T) *http.Request {
	t.Helper()
	profile := qbprofile.Profile{ID: "QB_504", WebAPIVersion: "2.11.2", AddStartField: "stopped"}
	request, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: "fixture.torrent", Content: []byte("torrent")}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func prepareChain(identityKey string) func(context.Context, *sql.DB) error {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	timestamp := now.Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','instance-1','/managed','VERIFIED',?,?)`, []any{timestamp, timestamp}},
		{`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1','target-1',1,'TRUSTED',1073741824,1073741824,1,1,'baseline-digest',?)`, []any{timestamp}},
		{`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('instance-1','READY',1,0,1,'baseline-digest',?)`, []any{timestamp}},
		{`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('instance-1',1,1,0,'baseline-digest','/managed/baseline.json',1,?)`, []any{timestamp}},
		{`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1',?,'READY','MATCHED','READY','PREPARED',?,?,?,?)`, []any{identityKey, now.Add(time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), timestamp, timestamp}},
	}
	return func(ctx context.Context, database *sql.DB) error {
		for _, statement := range statements {
			if _, err := database.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestRunDispatchesAfterPlacementReservationAndWaiter(t *testing.T) {
	database := orchestratorDatabase(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil || request.Method != http.MethodPost || request.URL.Path != "/api/v2/torrents/add" || len(body) == 0 {
			http.Error(response, "invalid add", http.StatusBadRequest)
			return
		}
		calls++
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("Ok."))
	}))
	defer server.Close()
	target, parseErr := url.Parse(server.URL)
	if parseErr != nil {
		t.Fatal(parseErr)
	}

	result, err := Run(context.Background(), database, Request{
		Placement:   placementInput("VERIFIED"),
		Reservation: reservationRequest(),
		Waiter:      waiterRequest(),
		Dispatch: dispatchflow.Request{
			AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1",
			APIVersion: "2.11.2", HTTPRequest: newAddRequest(t),
		},
		Prepare: prepareChain("identity-key"),
		Sender:  redirectSender{target: target, calls: &calls},
		Now:     func() time.Time { return time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PlacementSelected || result.AttemptID != "attempt-1" {
		t.Fatalf("result=%+v", result)
	}
	if calls != 1 {
		t.Fatalf("sender calls=%d, want exactly one", calls)
	}
	if result.Dispatch.Commit.State != "AWAITING_CONFIRMATION" {
		t.Fatalf("commit state=%s", result.Dispatch.Commit.State)
	}
	var attemptState, attemptDispatch, intentDispatch, sideEffect string
	if err := database.QueryRow(`SELECT a.attempt_state, a.dispatch_state, s.dispatch_state, COALESCE(a.side_effect_fact,'') FROM placement_attempts a JOIN submit_intents s ON s.attempt_id=a.attempt_id WHERE a.attempt_id='attempt-1'`).Scan(&attemptState, &attemptDispatch, &intentDispatch, &sideEffect); err != nil {
		t.Fatal(err)
	}
	if attemptState != "AWAITING_CONFIRMATION" || attemptDispatch != "STARTED" || intentDispatch != "STARTED" || sideEffect != "POSSIBLE_EFFECT" {
		t.Fatalf("attempt=%s intent=%s sideEffect=%s", attemptState, intentDispatch, sideEffect)
	}
}

func TestRunFailsClosedWhenProfileIsNotVerified(t *testing.T) {
	database := orchestratorDatabase(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("Ok."))
	}))
	defer server.Close()
	target, parseErr := url.Parse(server.URL)
	if parseErr != nil {
		t.Fatal(parseErr)
	}

	_, err := Run(context.Background(), database, Request{
		Placement:   placementInput("EXPECTED"),
		Reservation: reservationRequest(),
		Waiter:      waiterRequest(),
		Dispatch: dispatchflow.Request{
			AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1",
			APIVersion: "2.11.2", HTTPRequest: newAddRequest(t),
		},
		Prepare: prepareChain("identity-key"),
		Sender:  redirectSender{target: target, calls: &calls},
	})
	var rejection *RejectionError
	if err == nil {
		t.Fatal("expected fail-closed rejection")
	}
	rejection, ok := err.(*RejectionError)
	if !ok {
		t.Fatalf("err=%v", err)
	}
	if rejection.Reason != "PROFILE_NOT_VERIFIED" {
		t.Fatalf("reason=%s", rejection.Reason)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d after fail-closed rejection", calls)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_reservations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("reservations=%d after fail-closed rejection", count)
	}
}
