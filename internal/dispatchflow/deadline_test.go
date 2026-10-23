package dispatchflow

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
)

func TestRunnerNeverSendsWhenBeginCrossesParticipationDeadline(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	deadline := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clockCalls, sends := 0, 0
	runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		sends++
		return qbresponse.RawResponse{}, nil
	}), func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return deadline.Add(-time.Nanosecond)
		}
		return deadline
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.15.1", HTTPRequest: request})
	if !errors.Is(err, reservation.ErrParticipationExpired) || sends != 0 {
		t.Fatalf("error=%v sends=%d", err, sends)
	}
	var job, reserve, waiter string
	if err := database.QueryRow(`SELECT job_state,(SELECT reservation_state FROM capacity_reservations),(SELECT waiter_state FROM sync_waiters) FROM race_jobs`).Scan(&job, &reserve, &waiter); err != nil {
		t.Fatal(err)
	}
	if job != "EXPIRED" || reserve != "RELEASED" || waiter != "CLOSED" {
		t.Fatalf("job=%s reservation=%s waiter=%s", job, reserve, waiter)
	}
}

func TestRunnerKeepsUnknownWhenNetworkCrossesParticipationDeadline(t *testing.T) {
	database := flowDatabase(t)
	request := prepareReservedFlow(t, database)
	deadline := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	now := deadline.Add(-time.Nanosecond)
	sends := 0
	runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
		sends++
		now = deadline
		return qbresponse.RawResponse{}, context.DeadlineExceeded
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_, _ = runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.15.1", HTTPRequest: request})
	assertAttempt(t, database, "UNKNOWN", "STARTED", "POSSIBLE_EFFECT", true)
	if expired, err := reservation.ExpireParticipation(context.Background(), database, "job-1", func() time.Time { return now }); err != nil || expired {
		t.Fatalf("expired=%v error=%v", expired, err)
	}
	if _, err := runner.Run(context.Background(), Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.15.1", HTTPRequest: request}); err == nil {
		t.Fatal("unknown attempt accepted for resend")
	}
	var reserve string
	if err := database.QueryRow(`SELECT reservation_state FROM capacity_reservations`).Scan(&reserve); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || reserve != "ACTIVE" {
		t.Fatalf("sends=%d reservation=%s", sends, reserve)
	}
}
