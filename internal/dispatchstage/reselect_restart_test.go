package dispatchstage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
)

func TestReselectRecoveryRejectsAmbiguousHistoricalFailure(t *testing.T) {
	f, a, b, now := reselectFixture(t)
	runSelection(t, a)
	*now = now.Add(31 * time.Second)
	runSelection(t, b)
	// Normal cancellation proves FAILED_NO_EFFECT. A damaged historical fact
	// must not allow the successor to resume while the old Add may have worked.
	if _, err := f.database.Exec(`UPDATE placement_attempts SET side_effect_fact='POSSIBLE_EFFECT' WHERE attempt_id='attempt-job-1'`); err != nil {
		t.Fatal(err)
	}
	result, err := startuprecover.Recover(context.Background(), f.database, startuprecover.Input{Now: *now, ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour})
	if !errors.Is(err, startuprecover.ErrRecoveryFailed) || !strings.Contains(strings.Join(result.Violations, ","), "FAILED_CHAIN_FACT_MISMATCH:attempt-job-1") || len(result.ResumedChains) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// Exercise the production replacement and recovery entry points across a real
// database close/open, rather than merely reconstructing the coordinator.
func TestReselectRecoversAfterDatabaseReopen(t *testing.T) {
	for _, state := range []string{"RESERVED", "ADD_SENT", "HASH_SEEN"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			f, a, b, now := reselectFixture(t)
			sender := &deadlineSender{}
			b.config.Sender = sender
			runSelection(t, a)
			*now = now.Add(31 * time.Second)
			runSelection(t, b)
			const attempt = "attempt-job-1-2"
			if state == "ADD_SENT" {
				_, err := dispatchcommit.Begin(ctx, f.database, dispatchcommit.BeginRequest{AttemptID: attempt, SubmitIntentID: "intent-job-1-2", WaiterID: "waiter-attempt-job-1-2", Now: *now, Recheck: func(ctx context.Context, tx *db.ImmediateTx) error {
					_, err := tx.ExecContext(ctx, `DELETE FROM dispatch_queue WHERE attempt_id=?`, attempt)
					return err
				}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if state == "HASH_SEEN" {
				if worked, err := b.SendNext(ctx); !worked || err != nil {
					t.Fatalf("send=%v err=%v", worked, err)
				}
				_, err := firstconfirm.Confirm(ctx, f.database, firstconfirm.Request{BindingID: "binding-second", AttemptID: attempt, SubmitIntentID: "intent-job-1-2", ReservationID: "reservation-job-1-2", JobID: "job-1", StorageTargetID: "target-qb-2", IdentityKey: f.hash, QBTorrentID: f.hash, SyncGeneration: 1, SnapshotRevision: 2, SnapshotDigest: "qb-2-confirmed", PreAddSyncGeneration: 1, PreAddSnapshotRevision: 1, PreAddSnapshotDigest: "qb-2-digest-1", EvidenceDigest: "confirmed-second", Now: *now})
				if err != nil {
					t.Fatal(err)
				}
			}
			var seq int
			var name, filename string
			if err := f.database.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &filename); err != nil {
				t.Fatal(err)
			}
			if err := f.database.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := db.Open(ctx, db.Config{Path: filepath.Clean(filename)})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := db.CheckStartupConsistency(ctx, reopened); err != nil {
				t.Fatal(err)
			}
			input := startuprecover.Input{Now: *now, ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}
			for restart := 0; restart < 2; restart++ {
				result, err := startuprecover.Recover(ctx, reopened, input)
				if err != nil {
					t.Fatalf("restart=%d violations=%v err=%v", restart, result.Violations, err)
				}
				for _, id := range append(result.ResumedChains, result.HeldChains...) {
					if id != attempt {
						t.Fatalf("historical chain revived: %+v", result)
					}
				}
			}
			var owner, jobState, oldState, oldReservation, oldWaiter string
			if err := reopened.QueryRow(`SELECT current_attempt_id,job_state FROM race_jobs WHERE job_id='job-1'`).Scan(&owner, &jobState); err != nil {
				t.Fatal(err)
			}
			wantState := state
			if state == "ADD_SENT" {
				wantState = "UNKNOWN"
			}
			if owner != attempt || jobState != wantState {
				t.Fatalf("owner=%s state=%s want=%s", owner, jobState, wantState)
			}
			if err := reopened.QueryRow(`SELECT a.attempt_state,r.reservation_state,w.waiter_state FROM placement_attempts a JOIN capacity_reservations r ON r.attempt_id=a.attempt_id JOIN sync_waiters w ON w.attempt_id=a.attempt_id WHERE a.attempt_id='attempt-job-1'`).Scan(&oldState, &oldReservation, &oldWaiter); err != nil {
				t.Fatal(err)
			}
			if oldState != "ADD_FAILED" || oldReservation != "RELEASED" || oldWaiter != "CLOSED" {
				t.Fatalf("old chain=%s/%s/%s", oldState, oldReservation, oldWaiter)
			}
			oldStage, err := New(reopened, a.config)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := oldStage.SendNext(ctx); worked || err != nil {
				t.Fatalf("old send=%v %v", worked, err)
			}
			restored, err := New(reopened, b.config)
			if err != nil {
				t.Fatal(err)
			}
			if worked, err := restored.SendNext(ctx); err != nil || worked != (state == "RESERVED") {
				t.Fatalf("new send=%v %v", worked, err)
			}
			wantSends := 1
			if state == "ADD_SENT" {
				wantSends = 0
			}
			if sender.calls != wantSends {
				t.Fatalf("sends=%d want=%d", sender.calls, wantSends)
			}
			if worked, err := restored.SendNext(ctx); worked || err != nil {
				t.Fatalf("duplicate=%v %v", worked, err)
			}
		})
	}
}
