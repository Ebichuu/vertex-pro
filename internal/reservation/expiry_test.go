package reservation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveAtomicReadsClockAfterWaitingForConnection(t *testing.T) {
	database := openReservationDatabase(t)
	seedReservationFixtures(t, database, "job-1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Hold the sole connection so ReserveAtomic cannot start its write transaction.
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	deadline := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(deadline.Add(-time.Nanosecond).UnixNano())
	request := validRequest("job-1", "attempt-1", "intent-1", "reservation-1")
	request.Clock = func() time.Time { return time.Unix(0, clock.Load()) }
	result := make(chan error, 1)
	waits := database.Stats().WaitCount
	go func() { _, err := ReserveAtomic(ctx, database, request); result <- err }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for database.Stats().WaitCount == waits {
		select {
		case <-ctx.Done():
			t.Fatal("reservation did not wait for the held connection")
		case <-ticker.C:
		}
	}
	clock.Store(deadline.UnixNano())
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrParticipationExpired) {
		t.Fatalf("error=%v", err)
	}
	var attempts int
	if err := database.QueryRow(`SELECT count(*) FROM placement_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestReserveAtomicDeadlineUsesFreshClockAndIncludesEquality(t *testing.T) {
	deadline := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			database := openReservationDatabase(t)
			seedReservationFixtures(t, database, "job-1")
			request := validRequest("job-1", "attempt-1", "intent-1", "reservation-1")
			request.Clock = func() time.Time { return deadline.Add(offset) }
			_, err := ReserveAtomic(context.Background(), database, request)
			var state string
			var count int
			if scanErr := database.QueryRow(`SELECT job_state,(SELECT count(*) FROM placement_attempts) FROM race_jobs WHERE job_id='job-1'`).Scan(&state, &count); scanErr != nil {
				t.Fatal(scanErr)
			}
			if offset < 0 {
				if err != nil || state != "RESERVED" || count != 1 {
					t.Fatalf("state=%s attempts=%d error=%v", state, count, err)
				}
			} else if !errors.Is(err, ErrParticipationExpired) || state != "EXPIRED" || count != 0 {
				t.Fatalf("state=%s attempts=%d error=%v", state, count, err)
			}
		})
	}
}

func TestExpireParticipationQueuedStatesAndIdempotence(t *testing.T) {
	for _, state := range []string{"DISCOVERED", "PREPARING", "READY", "WAITING_CAPACITY"} {
		t.Run(state, func(t *testing.T) {
			database := openReservationDatabase(t)
			seedReservationFixtures(t, database, "job-1")
			if _, err := database.Exec(`UPDATE race_jobs SET job_state=?,participation_deadline='2026-08-25T09:00:00+08:00'`, state); err != nil {
				t.Fatal(err)
			}
			clock := func() time.Time { return time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC) }
			for i := 0; i < 2; i++ {
				if expired, err := ExpireParticipation(context.Background(), database, "job-1", clock); err != nil || !expired {
					t.Fatalf("expired=%v error=%v", expired, err)
				}
			}
			var got, reason string
			var events int
			if err := database.QueryRow(`SELECT job_state,placement_wait_reason,(SELECT count(*) FROM race_events) FROM race_jobs`).Scan(&got, &reason, &events); err != nil {
				t.Fatal(err)
			}
			wantEvents := 1
			if state == "DISCOVERED" {
				wantEvents = 2
			}
			if got != "EXPIRED" || reason != "PARTICIPATION_DEADLINE_EXPIRED" || events != wantEvents {
				t.Fatalf("state=%s reason=%s events=%d", got, reason, events)
			}
		})
	}
}

func TestExpireParticipationNeverReleasesPossibleEffect(t *testing.T) {
	for _, state := range []string{"ADD_SENT", "AWAITING_CONFIRMATION", "UNKNOWN", "EXPIRED_UNKNOWN"} {
		t.Run(state, func(t *testing.T) {
			database := openReservationDatabase(t)
			seedReservationFixtures(t, database, "job-1")
			if _, err := ReserveAtomic(context.Background(), database, validRequest("job-1", "attempt-1", "intent-1", "reservation-1")); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{
				`UPDATE placement_attempts SET attempt_state='` + state + `',dispatch_state='STARTED',side_effect_fact='POSSIBLE_EFFECT'`,
				`UPDATE submit_intents SET dispatch_state='STARTED',dispatch_started_at='2026-08-25T00:01:00Z'`,
				`UPDATE race_jobs SET job_state='` + state + `'`,
			} {
				if _, err := database.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if expired, err := ExpireParticipation(context.Background(), database, "job-1", func() time.Time { return time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) }); err != nil || expired {
				t.Fatalf("expired=%v error=%v", expired, err)
			}
			var got, pointer, reserve string
			if err := database.QueryRow(`SELECT job_state,current_attempt_id,(SELECT reservation_state FROM capacity_reservations) FROM race_jobs`).Scan(&got, &pointer, &reserve); err != nil {
				t.Fatal(err)
			}
			if got != state || pointer != "attempt-1" || reserve != "ACTIVE" {
				t.Fatalf("state=%s pointer=%s reservation=%s", got, pointer, reserve)
			}
		})
	}
}
