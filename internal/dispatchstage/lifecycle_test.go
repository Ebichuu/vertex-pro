package dispatchstage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

func TestCompletedBindingRemovedOnlyAfterDeletionAndCurrentAbsence(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "capacity.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	runner, _ := db.DefaultRunner()
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO storage_targets(storage_target_id,instance_id,path,storage_verification_state,created_at,updated_at) VALUES('target','2','/managed','VERIFIED','now','now')`,
		`INSERT INTO race_jobs(job_id,source_id,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES('job','site/key','DISCOVERED','later','later','now','now')`,
		`INSERT INTO job_observations(job_id,instance_id,qb_torrent_id,observed_at,snapshot_generation,snapshot_revision,snapshot_digest,missing_count,deleted_at) VALUES('job','2','hash','now',2,3,'digest',2,'now')`,
		`INSERT INTO capacity_task_bindings(binding_id,storage_target_id,qb_torrent_id,owner_kind,amount_left_state,amount_left,uncertainty_debit_kind,uncertainty_debit_bytes,snapshot_generation,snapshot_revision,snapshot_digest,updated_at) VALUES('binding','target','hash','UNMANAGED','KNOWN',0,'NONE',0,1,9,'old','now')`,
	} {
		if _, err := database.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	stage := Stage{database: database, config: Config{Storage: StorageConfig{InstanceID: "2", StorageTargetID: "target"}}}
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	left := int64(0)
	evidence := Evidence{Record: snapshot.Record{InstanceID: "2", Generation: 2, Revision: 3, Digest: "digest", Complete: true}, Snapshot: syncmerge.Snapshot{Torrents: map[string]syncmerge.Torrent{"hash": {ID: "hash", AmountLeftState: "KNOWN", AmountLeft: &left}}}}
	reconcile := func() {
		t.Helper()
		if err := db.RunImmediate(ctx, database, func(ctx context.Context, tx *db.ImmediateTx) error {
			return stage.reconcileCompleted(ctx, tx, evidence, now)
		}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	reconcile()
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_task_bindings`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("present torrent binding count=%d err=%v", count, err)
	}
	evidence.Snapshot.Torrents = map[string]syncmerge.Torrent{}
	reconcile()
	if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_task_bindings`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted torrent binding count=%d err=%v", count, err)
	}
}
