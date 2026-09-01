package historytimeline

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestForJobStableOrderAndStageMapping(t *testing.T) {
	database := openMigrated(t)
	seedJobWithEvents(t, database)
	events, err := ForJob(context.Background(), database, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d want 4", len(events))
	}
	// 相同 occurred_at 的事件按 event_id 升序：3,4 都在同一秒。
	if events[2].EventID > events[3].EventID {
		t.Fatalf("same-time events out of order: %d then %d", events[2].EventID, events[3].EventID)
	}
	stages := make([]string, 0, len(events))
	for _, event := range events {
		stages = append(stages, event.Stage())
	}
	expect := []string{"准备", "添加", "添加", "确认"}
	for index, want := range expect {
		if stages[index] != want {
			t.Fatalf("stage[%d] = %s want %s", index, stages[index], want)
		}
	}
}

func TestForJobEmptyAndNilDB(t *testing.T) {
	database := openMigrated(t)
	events, err := ForJob(context.Background(), database, "missing-job")
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if _, err := ForJob(context.Background(), nil, "job-1"); err == nil {
		t.Fatal("nil db error = nil")
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "timeline.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func seedJobWithEvents(t *testing.T, database *sql.DB) {
	t.Helper()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','src-1','identity-1','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, occurred_at) VALUES ('job-1','job.preparation.changed','','MATCHED','RULE_MATCHED','2026-08-29T12:00:01Z')", nil},
		{"INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, occurred_at) VALUES ('job-1','job.state.changed','READY','RESERVED','CHAIN_RESERVED','2026-08-29T12:00:02Z')", nil},
		{"INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, occurred_at) VALUES ('job-1','attempt.state.changed','RESERVED','ADD_SENT','DISPATCH_STARTED','2026-08-29T12:00:03Z')", nil},
		{"INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, occurred_at) VALUES ('job-1','attempt.state.changed','ADD_SENT','HASH_SEEN','IDENTITY_CONFIRMED','2026-08-29T12:00:03Z')", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}
