package terminalrelease

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestReleaseCompletedAtomicallyReflectsAndReleases(t *testing.T) {
	database := openReleaseDatabase(t)
	seedReleaseFixture(t, database, "KNOWN", 0)
	result, err := ReleaseCompleted(context.Background(), database, validRequest())
	if err != nil {
		t.Fatalf("ReleaseCompleted() error = %v", err)
	}
	if result.ReleasedUnconsumedBytes != 0 {
		t.Fatalf("result=%+v", result)
	}
	var reservationState, releaseReason string
	var reflected int64
	if err := database.QueryRow(`SELECT reservation_state, release_reason, reflected_physical_bytes FROM capacity_reservations WHERE reservation_id = 'reservation-1'`).Scan(&reservationState, &releaseReason, &reflected); err != nil {
		t.Fatal(err)
	}
	if reservationState != ReservationReleased || releaseReason != ReleaseReasonTerminalCompleted || reflected != 100 {
		t.Fatalf("reservation=%s/%s/%d", reservationState, releaseReason, reflected)
	}
	var ownerKind string
	var reservationID sql.NullString
	if err := database.QueryRow(`SELECT owner_kind, reservation_id FROM capacity_task_bindings WHERE binding_id = 'binding-1'`).Scan(&ownerKind, &reservationID); err != nil {
		t.Fatal(err)
	}
	if ownerKind != OwnerUnmanaged || reservationID.Valid {
		t.Fatalf("binding=%s/%v", ownerKind, reservationID)
	}
	var attemptState, jobState string
	if err := database.QueryRow(`SELECT attempt_state, (SELECT job_state FROM race_jobs WHERE job_id = 'job-1') FROM placement_attempts WHERE attempt_id = 'attempt-1'`).Scan(&attemptState, &jobState); err != nil {
		t.Fatal(err)
	}
	if attemptState != AttemptReconciledConfirmed || jobState != JobReconciledConfirmed {
		t.Fatalf("states=%s/%s", attemptState, jobState)
	}
}

func TestReleaseCompletedRejectsStalePhysicalEvidenceWithoutWrites(t *testing.T) {
	database := openReleaseDatabase(t)
	seedReleaseFixture(t, database, "KNOWN", 0)
	request := validRequest()
	request.FreshPhysicalFree = false
	if _, err := ReleaseCompleted(context.Background(), database, request); err == nil {
		t.Fatal("stale evidence error = nil")
	}
	assertReleaseUnchanged(t, database)
}

func TestReleaseCompletedRejectsNonZeroAmountAndDuplicateReflection(t *testing.T) {
	database := openReleaseDatabase(t)
	seedReleaseFixture(t, database, "KNOWN", 10)
	if _, err := ReleaseCompleted(context.Background(), database, validRequest()); err == nil {
		t.Fatal("nonzero amount error = nil")
	}
	assertReleaseUnchanged(t, database)
	database2 := openReleaseDatabase(t)
	seedReleaseFixture(t, database2, "KNOWN", 0)
	if _, err := ReleaseCompleted(context.Background(), database2, validRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseCompleted(context.Background(), database2, validRequest()); err == nil {
		t.Fatal("duplicate release error = nil")
	}
}

func validRequest() Request {
	return Request{ReservationID: "reservation-1", ReflectionID: "reflection-1", BindingID: "binding-1", AttemptID: "attempt-1", CapacityEpochID: "epoch-1", SnapshotGeneration: 2, SnapshotRevision: 3, SnapshotDigest: "digest-2", ReflectedPhysicalBytes: 100, PhysicalFreeBytes: 1000, FreshPhysicalFree: true, ObservedAt: "2026-08-25T01:00:00Z", ReleasedAt: "2026-08-25T01:00:01Z"}
}

func openReleaseDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "release.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func seedReleaseFixture(t *testing.T, database *sql.DB, amountState string, amountLeft int64) {
	t.Helper()
	now := "2026-08-25T00:00:00Z"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1', 'qb-1', '/downloads', 'VERIFIED', ?, ?)`, []any{now, now}},
		{`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1', 'target-1', 1, 'TRUSTED', 1000, 900, 2, 3, 'digest-2', ?)`, []any{now}},
		{`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'identity-1', 'READY', 'MATCHED', 'READY', 'PREPARED', ?, ?, ?, ?)`, []any{now, now, now, now}},
		{`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, qb_torrent_id, confirmation_sync_generation, confirmation_snapshot_revision, confirmation_evidence_digest, reserved_bytes, started_at) VALUES ('attempt-1', 'job-1', 'identity-1', 1, 'qb-1', 'target-1', 'HASH_SEEN', 'STARTED', 'OBSERVED_EFFECT', 1, 'torrent-1', 2, 3, 'digest-2', 100, ?)`, []any{now}},
		{`INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('submit-1', 'attempt-1', 'qb-1', 'identity-1', 'request-1', 'target-1', 'STARTED', 1, 1, 'old-digest', 'ABSENT', ?, ?)`, []any{now, now}},
		{`INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-1', 'attempt-1', 'target-1', 100, 1, 'ACTIVE')`, nil},
		{`UPDATE race_jobs SET current_attempt_id = 'attempt-1', job_state = 'HASH_SEEN' WHERE job_id = 'job-1'`, nil},
		{`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, identity_key, owner_kind, reservation_id, amount_left_state, amount_left, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('binding-1', 'target-1', 'torrent-1', 'identity-1', 'RESERVATION', 'reservation-1', ?, ?, 1, 1, 'old-digest', ?)`, []any{amountState, amountLeft, now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func assertReleaseUnchanged(t *testing.T, database *sql.DB) {
	t.Helper()
	var state string
	var reflections, unmanaged int
	if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations WHERE reservation_id = 'reservation-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM reservation_reflections WHERE reservation_id = 'reservation-1'`).Scan(&reflections); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_task_bindings WHERE binding_id = 'binding-1' AND owner_kind = 'UNMANAGED'`).Scan(&unmanaged); err != nil {
		t.Fatal(err)
	}
	if state != ReservationActive || reflections != 0 || unmanaged != 0 {
		t.Fatalf("state=%s reflections=%d unmanaged=%d", state, reflections, unmanaged)
	}
}
