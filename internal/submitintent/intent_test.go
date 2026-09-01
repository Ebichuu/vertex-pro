package submitintent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestCreateWritesUniqueIntentFromFreshBaseline(t *testing.T) {
	database := openIntentDatabase(t)
	seedIntentFixture(t, database, "READY")
	result, err := Create(context.Background(), database, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.SubmitIntentID != "submit-1" || result.AttemptID != "attempt-1" {
		t.Fatalf("result=%+v", result)
	}
	var count, generation int
	if err := database.QueryRow(`SELECT COUNT(*), MAX(pre_add_sync_generation) FROM submit_intents WHERE attempt_id = 'attempt-1'`).Scan(&count, &generation); err != nil {
		t.Fatal(err)
	}
	if count != 1 || generation != 2 {
		t.Fatalf("count=%d generation=%d", count, generation)
	}
}

func TestCreateRejectsDuplicateAndStaleIntent(t *testing.T) {
	database := openIntentDatabase(t)
	seedIntentFixture(t, database, "READY")
	if _, err := Create(context.Background(), database, validRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), database, validRequest()); err == nil {
		t.Fatal("duplicate error = nil")
	}

	database2 := openIntentDatabase(t)
	seedIntentFixture(t, database2, "READY")
	request := validRequest()
	request.PreAddSnapshotRevision = 99
	if _, err := Create(context.Background(), database2, request); err == nil || !strings.Contains(err.Error(), "BASELINE_NOT_FRESH") {
		t.Fatalf("error=%v", err)
	}
	var count int
	if err := database2.QueryRow(`SELECT COUNT(*) FROM submit_intents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("intent count=%d", count)
	}
}

func TestCreateRequiresAttemptAndJobPreAddFacts(t *testing.T) {
	cases := []struct {
		name     string
		jobState string
		mutate   func(*Request)
		want     string
	}{
		{"wrong job state", "PREPARE_FAILED", func(*Request) {}, "JOB_NOT_READY_FOR_INTENT"},
		{"present missing id", "READY", func(request *Request) { request.PreAddIdentityFact = IdentityPresent }, "PRESENT_IDENTITY_REQUIRES_QB_ID"},
		{"negative baseline", "READY", func(request *Request) { request.PreAddSyncGeneration = -1 }, "baseline revision is invalid"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database := openIntentDatabase(t)
			seedIntentFixture(t, database, testCase.jobState)
			request := validRequest()
			testCase.mutate(&request)
			if _, err := Create(context.Background(), database, request); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error=%v want=%s", err, testCase.want)
			}
		})
	}
}

func validRequest() Request {
	return Request{SubmitIntentID: "submit-1", AttemptID: "attempt-1", CapacityEpochID: "epoch-1", InstanceID: "qb-1", IdentityKey: "identity-1", RequestFingerprint: "request-1", TargetFingerprint: "target-1", PreAddSyncGeneration: 2, PreAddSnapshotRevision: 3, PreAddSnapshotDigest: "digest-2", PreAddIdentityFact: IdentityAbsent, CreatedAt: "2026-08-25T03:00:00Z"}
}

func openIntentDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "intent.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func seedIntentFixture(t *testing.T, database *sql.DB, jobState string) {
	t.Helper()
	now := "2026-08-25T00:00:00Z"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1', 'qb-1', '/downloads', 'VERIFIED', ?, ?)`, []any{now, now}},
		{`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1', 'target-1', 1, 'TRUSTED', 1000, 900, 2, 3, 'digest-2', ?)`, []any{now}},
		{`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'identity-1', ?, 'MATCHED', 'READY', 'PREPARED', ?, ?, ?, ?)`, []any{jobState, now, now, now, now}},
		{`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1', 'job-1', 'identity-1', 1, 'qb-1', 'target-1', 'RESERVED', 'NOT_STARTED', 1, 100, ?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}
