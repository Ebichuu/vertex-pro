package dispatchcommit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/confirmcoordinator"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/retention"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
)

func earlyObservation(now time.Time) firstconfirm.Request {
	return firstconfirm.Request{BindingID: "binding-1", AttemptID: "attempt-1", SubmitIntentID: "intent-1", ReservationID: "reservation-1", JobID: "job-1", StorageTargetID: "target-1", IdentityKey: "identity-1", QBTorrentID: "identity-1", SyncGeneration: 3, SnapshotRevision: 8, SnapshotDigest: "after", PreAddSyncGeneration: 3, PreAddSnapshotRevision: 7, PreAddSnapshotDigest: "baseline", EvidenceDigest: "seen-after", Now: now}
}
func acceptedResponse(now time.Time) Request {
	return Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: now, Result: dispatchworker.Result{AttemptID: "attempt-1", DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT", Response: qbresponse.Result{APIVersion: "2.11.2", ResponseFormat: "legacy_text", SuccessCount: 1, DispatchState: "STARTED", SideEffectFact: "POSSIBLE_EFFECT"}}}
}

func TestEarlyHashSurvivesLateResponseAndDuplicateEvents(t *testing.T) {
	for _, kind := range []string{"accepted", "transport-loss", "not-sent"} {
		t.Run(kind, func(t *testing.T) {
			database := openDatabase(t)
			seedReserved(t, database)
			now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
			beginDispatch(t, database, now)
			observation := earlyObservation(now.Add(time.Second))
			for range 2 {
				got, err := firstconfirm.Confirm(context.Background(), database, observation)
				if err != nil || !got.Pending {
					t.Fatalf("early=%+v %v", got, err)
				}
			}
			assertStates(t, database, "ADD_SENT", "STARTED", "POSSIBLE_EFFECT", true)
			request := acceptedResponse(now.Add(20 * time.Second))
			want := "HASH_SEEN"
			switch kind {
			case "transport-loss":
				request.DispatchErr = errors.New("response lost")
				want = "RECONCILED_CONFIRMED"
			case "not-sent":
				request.Result.DispatchState = "NOT_STARTED"
				request.Result.SideEffectFact = "FAILED_NO_EFFECT"
			}
			if _, err := Record(context.Background(), database, request); err != nil {
				t.Fatal(err)
			}
			assertStates(t, database, want, "STARTED", "OBSERVED_EFFECT", true)
			if got, err := Record(context.Background(), database, request); err != nil || !got.Idempotent || got.State != want {
				t.Fatalf("late duplicate=%+v %v", got, err)
			}
			var pending, bindings, open int
			if err := database.QueryRow(`SELECT (SELECT count(*) FROM pending_confirmations),(SELECT count(*) FROM capacity_task_bindings),(SELECT count(*) FROM sync_waiters WHERE waiter_state='ACTIVE')`).Scan(&pending, &bindings, &open); err != nil {
				t.Fatal(err)
			}
			if pending != 0 || bindings != 1 || open != 0 {
				t.Fatalf("pending=%d bindings=%d open=%d", pending, bindings, open)
			}
		})
	}
}

func TestEarlyEvidencePersistsAcrossConfirmationWriteFailure(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	beginDispatch(t, database, now)
	if _, err := firstconfirm.Confirm(context.Background(), database, earlyObservation(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TRIGGER fail_confirmation BEFORE INSERT ON capacity_task_bindings BEGIN SELECT RAISE(ABORT,'injected confirm failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(context.Background(), database, acceptedResponse(now.Add(2*time.Second))); err == nil {
		t.Fatal("failure not injected")
	}
	assertStates(t, database, "AWAITING_CONFIRMATION", "STARTED", "POSSIBLE_EFFECT", true)
	if _, err := database.Exec(`DROP TRIGGER fail_confirmation`); err != nil {
		t.Fatal(err)
	}
	if err := firstconfirm.Replay(context.Background(), database, "attempt-1"); err != nil {
		t.Fatal(err)
	}
	assertStates(t, database, "HASH_SEEN", "STARTED", "OBSERVED_EFFECT", true)
}

func TestEarlyEvidenceSurvivesRetentionAndOfflineRestart(t *testing.T) {
	ctx := context.Background()
	database := openDatabase(t)
	seedReserved(t, database)
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	beginDispatch(t, database, now)
	if _, err := firstconfirm.Confirm(ctx, database, earlyObservation(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	// The positive snapshot is neither the pre-Add baseline nor the latest
	// revision. Retention must keep it while the HTTP response is outstanding.
	if _, err := database.Exec(`INSERT INTO sync_snapshots(instance_id,sync_generation,snapshot_revision,rid,snapshot_digest,snapshot_path,is_complete,created_at) VALUES
		('qb-1',3,8,8,'after','observed.json',1,?),('qb-1',3,9,9,'latest','latest.json',1,?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := retention.Sweep(ctx, database, t.TempDir(), retention.DefaultPolicy(), now.Add(9*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := database.QueryRow(`SELECT count(*) FROM sync_snapshots WHERE snapshot_revision=8`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("pending positive snapshot retained=%d err=%v", retained, err)
	}
	var seq int
	var name, path string
	if err := database.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Open(ctx, db.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := startuprecover.Recover(ctx, reopened, startuprecover.Input{Now: now.Add(time.Minute), ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}); err != nil {
		t.Fatal(err)
	}
	assertStates(t, reopened, "UNKNOWN", "STARTED", "POSSIBLE_EFFECT", true)
	coordinator, err := confirmcoordinator.New(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.OnSyncUnavailable(ctx, "qb-1", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertStates(t, reopened, "RECONCILED_CONFIRMED", "STARTED", "OBSERVED_EFFECT", true)
	if _, err := Record(ctx, reopened, acceptedResponse(now.Add(3*time.Minute))); err != nil {
		t.Fatal(err)
	}
	assertStates(t, reopened, "RECONCILED_CONFIRMED", "STARTED", "OBSERVED_EFFECT", true)
}
