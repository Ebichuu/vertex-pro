package startuprecover

import (
	"context"
	"testing"
	"time"
)

func TestRecoverReusesClosedWaiterWithoutExtendingDeadlines(t *testing.T) {
	for _, state := range []string{"AWAITING_CONFIRMATION", "UNKNOWN", "ADD_SENT"} {
		t.Run(state, func(t *testing.T) {
			database := openMigrated(t)
			seedChain(t, database, "1", state, "STARTED", true, "CLOSED")
			for _, now := range []time.Time{time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC), time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC)} {
				result, err := Recover(context.Background(), database, Input{Now: now, ConfirmationWindow: 20 * time.Minute, ReconciliationWindow: 2 * time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				if len(result.ResumedChains) != 0 {
					t.Fatalf("dispatched chain became replayable: %+v", result)
				}
				var id, confirmation, reconciliation string
				var count int
				if err := database.QueryRow(`SELECT waiter_id,confirmation_deadline,reconciliation_deadline FROM sync_waiters WHERE attempt_id='attempt-1'`).Scan(&id, &confirmation, &reconciliation); err != nil {
					t.Fatal(err)
				}
				if err := database.QueryRow(`SELECT COUNT(*) FROM sync_waiters WHERE attempt_id='attempt-1'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if id != "waiter-1" || count != 1 || confirmation != "2026-08-29T01:10:00Z" || reconciliation != "2026-08-29T02:00:00Z" {
					t.Fatalf("recovery extended or replaced waiter: %s %d %s %s", id, count, confirmation, reconciliation)
				}
				assertWaiterState(t, database, "waiter-1", "ACTIVE")
			}
		})
	}
}

func TestRecoverMissingWaiterUsesDurableAttemptStart(t *testing.T) {
	database := openMigrated(t)
	seedChain(t, database, "1", "UNKNOWN", "STARTED", false, "")
	for _, day := range []int{29, 30} {
		if _, err := Recover(context.Background(), database, Input{Now: time.Date(2026, 8, day, 3, 0, 0, 0, time.UTC), ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}); err != nil {
			t.Fatal(err)
		}
		var confirmation, reconciliation string
		if err := database.QueryRow(`SELECT confirmation_deadline,reconciliation_deadline FROM sync_waiters WHERE attempt_id='attempt-1'`).Scan(&confirmation, &reconciliation); err != nil {
			t.Fatal(err)
		}
		if confirmation != "2026-08-29T01:10:00Z" || reconciliation != "2026-08-29T02:00:00Z" {
			t.Fatalf("restart-relative deadlines: %s %s", confirmation, reconciliation)
		}
	}
}

func TestRecoverDoesNotReopenTerminalClosedWaiter(t *testing.T) {
	database := openMigrated(t)
	seedChain(t, database, "1", "HASH_SEEN", "STARTED", true, "CLOSED")
	if _, err := Recover(context.Background(), database, Input{Now: time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC), ConfirmationWindow: 10 * time.Minute, ReconciliationWindow: time.Hour}); err != nil {
		t.Fatal(err)
	}
	assertWaiterState(t, database, "waiter-1", "CLOSED")
}
