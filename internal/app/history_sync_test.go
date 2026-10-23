package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/syncwatcher"
)

func TestObservationSurvivesGenerationResetAndRequiresTwoMissingSnapshots(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, _ := db.DefaultRunner()
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO sync_instance_state(instance_id,sync_state,sync_generation,rid,snapshot_revision,snapshot_digest,updated_at) VALUES('2','READY',2,2,2,'d3','2026-10-23T00:00:00Z')`,
		`INSERT INTO sync_snapshots(instance_id,sync_generation,snapshot_revision,rid,snapshot_digest,snapshot_path,is_complete,created_at) VALUES('2',1,8,8,'d1','x',1,'2026-10-23T00:00:00Z'),('2',2,1,1,'d2','y',1,'2026-10-23T00:00:00Z'),('2',2,2,2,'d3','z',1,'2026-10-23T00:00:00Z')`,
		`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES('j','s/key','hash','DISCOVERED','2026-10-24T00:00:00Z','2026-10-24T00:00:00Z','2026-10-23T00:00:00Z','2026-10-23T00:00:00Z')`,
		`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,qb_torrent_id,confirmation_sync_generation,confirmation_snapshot_revision,confirmation_evidence_digest,reserved_bytes,started_at) VALUES('a','j','hash',1,'2','target','HASH_SEEN','STARTED','OBSERVED_EFFECT',1,'hash',1,8,'d1',4096,'2026-10-23T00:00:00Z')`,
		`INSERT INTO submit_intents(submit_intent_id,attempt_id,instance_id,identity_key,request_fingerprint,target_fingerprint,dispatch_state,pre_add_sync_generation,pre_add_snapshot_revision,pre_add_snapshot_digest,pre_add_identity_fact,created_at,dispatch_started_at) VALUES('i','a','2','hash','fp','target','STARTED',1,8,'d1','ABSENT','2026-10-23T00:00:00Z','2026-10-23T00:00:00Z')`,
		`UPDATE race_jobs SET job_state='HASH_SEEN',current_attempt_id='a' WHERE job_id='j'`,
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &Runtime{Database: database}
	left := int64(4096)
	first := syncwatcher.Event{InstanceID: "2", Generation: 1, Revision: 8, Snapshot: syncmerge.Snapshot{Torrents: map[string]syncmerge.Torrent{"hash": {ID: "hash", AmountLeftState: "KNOWN", AmountLeft: &left, Fields: map[string]any{"name": "Download", "state": "downloading", "downloaded": int64(128)}}}}}
	if err := runtime.observeJobs(ctx, first, time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := database.QueryRow(`SELECT state FROM job_observations WHERE job_id='j'`).Scan(&state); err != nil || state != "DOWNLOADING" {
		t.Fatalf("download state=%q err=%v", state, err)
	}
	missing := syncwatcher.Event{InstanceID: "2", Generation: 2, Revision: 1, Snapshot: syncmerge.Snapshot{Torrents: map[string]syncmerge.Torrent{}}}
	for _, event := range []syncwatcher.Event{missing, missing} {
		if err := runtime.observeJobs(ctx, event, time.Date(2026, 10, 23, 0, 1, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var deleted string
	if err := database.QueryRow(`SELECT missing_count,deleted_at FROM job_observations WHERE job_id='j'`).Scan(&count, &deleted); err != nil || count != 1 || deleted != "" {
		t.Fatalf("first absence count=%d deleted=%q err=%v", count, deleted, err)
	}
	missing.Revision = 2
	if err := runtime.observeJobs(ctx, missing, time.Date(2026, 10, 23, 0, 2, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var generation, revision uint64
	if err := database.QueryRow(`SELECT missing_count,deleted_at,snapshot_generation,snapshot_revision FROM job_observations WHERE job_id='j'`).Scan(&count, &deleted, &generation, &revision); err != nil || count != 2 || deleted == "" || generation != 2 || revision != 2 {
		t.Fatalf("second absence count=%d deleted=%q coord=%d/%d err=%v", count, deleted, generation, revision, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM job_observation_events WHERE job_id='j' AND state='DELETED'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("deleted events=%d err=%v", count, err)
	}
}

func TestObservedTorrentStateRequiresCompletionEvidence(t *testing.T) {
	if observedTorrentState("downloading", false) != "DOWNLOADING" || observedTorrentState("uploading", false) != "" || observedTorrentState("uploading", true) != "SEEDING" {
		t.Fatal("torrent state inferred without enough evidence")
	}
}
