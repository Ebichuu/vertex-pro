package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/confirmcoordinator"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/metrics"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/syncwatcher"
)

type unavailableSync struct{}

func (unavailableSync) Fetch(context.Context, uint64) (syncmerge.Delta, error) {
	return syncmerge.Delta{}, errors.New("fixture offline")
}

func TestOfflineSyncTickAdvancesDeadlinesWithoutReleasingDelivery(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "offline.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO storage_targets(storage_target_id,instance_id,path,storage_verification_state,created_at,updated_at) VALUES ('target','qb','/managed','VERIFIED','2026-10-23','2026-10-23')`,
		`INSERT INTO sync_instance_state VALUES ('qb','READY',1,1,1,'d','2026-10-23T00:00:00Z')`,
		`INSERT INTO sync_snapshots VALUES ('qb',1,1,1,'d','missing-baseline.json',1,'2026-10-23T00:00:00Z')`,
		`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES ('j','s','hash','READY','2026-10-23T01:00:00Z','2026-10-23T02:00:00Z','2026-10-23T00:00:00Z','2026-10-23T00:00:00Z')`,
		`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,reserved_bytes,started_at) VALUES ('a','j','hash',1,'qb','target','AWAITING_CONFIRMATION','STARTED','POSSIBLE_EFFECT',1,100,'2026-10-23T00:00:00Z')`,
		`INSERT INTO capacity_reservations(reservation_id,attempt_id,storage_target_id,reserved_bytes,allocation_epoch,reservation_state) VALUES ('r','a','target',100,1,'ACTIVE')`,
		`INSERT INTO submit_intents(submit_intent_id,attempt_id,instance_id,identity_key,request_fingerprint,target_fingerprint,dispatch_state,pre_add_sync_generation,pre_add_snapshot_revision,pre_add_snapshot_digest,pre_add_identity_fact,created_at,dispatch_started_at) VALUES ('i','a','qb','hash','f','f','STARTED',1,1,'d','ABSENT','2026-10-23T00:00:00Z','2026-10-23T00:00:00Z')`,
		`UPDATE race_jobs SET job_state='AWAITING_CONFIRMATION',current_attempt_id='a' WHERE job_id='j'`,
		`INSERT INTO sync_waiters VALUES ('w','a','qb',1,1,'hash','2026-10-23T00:02:00Z','2026-10-23T00:05:00Z','ACTIVE','2026-10-23T00:00:00Z',NULL)`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	store, err := snapshot.New(database, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := syncwatcher.New("qb", unavailableSync{}, store)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := confirmcoordinator.New(database)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Database: database, instanceID: "qb", watcher: watcher, confirmations: coordinator, Metrics: metrics.New()}
	for _, step := range []struct {
		minute int
		want   string
	}{{2, "UNKNOWN"}, {5, "EXPIRED_UNKNOWN"}, {6, "EXPIRED_UNKNOWN"}} {
		now := func() time.Time { return time.Date(2026, 10, 23, 0, step.minute, 0, 0, time.UTC) }
		if err := runtime.tickSync(ctx, now); err == nil {
			t.Fatal("offline error disappeared")
		}
		var job, attempt, reservation, dispatch string
		if err := database.QueryRow(`SELECT j.job_state,a.attempt_state,r.reservation_state,i.dispatch_state FROM race_jobs j JOIN placement_attempts a ON j.current_attempt_id=a.attempt_id JOIN capacity_reservations r ON r.attempt_id=a.attempt_id JOIN submit_intents i ON i.attempt_id=a.attempt_id`).Scan(&job, &attempt, &reservation, &dispatch); err != nil {
			t.Fatal(err)
		}
		if job != step.want || attempt != step.want || reservation != "ACTIVE" || dispatch != "STARTED" {
			t.Fatalf("state=%s/%s/%s/%s", job, attempt, reservation, dispatch)
		}
	}
}
