package db

import (
	"context"
	"testing"
)

func TestRaceAttemptIntentInsertionOrderAndOneToOne(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	createdAt := "2026-08-24T00:00:00Z"
	if _, err := database.Exec(`INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, "job-1", "source-1", "READY", createdAt, createdAt, createdAt, createdAt); err != nil {
		t.Fatalf("insert race job error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO race_job_rule_matches(rule_match_id, job_id, rule_id, rule_revision, match_state, aggregation_revision, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, "match-1", "job-1", "rule-1", 1, "MATCHED", 1, createdAt); err != nil {
		t.Fatalf("insert rule match error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "attempt-1", "job-1", "identity-1", 1, "qb-1", "target-1", "RESERVED", "NOT_STARTED", 1, 500, createdAt); err != nil {
		t.Fatalf("insert placement attempt error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "intent-1", "attempt-1", "qb-1", "identity-1", "request-1", "target-1", "NOT_STARTED", 1, 1, "digest-1", "ABSENT", createdAt); err != nil {
		t.Fatalf("insert submit intent error = %v", err)
	}
	if _, err := database.Exec(`UPDATE race_jobs SET current_attempt_id = ?, job_state = ?, updated_at = ? WHERE job_id = ?`, "attempt-1", "RESERVED", createdAt, "job-1"); err != nil {
		t.Fatalf("publish current attempt error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "intent-duplicate", "attempt-1", "qb-1", "identity-1", "request-2", "target-1", "NOT_STARTED", 1, 1, "digest-1", "ABSENT", createdAt); err == nil {
		t.Fatal("duplicate SubmitIntent error = nil, want UNIQUE(attempt_id)")
	}
	if _, err := database.Exec(`UPDATE placement_attempts SET attempt_state = 'ADD_FAILED', side_effect_fact = 'FAILED_NO_EFFECT', finished_at = ? WHERE attempt_id = ?`, createdAt, "attempt-1"); err != nil {
		t.Fatalf("close first placement attempt error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "attempt-2", "job-1", "identity-2", 2, "qb-1", "target-1", "RESERVED", "NOT_STARTED", 2, 600, createdAt); err != nil {
		t.Fatalf("insert second placement attempt error = %v", err)
	}
	if _, err := database.Exec(`INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "intent-mismatch", "attempt-2", "qb-1", "wrong-identity", "request-3", "target-1", "NOT_STARTED", 1, 1, "digest-1", "ABSENT", createdAt); err == nil {
		t.Fatal("mismatched SubmitIntent error = nil, want trigger error")
	}
}

func TestRaceAttemptChecksRejectInvalidStateAndCurrentPointer(t *testing.T) {
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	createdAt := "2026-08-24T00:00:00Z"
	for name, statement := range map[string]string{
		"reserved side effect":   `INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-invalid', 'source', 'RESERVED', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z')`,
		"attempt zero":           `INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('a', 'missing-job', 'id', 0, 'qb', 'target', 'RESERVED', 'NOT_STARTED', 0, 1, '2026-08-24T00:00:00Z')`,
		"current without intent": `INSERT INTO race_jobs(job_id, source_id, job_state, current_attempt_id, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-pointer', 'source', 'RESERVED', 'missing-attempt', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z', '2026-08-24T00:00:00Z')`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.Exec(statement); err == nil {
				t.Fatal("constraint error = nil, want error")
			}
		})
	}
	_ = createdAt
}
