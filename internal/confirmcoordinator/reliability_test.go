package confirmcoordinator

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"github.com/vertex-pro/vertex-pro/internal/startuprecover"
)

func TestConfirmationFailuresRollbackWaiterAndReplay(t *testing.T) {
	for _, fault := range []struct{ name, trigger string }{
		{"binding", `CREATE TRIGGER injected_failure BEFORE INSERT ON capacity_task_bindings BEGIN SELECT RAISE(ABORT,'injected'); END`},
		{"event", `CREATE TRIGGER injected_failure BEFORE INSERT ON race_events WHEN NEW.reason_code='IDENTITY_CONFIRMED' BEGIN SELECT RAISE(ABORT,'injected'); END`},
		{"waiter_close", `CREATE TRIGGER injected_failure BEFORE UPDATE ON sync_waiters WHEN NEW.waiter_state='CLOSED' BEGIN SELECT RAISE(ABORT,'injected'); END`},
	} {
		t.Run(fault.name, func(t *testing.T) {
			database := coordinatorDatabase(t)
			runChainToAwaitingConfirmation(t, database)
			coordinator, _ := New(database)
			now := time.Date(2026, 9, 3, 10, 1, 0, 0, time.UTC)
			if _, err := database.Exec(fault.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 2, true), now); err == nil {
				t.Fatal("injected confirmation failure was ignored")
			}
			assertChain(t, database, "AWAITING_CONFIRMATION", "ACTIVE", "ACTIVE")
			var bindings, confirmations int
			if err := database.QueryRow(`SELECT COUNT(*) FROM capacity_task_bindings`).Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE reason_code='IDENTITY_CONFIRMED'`).Scan(&confirmations); err != nil {
				t.Fatal(err)
			}
			if bindings != 0 || confirmations != 0 {
				t.Fatalf("partial confirmation: bindings=%d events=%d", bindings, confirmations)
			}
			if _, err := database.Exec(`DROP TRIGGER injected_failure`); err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 2, true), now); err != nil {
				t.Fatal(err)
			}
			assertChain(t, database, "HASH_SEEN", "REFLECTING", "CLOSED")
			if _, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 2, true), now); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE reason_code='IDENTITY_CONFIRMED'`).Scan(&confirmations); err != nil || confirmations != 1 {
				t.Fatalf("confirmation replay count=%d err=%v", confirmations, err)
			}
		})
	}
}

func TestOfflineDeadlinesRetainOwnershipAndNeverReplayAdd(t *testing.T) {
	database := coordinatorDatabase(t)
	runChainToAwaitingConfirmation(t, database)
	coordinator, _ := New(database)
	deadline := time.Date(2026, 9, 3, 10, 2, 0, 0, time.UTC)
	for _, step := range []struct {
		now           time.Time
		state, waiter string
	}{
		{deadline.Add(-time.Nanosecond), "AWAITING_CONFIRMATION", "ACTIVE"},
		{deadline, "UNKNOWN", "ACTIVE"},
		{deadline.Add(time.Minute), "UNKNOWN", "ACTIVE"},
		{deadline.Add(3 * time.Minute), "EXPIRED_UNKNOWN", "CLOSED"},
		{deadline.Add(time.Hour), "EXPIRED_UNKNOWN", "CLOSED"},
	} {
		if _, err := coordinator.OnSyncUnavailable(context.Background(), "instance-1", step.now); err != nil {
			t.Fatal(err)
		}
		assertChain(t, database, step.state, "ACTIVE", step.waiter)
	}
	var timeoutEvents, attempts int
	if err := database.QueryRow(`SELECT COUNT(*) FROM race_events WHERE reason_code='CONFIRMATION_DEADLINE_EXPIRED'`).Scan(&timeoutEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM placement_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if timeoutEvents != 1 || attempts != 1 {
		t.Fatalf("timeout events=%d attempts=%d", timeoutEvents, attempts)
	}
	if _, err := dispatchcommit.Begin(context.Background(), database, dispatchcommit.BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: deadline.Add(time.Hour)}); !errors.Is(err, dispatchcommit.ErrDispatchAlreadyStarted) {
		t.Fatalf("Add replay allowed: %v", err)
	}
}

func TestConfirmationTimeoutRollsBackOnEventFailure(t *testing.T) {
	database := coordinatorDatabase(t)
	runChainToAwaitingConfirmation(t, database)
	coordinator, _ := New(database)
	if _, err := database.Exec(`CREATE TRIGGER injected_timeout_failure BEFORE INSERT ON race_events WHEN NEW.reason_code='CONFIRMATION_DEADLINE_EXPIRED' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 10, 2, 0, 0, time.UTC)
	if _, err := coordinator.OnSyncUnavailable(context.Background(), "instance-1", now); err == nil {
		t.Fatal("expected timeout rollback")
	}
	assertChain(t, database, "AWAITING_CONFIRMATION", "ACTIVE", "ACTIVE")
	if _, err := database.Exec(`DROP TRIGGER injected_timeout_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.OnSyncUnavailable(context.Background(), "instance-1", now); err != nil {
		t.Fatal(err)
	}
	assertChain(t, database, "UNKNOWN", "ACTIVE", "ACTIVE")
}

func TestTimeoutCanConfirmFromFreshEvidenceButNotStaleEvidence(t *testing.T) {
	database := coordinatorDatabase(t)
	runChainToAwaitingConfirmation(t, database)
	coordinator, _ := New(database)
	now := time.Date(2026, 9, 3, 10, 2, 0, 0, time.UTC)
	if _, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 1, true), now); err != nil {
		t.Fatal(err)
	}
	assertChain(t, database, "UNKNOWN", "ACTIVE", "ACTIVE")
	if _, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 2, true), now); err != nil {
		t.Fatal(err)
	}
	assertChain(t, database, "RECONCILED_CONFIRMED", "REFLECTING", "CLOSED")
}

func TestLegacyClosedWaiterRecoversAndConfirms(t *testing.T) {
	database := coordinatorDatabase(t)
	runChainToAwaitingConfirmation(t, database)
	if _, err := database.Exec(`UPDATE sync_waiters SET waiter_state='CLOSED',closed_at='2026-09-03T10:00:30Z' WHERE waiter_id='waiter-1'`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 10, 1, 0, 0, time.UTC)
	if _, err := startuprecover.Recover(context.Background(), database, startuprecover.Input{Now: now, ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}); err != nil {
		t.Fatal(err)
	}
	assertChain(t, database, "AWAITING_CONFIRMATION", "ACTIVE", "ACTIVE")
	coordinator, _ := New(database)
	if _, err := coordinator.OnSyncEvent(context.Background(), eventWithIdentity(1, 2, true), now); err != nil {
		t.Fatal(err)
	}
	assertChain(t, database, "HASH_SEEN", "REFLECTING", "CLOSED")
}

func assertChain(t *testing.T, database *sql.DB, wantState, wantReservation, wantWaiter string) {
	t.Helper()
	var attempt, job, reservation, waiter, pointer, effect string
	if err := database.QueryRow(`SELECT a.attempt_state,j.job_state,r.reservation_state,w.waiter_state,j.current_attempt_id,a.side_effect_fact FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id JOIN capacity_reservations r ON r.attempt_id=a.attempt_id JOIN sync_waiters w ON w.attempt_id=a.attempt_id WHERE a.attempt_id='attempt-1'`).Scan(&attempt, &job, &reservation, &waiter, &pointer, &effect); err != nil {
		t.Fatal(err)
	}
	if attempt != wantState || job != wantState || reservation != wantReservation || waiter != wantWaiter || pointer != "attempt-1" {
		t.Fatalf("chain=%s/%s/%s/%s ptr=%s", attempt, job, reservation, waiter, pointer)
	}
	if wantReservation == "ACTIVE" && effect != "POSSIBLE_EFFECT" {
		t.Fatalf("lost possible effect: %s", effect)
	}
}
