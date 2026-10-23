package dispatchcommit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/reservation"
)

func TestBeginExpiresNeverSentReservationAtDeadline(t *testing.T) {
	for _, waiter := range []bool{true, false} {
		t.Run(map[bool]string{true: "registered", false: "crashed_before_waiter"}[waiter], func(t *testing.T) {
			database := openDatabase(t)
			seedReserved(t, database)
			deadline := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
			if waiter {
				_, err := Begin(context.Background(), database, BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: deadline.Add(-time.Second), Clock: func() time.Time { return deadline }})
				if !errors.Is(err, reservation.ErrParticipationExpired) {
					t.Fatalf("error=%v", err)
				}
			} else {
				if _, err := database.Exec(`DELETE FROM sync_waiters`); err != nil {
					t.Fatal(err)
				}
				if expired, err := reservation.ExpireParticipation(context.Background(), database, "job-1", func() time.Time { return deadline }); err != nil || !expired {
					t.Fatalf("expired=%v error=%v", expired, err)
				}
			}
			var job, attempt, dispatch, fact, reserve, reason, intent string
			var pointer, started, activeWaiters, released int
			err := database.QueryRow(`SELECT j.job_state,a.attempt_state,a.dispatch_state,a.side_effect_fact,r.reservation_state,r.release_reason,s.dispatch_state,j.current_attempt_id IS NOT NULL,s.dispatch_started_at IS NOT NULL,(SELECT count(*) FROM sync_waiters WHERE waiter_state='ACTIVE'),r.released_unconsumed_bytes FROM race_jobs j JOIN placement_attempts a ON a.job_id=j.job_id JOIN capacity_reservations r ON r.attempt_id=a.attempt_id JOIN submit_intents s ON s.attempt_id=a.attempt_id`).Scan(&job, &attempt, &dispatch, &fact, &reserve, &reason, &intent, &pointer, &started, &activeWaiters, &released)
			if err != nil {
				t.Fatal(err)
			}
			if job != "EXPIRED" || attempt != "ADD_FAILED" || dispatch != "NOT_STARTED" || fact != "FAILED_NO_EFFECT" || reserve != "RELEASED" || reason != "NO_EFFECT" || intent != "NOT_STARTED" || pointer != 0 || started != 0 || activeWaiters != 0 || released != 100 {
				t.Fatalf("chain=%s/%s/%s/%s/%s/%s/%s pointer=%d started=%d waiter=%d released=%d", job, attempt, dispatch, fact, reserve, reason, intent, pointer, started, activeWaiters, released)
			}
			assertEventCount(t, database, 1)
			if expired, err := reservation.ExpireParticipation(context.Background(), database, "job-1", func() time.Time { return deadline }); err != nil || !expired {
				t.Fatalf("repeat expired=%v error=%v", expired, err)
			}
			assertEventCount(t, database, 1)
		})
	}
}

func TestBeginExpiryRollsBackEntireChainIfAuditWriteFails(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	if _, err := database.Exec(`CREATE TRIGGER fail_expiry BEFORE INSERT ON race_events WHEN NEW.to_state='EXPIRED' BEGIN SELECT RAISE(ABORT,'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := Begin(context.Background(), database, BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)})
	if err == nil || !strings.Contains(err.Error(), "audit failure") {
		t.Fatalf("error=%v", err)
	}
	assertStates(t, database, "RESERVED", "NOT_STARTED", "", true)
	assertEventCount(t, database, 0)
	var reserve, waiter string
	if err := database.QueryRow(`SELECT reservation_state,(SELECT waiter_state FROM sync_waiters) FROM capacity_reservations`).Scan(&reserve, &waiter); err != nil {
		t.Fatal(err)
	}
	if reserve != "ACTIVE" || waiter != "ACTIVE" {
		t.Fatalf("reservation=%s waiter=%s", reserve, waiter)
	}
}

func TestBeginAfterDeadlineDoesNotReleaseAlreadyStartedChain(t *testing.T) {
	database := openDatabase(t)
	seedReserved(t, database)
	deadline := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	beginDispatch(t, database, deadline.Add(-time.Nanosecond))
	_, err := Begin(context.Background(), database, BeginRequest{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", Now: deadline})
	if !errors.Is(err, ErrDispatchAlreadyStarted) {
		t.Fatalf("error=%v", err)
	}
	assertStates(t, database, "ADD_SENT", "STARTED", "POSSIBLE_EFFECT", true)
	var reserve string
	if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations`).Scan(&reserve); err != nil {
		t.Fatal(err)
	}
	if reserve != "ACTIVE" {
		t.Fatalf("reservation=%s", reserve)
	}
}
