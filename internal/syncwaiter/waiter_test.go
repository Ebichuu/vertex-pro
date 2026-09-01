package syncwaiter

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestRegisterNotifyAndRestoreWaiter(t *testing.T) {
	database := openWaiterDatabase(t)
	seedWaiterFixture(t, database)
	waiter, err := Register(context.Background(), database, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if waiter.State != WaiterActive || waiter.Generation != 1 || waiter.SourceRevision != 2 {
		t.Fatalf("waiter=%+v", waiter)
	}
	stale, err := Notify(context.Background(), database, waiter.ID, Notification{Generation: 1, SnapshotRevision: 2, ObservedIdentityKey: "identity-1"}, "2026-08-25T04:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Ignored {
		t.Fatalf("stale=%+v", stale)
	}
	active, err := RestoreActive(context.Background(), database, "qb-1")
	if err != nil || len(active) != 1 {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	fresh, err := Notify(context.Background(), database, waiter.ID, Notification{Generation: 1, SnapshotRevision: 3, ObservedIdentityKey: "identity-1"}, "2026-08-25T04:00:01Z")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Ignored || !fresh.IdentityMatched || fresh.Waiter.State != WaiterClosed {
		t.Fatalf("fresh=%+v", fresh)
	}
	active, err = RestoreActive(context.Background(), database, "qb-1")
	if err != nil || len(active) != 0 {
		t.Fatalf("active after notify=%+v err=%v", active, err)
	}
	duplicate, err := Notify(context.Background(), database, waiter.ID, Notification{Generation: 2, SnapshotRevision: 1}, "2026-08-25T04:00:02Z")
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Ignored {
		t.Fatalf("duplicate=%+v", duplicate)
	}
}

func TestRegisterRejectsIncompleteOrStaleFacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{"missing snapshot", func(request *Request) { request.SourceSnapshotRevision = 99 }, "WAITER_SOURCE_SNAPSHOT_NOT_FOUND"},
		{"deadline order", func(request *Request) { request.ReconciliationDeadline = "2026-08-25T03:30:00Z" }, "reconciliation deadline precedes"},
		{"identity mismatch", func(request *Request) { request.IdentityKey = "wrong" }, "WAITER_ATTEMPT_FACT_MISMATCH"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database := openWaiterDatabase(t)
			seedWaiterFixture(t, database)
			request := validRequest()
			testCase.mutate(&request)
			if _, err := Register(context.Background(), database, request); err == nil || !contains(err.Error(), testCase.want) {
				t.Fatalf("error=%v want=%s", err, testCase.want)
			}
		})
	}
}

func TestCloseIsIdempotentAndRestorable(t *testing.T) {
	database := openWaiterDatabase(t)
	seedWaiterFixture(t, database)
	if _, err := Register(context.Background(), database, validRequest()); err != nil {
		t.Fatal(err)
	}
	changed, err := Close(context.Background(), database, "waiter-1", "2026-08-25T04:00:00Z")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	changed, err = Close(context.Background(), database, "waiter-1", "2026-08-25T04:00:01Z")
	if err != nil || changed {
		t.Fatalf("second close changed=%v err=%v", changed, err)
	}
}

func validRequest() Request {
	return Request{WaiterID: "waiter-1", AttemptID: "attempt-1", InstanceID: "qb-1", RegisteredGeneration: 1, SourceSnapshotRevision: 2, IdentityKey: "identity-1", ConfirmationDeadline: "2026-08-25T05:00:00Z", ReconciliationDeadline: "2026-08-25T06:00:00Z", CreatedAt: "2026-08-25T03:00:00Z"}
}

func openWaiterDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "waiter.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func seedWaiterFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	now := "2026-08-25T00:00:00Z"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1', 'READY', 1, 2, 2, 'digest-2', ?)`, []any{now}},
		{`INSERT INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1', 1, 2, 2, 'digest-2', '/tmp/snapshot-2.json', 1, ?)`, []any{now}},
		{`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'identity-1', 'READY', 'MATCHED', 'READY', 'PREPARED', ?, ?, ?, ?)`, []any{now, now, now, now}},
		{`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1', 'job-1', 'identity-1', 1, 'qb-1', 'target-1', 'RESERVED', 'NOT_STARTED', 1, 100, ?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
