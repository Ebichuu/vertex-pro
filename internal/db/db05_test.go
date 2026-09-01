package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestStorageCapacityEpochAndOwnerGuards(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	createdAt := "2026-08-24T00:00:00Z"
	if _, err := database.Exec(`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, "target-1", "qb-1", "/downloads", "VERIFIED", createdAt, createdAt); err != nil {
		t.Fatalf("insert storage target error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "epoch-1", "target-1", 1, "TRUSTED", 1000, 900, 1, 1, "digest-1", createdAt); err != nil {
		t.Fatalf("insert capacity epoch error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "epoch-duplicate", "target-1", 1, "TRUSTED", 900, 800, 1, 2, "digest-2", createdAt); err == nil {
		t.Fatal("duplicate allocation epoch error = nil, want unique constraint")
	}
	if _, err := database.Exec(`INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, "job-1", "source-1", "READY", createdAt, createdAt, createdAt, createdAt); err != nil {
		t.Fatalf("insert race job error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "attempt-1", "job-1", "identity-1", 1, "qb-1", "target-1", "RESERVED", "NOT_STARTED", 1, 500, createdAt); err != nil {
		t.Fatalf("insert placement attempt error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES (?, ?, ?, ?, ?, ?)`, "reservation-1", "attempt-1", "target-1", 500, 1, "ACTIVE"); err != nil {
		t.Fatalf("insert reservation error = %v", err)
	}
	insertBinding(t, database, "binding-1", "target-1", "torrent-1", "RESERVATION", "reservation-1", createdAt)
	if _, err := database.Exec(`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, owner_kind, amount_left_state, amount_left, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "binding-duplicate", "target-1", "torrent-1", "UNMANAGED", "KNOWN", 0, 1, 1, "digest-1", createdAt); err == nil {
		t.Fatal("duplicate torrent owner error = nil, want unique constraint")
	}
	if _, err := database.Exec(`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, owner_kind, reservation_id, amount_left_state, amount_left, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "binding-invalid-owner", "target-1", "torrent-2", "RESERVATION", "missing-reservation", "KNOWN", 0, 1, 1, "digest-1", createdAt); err == nil {
		t.Fatal("invalid reservation owner error = nil, want trigger error")
	}
	if _, err := database.Exec(`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, owner_kind, amount_left_state, amount_left, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "binding-2", "target-1", "torrent-2", "UNMANAGED", "KNOWN", 0, 1, 1, "digest-1", createdAt); err != nil {
		t.Fatalf("insert unmanaged binding error = %v", err)
	}
	if _, err := database.Exec(`UPDATE capacity_reservations SET reservation_state = 'RELEASED', release_reason = 'NO_EFFECT', released_unconsumed_bytes = 500, released_at = ? WHERE reservation_id = ?`, createdAt, "reservation-1"); err == nil {
		t.Fatal("release with reservation owner error = nil, want trigger error")
	}
}

func TestStorageCapacityChecksRejectInvalidValues(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	createdAt := "2026-08-24T00:00:00Z"
	if _, err := database.Exec(`INSERT INTO storage_targets(storage_target_id, instance_id, path, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, "target-1", "qb-1", "/downloads", createdAt, createdAt); err != nil {
		t.Fatalf("insert storage target error = %v", err)
	}
	for name, statement := range map[string]string{
		"negative epoch":            `INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('e', 'target-1', -1, 'TRUSTED', 0, 0, 'd', '2026-08-24T00:00:00Z')`,
		"released missing metadata": `INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('r', 'a', 'target-1', 100, 0, 'RELEASED')`,
		"known missing amount":      `INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, owner_kind, amount_left_state, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('b', 'target-1', 't', 'UNMANAGED', 'KNOWN', 0, 0, 'd', '2026-08-24T00:00:00Z')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.Exec(statement); err == nil {
				t.Fatal("constraint error = nil, want error")
			}
		})
	}
}

func insertBinding(t *testing.T, database *sql.DB, id, targetID, torrentID, ownerKind, reservationID, updatedAt string) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, owner_kind, reservation_id, amount_left_state, amount_left, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, targetID, torrentID, ownerKind, nullIfEmpty(reservationID), "KNOWN", 0, 1, 1, "digest-1", updatedAt)
	if err != nil {
		t.Fatalf("insert binding %s error = %v", id, err)
	}
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
