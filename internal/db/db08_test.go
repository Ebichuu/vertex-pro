package db

import (
	"context"
	"testing"
)

func TestDB08IndexesAndTriggers(t *testing.T) {
	database := openMigratedDatabase(t)
	ctx := context.Background()
	createdAt := "2026-08-24T00:00:00Z"
	if _, err := database.ExecContext(ctx, `INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'READY', ?, ?, ?, ?)`, createdAt, createdAt, createdAt, createdAt); err != nil {
		t.Fatalf("insert race job: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1', 'job-1', 'identity-1', 1, 'instance-1', 'target-1', 'RESERVED', 'NOT_STARTED', 1, 100, ?)`, createdAt); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES ('intent-1', 'attempt-1', 'instance-1', 'identity-1', 'request-1', 'target-1', 'NOT_STARTED', 1, 1, 'digest-1', 'ABSENT', ?)`, createdAt); err != nil {
		t.Fatalf("insert intent: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE race_jobs SET current_attempt_id = 'attempt-1', job_state = 'RESERVED', updated_at = ? WHERE job_id = 'job-1'`, createdAt); err != nil {
		t.Fatalf("publish current attempt: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, occurred_at, expires_at) VALUES ('job.state.changed', 'race_job', 'job-1', 0, ?, '2026-08-31T00:00:00Z')`, createdAt); err != nil {
		t.Fatalf("insert first event: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, occurred_at, expires_at) VALUES ('job.state.changed', 'race_job', 'job-1', 1, ?, '2026-08-31T00:00:00Z')`, createdAt); err != nil {
		t.Fatalf("insert second event: %v", err)
	}
}

func TestDB08RejectsCrossTableDriftAndImmutableUpdates(t *testing.T) {
	database := openMigratedDatabase(t)
	ctx := context.Background()
	createdAt := "2026-08-24T00:00:00Z"
	if _, err := database.ExecContext(ctx, `INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'READY', ?, ?, ?, ?)`, createdAt, createdAt, createdAt, createdAt); err != nil {
		t.Fatalf("insert race job: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1', 'job-1', 'identity-1', 1, 'instance-1', 'target-1', 'RESERVED', 'NOT_STARTED', 1, 100, ?)`, createdAt); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES ('intent-1', 'attempt-1', 'instance-1', 'identity-1', 'request-1', 'target-1', 'NOT_STARTED', 1, 1, 'digest-1', 'ABSENT', ?)`, createdAt); err != nil {
		t.Fatalf("insert intent: %v", err)
	}
	cases := map[string]string{
		"attempt target":            `UPDATE placement_attempts SET storage_target_id = 'target-2' WHERE attempt_id = 'attempt-1'`,
		"reservation attempt facts": `INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-1', 'attempt-1', 'target-2', 100, 1, 'ACTIVE')`,
		"event revision gap":        `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, occurred_at, expires_at) VALUES ('job.state.changed', 'race_job', 'job-1', 2, '2026-08-24T00:00:00Z', '2026-08-31T00:00:00Z')`,
		"append only event update":  `UPDATE event_stream SET safe_data_json = '{}' WHERE event_id = 1`,
		"command identity":          `UPDATE api_commands SET request_fingerprint = 'changed' WHERE command_id = 'command-1'`,
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, occurred_at, expires_at) VALUES ('job.state.changed', 'race_job', 'job-1', 0, '2026-08-24T00:00:00Z', '2026-08-31T00:00:00Z')`); err != nil {
		t.Fatalf("insert baseline event: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO api_commands(command_id, command_type, subject_id, command_status, request_fingerprint, created_at, expires_at) VALUES ('command-1', 'probe', 'subject-1', 'ACCEPTED', 'fingerprint-1', '2026-08-24T00:00:00Z', '2026-08-31T00:00:00Z')`); err != nil {
		t.Fatalf("insert command: %v", err)
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, statement); err == nil {
				t.Fatal("constraint error = nil, want error")
			}
		})
	}
}

func TestDB08CommandStateAndActiveChainUniqueness(t *testing.T) {
	database := openMigratedDatabase(t)
	ctx := context.Background()
	createdAt := "2026-08-24T00:00:00Z"
	if _, err := database.ExecContext(ctx, `INSERT INTO api_commands(command_id, command_type, subject_id, command_status, request_fingerprint, created_at, expires_at) VALUES ('command-1', 'probe', 'subject-1', 'ACCEPTED', 'fingerprint-1', ?, '2026-08-31T00:00:00Z')`, createdAt); err != nil {
		t.Fatalf("insert command: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE api_commands SET command_status = 'RUNNING', revision = 1, started_at = ? WHERE command_id = 'command-1'`, createdAt); err != nil {
		t.Fatalf("accepted to running: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE api_commands SET command_status = 'SUCCEEDED', revision = 2, finished_at = ?, result_json = '{}' WHERE command_id = 'command-1'`, createdAt); err != nil {
		t.Fatalf("running to succeeded: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE api_commands SET command_status = 'FAILED', revision = 3, finished_at = ?, error_code = 'CONFLICT', error_json = '{}' WHERE command_id = 'command-1'`, createdAt); err == nil {
		t.Fatal("terminal command transition error = nil, want error")
	}
}
