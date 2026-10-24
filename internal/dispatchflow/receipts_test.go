package dispatchflow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/qbresponse"
)

func TestReceiptRecoveryRetriesOnlyDatabaseWrites(t *testing.T) {
	for _, mode := range []string{"success", "uncertain", "not-sent"} {
		t.Run(mode, func(t *testing.T) {
			database := flowDatabase(t)
			httpRequest := prepareReservedFlow(t, database)
			if _, err := database.Exec(`CREATE TRIGGER fail_receipt BEFORE UPDATE OF response_fact_at ON submit_intents BEGIN SELECT RAISE(ABORT,'fixture receipt unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			calls := 0
			now := flowFixtureNow()
			runner, err := New(database, senderFunc(func(context.Context, *http.Request) (qbresponse.RawResponse, error) {
				calls++
				if mode == "not-sent" {
					return qbresponse.RawResponse{}, &dispatchworker.NotSentError{Err: io.ErrClosedPipe}
				}
				if mode == "uncertain" {
					return qbresponse.RawResponse{}, io.ErrUnexpectedEOF
				}
				return qbresponse.RawResponse{StatusCode: 200, Body: []byte("Ok.")}, nil
			}), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			recovery := NewReceiptRecovery(database)
			runner.SetReceiptRecovery(recovery)
			request := Request{AttemptID: "attempt-1", SubmitIntentID: "intent-1", WaiterID: "waiter-1", APIVersion: "2.11.2", HTTPRequest: httpRequest}
			if _, err := runner.Run(context.Background(), request); err == nil {
				t.Fatal("write fault was ignored")
			}
			assertAttempt(t, database, "ADD_SENT", "STARTED", "POSSIBLE_EFFECT", true)
			if len(recovery.pending) != 1 {
				t.Fatal("receipt not retained")
			}
			if err := recovery.Retry(context.Background()); err == nil {
				t.Fatal("persistent write fault was ignored")
			}
			if _, err := database.Exec(`DROP TRIGGER fail_receipt`); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			if err := recovery.Retry(context.Background()); err != nil {
				t.Fatal(err)
			}
			want, effect, held := "AWAITING_CONFIRMATION", "POSSIBLE_EFFECT", true
			if mode == "uncertain" {
				want = "UNKNOWN"
			}
			if mode == "not-sent" {
				want, effect, held = "ADD_FAILED", "FAILED_NO_EFFECT", false
			}
			assertAttempt(t, database, want, "STARTED", effect, held)
			var at string
			if err := database.QueryRow(`SELECT response_fact_at FROM submit_intents WHERE submit_intent_id='intent-1'`).Scan(&at); err != nil || at != flowFixtureNow().Format(time.RFC3339Nano) {
				t.Fatalf("receipt time=%s err=%v", at, err)
			}
			if err := recovery.Retry(context.Background()); err != nil {
				t.Fatal(err)
			}
			request.HTTPRequest = addRequest(t, "2.11.2")
			if _, err := runner.Run(context.Background(), request); !errors.Is(err, dispatchcommit.ErrDispatchAlreadyStarted) {
				t.Fatal(err)
			}
			if calls != 1 || len(recovery.pending) != 0 {
				t.Fatalf("network calls=%d pending=%d", calls, len(recovery.pending))
			}
		})
	}
}
