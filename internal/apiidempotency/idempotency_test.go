package apiidempotency

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestSameKeySameBodyReplaysWithoutReexecution(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	body := []byte(`{"name":"rule-a"}`)
	fingerprint := Fingerprint(body)
	first, err := Begin(context.Background(), database, "key-1", "job-1", fingerprint, now)
	if err != nil || first.Replayed {
		t.Fatalf("first = %+v err = %v", first, err)
	}
	if err := Complete(context.Background(), database, first.CommandID, "", `{"ok":true}`, now); err != nil {
		t.Fatal(err)
	}
	// 响应丢失后的重试：同 key 同 body，返回已存命令且不重复执行。
	replay, err := Begin(context.Background(), database, "key-1", "job-1", fingerprint, now.Add(time.Minute))
	if err != nil || !replay.Replayed {
		t.Fatalf("replay = %+v err = %v", replay, err)
	}
	if replay.CommandID != first.CommandID {
		t.Fatalf("command id = %s want %s", replay.CommandID, first.CommandID)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM api_commands`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("command rows = %d", count)
	}
}

func TestSameKeyDifferentBodyRejected(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	if _, err := Begin(context.Background(), database, "key-1", "job-1", Fingerprint([]byte(`{"a":1}`)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(context.Background(), database, "key-1", "job-1", Fingerprint([]byte(`{"a":2}`)), now.Add(time.Minute)); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("error = %v", err)
	}
}

func TestExpiredKeyIsReRegistered(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	first, err := Begin(context.Background(), database, "key-1", "job-1", Fingerprint([]byte(`{"a":1}`)), now)
	if err != nil {
		t.Fatal(err)
	}
	// 超过 24h：旧记录清除并按新命令注册。
	again, err := Begin(context.Background(), database, "key-1", "job-1", Fingerprint([]byte(`{"a":1}`)), now.Add(RecordTTL+time.Minute))
	if err != nil || again.Replayed {
		t.Fatalf("again = %+v err = %v", again, err)
	}
	if again.CommandID == first.CommandID {
		t.Fatal("expired key must register a fresh command")
	}
}

func TestCompleteTerminalStates(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	first, err := Begin(context.Background(), database, "key-1", "job-1", Fingerprint([]byte(`{}`)), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := Complete(context.Background(), database, first.CommandID, "JOB_STATE_CONFLICT", "", now); err != nil {
		t.Fatal(err)
	}
	var status, errorCode string
	if err := database.QueryRow(`SELECT command_status, COALESCE(error_code,'') FROM api_commands WHERE command_id = ?`, first.CommandID).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || errorCode != "JOB_STATE_CONFLICT" {
		t.Fatalf("command = %s/%s", status, errorCode)
	}
	if err := Complete(context.Background(), database, first.CommandID, "", `{"ok":true}`, now); err == nil {
		t.Fatal("completing a finished command must fail")
	}
}

func TestMissingInputsRejected(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	if _, err := Begin(context.Background(), database, "", "job-1", "fp", now); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("missing key error = %v", err)
	}
	if _, err := Begin(context.Background(), database, "key-1", "job-1", "", now); err == nil {
		t.Fatal("missing fingerprint error = nil")
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "idempotency.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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
