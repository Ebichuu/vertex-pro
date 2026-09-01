package resourcesapi

import (
	"database/sql"
	"io"
	"strings"
	"testing"
	"time"
)

func seedResourceFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	now := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC).Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO sites(site_id, name, base_url, created_at, updated_at) VALUES ('site-1','HDSky','https://hdsky.me',?,?)", []any{now, now}},
		{"INSERT INTO feeds(feed_id, site_id, name, feed_url, enabled, created_at, updated_at) VALUES ('feed-1','site-1','HDSky RSS','https://hdsky.me/feed',1,?,?)", []any{now, now, now, now}},
		{"INSERT INTO rules(rule_id, name, priority, state, definition_json, created_at, updated_at) VALUES ('rule-1','free-rule',10,'MATCHED','{}',?,?)", []any{now, now}},
		{"INSERT INTO identities(identity_id, canonical_hash, title, created_at, updated_at) VALUES ('id-1','identity-1','Ubuntu 24.04 LTS',?,?)", []any{now, now}},
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED',?,?)", []any{now, now}},
		{"INSERT INTO sync_instance_state(instance_id, sync_state, sync_generation, rid, snapshot_revision, snapshot_digest, updated_at) VALUES ('qb-1','READY',5,0,2,'after',?)", []any{now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, observation_state, effective_target_mode, effective_target_instance_id, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','src-1','identity-1','READY','MATCHED','READY','PREPARED','NOT_STARTED','FIXED_INSTANCE','qb-1',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','UNKNOWN','STARTED','POSSIBLE_EFFECT',1,100,?)", []any{now}},
		{"INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-1','attempt-1','target-1',100,1,'ACTIVE')", nil},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','req','target','STARTED',3,7,'baseline','ABSENT',?,?)", []any{now, now}},
		{"INSERT OR IGNORE INTO sync_snapshots(instance_id, sync_generation, snapshot_revision, rid, snapshot_digest, snapshot_path, is_complete, created_at) VALUES ('qb-1',5,2,0,'after','/snapshots/after.json',1,?)", []any{now}},
		{"INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state, created_at) VALUES ('waiter-1','attempt-1','qb-1',5,2,'identity-1','2026-08-29T08:10:00Z','2026-08-29T09:00:00Z','ACTIVE',?)", []any{now}},
		{"UPDATE race_jobs SET job_state='UNKNOWN', current_attempt_id='attempt-1' WHERE job_id='job-1'", nil},
		{"INSERT INTO instance_traffic_hourly(instance_id, bucket_start, download_bytes, upload_bytes, sample_count, revision, updated_at) VALUES ('qb-1','2026-08-29T08:00:00Z',1234,567,3,1,?)", []any{now}},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func stringReader(body string) io.Reader { return strings.NewReader(body) }
