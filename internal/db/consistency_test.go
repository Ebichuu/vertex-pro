package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestCheckStartupConsistencyPassesForMigratedDatabase(t *testing.T) {
	database := openMigratedDatabase(t)
	if err := CheckStartupConsistency(context.Background(), database); err != nil {
		t.Fatalf("CheckStartupConsistency() error = %v", err)
	}
}

func TestCheckStartupConsistencyRejectsBrokenRelationshipsAndMissingObjects(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T){
		"missing submit intent": func(t *testing.T) {
			database := openMigratedDatabase(t)
			insertConsistencyFixture(t, database)
			if _, err := database.ExecContext(ctx, `DELETE FROM submit_intents WHERE attempt_id = 'attempt-1'`); err != nil {
				t.Fatalf("delete submit intent: %v", err)
			}
			assertConsistencyFails(t, database, "attempt_submit_intent_cardinality")
		},
		"missing trigger": func(t *testing.T) {
			database := openMigratedDatabase(t)
			if _, err := database.ExecContext(ctx, `DROP TRIGGER placement_attempt_target_immutable_guard`); err != nil {
				t.Fatalf("drop trigger: %v", err)
			}
			assertConsistencyFails(t, database, "missing trigger placement_attempt_target_immutable_guard")
		},
		"missing index": func(t *testing.T) {
			database := openMigratedDatabase(t)
			if _, err := database.ExecContext(ctx, `DROP INDEX uq_job_active_attempt`); err != nil {
				t.Fatalf("drop index: %v", err)
			}
			assertConsistencyFails(t, database, "missing index uq_job_active_attempt")
		},
	}
	for name, testCase := range cases {
		t.Run(name, testCase)
	}
}

func TestCheckStartupConsistencyRejectsNilDatabase(t *testing.T) {
	if err := CheckStartupConsistency(context.Background(), nil); err == nil {
		t.Fatal("nil database error = nil, want error")
	}
}

func insertConsistencyFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	createdAt := "2026-08-25T00:00:00Z"
	if _, err := database.ExecContext(context.Background(), `INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'READY', ?, ?, ?, ?)`, createdAt, createdAt, createdAt, createdAt); err != nil {
		t.Fatalf("insert consistency race job: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1', 'job-1', 'identity-1', 1, 'instance-1', 'target-1', 'RESERVED', 'NOT_STARTED', 1, 100, ?)`, createdAt); err != nil {
		t.Fatalf("insert consistency attempt: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES ('intent-1', 'attempt-1', 'instance-1', 'identity-1', 'request-1', 'target-1', 'NOT_STARTED', 1, 1, 'digest-1', 'ABSENT', ?)`, createdAt); err != nil {
		t.Fatalf("insert consistency intent: %v", err)
	}
}

func assertConsistencyFails(t *testing.T, database *sql.DB, expected string) {
	t.Helper()
	err := CheckStartupConsistency(context.Background(), database)
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Fatalf("consistency error = %v, want substring %q", err, expected)
	}
}
