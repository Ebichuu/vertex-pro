package db

import (
	"context"
	"testing"
)

func TestDB07EventsCommandsAndStats(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if _, err := database.ExecContext(ctx, `INSERT INTO api_commands(command_id, command_type, subject_id, command_status, request_fingerprint, created_at, expires_at) VALUES ('cmd-1', 'probe', 'subject-1', 'ACCEPTED', 'fp-1', '2026-08-24T00:00:00Z', '2026-08-31T00:00:00Z')`); err != nil {
		t.Fatalf("insert command: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO api_idempotency_records(idempotency_key, subject_id, request_fingerprint, command_id, created_at, expires_at) VALUES ('key-1', 'subject-1', 'fp-1', 'cmd-1', '2026-08-24T00:00:00Z', '2026-08-25T00:00:00Z')`); err != nil {
		t.Fatalf("insert idempotency record: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO instance_counter_samples(instance_id, sampled_at, download_bytes, upload_bytes, download_rate, upload_rate) VALUES ('instance-1', '2026-08-24T01:00:00Z', 10, 20, 1.5, 2.5)`); err != nil {
		t.Fatalf("insert counter sample: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO instance_traffic_hourly(instance_id, bucket_start, download_bytes, upload_bytes, sample_count, updated_at) VALUES ('instance-1', '2026-08-24T01:00:00Z', 10, 20, 1, '2026-08-24T01:00:00Z')`); err != nil {
		t.Fatalf("insert hourly bucket: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO stats_collection_events(instance_id, collection_kind, collected_at, status) VALUES ('instance-1', 'COUNTER_SAMPLE', '2026-08-24T01:00:00Z', 'SUCCEEDED')`); err != nil {
		t.Fatalf("insert stats collection event: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, safe_data_json, occurred_at, expires_at) VALUES ('command.state.changed', 'api_command', 'cmd-1', 0, '{"status":"ACCEPTED"}', '2026-08-24T00:00:00Z', '2026-08-31T00:00:00Z')`); err != nil {
		t.Fatalf("insert event stream event: %v", err)
	}
	var eventID int64
	if err := database.QueryRowContext(ctx, `SELECT event_id FROM event_stream WHERE aggregate_id = 'cmd-1'`).Scan(&eventID); err != nil {
		t.Fatalf("read event id: %v", err)
	}
	if eventID != 1 {
		t.Fatalf("event id = %d, want 1", eventID)
	}
}

func TestDB07RejectsDuplicateAndExpiredRecords(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	_, err = database.ExecContext(ctx, `INSERT INTO api_commands(command_id, command_type, subject_id, command_status, request_fingerprint, created_at, expires_at) VALUES ('cmd-1', 'probe', 'subject-1', 'ACCEPTED', 'fp-1', '2026-08-24T00:00:00Z', '2026-08-31T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert command: %v", err)
	}
	cases := map[string]string{
		"duplicate command idempotency": `INSERT INTO api_idempotency_records(idempotency_key, subject_id, request_fingerprint, command_id, created_at, expires_at) VALUES ('key-1', 'subject-1', 'fp-1', 'cmd-1', '2026-08-24T00:00:00Z', '2026-08-25T00:00:00Z'), ('key-2', 'subject-1', 'fp-2', 'cmd-1', '2026-08-24T00:00:00Z', '2026-08-25T00:00:00Z')`,
		"expired command":               `INSERT INTO api_commands(command_id, command_type, subject_id, command_status, request_fingerprint, created_at, expires_at) VALUES ('cmd-expired', 'probe', 'subject-1', 'ACCEPTED', 'fp-1', '2026-08-24T00:00:00Z', '2026-08-23T00:00:00Z')`,
		"invalid command terminal":      `INSERT INTO api_commands(command_id, command_type, subject_id, command_status, request_fingerprint, created_at, finished_at, expires_at) VALUES ('cmd-failed', 'probe', 'subject-1', 'FAILED', 'fp-1', '2026-08-24T00:00:00Z', '2026-08-24T01:00:00Z', '2026-08-31T00:00:00Z')`,
		"invalid event expiry":          `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, occurred_at, expires_at) VALUES ('job.state.changed', 'race_job', 'job-1', 0, '2026-08-24T01:00:00Z', '2026-08-24T00:00:00Z')`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, statement); err == nil {
				t.Fatal("constraint error = nil, want error")
			}
		})
	}
}

func TestDB07EventIDsRemainMonotonicAfterDelete(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	runner, err := DefaultRunner()
	if err != nil {
		t.Fatalf("DefaultRunner() error = %v", err)
	}
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	for index := 0; index < 2; index++ {
		if _, err := database.ExecContext(ctx, `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, occurred_at, expires_at) VALUES ('system.alert', 'system', ?, ?, '2026-08-24T00:00:00Z', '2026-08-31T00:00:00Z')`, "system", index); err != nil {
			t.Fatalf("insert event %d: %v", index, err)
		}
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM event_stream WHERE event_id = 1`); err != nil {
		t.Fatalf("delete event: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, occurred_at, expires_at) VALUES ('system.alert', 'system', 'system', 2, '2026-08-24T00:00:00Z', '2026-08-31T00:00:00Z')`); err != nil {
		t.Fatalf("insert after delete: %v", err)
	}
	var eventID int64
	if err := database.QueryRowContext(ctx, `SELECT MAX(event_id) FROM event_stream`).Scan(&eventID); err != nil {
		t.Fatalf("read max event id: %v", err)
	}
	if eventID != 3 {
		t.Fatalf("max event id = %d, want 3", eventID)
	}
}
