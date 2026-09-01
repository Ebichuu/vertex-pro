package unknownreconciler

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

type stubProvider struct {
	snapshots map[string]Snapshot
	calls     int
}

func (p *stubProvider) FreshestComplete(_ context.Context, instanceID string) (Snapshot, bool) {
	p.calls++
	snapshot, ok := p.snapshots[instanceID]
	return snapshot, ok
}

func TestSweepConfirmsPresentIdentityViaFirstConfirm(t *testing.T) {
	database := openMigrated(t)
	seedUnknown(t, database, "1")
	provider := &stubProvider{snapshots: map[string]Snapshot{"qb-1": {InstanceID: "qb-1", SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: true, QBTorrentID: "torrent-9"}}}
	outcomes, err := Sweep(context.Background(), database, provider, SweepInput{Now: time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC)})
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome != OutcomeConfirmed {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	var attemptState, torrentID string
	var confirmationGeneration sql.NullInt64
	if err := database.QueryRow(`SELECT attempt_state, qb_torrent_id, confirmation_sync_generation FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState, &torrentID, &confirmationGeneration); err != nil {
		t.Fatal(err)
	}
	if attemptState != "RECONCILED_CONFIRMED" || torrentID != "torrent-9" || !confirmationGeneration.Valid {
		t.Fatalf("attempt facts: %s/%s/%v", attemptState, torrentID, confirmationGeneration)
	}
	var jobState string
	var currentAttempt sql.NullString
	if err := database.QueryRow(`SELECT job_state, current_attempt_id FROM race_jobs WHERE job_id='job-1'`).Scan(&jobState, &currentAttempt); err != nil {
		t.Fatal(err)
	}
	if jobState != "RECONCILED_CONFIRMED" || !currentAttempt.Valid || currentAttempt.String != "attempt-1" {
		t.Fatalf("job facts: %s/%v", jobState, currentAttempt)
	}
	var ownerKind, bindingReservation string
	if err := database.QueryRow(`SELECT owner_kind, reservation_id FROM capacity_task_bindings WHERE binding_id='binding-attempt-1'`).Scan(&ownerKind, &bindingReservation); err != nil {
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
	assertWaiterClosed(t, database, "waiter-1")
	assertEvents(t, database, 1, "RECONCILED_CONFIRMED")
	after, err := Sweep(context.Background(), database, provider, SweepInput{Now: time.Date(2026, 8, 29, 0, 31, 0, 0, time.UTC)})
	if err != nil || len(after) != 0 {
		t.Fatalf("second sweep outcomes=%+v err=%v", after, err)
	}
}

func TestSweepReconcilesNoEffectAndReleasesReservation(t *testing.T) {
	database := openMigrated(t)
	seedUnknown(t, database, "1")
	provider := &stubProvider{snapshots: map[string]Snapshot{"qb-1": {InstanceID: "qb-1", SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: false}}}
	outcomes, err := Sweep(context.Background(), database, provider, SweepInput{Now: time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC)})
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome != OutcomeNoEffect {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
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
	if jobState != "RECONCILED_NO_EFFECT" || currentAttempt.Valid {
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
	assertWaiterClosed(t, database, "waiter-1")
	assertEvents(t, database, 2, "RECONCILED_NO_EFFECT")
}

func TestSweepExpiresPastDeadlineWhenOffline(t *testing.T) {
	database := openMigrated(t)
	seedUnknown(t, database, "1")
	provider := &stubProvider{snapshots: map[string]Snapshot{}}
	outcomes, err := Sweep(context.Background(), database, provider, SweepInput{Now: time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC)})
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome != OutcomeExpired {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	var attemptState string
	if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "EXPIRED_UNKNOWN" {
		t.Fatalf("attempt state = %s", attemptState)
	}
	var jobState string
	var currentAttempt sql.NullString
	if err := database.QueryRow(`SELECT job_state, current_attempt_id FROM race_jobs WHERE job_id='job-1'`).Scan(&jobState, &currentAttempt); err != nil {
		t.Fatal(err)
	}
	if jobState != "EXPIRED_UNKNOWN" || !currentAttempt.Valid || currentAttempt.String != "attempt-1" {
		t.Fatalf("job facts: %s/%v", jobState, currentAttempt)
	}
	var reservationState string
	if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations WHERE reservation_id='reservation-1'`).Scan(&reservationState); err != nil {
		t.Fatal(err)
	}
	if reservationState != "ACTIVE" {
		t.Fatalf("reservation state = %s, must be kept for manual closure", reservationState)
	}
	assertWaiterClosed(t, database, "waiter-1")
	assertEvents(t, database, 2, "EXPIRED_UNKNOWN")
}

func TestSweepHoldsWhenOfflineBeforeDeadline(t *testing.T) {
	database := openMigrated(t)
	seedUnknown(t, database, "1")
	provider := &stubProvider{snapshots: map[string]Snapshot{}}
	outcomes, err := Sweep(context.Background(), database, provider, SweepInput{Now: time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC)})
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome != OutcomeHoldNoFreshEvidence {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	var attemptState, waiterState, reservationState string
	if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT waiter_state FROM sync_waiters WHERE waiter_id='waiter-1'`).Scan(&waiterState); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations WHERE reservation_id='reservation-1'`).Scan(&reservationState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "UNKNOWN" || waiterState != "ACTIVE" || reservationState != "ACTIVE" {
		t.Fatalf("hold facts: %s/%s/%s", attemptState, waiterState, reservationState)
	}
	var events int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("event count = %d", events)
	}
}

func TestSweepIgnoresStaleSnapshotUntilDeadline(t *testing.T) {
	database := openMigrated(t)
	seedUnknown(t, database, "1")
	provider := &stubProvider{snapshots: map[string]Snapshot{"qb-1": {InstanceID: "qb-1", SyncGeneration: 3, SnapshotRevision: 7, SnapshotDigest: "baseline", IdentityPresent: true, QBTorrentID: "torrent-9"}}}
	outcomes, err := Sweep(context.Background(), database, provider, SweepInput{Now: time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC)})
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome != OutcomeHoldNoFreshEvidence {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	var attemptState string
	if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id='attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if attemptState != "UNKNOWN" {
		t.Fatalf("stale snapshot must not drive a decision, attempt = %s", attemptState)
	}
	later, err := Sweep(context.Background(), database, provider, SweepInput{Now: time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC)})
	if err != nil || len(later) != 1 || later[0].Outcome != OutcomeExpired {
		t.Fatalf("later outcomes=%+v err=%v", later, err)
	}
}

func TestSweepRecoversAfterRestartFromDatabaseState(t *testing.T) {
	database := openMigrated(t)
	seedUnknown(t, database, "1")
	seedUnknown(t, database, "2")
	offline := &stubProvider{snapshots: map[string]Snapshot{}}
	first, err := Sweep(context.Background(), database, offline, SweepInput{Now: time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC)})
	if err != nil || len(first) != 2 || first[0].AttemptID != "attempt-1" || first[1].AttemptID != "attempt-2" {
		t.Fatalf("first sweep outcomes=%+v err=%v", first, err)
	}
	recovered := &stubProvider{snapshots: map[string]Snapshot{"qb-1": {InstanceID: "qb-1", SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: true, QBTorrentID: "torrent-9"}}}
	second, err := Sweep(context.Background(), database, recovered, SweepInput{Now: time.Date(2026, 8, 29, 0, 40, 0, 0, time.UTC)})
	if err != nil || len(second) != 2 {
		t.Fatalf("recovered sweep outcomes=%+v err=%v", second, err)
	}
	for index, outcome := range second {
		expected := "attempt-" + string(rune('1'+index))
		if outcome.AttemptID != expected || outcome.Outcome != OutcomeConfirmed {
			t.Fatalf("outcome %d = %+v", index, outcome)
		}
	}
	var confirmed int
	if err := database.QueryRow(`SELECT COUNT(*) FROM placement_attempts WHERE attempt_state='RECONCILED_CONFIRMED'`).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed != 2 {
		t.Fatalf("confirmed attempts = %d", confirmed)
	}
}

func TestSweepRejectsInvalidInputsAndProviderFacts(t *testing.T) {
	if _, err := Sweep(context.Background(), nil, nil, SweepInput{Now: time.Now()}); err == nil || !errors.Is(err, ErrSweepInvalid) {
		t.Fatalf("nil provider error = %v", err)
	}
	database := openMigrated(t)
	seedUnknown(t, database, "1")
	if _, err := Sweep(context.Background(), database, &stubProvider{}, SweepInput{}); err == nil || !errors.Is(err, ErrSweepInvalid) {
		t.Fatalf("zero time error = %v", err)
	}
	broken := &stubProvider{snapshots: map[string]Snapshot{"qb-1": {InstanceID: "qb-1", SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after", IdentityPresent: true}}}
	if _, err := Sweep(context.Background(), database, broken, SweepInput{Now: time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC)}); err == nil || !strings.Contains(err.Error(), "lacks qb torrent id") {
		t.Fatalf("missing torrent id error = %v", err)
	}
	foreign := &stubProvider{snapshots: map[string]Snapshot{"qb-1": {InstanceID: "qb-2", SyncGeneration: 5, SnapshotRevision: 2, SnapshotDigest: "after"}}}
	if _, err := Sweep(context.Background(), database, foreign, SweepInput{Now: time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC)}); err == nil || !strings.Contains(err.Error(), "invalid snapshot") {
		t.Fatalf("foreign snapshot error = %v", err)
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "unknown-reconciler.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func seedUnknown(t *testing.T, database *sql.DB, suffix string) {
	t.Helper()
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	attemptID := "attempt-" + suffix
	jobID := "job-" + suffix
	waiterID := "waiter-" + suffix
	reservationID := "reservation-" + suffix
	intentID := "intent-" + suffix
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-" + suffix + "','qb-1','/data-" + suffix + "','VERIFIED',?,?)", []any{now, now}},
		{"INSERT OR IGNORE INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1','READY',3,0,7,'baseline',?)", []any{now}},
		{"INSERT OR IGNORE INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1',3,7,0,'baseline','/snapshots/baseline.json',1,?)", []any{now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('" + jobID + "','source-1','identity-" + suffix + "','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at) VALUES ('" + attemptID + "','" + jobID + "','identity-" + suffix + "'," + suffix + ",'qb-1','target-" + suffix + "','UNKNOWN','STARTED','POSSIBLE_EFFECT',1,100,?)", []any{now}},
		{"INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('" + reservationID + "','" + attemptID + "','target-" + suffix + "',100,1,'ACTIVE')", nil},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('" + intentID + "','" + attemptID + "','qb-1','identity-" + suffix + "','req','target','STARTED',3,7,'baseline','ABSENT',?,?)", []any{now, now}},
		{"INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state, created_at) VALUES ('" + waiterID + "','" + attemptID + "','qb-1',3,7,'identity-" + suffix + "','2026-08-29T00:10:00Z','2026-08-29T01:00:00Z','ACTIVE',?)", []any{now}},
		{"UPDATE race_jobs SET job_state='UNKNOWN', current_attempt_id='" + attemptID + "' WHERE job_id='" + jobID + "'", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func assertWaiterClosed(t *testing.T, database *sql.DB, waiterID string) {
	t.Helper()
	var waiterState string
	if err := database.QueryRow(`SELECT waiter_state FROM sync_waiters WHERE waiter_id=?`, waiterID).Scan(&waiterState); err != nil {
		t.Fatal(err)
	}
	if waiterState != "CLOSED" {
		t.Fatalf("waiter %s state = %s", waiterID, waiterState)
	}
}

func assertEvents(t *testing.T, database *sql.DB, expected int, toState string) {
	t.Helper()
	var events int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE to_state=?`, toState).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != expected {
		t.Fatalf("events to %s = %d, want %d", toState, events, expected)
	}
}
