package placementaudit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/placement"
)

func TestRecordAndGetPreservesDecisionSnapshot(t *testing.T) {
	database := openDatabase(t)
	if _, err := database.Exec(`INSERT INTO sites(site_id, name, base_url, created_at, updated_at) VALUES ('site-1', 'Site', 'https://site.example', ?, ?)`, nowText(), nowText()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO race_jobs(job_id, source_id, job_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1', 'source-1', 'DISCOVERED', ?, ?, ?, ?)`, nowText(), nowText(), nowText(), nowText()); err != nil {
		t.Fatal(err)
	}
	decision, err := placement.Decide(placement.DecisionInput{
		IdentityKey:   "sha1:identity-1",
		TargetMode:    placement.TargetInstanceGroup,
		RequiredBytes: 100,
		Weights:       testWeights(),
		Candidates:    []placement.DecisionCandidate{testCandidate("qb-a", "target-a")},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(context.Background(), RecordInput{
		AuditID:        "audit-1",
		JobID:          "job-1",
		SourceID:       "source-1",
		SiteID:         "site-1",
		FeedHash:       "sha256:feed-1",
		CanonicalHash:  "sha1:identity-1",
		OfficialState:  "OFFICIAL",
		OfficialReason: "site category=official",
		TargetMode:     placement.TargetInstanceGroup,
		Decision:       decision,
		AddResultState: "SUCCEEDED",
		AddResultCode:  "OBSERVED_EFFECT",
		CreatedAt:      nowText(),
		UpdatedAt:      nowText(),
	}); err != nil {
		t.Fatal(err)
	}
	audit, err := recorder.Get(context.Background(), "audit-1")
	if err != nil {
		t.Fatal(err)
	}
	if audit.AuditID != "audit-1" || audit.JobID != "job-1" || audit.SiteID != "site-1" || audit.FeedHash != "sha256:feed-1" || audit.CanonicalHash != "sha1:identity-1" {
		t.Fatalf("audit identity=%+v", audit)
	}
	if audit.OfficialState != "OFFICIAL" || audit.OfficialReason != "site category=official" || audit.DecisionState != "SELECTED" || audit.SelectedInstanceID != "qb-a" || audit.SelectedStorageTargetID != "target-a" || audit.SelectedScore == nil || audit.AddResultState != "SUCCEEDED" || audit.AddResultCode != "OBSERVED_EFFECT" {
		t.Fatalf("audit decision=%+v", audit)
	}
	if audit.CandidateSnapshotJSON == "" || audit.DecisionSummaryJSON == "" {
		t.Fatalf("audit snapshot missing: %+v", audit)
	}
	var validSnapshot, validSummary int
	if err := database.QueryRow(`SELECT json_valid(candidate_snapshot_json), json_valid(decision_summary_json) FROM placement_decision_audits WHERE audit_id = 'audit-1'`).Scan(&validSnapshot, &validSummary); err != nil {
		t.Fatal(err)
	}
	if validSnapshot != 1 || validSummary != 1 {
		t.Fatalf("json validity snapshot=%d summary=%d", validSnapshot, validSummary)
	}
}

func TestRecordNoEligibleDecisionDoesNotInventTarget(t *testing.T) {
	database := openDatabase(t)
	if _, err := database.Exec(`INSERT INTO sites(site_id, name, base_url, created_at, updated_at) VALUES ('site-1', 'Site', 'https://site.example', ?, ?)`, nowText(), nowText()); err != nil {
		t.Fatal(err)
	}
	candidate := testCandidate("qb-a", "target-a")
	candidate.CapacityState = "CAPACITY_UNCERTAIN"
	decision, err := placement.Decide(placement.DecisionInput{IdentityKey: "identity", TargetMode: placement.TargetInstanceGroup, RequiredBytes: 100, Weights: testWeights(), Candidates: []placement.DecisionCandidate{candidate}})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(context.Background(), RecordInput{
		AuditID:        "audit-no-target",
		SourceID:       "source-1",
		SiteID:         "site-1",
		FeedHash:       "feed-hash",
		OfficialState:  "UNKNOWN",
		OfficialReason: "official marker unavailable",
		TargetMode:     placement.TargetInstanceGroup,
		Decision:       decision,
		AddResultState: "NOT_ATTEMPTED",
		CreatedAt:      nowText(),
		UpdatedAt:      nowText(),
	}); err != nil {
		t.Fatal(err)
	}
	audit, err := recorder.Get(context.Background(), "audit-no-target")
	if err != nil {
		t.Fatal(err)
	}
	if audit.DecisionState != "NO_ELIGIBLE" || audit.SelectedInstanceID != "" || audit.SelectedStorageTargetID != "" || audit.SelectedScore != nil || audit.AddResultState != "NOT_ATTEMPTED" {
		t.Fatalf("audit=%+v", audit)
	}
}

func TestRecordValidatesAddResultState(t *testing.T) {
	database := openDatabase(t)
	if _, err := database.Exec(`INSERT INTO sites(site_id, name, base_url, created_at, updated_at) VALUES ('site-1', 'Site', 'https://site.example', ?, ?)`, nowText(), nowText()); err != nil {
		t.Fatal(err)
	}
	decision, err := placement.Decide(placement.DecisionInput{IdentityKey: "identity", TargetMode: placement.TargetInstanceGroup, RequiredBytes: 100, Weights: testWeights(), Candidates: []placement.DecisionCandidate{testCandidate("qb-a", "target-a")}})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	err = recorder.Record(context.Background(), RecordInput{
		AuditID:        "audit-invalid",
		SourceID:       "source-1",
		SiteID:         "site-1",
		FeedHash:       "feed-hash",
		OfficialState:  "NON_OFFICIAL",
		OfficialReason: "not marked official",
		TargetMode:     placement.TargetInstanceGroup,
		Decision:       decision,
		AddResultState: "UNKNOWN",
		CreatedAt:      nowText(),
		UpdatedAt:      nowText(),
	})
	if err == nil {
		t.Fatal("Record() error = nil, want missing add result code")
	}
}

func TestGetMissingReturnsSentinel(t *testing.T) {
	recorder, err := New(openDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = recorder.Get(context.Background(), "missing")
	if err != ErrNotFound {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

func openDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "test.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func nowText() string {
	return time.Date(2026, 8, 31, 21, 30, 0, 0, time.UTC).Format(time.RFC3339)
}

func testWeights() placement.DecisionWeights {
	return placement.DecisionWeights{InstanceWeight: 10, FreeRatioWeight: 100, InflightPenaltyWeight: 5, CurrentDownloadLoadPenaltyWeight: 2, RecentAddLatencyPenaltyWeight: 1}
}

func testCandidate(instanceID, targetID string) placement.DecisionCandidate {
	return placement.DecisionCandidate{
		InstanceID:             instanceID,
		StorageTargetID:        targetID,
		CanPlace:               true,
		HealthState:            "AVAILABLE",
		ProfileState:           "VERIFIED",
		StorageState:           "VERIFIED",
		CapacityState:          "TRUSTED",
		InstanceWeight:         1,
		FreeRatioPPM:           500_000,
		EffectiveFreeBytes:     1_000,
		MaxInflight:            4,
		RecentAddLatencyMillis: 1,
	}
}
