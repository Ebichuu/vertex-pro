package dispatchstage

import (
	"context"
	"errors"
	"github.com/vertex-pro/vertex-pro/internal/dispatchcommit"
	"testing"
	"time"
)

func reselectFixture(t *testing.T) (selectionFixture, *Stage, *Stage, *time.Time) {
	t.Helper()
	f, a, _ := durableFixture(t)
	allocateQueue(t, a)
	now := f.now
	e := f.evidence("qb-2", 1, 1<<30)
	b, _ := f.stage(t, "qb-2", e, e)
	b.config.DurableQueue = true
	b.config.Collect = func(context.Context) (Evidence, func(), error) { e.CollectedAt = now; return e, nil, nil }
	b.config.CollectForSelection = b.config.Collect
	a.config.Now = func() time.Time { return now }
	b.config.Now = a.config.Now
	a.config.CollectForSelection = func(context.Context) (Evidence, func(), error) { return Evidence{}, nil, errors.New("offline") }
	a.config.Collect = a.config.CollectForSelection
	Connect(a, b)
	return f, a, b, &now
}

func runSelection(t *testing.T, s *Stage) {
	t.Helper()
	_, failures, err := s.Run(context.Background())
	if err != nil || len(failures) > 0 {
		t.Fatalf("failures=%v err=%v", failures, err)
	}
}

func TestReselectSustainedUnavailabilitySurvivesCoordinatorRestart(t *testing.T) {
	f, a, b, now := reselectFixture(t)
	runSelection(t, a)
	*now = now.Add(29 * time.Second)
	runSelection(t, b)
	var id string
	f.database.QueryRow(`SELECT current_attempt_id FROM race_jobs WHERE job_id='job-1'`).Scan(&id)
	if id != "attempt-job-1" {
		t.Fatal(id)
	}
	Connect(a, b) // No in-memory unavailable clock is required after restart.
	*now = now.Add(time.Second)
	runSelection(t, b)
	f.database.QueryRow(`SELECT current_attempt_id FROM race_jobs WHERE job_id='job-1'`).Scan(&id)
	if id != "attempt-job-1-2" {
		t.Fatal(id)
	}
	var active, released, closed int
	f.database.QueryRow(`SELECT count(*) FROM capacity_reservations WHERE reservation_state='ACTIVE'`).Scan(&active)
	f.database.QueryRow(`SELECT count(*) FROM capacity_reservations WHERE reservation_state='RELEASED'`).Scan(&released)
	f.database.QueryRow(`SELECT count(*) FROM sync_waiters WHERE waiter_state='CLOSED'`).Scan(&closed)
	if active != 1 || released != 1 || closed != 1 {
		t.Fatalf("active=%d released=%d closed=%d", active, released, closed)
	}
	if worked, err := a.SendNext(context.Background()); worked || err != nil {
		t.Fatalf("old sender=%v %v", worked, err)
	}
	if worked, err := b.SendNext(context.Background()); !worked || err != nil {
		t.Fatalf("new sender=%v %v", worked, err)
	}
}

func TestReselectBriefRecoveryResetsClockAndCapacityFailureRetainsOwner(t *testing.T) {
	f, a, b, now := reselectFixture(t)
	runSelection(t, a)
	offline := a.config.CollectForSelection
	a.config.CollectForSelection = func(context.Context) (Evidence, func(), error) {
		e := f.evidence("qb-1", 1, 1<<30)
		e.CollectedAt = *now
		return e, nil, nil
	}
	*now = now.Add(29 * time.Second)
	runSelection(t, a)
	a.config.CollectForSelection = offline
	runSelection(t, a)
	*now = now.Add(29 * time.Second)
	runSelection(t, a)
	var count int
	f.database.QueryRow(`SELECT count(*) FROM placement_attempts`).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	*now = now.Add(time.Second)
	if _, err := f.database.Exec(`CREATE TRIGGER reject_new_waiter BEFORE INSERT ON sync_waiters WHEN NEW.attempt_id='attempt-job-1-2' BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, failures, err := b.Run(context.Background())
	if err != nil || len(failures) != 1 {
		t.Fatalf("failures=%v err=%v", failures, err)
	}
	var id, state string
	f.database.QueryRow(`SELECT current_attempt_id,job_state FROM race_jobs WHERE job_id='job-1'`).Scan(&id, &state)
	f.database.QueryRow(`SELECT count(*) FROM placement_attempts`).Scan(&count)
	if id != "attempt-job-1" || state != "RESERVED" || count != 1 {
		t.Fatalf("id=%s state=%s count=%d", id, state, count)
	}
}

func TestReselectNeverTakesStartedUnknownOrObservedAttempt(t *testing.T) {
	for _, kind := range []string{"STARTED", "UNKNOWN", "OBSERVED", "DISABLED"} {
		t.Run(kind, func(t *testing.T) {
			f, a, b, now := reselectFixture(t)
			runSelection(t, a)
			*now = now.Add(31 * time.Second)
			switch kind {
			case "STARTED", "UNKNOWN":
				if _, err := dispatchcommit.Begin(context.Background(), f.database, dispatchcommit.BeginRequest{AttemptID: "attempt-job-1", SubmitIntentID: "intent-job-1", WaiterID: "waiter-attempt-job-1", Now: *now}); err != nil {
					t.Fatal(err)
				}
				if kind == "UNKNOWN" {
					if _, err := f.database.Exec(`UPDATE placement_attempts SET attempt_state='UNKNOWN' WHERE attempt_id='attempt-job-1'; UPDATE race_jobs SET job_state='UNKNOWN' WHERE job_id='job-1'`); err != nil {
						t.Fatal(err)
					}
				}
			case "OBSERVED":
				if _, err := f.database.Exec(`INSERT INTO pending_confirmations(attempt_id,evidence_json) VALUES('attempt-job-1','{}')`); err != nil {
					t.Fatal(err)
				}
			case "DISABLED":
				if _, err := f.database.Exec(`UPDATE rules SET enabled=0`); err != nil {
					t.Fatal(err)
				}
			}
			_, _, _ = b.Run(context.Background())
			var count int
			if err := f.database.QueryRow(`SELECT count(*) FROM placement_attempts`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("count=%d err=%v", count, err)
			}
		})
	}
}

func TestReselectLimitsMovesAndArbitratesConcurrentBegin(t *testing.T) {
	t.Run("at most three attempts", func(t *testing.T) {
		f, a, b, now := reselectFixture(t)
		runSelection(t, a)
		*now = now.Add(31 * time.Second)
		runSelection(t, b)
		onlineB := b.config.CollectForSelection
		b.config.CollectForSelection = a.config.CollectForSelection
		b.config.Collect = b.config.CollectForSelection
		a.config.CollectForSelection = func(context.Context) (Evidence, func(), error) {
			e := f.evidence("qb-1", 1, 1<<30)
			e.CollectedAt = *now
			return e, nil, nil
		}
		a.config.Collect = a.config.CollectForSelection
		runSelection(t, b)
		*now = now.Add(31 * time.Second)
		runSelection(t, a)
		a.config.CollectForSelection = b.config.CollectForSelection
		a.config.Collect = a.config.CollectForSelection
		b.config.CollectForSelection = onlineB
		b.config.Collect = onlineB
		runSelection(t, a)
		*now = now.Add(time.Minute)
		runSelection(t, b)
		var count int
		var owner string
		f.database.QueryRow(`SELECT count(*) FROM placement_attempts`).Scan(&count)
		f.database.QueryRow(`SELECT current_attempt_id FROM race_jobs WHERE job_id='job-1'`).Scan(&owner)
		if count != 3 || owner != "attempt-job-1-3" {
			t.Fatalf("count=%d owner=%s", count, owner)
		}
	})
	t.Run("Begin competes with replacement", func(t *testing.T) {
		f, a, b, now := reselectFixture(t)
		runSelection(t, a)
		*now = now.Add(31 * time.Second)
		began := make(chan error, 1)
		go func() {
			_, err := dispatchcommit.Begin(context.Background(), f.database, dispatchcommit.BeginRequest{AttemptID: "attempt-job-1", SubmitIntentID: "intent-job-1", WaiterID: "waiter-attempt-job-1", Now: *now})
			began <- err
		}()
		_, _, _ = b.Run(context.Background())
		beginErr := <-began
		var count int
		var owner, state string
		f.database.QueryRow(`SELECT count(*) FROM placement_attempts`).Scan(&count)
		f.database.QueryRow(`SELECT current_attempt_id,job_state FROM race_jobs WHERE job_id='job-1'`).Scan(&owner, &state)
		if beginErr == nil {
			if count != 1 || owner != "attempt-job-1" || state != "ADD_SENT" {
				t.Fatalf("duplicate after Begin: %d %s %s", count, owner, state)
			}
		} else if count != 2 || owner != "attempt-job-1-2" || !errors.Is(beginErr, dispatchcommit.ErrDispatchAlreadyStarted) {
			t.Fatalf("replacement=%d %s %v", count, owner, beginErr)
		}
	})
}
