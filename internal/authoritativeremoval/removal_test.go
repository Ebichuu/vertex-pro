package authoritativeremoval

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestReleaseRemovedSuccess(t *testing.T) {
	database := openRemovalDatabase(t)
	seedRemovalFixture(t, database, "UNKNOWN", "UNKNOWN")

	result, err := ReleaseRemoved(context.Background(), database, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.ReleasedUnconsumedBytes != 100 {
		t.Fatalf("released bytes = %d", result.ReleasedUnconsumedBytes)
	}

	var reservationState, reason, attemptState, jobState string
	if err := database.QueryRow(`SELECT reservation_state, release_reason FROM capacity_reservations WHERE reservation_id = 'reservation-1'`).Scan(&reservationState, &reason); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT attempt_state FROM placement_attempts WHERE attempt_id = 'attempt-1'`).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT job_state FROM race_jobs WHERE job_id = 'job-1'`).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if reservationState != ReservationReleased || reason != ReleaseReasonQBTorrentRemoved || attemptState != AttemptReconciledNoEffect || jobState != JobReconciledNoEffect {
		t.Fatalf("state=%s reason=%s attempt=%s job=%s", reservationState, reason, attemptState, jobState)
	}
	var bindingCount, reflectionCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_task_bindings WHERE binding_id = 'binding-1'`).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM reservation_reflections WHERE reservation_id = 'reservation-1'`).Scan(&reflectionCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 0 || reflectionCount != 0 {
		t.Fatalf("bindings=%d reflections=%d", bindingCount, reflectionCount)
	}
}

func TestReleaseRemovedFailClosedEvidenceAndIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{"not authoritative", func(request *Request) { request.AuthoritativeRemovalConfirmed = false }, "REMOVAL_NOT_AUTHORITATIVE"},
		{"stale physical free", func(request *Request) { request.FreshPhysicalFree = false }, "PHYSICAL_FREE_NOT_FRESH"},
		{"owner mismatch", func(request *Request) { request.IdentityKey = "wrong" }, "BINDING_IDENTITY_MISMATCH"},
		{"snapshot mismatch", func(request *Request) { request.SnapshotRevision = 99 }, "PHYSICAL_FREE_EVIDENCE_MISMATCH"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database := openRemovalDatabase(t)
			seedRemovalFixture(t, database, "UNKNOWN", "UNKNOWN")
			request := validRequest()
			testCase.mutate(&request)
			if _, err := ReleaseRemoved(context.Background(), database, request); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %s", err, testCase.want)
			}
			var state string
			if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations WHERE reservation_id = 'reservation-1'`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != ReservationActive {
				t.Fatalf("reservation state = %s", state)
			}
		})
	}
}

func TestReleaseRemovedRejectsCompletedAndDuplicate(t *testing.T) {
	database := openRemovalDatabase(t)
	seedRemovalFixture(t, database, "UNKNOWN", "UNKNOWN")
	if _, err := ReleaseRemoved(context.Background(), database, validRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseRemoved(context.Background(), database, validRequest()); err == nil {
		t.Fatal("duplicate release error = nil")
	}

	database2 := openRemovalDatabase(t)
	seedRemovalFixture(t, database2, "UNKNOWN", "UNKNOWN")
	if _, err := database2.Exec(`UPDATE placement_attempts SET attempt_state = 'HASH_SEEN', confirmation_sync_generation = 2, confirmation_snapshot_revision = 3, confirmation_evidence_digest = 'digest-2' WHERE attempt_id = 'attempt-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseRemoved(context.Background(), database2, validRequest()); err == nil {
		t.Fatal("completed attempt error = nil")
	}
}

func validRequest() Request {
	return Request{ReservationID: "reservation-1", BindingID: "binding-1", AttemptID: "attempt-1", CapacityEpochID: "epoch-1", QBTorrentID: "torrent-1", IdentityKey: "identity-1", SnapshotGeneration: 2, SnapshotRevision: 3, SnapshotDigest: "digest-2", PhysicalFreeBytes: 1000, FreshPhysicalFree: true, AuthoritativeRemovalConfirmed: true, RemovalObservedAt: "2026-08-25T01:00:00Z", ReleasedAt: "2026-08-25T01:00:01Z"}
}

func openRemovalDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "removal.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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

func seedRemovalFixture(t *testing.T, database *sql.DB, attemptState, amountState string) {
	t.Helper()
	now := "2026-08-25T00:00:00Z"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1', 'qb-1', '/downloads', 'VERIFIED', ?, ?)`, []any{now, now}},
		{`INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES ('epoch-1', 'target-1', 1, 'TRUSTED', 1000, 900, 2, 3, 'digest-2', ?)`, []any{now}},
		{`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, observation_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'identity-1', 'READY', 'MATCHED', 'READY', 'PREPARED', 'ACTIVE', ?, ?, ?, ?)`, []any{now, now, now, now}},
		{`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, qb_torrent_id, reserved_bytes, started_at) VALUES ('attempt-1', 'job-1', 'identity-1', 1, 'qb-1', 'target-1', ?, 'STARTED', 'OBSERVED_EFFECT', 1, 'torrent-1', 100, ?)`, []any{attemptState, now}},
		{`INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('submit-1', 'attempt-1', 'qb-1', 'identity-1', 'request-1', 'target-1', 'STARTED', 1, 1, 'old-digest', 'ABSENT', ?, ?)`, []any{now, now}},
		{`INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-1', 'attempt-1', 'target-1', 100, 1, 'ACTIVE')`, nil},
		{`UPDATE race_jobs SET current_attempt_id = 'attempt-1', job_state = ? WHERE job_id = 'job-1'`, []any{attemptState}},
		{`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, identity_key, owner_kind, reservation_id, amount_left_state, amount_left, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('binding-1', 'target-1', 'torrent-1', 'identity-1', 'RESERVATION', 'reservation-1', 'KNOWN', 0, 1, 1, 'old-digest', ?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	_ = amountState
}
