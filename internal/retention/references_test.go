package retention

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedReferencedSnapshots(t *testing.T, database *sql.DB, root string) {
	t.Helper()
	for _, query := range []string{
		`INSERT INTO sync_instance_state VALUES ('qb','READY',1,4,4,'d','2026-10-23T00:00:00Z')`,
		`INSERT INTO sync_snapshots VALUES ('qb',1,1,1,'d','active.json',1,'2026-10-01T00:00:00Z'),('qb',1,2,2,'d','closed.json',1,'2026-10-01T00:00:00Z'),('qb',1,3,3,'d','unused.json',1,'2026-10-01T00:00:00Z'),('qb',1,4,4,'d','latest.json',1,'2026-10-01T00:00:00Z')`,
		`INSERT INTO sync_waiters VALUES ('active','a1','qb',1,1,'h1','2026-10-01T01:00:00Z','2026-10-02T00:00:00Z','ACTIVE','2026-10-01T00:00:00Z',NULL),('closed','a2','qb',1,2,'h2','2026-10-01T01:00:00Z','2026-10-02T00:00:00Z','CLOSED','2026-10-01T00:00:00Z','2026-10-01T01:00:00Z')`,
		`INSERT INTO instance_counter_samples(instance_id,sampled_at,download_bytes,upload_bytes,download_rate,upload_rate) VALUES ('qb','2026-08-01T00:00:00Z',1,1,1,1)`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"active.json", "closed.json", "unused.json", "latest.json"} {
		writeFile(t, root, name)
	}
}

func TestSweepKeepsActiveAndClosedWaiterEvidence(t *testing.T) {
	database, root := openDatabase(t), t.TempDir()
	seedReferencedSnapshots(t, database, root)
	result, err := Sweep(context.Background(), database, root, DefaultPolicy(), time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshots != 1 || result.ClosedWaiters != 0 || result.StatsRaw != 1 {
		t.Fatalf("result=%+v", result)
	}
	for _, name := range []string{"active.json", "closed.json", "latest.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "unused.json")); !os.IsNotExist(err) {
		t.Fatalf("unused file: %v", err)
	}
}

func TestSnapshotFailureDoesNotPreventStatsCleanup(t *testing.T) {
	database, root := openDatabase(t), t.TempDir()
	seedReferencedSnapshots(t, database, root)
	if _, err := database.Exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON sync_snapshots BEGIN SELECT RAISE(ABORT,'injected snapshot failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := Sweep(context.Background(), database, root, DefaultPolicy(), time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	if err == nil || result.StatsRaw != 1 || result.Snapshots != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "unused.json")); err != nil {
		t.Fatalf("failed transaction deleted file: %v", err)
	}
}

func TestSweepReleasesOnlyProvenTerminalWaiterReferences(t *testing.T) {
	database, root := openDatabase(t), t.TempDir()
	seedReferencedSnapshots(t, database, root)
	for _, query := range []string{
		`INSERT INTO storage_targets(storage_target_id,instance_id,path,storage_verification_state,created_at,updated_at) VALUES ('s','qb','/downloads','VERIFIED','2026-10-01','2026-10-01')`,
		`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES ('j','source','h2','EXPIRED','2026-10-01','2026-10-01','2026-10-01','2026-10-01')`,
		`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,reserved_bytes,started_at) VALUES ('a2','j','h2',1,'qb','s','ADD_FAILED','NOT_STARTED','FAILED_NO_EFFECT',1,100,'2026-10-01')`,
		`INSERT INTO capacity_reservations(reservation_id,storage_target_id,attempt_id,reserved_bytes,reflected_physical_bytes,allocation_epoch,reservation_state,release_reason,released_unconsumed_bytes,released_at) VALUES ('r','s','a2',100,0,1,'RELEASED','NO_EFFECT',100,'2026-10-01')`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Sweep(context.Background(), database, root, DefaultPolicy(), time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.ClosedWaiters != 1 || result.Snapshots != 2 {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "closed.json")); !os.IsNotExist(err) {
		t.Fatalf("terminal baseline file: %v", err)
	}
}

func seedCleanupChain(t *testing.T, database *sql.DB, state, reservationState string) {
	t.Helper()
	sideEffect := "POSSIBLE_EFFECT"
	if state == "HASH_SEEN" {
		sideEffect = "OBSERVED_EFFECT"
	}
	if state == "ADD_FAILED" {
		sideEffect = "FAILED_NO_EFFECT"
	}
	for _, query := range []string{
		`INSERT INTO storage_targets(storage_target_id,instance_id,path,storage_verification_state,created_at,updated_at) VALUES ('s','qb','/downloads','VERIFIED','2026-10-01','2026-10-01')`,
		`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES ('j','source','h2','READY','2026-10-01','2026-10-01','2026-10-01','2026-10-01')`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,reserved_bytes,started_at,qb_torrent_id,confirmation_sync_generation,confirmation_snapshot_revision,confirmation_evidence_digest) VALUES ('a2','j','h2',1,'qb','s',?,'STARTED',?,1,100,'2026-10-01','torrent-2',1,4,'confirmed')`, state, sideEffect); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO submit_intents(submit_intent_id,attempt_id,instance_id,identity_key,request_fingerprint,target_fingerprint,dispatch_state,pre_add_sync_generation,pre_add_snapshot_revision,pre_add_snapshot_digest,pre_add_identity_fact,created_at,dispatch_started_at) VALUES ('i2','a2','qb','h2','req','target','STARTED',1,2,'d','ABSENT','2026-10-01','2026-10-01')`); err != nil {
		t.Fatal(err)
	}
	if state != "ADD_FAILED" {
		if _, err := database.Exec(`UPDATE race_jobs SET job_state=?,current_attempt_id='a2' WHERE job_id='j'`, state); err != nil {
			t.Fatal(err)
		}
	}
	switch reservationState {
	case "RELEASED":
		if _, err := database.Exec(`INSERT INTO capacity_reservations(reservation_id,storage_target_id,attempt_id,reserved_bytes,reflected_physical_bytes,allocation_epoch,reservation_state,release_reason,released_unconsumed_bytes,released_at) VALUES ('r','s','a2',100,0,1,'RELEASED','NO_EFFECT',100,'2026-10-01')`); err != nil {
			t.Fatal(err)
		}
	case "ACTIVE", "REFLECTING":
		if _, err := database.Exec(`INSERT INTO capacity_reservations(reservation_id,storage_target_id,attempt_id,reserved_bytes,allocation_epoch,reservation_state) VALUES ('r','s','a2',100,1,?)`, reservationState); err != nil {
			t.Fatal(err)
		}
	}
}

func seedUnmanagedBinding(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO capacity_task_bindings(binding_id,storage_target_id,qb_torrent_id,identity_key,owner_kind,amount_left_state,amount_left,snapshot_generation,snapshot_revision,snapshot_digest,updated_at) VALUES ('b','s','torrent-2','h2','UNMANAGED','KNOWN',0,1,4,'d','2026-10-01')`); err != nil {
		t.Fatal(err)
	}
}

func TestSweepRetainsUnresolvedDebtAndExistingBindingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, state, reservation       string
		binding, recent, missingWaiter bool
	}{
		{"unknown", "UNKNOWN", "RELEASED", false, false, false},
		{"expired_unknown", "EXPIRED_UNKNOWN", "RELEASED", false, false, false},
		{"awaiting", "AWAITING_CONFIRMATION", "ACTIVE", false, false, false},
		{"debt", "HASH_SEEN", "REFLECTING", false, false, false},
		{"unmanaged_binding", "HASH_SEEN", "RELEASED", true, false, false},
		{"missing_reservation", "ADD_FAILED", "", false, false, false},
		{"recently_closed", "ADD_FAILED", "RELEASED", false, true, false},
		{"missing_waiter_unknown", "UNKNOWN", "ACTIVE", false, false, true},
		{"missing_waiter_expired", "EXPIRED_UNKNOWN", "ACTIVE", false, false, true},
		{"missing_waiter_debt", "HASH_SEEN", "REFLECTING", false, false, true},
		{"missing_waiter_binding", "HASH_SEEN", "RELEASED", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, root := openDatabase(t), t.TempDir()
			seedReferencedSnapshots(t, database, root)
			seedCleanupChain(t, database, tc.state, tc.reservation)
			if tc.binding {
				seedUnmanagedBinding(t, database)
			}
			if tc.recent {
				if _, err := database.Exec(`UPDATE sync_waiters SET closed_at='2026-10-22T00:00:00Z' WHERE waiter_id='closed'`); err != nil {
					t.Fatal(err)
				}
			}
			if tc.missingWaiter {
				if _, err := database.Exec(`DELETE FROM sync_waiters WHERE waiter_id='closed'`); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Sweep(context.Background(), database, root, DefaultPolicy(), time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
			if err != nil || result.ClosedWaiters != 0 || result.Snapshots != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if _, err := os.Stat(filepath.Join(root, "closed.json")); err != nil {
				t.Fatalf("required evidence lost: %v", err)
			}
		})
	}
}

func TestTerminalWaiterCleanupFailureStillPrunesStats(t *testing.T) {
	database, root := openDatabase(t), t.TempDir()
	seedReferencedSnapshots(t, database, root)
	seedCleanupChain(t, database, "ADD_FAILED", "RELEASED")
	if _, err := database.Exec(`CREATE TRIGGER fail_waiter_delete BEFORE DELETE ON sync_waiters BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := Sweep(context.Background(), database, root, DefaultPolicy(), time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	if err == nil || result.ClosedWaiters != 0 || result.StatsRaw != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSnapshotLaterBatchFailureCleansOnlyCommittedFiles(t *testing.T) {
	database, root := openDatabase(t), t.TempDir()
	seedReferencedSnapshots(t, database, root)
	if _, err := database.Exec(`INSERT INTO sync_snapshots VALUES ('qb',1,0,0,'d','failed.json',1,'2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "failed.json")
	if _, err := database.Exec(`CREATE TRIGGER fail_later_batch BEFORE DELETE ON sync_snapshots WHEN OLD.snapshot_revision=0 BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	policy := DefaultPolicy()
	policy.BatchSize = 1
	result, err := Sweep(context.Background(), database, root, policy, time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	if err == nil || result.Snapshots != 1 || result.StatsRaw != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "unused.json")); !os.IsNotExist(err) {
		t.Fatalf("committed batch orphan: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "failed.json")); err != nil {
		t.Fatalf("rolled back batch file deleted: %v", err)
	}
}
