package domainevents

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestEmitWritesRaceEventAndStreamAtomically(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	seedJob(t, database)
	entry := Entry{
		EventType: "job.state.changed", AggregateType: "race_job", AggregateID: "job-1",
		AggregateRevision: 3, JobID: "job-1", AttemptID: "attempt-1",
		FromState: "RESERVED", ToState: "ADD_SENT", ReasonCode: "DISPATCH_STARTED",
		SafeDataJSON: `{"job_state":"ADD_SENT"}`, OccurredAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
	err := db.RunImmediate(context.Background(), database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		if _, err := transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state='ADD_SENT' WHERE job_id='job-1'`); err != nil {
			return err
		}
		return Emit(transaction, entry)
	})
	if err != nil {
		t.Fatal(err)
	}
	var raceEvents, streamRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE to_state='ADD_SENT'`).Scan(&raceEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_stream WHERE aggregate_id='job-1'`).Scan(&streamRows); err != nil {
		t.Fatal(err)
	}
	if raceEvents != 1 || streamRows != 1 {
		t.Fatalf("race_events=%d event_stream=%d", raceEvents, streamRows)
	}
}

func TestRollbackDiscardsBothRows(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	seedJob(t, database)
	entry := Entry{
		EventType: "attempt.state.changed", AggregateType: "placement_attempt", AggregateID: "attempt-1",
		AggregateRevision: 2, JobID: "job-1", AttemptID: "attempt-1",
		FromState: "RESERVED", ToState: "ADD_SENT", ReasonCode: "DISPATCH_STARTED",
		SafeDataJSON: `{"attempt_state":"ADD_SENT"}`, OccurredAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
	err := db.RunImmediate(context.Background(), database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		if err := Emit(transaction, entry); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("rollback error = nil")
	}
	var raceEvents, streamRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events`).Scan(&raceEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_stream`).Scan(&streamRows); err != nil {
		t.Fatal(err)
	}
	if raceEvents != 0 || streamRows != 0 {
		t.Fatalf("rollback left rows: race_events=%d event_stream=%d", raceEvents, streamRows)
	}
}

func TestStreamOnlyEventTypesAndValidation(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	if err := Append(context.Background(), database, Entry{
		EventType: "system.alert", AggregateType: "system", AggregateID: "vertex-pro",
		AggregateRevision: 1, ReasonCode: "UNBOUNDED_UNMANAGED_LIABILITY",
		SafeDataJSON: `{"code":"UNBOUNDED_UNMANAGED_LIABILITY"}`, OccurredAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	var streamRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_stream WHERE event_type='system.alert'`).Scan(&streamRows); err != nil {
		t.Fatal(err)
	}
	if streamRows != 1 {
		t.Fatalf("stream rows = %d", streamRows)
	}
	if err := Append(context.Background(), database, Entry{
		EventType: "system.alert", AggregateType: "alien", AggregateID: "x",
		SafeDataJSON: `{}`, OccurredAt: now, ExpiresAt: now.Add(time.Hour),
	}); !errors.Is(err, ErrEventInvalid) {
		t.Fatalf("invalid aggregate error = %v", err)
	}
	if err := Append(context.Background(), nil, Entry{}); err == nil {
		t.Fatal("nil db error = nil")
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "domain-events.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func seedJob(t *testing.T, database *sql.DB) {
	t.Helper()
	now := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC).Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','src-1','identity-1','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','RESERVED','NOT_STARTED',1,100,?)", []any{now}},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','req','target','NOT_STARTED',3,7,'baseline','ABSENT',?)", []any{now}},
		{"UPDATE race_jobs SET job_state='RESERVED', current_attempt_id='attempt-1' WHERE job_id='job-1'", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}
