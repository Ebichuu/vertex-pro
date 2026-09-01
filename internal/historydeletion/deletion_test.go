package historydeletion

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/historyproject"
)

func TestRecordPersistsDeletionFactIdempotently(t *testing.T) {
	database := openMigrated(t)
	seedJob(t, database)
	request := baseRequest("fact-1")
	result, err := Record(context.Background(), database, request)
	if err != nil || result.Idempotent {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	facts, err := ForJob(context.Background(), database, "job-1")
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	fact := facts[0]
	if fact.QBTorrentID != "torrent-9" || fact.DeletionSource != SourceQBUserRemoved || fact.LastObservation != "SEEDING" || fact.LastUploaded != 4096 || fact.LastRatio == nil || *fact.LastRatio != 2 {
		// ratio 由 baseRequest 设置为 2.0
		t.Fatalf("fact = %+v", fact)
	}
	replay, err := Record(context.Background(), database, request)
	if err != nil || !replay.Idempotent {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	conflict := request
	conflict.FactID = "fact-2"
	conflict.SnapshotDigest = "other-digest"
	if _, err := Record(context.Background(), database, conflict); !errors.Is(err, ErrNotRecordable) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestBindingDeletionKeepsHistoryQueryable(t *testing.T) {
	database := openMigrated(t)
	seedJob(t, database)
	// 一条已完成投递链：HASH_SEEN + binding 存在。
	now := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC).Format(time.RFC3339)
	statements := []string{
		`INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES ('reservation-1','attempt-1','target-1',100,1,'REFLECTING')`,
		`INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, identity_key, owner_kind, reservation_id, amount_left_state, amount_left, uncertainty_debit_kind, uncertainty_debit_bytes, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES ('binding-1','target-1','torrent-9','identity-1','RESERVATION','reservation-1','KNOWN',0,'NONE',0,5,2,'after','` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// 权威删除：记录删除事实（binding 随后由 CAP-12 事务移除）。
	if _, err := Record(context.Background(), database, baseRequest("fact-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM capacity_task_bindings WHERE binding_id='binding-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM capacity_reservations WHERE reservation_id='reservation-1'`); err != nil {
		t.Fatal(err)
	}
	projection := historyproject.Projection{}
	entries, err := projection.Project(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	var deleted *historyproject.Entry
	for index := range entries {
		if entries[index].JobID == "job-1" {
			deleted = &entries[index]
		}
	}
	if deleted == nil {
		t.Fatal("history entry disappeared after binding deletion")
	}
	if deleted.UserStatus.ID != "DELETED" || deleted.DeletedAt == "" || deleted.DeletionSource != SourceQBUserRemoved {
		t.Fatalf("entry = %+v", *deleted)
	}
	if err := historyproject.ValidateEntry(*deleted); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsInvalidFacts(t *testing.T) {
	valid := baseRequest("fact-v")
	if err := validate(valid); err != nil {
		t.Fatal(err)
	}
	ratio := 1.5
	valid.LastRatio = &ratio
	if err := validate(valid); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Request){
		"missing torrent": func(r *Request) { r.QBTorrentID = "" },
		"bad source":      func(r *Request) { r.DeletionSource = "SOMEONE" },
		"bad observation": func(r *Request) { r.LastObservation = "UNKNOWN" },
		"negative upload": func(r *Request) { r.LastUploaded = -1 },
		"negative ratio":  func(r *Request) { r.LastRatio = floatPtr(-0.5) },
		"zero deleted at": func(r *Request) { r.DeletedAt = time.Time{} },
		"missing digest":  func(r *Request) { r.SnapshotDigest = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if err := validate(request); err == nil {
				t.Fatal("validation error = nil")
			}
		})
	}
}

func floatPtr(value float64) *float64 { return &value }

var ratioValue = 2.0

func baseRequest(factID string) Request {
	return Request{
		LastRatio:        &ratioValue,
		FactID:           factID,
		JobID:            "job-1",
		IdentityKey:      "identity-1",
		QBTorrentID:      "torrent-9",
		StorageTargetID:  "target-1",
		DeletionSource:   SourceQBUserRemoved,
		DeletedAt:        time.Date(2026, 8, 29, 7, 30, 0, 0, time.UTC),
		LastObservation:  "SEEDING",
		LastUploaded:     4096,
		LastDownloaded:   2048,
		SnapshotDigest:   "after",
		SnapshotRevision: 2,
		Now:              time.Date(2026, 8, 29, 7, 31, 0, 0, time.UTC),
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "history-deletion.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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
		{"INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED',?,?)", []any{now, now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','src-1','identity-1','READY','MATCHED','READY','PREPARED',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, qb_torrent_id, confirmation_sync_generation, confirmation_snapshot_revision, confirmation_evidence_digest, allocation_epoch, reserved_bytes, started_at, confirmed_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','HASH_SEEN','STARTED','OBSERVED_EFFECT','torrent-9',5,2,'after',1,100,?,?)", []any{now, now}},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','req','target','STARTED',5,2,'after','ABSENT',?,?)", []any{now, now}},
		{"UPDATE race_jobs SET job_state='RECONCILED_CONFIRMED', current_attempt_id='attempt-1' WHERE job_id='job-1'", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}
