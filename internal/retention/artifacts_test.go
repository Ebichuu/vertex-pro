package retention

import (
	"context"
	"testing"
	"time"
)

func TestArtifactRetentionReconciledNoEffectAfterRetryWindow(t *testing.T) {
	for _, tc := range []struct {
		name, deadline, reservation string
		wantPruned                  int64
	}{
		{"settled", "2026-10-01", "RELEASED", 1},
		{"retry_allowed", "2026-10-25", "RELEASED", 0},
		{"diagnostic_window", "2026-10-23", "RELEASED", 0},
		{"active_debt", "2026-10-01", "ACTIVE", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := openDatabase(t)
			seedCleanupChain(t, database, "ADD_FAILED", tc.reservation)
			if _, err := database.Exec(`UPDATE placement_attempts SET attempt_state='RECONCILED_NO_EFFECT' WHERE attempt_id='a2'`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE race_jobs SET job_state='RECONCILED_NO_EFFECT',participation_deadline=?,observation_deadline=? WHERE job_id='j'`, tc.deadline, tc.deadline); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO job_torrent_artifacts VALUES('j',?,?,'2026-10-01')`, []byte("torrent"), "0000000000000000000000000000000000000000000000000000000000000000"); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
			result, err := Sweep(context.Background(), database, "", DefaultPolicy(), now)
			if err != nil || result.TorrentArtifacts != tc.wantPruned {
				t.Fatalf("pruned=%d want=%d err=%v", result.TorrentArtifacts, tc.wantPruned, err)
			}
			var remaining int64
			if err := database.QueryRow(`SELECT count(*) FROM job_torrent_artifacts WHERE job_id='j'`).Scan(&remaining); err != nil || remaining != 1-tc.wantPruned {
				t.Fatalf("remaining=%d err=%v", remaining, err)
			}
		})
	}
}

func TestArtifactRetentionKeepsUnresolvedJobs(t *testing.T) {
	database := openDatabase(t)
	states := []string{"DISCOVERED", "READY", "WAITING_CAPACITY", "RESERVED", "ADD_SENT", "UNKNOWN", "EXPIRED_UNKNOWN", "ADD_FAILED", "EXPIRED", "ALREADY_PRESENT"}
	for _, state := range states {
		initial := state
		pointer := state == "RESERVED" || state == "ADD_SENT" || state == "UNKNOWN" || state == "EXPIRED_UNKNOWN"
		if pointer {
			initial = "DISCOVERED"
		}
		if _, err := database.Exec(`INSERT INTO race_jobs(job_id,source_id,identity_key,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES(?,?,?,?,'2026-10-01','2026-10-01','2026-10-01','2026-10-01')`, state, state, state, initial); err != nil {
			t.Fatal(err)
		}
		if pointer {
			dispatch := "STARTED"
			var side any = "POSSIBLE_EFFECT"
			if state == "RESERVED" {
				dispatch = "NOT_STARTED"
				side = nil
			}
			if _, err := database.Exec(`INSERT INTO placement_attempts(attempt_id,job_id,identity_key,attempt_no,instance_id,storage_target_id,attempt_state,dispatch_state,side_effect_fact,allocation_epoch,reserved_bytes,started_at) VALUES(?,?,?,1,'qb','storage',?,?,?,1,1,'2026-10-01')`, state, state, state, state, dispatch, side); err != nil {
				t.Fatal(err)
			}
			var started any
			if dispatch == "STARTED" {
				started = "2026-10-01"
			}
			if _, err := database.Exec(`INSERT INTO submit_intents(submit_intent_id,attempt_id,instance_id,identity_key,request_fingerprint,target_fingerprint,dispatch_state,pre_add_sync_generation,pre_add_snapshot_revision,pre_add_snapshot_digest,pre_add_identity_fact,created_at,dispatch_started_at) VALUES(?,?,'qb',?,'fp','fp',?,1,1,'digest','ABSENT','2026-10-01',?)`, state, state, state, dispatch, started); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE race_jobs SET job_state=?,current_attempt_id=? WHERE job_id=?`, state, state, state); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := database.Exec(`INSERT INTO job_torrent_artifacts VALUES(?,?,?,'2026-10-01')`, state, []byte("torrent"), "0000000000000000000000000000000000000000000000000000000000000000"); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	result, err := Sweep(context.Background(), database, "", DefaultPolicy(), now)
	if err != nil || result.TorrentArtifacts != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, state := range states {
		var count int
		if err := database.QueryRow(`SELECT count(*) FROM job_torrent_artifacts WHERE job_id=?`, state).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 1
		if state == "EXPIRED" || state == "ALREADY_PRESENT" {
			want = 0
		}
		if count != want {
			t.Fatalf("state %s count %d", state, count)
		}
	}
}
