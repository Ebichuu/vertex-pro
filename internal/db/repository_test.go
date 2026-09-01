package db

import (
	"context"
	"testing"
)

func TestRepositoryUpdatesOnlyMutableAttemptFacts(t *testing.T) {
	database := openMigratedDatabase(t)
	ctx := context.Background()
	createdAt := "2026-08-25T00:00:00Z"
	if _, err := database.ExecContext(ctx, `INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'READY', ?, ?, ?, ?)`, createdAt, createdAt, createdAt, createdAt); err != nil {
		t.Fatalf("insert race job: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1', 'job-1', 'identity-1', 1, 'instance-1', 'target-1', 'ADD_SENT', 'STARTED', 'POSSIBLE_EFFECT', 1, 100, ?)`, createdAt); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	state := "AWAITING_CONFIRMATION"
	dispatch := "STARTED"
	if err := RunImmediate(ctx, database, func(ctx context.Context, transaction *ImmediateTx) error {
		return (Repository{}).UpdatePlacementAttempt(ctx, transaction, "attempt-1", AttemptUpdate{AttemptState: &state, DispatchState: &dispatch})
	}); err != nil {
		t.Fatalf("allowlisted update: %v", err)
	}
	var attemptState string
	if err := database.QueryRowContext(ctx, `SELECT attempt_state FROM placement_attempts WHERE attempt_id = 'attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatalf("read attempt state: %v", err)
	}
	if attemptState != state {
		t.Fatalf("attempt state = %q, want %q", attemptState, state)
	}
	if err := RunImmediate(ctx, database, func(ctx context.Context, transaction *ImmediateTx) error {
		return (Repository{}).UpdatePlacementAttempt(ctx, transaction, "missing", AttemptUpdate{AttemptState: &state})
	}); err == nil {
		t.Fatal("missing attempt update error = nil, want error")
	}
}
