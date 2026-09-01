package historyproject

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/generated"
)

// TestAllInternalJobStatesCovered 要求每个内部 Job 状态都映射到十个可见
// 用户状态之一，且确定性输出。
func TestAllInternalJobStatesCovered(t *testing.T) {
	if len(generated.JobStateValues) == 0 {
		t.Fatal("generated job states missing")
	}
	visible := map[string]bool{}
	for id := range visibleLabels {
		visible[string(id)] = true
	}
	for _, jobState := range generated.JobStateValues {
		status := ProjectStatus(Facts{JobState: jobState, InstanceAvailable: true})
		if !visible[string(status.ID)] {
			t.Fatalf("job state %s mapped to %s which is not a visible status", jobState, status.ID)
		}
		if status.Label == "" || status.Label != visibleLabels[status.ID] {
			t.Fatalf("job state %s label mismatch: %+v", jobState, status)
		}
		again := ProjectStatus(Facts{JobState: jobState, InstanceAvailable: true})
		if again != status {
			t.Fatalf("mapping for %s is not deterministic", jobState)
		}
	}
}

func TestMappingPrecedenceAndForbiddenSuccess(t *testing.T) {
	tests := []struct {
		name  string
		facts Facts
		want  UserStatusID
	}{
		{"unknown never succeeds", Facts{InstanceAvailable: true, JobState: "UNKNOWN", TorrentObservation: "SEEDING"}, "NEEDS_CONFIRMATION"},
		{"expired unknown never succeeds", Facts{InstanceAvailable: true, JobState: "EXPIRED_UNKNOWN", TorrentObservation: "COMPLETED"}, "NEEDS_CONFIRMATION"},
		{"failed beats seeding", Facts{InstanceAvailable: true, JobState: "ADD_FAILED", TorrentObservation: "SEEDING"}, "FAILED"},
		{"rule rejection fails", Facts{InstanceAvailable: true, JobState: "DISCOVERED", RuleState: "REJECTED"}, "FAILED"},
		{"deleted beats completed", Facts{DeletionFact: "CONFIRMED", TorrentObservation: "COMPLETED"}, "DELETED"},
		{"removed externally deletes", Facts{HistoryState: "REMOVED_EXTERNALLY", JobState: "RECONCILED_CONFIRMED"}, "DELETED"},
		{"seeding observation", Facts{InstanceAvailable: true, JobState: "HASH_SEEN", TorrentObservation: "SEEDING"}, "SEEDING"},
		{"downloading observation", Facts{InstanceAvailable: true, JobState: "HASH_SEEN", TorrentObservation: "DOWNLOADING"}, "DOWNLOADING"},
		{"completed observation", Facts{InstanceAvailable: true, JobState: "RECONCILED_CONFIRMED", TorrentObservation: "COMPLETED"}, "COMPLETED"},
		{"adding while unconfirmed", Facts{InstanceAvailable: true, JobState: "AWAITING_CONFIRMATION"}, "ADDING"},
		{"waiting capacity", Facts{InstanceAvailable: true, JobState: "WAITING_CAPACITY"}, "WAITING_DOWNLOADER"},
		{"placement waiting capacity", Facts{InstanceAvailable: true, JobState: "PREPARING", PlacementState: "WAITING_CAPACITY"}, "WAITING_DOWNLOADER"},
		{"preparing seed", Facts{InstanceAvailable: true, JobState: "PREPARING", RuleState: "RUNNING"}, "PREPARING_SEED"},
		{"waiting rule", Facts{InstanceAvailable: true, JobState: "DISCOVERED", RuleState: "PENDING"}, "WAITING_RULE"},
		{"hash seen before observation stays adding", Facts{InstanceAvailable: true, JobState: "HASH_SEEN"}, "ADDING"},
		{"already present fails", Facts{InstanceAvailable: true, JobState: "ALREADY_PRESENT"}, "FAILED"},
		{"reconciled no effect fails", Facts{InstanceAvailable: true, JobState: "RECONCILED_NO_EFFECT"}, "FAILED"},
		{"expired fails", Facts{InstanceAvailable: true, JobState: "EXPIRED"}, "FAILED"},
		{"ready keeps preparing", Facts{InstanceAvailable: true, JobState: "READY", PlacementState: "PREPARED"}, "PREPARING_SEED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := ProjectStatus(test.facts)
			if status.ID != test.want {
				t.Fatalf("status = %s want %s", status.ID, test.want)
			}
		})
	}
	for _, forbidden := range []string{"UNKNOWN", "EXPIRED_UNKNOWN", "CAPACITY_UNCERTAIN", "SNAPSHOT_STALE", "STORAGE_UNVERIFIED"} {
		status := ProjectStatus(Facts{InstanceAvailable: true, JobState: forbidden})
		switch status.ID {
		case "SEEDING", "DOWNLOADING", "COMPLETED", "ADDING":
			t.Fatalf("%s must never map to success, got %s", forbidden, status.ID)
		}
	}
}

// TestUserStatusLabelsMatchContract 守护标签与 user_status_mapping.yaml 一致。
func TestUserStatusLabelsMatchContract(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/user_status_mapping.yaml")
	if err != nil {
		t.Fatal(err)
	}
	linePattern := regexp.MustCompile(`^\s{2}([A-Z_]+): (.+)$`)
	matches := 0
	for _, line := range strings.Split(string(raw), "\n") {
		match := linePattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		id := UserStatusID(match[1])
		label, ok := visibleLabels[id]
		if !ok {
			continue
		}
		matches++
		if label != match[2] {
			t.Fatalf("label for %s = %s want %s", id, label, match[2])
		}
	}
	if matches != len(visibleLabels) {
		t.Fatalf("contract covered %d of %d labels", matches, len(visibleLabels))
	}
}

func TestProjectionBuildsEntriesWithoutInternalTerms(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC).Format(time.RFC3339)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO sites(site_id, name, base_url, created_at, updated_at) VALUES ('site-1','HDSky','https://hdsky.me',?,?)", []any{now, now}},
		{"INSERT INTO identities(identity_id, canonical_hash, title, size_bytes, created_at, updated_at) VALUES ('id-1','identity-1','Ubuntu 24.04 LTS',3221225472,?,?)", []any{now, now}},
		{"INSERT INTO identity_sources(identity_id, site_id, source_key, created_at) VALUES ('id-1','site-1','src-1',?)", []any{now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, observation_state, effective_target_mode, effective_target_instance_id, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-2','src-1','identity-1','DISCOVERED','PENDING','PENDING','PENDING','NOT_STARTED',NULL,NULL,?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, observation_state, effective_target_mode, effective_target_instance_id, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','src-1','identity-1','READY','MATCHED','READY','PREPARED','NOT_STARTED','FIXED_INSTANCE','qb-1',?,?,?,?)", []any{now, now, now, now}},
		{"INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, side_effect_fact, qb_torrent_id, confirmation_sync_generation, confirmation_snapshot_revision, confirmation_evidence_digest, allocation_epoch, reserved_bytes, started_at, confirmed_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','HASH_SEEN','STARTED','OBSERVED_EFFECT','torrent-9',5,2,'evidence',1,100,?,?)", []any{now, now}},
		{"INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','req','target','STARTED',5,2,'after','ABSENT',?,?)", []any{now, now}},
		{"UPDATE race_jobs SET job_state='HASH_SEEN', current_attempt_id='attempt-1' WHERE job_id='job-1'", nil},
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := Projection{}.Project(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	// 稳定排序：created_at 相同按 job_id 倒序无歧义，这里校验两条都在且字段正确。
	byJob := map[string]Entry{}
	for _, entry := range entries {
		byJob[entry.JobID] = entry
	}
	delivering := byJob["job-1"]
	if delivering.UserStatus.ID != "ADDING" || delivering.Title != "Ubuntu 24.04 LTS" || delivering.SiteName != "HDSky" || delivering.InstanceID != "qb-1" || delivering.AttemptCount != 1 || delivering.AddedAt == "" || delivering.ConfirmedAt == "" {
		t.Fatalf("delivering entry = %+v", delivering)
	}
	discovered := byJob["job-2"]
	if discovered.UserStatus.ID != "WAITING_RULE" {
		t.Fatalf("discovered status = %s", discovered.UserStatus.ID)
	}
	for _, entry := range entries {
		if err := ValidateEntry(entry); err != nil {
			t.Fatal(err)
		}
		serialized, _ := json.Marshal(entry)
		for _, term := range HiddenTerms() {
			if strings.Contains(string(serialized), term) {
				t.Fatalf("entry %s leaks %s", entry.JobID, term)
			}
		}
	}
	projection := Projection{}
	if _, err := projection.Project(context.Background(), nil); err == nil {
		t.Fatal("nil db error = nil")
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "history.db"), MaxOpenConns: 1, MaxIdleConns: 1})
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
