package confirmcoordinator

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchorchestrator"
	"github.com/vertex-pro/vertex-pro/internal/placement"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
	"github.com/vertex-pro/vertex-pro/internal/unknownreconciler"
)

func coordinatorDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "confirmcoordinator.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

const identityKey = "identity-key"

func runChainToAwaitingConfirmation(t *testing.T, database *sql.DB) dispatchorchestrator.Result {
	t.Helper()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	timestamp := now.Format(time.RFC3339)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','instance-1','/managed','VERIFIED',?,?)`, []any{timestamp, timestamp}},
		{`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1','target-1',1,'TRUSTED',1073741824,1073741824,1,1,'baseline-digest',?)`, []any{timestamp}},
		{`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('instance-1','READY',1,0,1,'baseline-digest',?)`, []any{timestamp}},
		{`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('instance-1',1,1,0,'baseline-digest','/managed/baseline.json',1,?)`, []any{timestamp}},
		{`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1',?,'READY','MATCHED','READY','PREPARED',?,?,?,?)`, []any{identityKey, now.Add(time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), timestamp, timestamp}},
	} {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	result, err := dispatchorchestrator.Run(context.Background(), database, dispatchorchestrator.Request{
		Placement: placement.DecisionInput{
			IdentityKey: identityKey, TargetMode: placement.TargetFixedInstance, TargetInstanceID: "instance-1", RequiredBytes: 1,
			Weights:    placement.DecisionWeights{InstanceWeight: 1, FreeRatioWeight: 1, InflightPenaltyWeight: 1, CurrentDownloadLoadPenaltyWeight: 1, RecentAddLatencyPenaltyWeight: 1},
			Candidates: []placement.DecisionCandidate{{InstanceID: "instance-1", StorageTargetID: "target-1", CanPlace: true, HealthState: "AVAILABLE", ProfileState: "VERIFIED", StorageState: "VERIFIED", CapacityState: "TRUSTED", InstanceWeight: 1, FreeRatioPPM: 1_000_000, EffectiveFreeBytes: 1 << 30, MaxInflight: 1}},
		},
		Reservation: reservation.Request{
			JobID: "job-1", AttemptID: "attempt-1", SubmitIntentID: "intent-1", ReservationID: "reservation-1",
			IdentityKey: identityKey, InstanceID: "instance-1", StorageTargetID: "target-1", RequiredBytes: 1,
			CapacityEpochID: "epoch-1", AllocationEpoch: 1, CapacityRevision: 1, SnapshotGeneration: 1,
			SnapshotRevision: 1, SnapshotDigest: "baseline-digest", RequestFingerprint: "request-1", TargetFingerprint: "target-1",
			PreAddIdentityFact: reservation.IdentityAbsent, FinalCandidateRecheckRevision: 1, MaxInflight: 1,
			Now: now.Format(time.RFC3339),
		},
		Waiter: syncwaiter.Request{
			WaiterID: "waiter-1", InstanceID: "instance-1", RegisteredGeneration: 1,
			SourceSnapshotRevision: 1, IdentityKey: identityKey,
			ConfirmationDeadline:   now.Add(2 * time.Minute).Format(time.RFC3339),
			ReconciliationDeadline: now.Add(5 * time.Minute).Format(time.RFC3339),
			CreatedAt:              now.Format(time.RFC3339),
		},
		Dispatch: dispatchflow.Request{
			AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1",
			APIVersion: "2.11.2", HTTPRequest: newAddRequest(t),
		},
		Sender: acceptingSender{},
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
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

type acceptingSender struct{}

func (acceptingSender) Send(context.Context, *http.Request) (qbresponse.RawResponse, error) {
	return qbresponse.RawResponse{StatusCode: http.StatusOK, Body: []byte("Ok.")}, nil
}

func eventWithIdentity(generation, revision uint64, present bool) Event {
	snapshot := syncmerge.Snapshot{Torrents: map[string]syncmerge.Torrent{}}
	if present {
		snapshot.Torrents[identityKey] = syncmerge.Torrent{Fields: map[string]any{"state": "stoppedDL"}}
	}
	return Event{InstanceID: "instance-1", Generation: generation, Revision: revision, Snapshot: snapshot}
}

func TestOnSyncEventConfirmsMatchedWaiter(t *testing.T) {
	database := coordinatorDatabase(t)
	chain := runChainToAwaitingConfirmation(t, database)
	coordinator, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 2, true), time.Date(2026, 9, 3, 10, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) == 0 {
		t.Fatal("no outcomes")
	}
	confirmed := false
	for _, outcome := range outcomes {
		if outcome.WaiterID == "waiter-1" {
			if outcome.Action != "CONFIRMED" {
				t.Fatalf("waiter outcome=%+v", outcome)
			}
			confirmed = true
		}
	}
	if !confirmed {
		t.Fatalf("waiter-1 not confirmed: %+v", outcomes)
	}
	_ = chain
	var attemptState, waiterState string
	var bindings int
	if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT waiter_state FROM sync_waiters WHERE waiter_id='waiter-1'`).Scan(&waiterState); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_task_bindings`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if attemptState != "HASH_SEEN" || waiterState != "CLOSED" || bindings != 1 {
		t.Fatalf("attempt=%s waiter=%s bindings=%d", attemptState, waiterState, bindings)
	}
}

func setChainUnknown(t *testing.T, database *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE race_jobs SET job_state='UNKNOWN', current_attempt_id='attempt-1', updated_at=? WHERE job_id='job-1'`, []any{now}},
		{`UPDATE placement_attempts SET attempt_state='UNKNOWN' WHERE attempt_id='attempt-1'`, nil},
	} {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOnSyncEventReconcilesUnknownChain(t *testing.T) {
	database := coordinatorDatabase(t)
	runChainToAwaitingConfirmation(t, database)
	setChainUnknown(t, database)
	coordinator, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	// Identity absent from the snapshot: the Add never took effect.
	outcomes, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 2, false), time.Date(2026, 9, 3, 10, 6, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, outcome := range outcomes {
		if outcome.AttemptID == "attempt-1" && outcome.Action == "RECONCILED_NO_EFFECT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("outcomes=%+v", outcomes)
	}
	var attemptState string
	if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "RECONCILED_NO_EFFECT" {
		t.Fatalf("attemptState=%s", attemptState)
	}
}

func TestOnSyncEventReconcilesOnlyItsOwnInstance(t *testing.T) {
	database := coordinatorDatabase(t)
	runChainToAwaitingConfirmation(t, database)
	setChainUnknown(t, database)
	// This offline instance sorts before the active instance. Its UNKNOWN
	// chain must not consume evidence belonging to instance-1 or block it.
	for _, statement := range []string{
		`INSERT INTO storage_targets(storage_target_id,instance_id,path,storage_verification_state,created_at,updated_at) VALUES('target-0','instance-0','/other','VERIFIED','2026-09-03T10:00:00Z','2026-09-03T10:00:00Z')`,
		`INSERT INTO sync_instance_state(instance_id,sync_state,sync_generation,rid,snapshot_revision,snapshot_digest,updated_at) VALUES('instance-0','OFFLINE',1,0,1,'baseline','2026-09-03T10:00:00Z')`,
		`INSERT INTO sync_snapshots(instance_id,sync_generation,snapshot_revision,rid,snapshot_digest,snapshot_path,is_complete,created_at) VALUES('instance-0',1,1,0,'baseline','/other/baseline.json',1,'2026-09-03T10:00:00Z')`,
		`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,rule_state,torrent_state,placement_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES('job-0','source-0','identity-0','READY','MATCHED','READY','PREPARED','2026-09-03T11:00:00Z','2026-09-03T11:00:00Z','2026-09-03T10:00:00Z','2026-09-03T10:00:00Z')`,
		`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,reserved_bytes,started_at) VALUES('attempt-0','job-0','identity-0',1,'instance-0','target-0','UNKNOWN','STARTED','POSSIBLE_EFFECT',1,100,'2026-09-03T10:00:00Z')`,
		`INSERT INTO capacity_reservations(reservation_id,attempt_id,storage_target_id,reserved_bytes,allocation_epoch,reservation_state) VALUES('reservation-0','attempt-0','target-0',100,1,'ACTIVE')`,
		`INSERT INTO submit_intents(submit_intent_id,attempt_id,instance_id,identity_key,request_fingerprint,target_fingerprint,dispatch_state,pre_add_sync_generation,pre_add_snapshot_revision,pre_add_snapshot_digest,pre_add_identity_fact,created_at,dispatch_started_at) VALUES('intent-0','attempt-0','instance-0','identity-0','req-0','target-0','STARTED',1,1,'baseline','ABSENT','2026-09-03T10:00:00Z','2026-09-03T10:00:00Z')`,
		`INSERT INTO sync_waiters(waiter_id,attempt_id,instance_id,registered_generation,source_snapshot_revision,identity_key,confirmation_deadline,reconciliation_deadline,waiter_state,created_at) VALUES('waiter-0','attempt-0','instance-0',1,1,'identity-0','2026-09-03T10:02:00Z','2026-09-03T10:05:00Z','ACTIVE','2026-09-03T10:00:00Z')`,
		`UPDATE race_jobs SET job_state='UNKNOWN',current_attempt_id='attempt-0' WHERE job_id='job-0'`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	coordinator, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 2, false), time.Date(2026, 9, 3, 10, 6, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	resolved := false
	for _, outcome := range outcomes {
		if outcome.AttemptID != "attempt-1" {
			t.Fatalf("foreign outcome: %+v", outcome)
		}
		resolved = resolved || outcome.Action == "RECONCILED_NO_EFFECT"
	}
	if !resolved {
		t.Fatalf("local chain was not reconciled: %+v", outcomes)
	}
	var attemptState, reservationState, waiterState string
	if err := database.QueryRow(`SELECT a.attempt_state,r.reservation_state,w.waiter_state FROM placement_attempts a JOIN capacity_reservations r ON r.attempt_id=a.attempt_id JOIN sync_waiters w ON w.attempt_id=a.attempt_id WHERE a.attempt_id='attempt-0'`).Scan(&attemptState, &reservationState, &waiterState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "UNKNOWN" || reservationState != "ACTIVE" || waiterState != "ACTIVE" {
		t.Fatalf("foreign chain changed: %s/%s/%s", attemptState, reservationState, waiterState)
	}
}

func TestEventSnapshotProviderRejectsOtherInstance(t *testing.T) {
	provider := eventSnapshotProvider{complete: true, snapshot: unknownreconciler.Snapshot{InstanceID: "instance-1"}}
	if _, available := provider.FreshestComplete(context.Background(), "instance-2"); available {
		t.Fatal("foreign instance accepted another instance's evidence")
	}
}
