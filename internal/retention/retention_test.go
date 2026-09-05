package retention

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func openDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "retention.db")})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func insertEvent(t *testing.T, database *sql.DB, aggregateID string, revision int64, eventType, occurred, expires string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO event_stream(event_type, aggregate_type, aggregate_id, aggregate_revision, occurred_at, expires_at) VALUES (?, 'system', ?, ?, ?, ?)`,
		eventType, aggregateID, revision, occurred, expires); err != nil {
		t.Fatalf("insert event %s rev %d: %v", aggregateID, revision, err)
	}
}

func insertCommand(t *testing.T, database *sql.DB, id, status, created, expires string) {
	t.Helper()
	finished := "NULL"
	if status == "SUCCEEDED" || status == "FAILED" {
		finished = "'" + created + "'"
	}
	errorCode := "NULL"
	if status == "FAILED" {
		errorCode = "'INTERNAL_ERROR'"
	}
	if _, err := database.Exec(`INSERT INTO api_commands(command_id, command_type, subject_id, command_status, request_fingerprint, created_at, finished_at, error_code, expires_at) VALUES (?, 'TEST', 'subject', ?, 'fingerprint', ?, `+finished+`, `+errorCode+`, ?)`,
		id, status, created, expires); err != nil {
		t.Fatal(err)
	}
}

func insertIdempotency(t *testing.T, database *sql.DB, key, commandID, created, expires string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO api_idempotency_records(idempotency_key, subject_id, request_fingerprint, command_id, created_at, expires_at) VALUES (?, 'subject', 'fingerprint', ?, ?, ?)`,
		key, commandID, created, expires); err != nil {
		t.Fatal(err)
	}
}

func TestSweepEnforcesExpiresAtFacts(t *testing.T) {
	database := openDatabase(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	past := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	future := now.Add(24 * time.Hour).Format(time.RFC3339)

	insertEvent(t, database, "job-expired", 0, "job.state.changed", now.Add(-9*24*time.Hour).Format(time.RFC3339), past) // expired
	insertEvent(t, database, "job-fresh", 0, "job.state.changed", now.Format(time.RFC3339), future)                      // fresh
	oldCreated := now.Add(-8*24*time.Hour - time.Hour).Format(time.RFC3339)
	insertCommand(t, database, "cmd-expired", "SUCCEEDED", oldCreated, past)
	insertCommand(t, database, "cmd-running-expired", "RUNNING", oldCreated, past) // non-terminal: never swept
	insertCommand(t, database, "cmd-fresh", "SUCCEEDED", now.Format(time.RFC3339), future)
	insertIdempotency(t, database, "key-expired", "cmd-expired", oldCreated, past)
	insertIdempotency(t, database, "key-fresh", "cmd-fresh", now.Format(time.RFC3339), future)

	result, err := Sweep(context.Background(), database, "", DefaultPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 1 || result.SystemEvents != 0 {
		t.Fatalf("events result = %+v", result)
	}
	if result.IdempotencyRecords != 1 || result.Commands != 1 {
		t.Fatalf("command result = %+v", result)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_stream`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fresh event survived: count=%d err=%v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM api_commands`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("fresh + non-terminal commands must survive: count=%d err=%v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM api_idempotency_records`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fresh idempotency record must survive: count=%d err=%v", count, err)
	}
}

func TestSweepBatchesDeletesUntilEmpty(t *testing.T) {
	database := openDatabase(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	policy := DefaultPolicy()
	policy.BatchSize = 2
	for index := 0; index < 5; index++ {
		insertEvent(t, database, fmt.Sprintf("job-%d", index), 0, "job.state.changed", now.Add(-9*24*time.Hour).Format(time.RFC3339), expired)
	}
	result, err := Sweep(context.Background(), database, "", policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 5 {
		t.Fatalf("batched sweep deleted %d events, want 5", result.Events)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_stream`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("batched sweep left rows: count=%d err=%v", count, err)
	}
}

func TestSweepRemovesOldSnapshotsButKeepsLatestRevision(t *testing.T) {
	database := openDatabase(t)
	root := t.TempDir()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	old := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
	fresh := now.Add(-time.Hour).Format(time.RFC3339Nano)

	type snapshotRow struct {
		instance, generation, revision, path, created string
	}
	rows := []snapshotRow{
		// qb-1：generation 2 的 rev 0 是最新（即使更旧），generation 1 的 rev 9 已过期
		{"qb-1", "1", "9", "qb-1/1-9-aaa.json", old},
		{"qb-1", "2", "0", "qb-1/2-0-bbb.json", old},
		// qb-2：唯一快照即最新，过期也必须保留
		{"qb-2", "1", "3", "qb-2/1-3-ccc.json", old},
		{"qb-2", "1", "4", "qb-2/1-4-ddd.json", fresh},
	}
	// 外键要求先有 sync_instance_state 行：先落每实例最终坐标（即最新快照）。
	states := []snapshotRow{
		{"qb-1", "2", "0", "qb-1/2-0-bbb.json", old},
		{"qb-2", "1", "4", "qb-2/1-4-ddd.json", fresh},
	}
	for _, state := range states {
		if _, err := database.Exec(`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES (?, 'READY', ?, 0, ?, 'digest', ?)`,
			state.instance, state.generation, state.revision, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range rows {
		if _, err := database.Exec(`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES (?, ?, ?, 0, 'digest', ?, 1, ?)`,
			row.instance, row.generation, row.revision, row.path, row.created); err != nil {
			t.Fatal(err)
		}
		writeFile(t, root, row.path)
	}

	result, err := Sweep(context.Background(), database, root, DefaultPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshots != 2 || len(result.SnapshotFileFailures) != 0 {
		t.Fatalf("snapshot result = %+v", result)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sync_snapshots WHERE instance_id = 'qb-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("qb-1 must keep exactly its latest snapshot: count=%d err=%v", count, err)
	}
	var keptPath string
	if err := database.QueryRow(`SELECT snapshot_path FROM sync_snapshots WHERE instance_id = 'qb-1'`).Scan(&keptPath); err != nil || keptPath != "qb-1/2-0-bbb.json" {
		t.Fatalf("kept path=%q err=%v", keptPath, err)
	}
	// 过期的非最新行（qb-1 rev9、qb-2 rev3）已删且文件移除；最新行文件必须仍在磁盘上。
	if _, err := os.Stat(filepath.Join(root, "qb-1", "1-9-aaa.json")); !os.IsNotExist(err) {
		t.Fatalf("expired snapshot file still present: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "qb-2", "1-3-ccc.json")); !os.IsNotExist(err) {
		t.Fatalf("qb-2 expired snapshot file still present: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "qb-2", "1-4-ddd.json")); err != nil {
		t.Fatalf("qb-2 latest snapshot file must survive: err=%v", err)
	}
	var qb2Count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sync_snapshots WHERE instance_id = 'qb-2'`).Scan(&qb2Count); err != nil || qb2Count != 1 {
		t.Fatalf("qb-2 must keep exactly its latest snapshot: count=%d err=%v", qb2Count, err)
	}
	if _, err := os.Stat(filepath.Join(root, "qb-1", "2-0-bbb.json")); err != nil {
		t.Fatalf("latest snapshot file must survive: err=%v", err)
	}
}

func TestSweepPrunesStatsByRetention(t *testing.T) {
	database := openDatabase(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	if _, err := database.Exec(`INSERT INTO instance_counter_samples(instance_id, sampled_at, download_bytes, upload_bytes, download_rate, upload_rate) VALUES ('qb-1', ?, 1, 1, 1, 1)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO instance_counter_samples(instance_id, sampled_at, download_bytes, upload_bytes, download_rate, upload_rate) VALUES ('qb-1', ?, 1, 1, 1, 1)`, fresh); err != nil {
		t.Fatal(err)
	}
	result, err := Sweep(context.Background(), database, "", DefaultPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.StatsRaw != 1 || result.StatsHourly != 0 {
		t.Fatalf("stats result = %+v", result)
	}
}

func writeFile(t *testing.T, root, relative string) {
	t.Helper()
	absolute, ok := safeJoin(root, relative)
	if !ok {
		t.Fatalf("invalid snapshot path %q", relative)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}
