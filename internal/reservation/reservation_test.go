package reservation

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestReserveAtomicWritesAttemptIntentReservationAndJobTogether(t *testing.T) {
	database := openReservationDatabase(t)
	seedReservationFixtures(t, database, "job-1")
	request := validRequest("job-1", "attempt-1", "submit-1", "reservation-1")
	result, err := ReserveAtomic(context.Background(), database, request)
	if err != nil {
		t.Fatalf("ReserveAtomic() error = %v", err)
	}
	if result.AttemptNo != 1 || result.AllocationEpoch != 1 {
		t.Fatalf("result=%+v", result)
	}
	var jobState, currentAttempt string
	if err := database.QueryRow(`SELECT job_state, current_attempt_id FROM race_jobs WHERE job_id = 'job-1'`).Scan(&jobState, &currentAttempt); err != nil {
		t.Fatal(err)
	}
	if jobState != "RESERVED" || currentAttempt != "attempt-1" {
		t.Fatalf("job=%s/%s", jobState, currentAttempt)
	}
	var attempts, intents, reservations int
	if err := database.QueryRow(`SELECT (SELECT COUNT(*) FROM placement_attempts), (SELECT COUNT(*) FROM submit_intents), (SELECT COUNT(*) FROM capacity_reservations)`).Scan(&attempts, &intents, &reservations); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || intents != 1 || reservations != 1 {
		t.Fatalf("counts=%d/%d/%d", attempts, intents, reservations)
	}
}

func TestReserveAtomicRollsBackWhenLaterInsertFails(t *testing.T) {
	database := openReservationDatabase(t)
	seedReservationFixtures(t, database, "job-1")
	seedReservationFixtures(t, database, "job-2")
	if _, err := ReserveAtomic(context.Background(), database, validRequest("job-1", "attempt-1", "submit-1", "reservation-1")); err != nil {
		t.Fatal(err)
	}
	_, err := ReserveAtomic(context.Background(), database, validRequest("job-2", "attempt-2", "submit-1", "reservation-2"))
	if err == nil {
		t.Fatal("duplicate submit intent error = nil")
	}
	var attempts, reservations int
	if err := database.QueryRow(`SELECT (SELECT COUNT(*) FROM placement_attempts WHERE attempt_id = 'attempt-2'), (SELECT COUNT(*) FROM capacity_reservations WHERE reservation_id = 'reservation-2')`).Scan(&attempts, &reservations); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || reservations != 0 {
		t.Fatalf("rolled back counts=%d/%d", attempts, reservations)
	}
	var state string
	if err := database.QueryRow(`SELECT job_state FROM race_jobs WHERE job_id = 'job-2'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "READY" {
		t.Fatalf("job-2 state=%s", state)
	}
}

func TestReserveAtomicRejectsUntrustedCapacityBeforeWrites(t *testing.T) {
	database := openReservationDatabase(t)
	seedReservationFixtures(t, database, "job-1")
	if _, err := database.Exec(`UPDATE capacity_epochs SET capacity_state = 'CAPACITY_UNCERTAIN' WHERE capacity_epoch_id = 'epoch-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ReserveAtomic(context.Background(), database, validRequest("job-1", "attempt-1", "submit-1", "reservation-1")); err == nil {
		t.Fatal("untrusted capacity error = nil")
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM placement_attempts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("attempt count=%d", count)
	}
}

func validRequest(jobID, attemptID, submitID, reservationID string) Request {
	return Request{JobID: jobID, AttemptID: attemptID, SubmitIntentID: submitID, ReservationID: reservationID, IdentityKey: "identity-1", InstanceID: "qb-1", StorageTargetID: "target-1", RequiredBytes: 100, CapacityEpochID: "epoch-1", AllocationEpoch: 1, CapacityRevision: 1, SnapshotGeneration: 1, SnapshotRevision: 1, SnapshotDigest: "digest-1", RequestFingerprint: "request-1", TargetFingerprint: "target-fingerprint-1", PreAddIdentityFact: IdentityAbsent, FinalCandidateRecheckRevision: 5, MaxInflight: 4, Now: "2026-08-25T00:00:00Z"}
}

func openReservationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "reservation.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func seedReservationFixtures(t *testing.T, database *sql.DB, jobID string) {
	t.Helper()
	now := "2026-08-25T00:00:00Z"
	if _, err := database.Exec(`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1', 'qb-1', '/downloads', 'VERIFIED', ?, ?) ON CONFLICT(storage_target_id) DO NOTHING`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1', 'target-1', 1, 'TRUSTED', 1000, 900, 1, 1, 'digest-1', ?) ON CONFLICT(capacity_epoch_id) DO NOTHING`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, preparation_revision, participation_deadline, observation_deadline, created_at, updated_at) VALUES (?, 'source-1', 'identity-1', 'READY', 'MATCHED', 'READY', 'PREPARED', 3, ?, ?, ?, ?)`, jobID, now, now, now, now); err != nil {
		t.Fatal(err)
	}
}
