package db

import (
	"context"
	"database/sql"
	"testing"
)

func TestSyncSnapshotWaiterRecoveryAndDanglingReferenceGuard(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	createdAt := "2026-08-24T00:00:00Z"
	deadline := "2026-08-24T00:00:15Z"
	reconcileDeadline := "2026-08-24T00:10:00Z"
	if _, err := database.Exec(`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, "qb-1", "RECONCILING", 1, 0, 1, "digest-1", createdAt); err != nil {
		t.Fatalf("insert sync state error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "qb-1", 1, 1, 0, "digest-1", "snapshots/qb-1/1-1.json.zst", 1, createdAt); err != nil {
		t.Fatalf("insert snapshot error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "waiter-1", "attempt-1", "qb-1", 1, 1, "identity-1", deadline, reconcileDeadline, createdAt); err != nil {
		t.Fatalf("insert waiter error = %v", err)
	}
	if _, err := database.Exec(`UPDATE sync_instance_state SET sync_state = ?, rid = ?, snapshot_revision = ?, updated_at = ? WHERE instance_id = ?`, "READY", 5, 2, reconcileDeadline, "qb-1"); err != nil {
		t.Fatalf("recover sync state error = %v", err)
	}
	if _, err := database.Exec(`UPDATE sync_waiters SET waiter_state = 'CLOSED', closed_at = ? WHERE waiter_id = ?`, reconcileDeadline, "waiter-1"); err != nil {
		t.Fatalf("close waiter error = %v", err)
	}

	var syncState string
	var rid int
	if err := database.QueryRow(`SELECT sync_state, rid FROM sync_instance_state WHERE instance_id = ?`, "qb-1").Scan(&syncState, &rid); err != nil {
		t.Fatalf("read recovered sync state error = %v", err)
	}
	if syncState != "READY" || rid != 5 {
		t.Fatalf("recovered state = %s/rid=%d, want READY/rid=5", syncState, rid)
	}

	if _, err := database.Exec(`INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "waiter-dangling", "attempt-dangling", "qb-1", 99, 1, "identity-2", deadline, reconcileDeadline, createdAt); err == nil {
		t.Fatal("dangling waiter reference error = nil, want foreign key error")
	}
}

func TestSyncSnapshotWaiterChecks(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	createdAt := "2026-08-24T00:00:00Z"
	if _, err := database.Exec(`INSERT INTO sync_instance_state(instance_id, snapshot_digest, updated_at) VALUES (?, ?, ?)`, "qb-1", "digest-1", createdAt); err != nil {
		t.Fatalf("insert sync state error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "qb-1", 0, 0, 0, "digest-1", "snapshots/qb-1/0-0.json.zst", 1, createdAt); err != nil {
		t.Fatalf("insert snapshot error = %v", err)
	}
	for name, statement := range map[string]string{
		"negative rid":         `INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1', 0, 2, -1, 'digest', 'path', 1, '2026-08-24T00:00:00Z')`,
		"invalid waiter state": `INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state, created_at) VALUES ('w-2', 'a-2', 'qb-1', 0, 0, 'id', '2026-08-24T00:00:15Z', '2026-08-24T00:10:00Z', 'BAD', '2026-08-24T00:00:00Z')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.Exec(statement); err == nil {
				t.Fatal("constraint error = nil, want error")
			}
		})
	}
}

var _ *sql.DB
