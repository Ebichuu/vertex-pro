package reservation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestConcurrentBudget100GBAllowsOnlyOne60GBReservation(t *testing.T) {
	database := openReservationDatabase(t)
	database.SetMaxOpenConns(4)
	seedReservationFixtures(t, database, "job-1")
	seedReservationFixtures(t, database, "job-2")
	if _, err := database.Exec(`UPDATE race_jobs SET identity_key='identity-2' WHERE job_id='job-2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE capacity_epochs SET physical_free_bytes=100000000000,effective_free_bytes=100000000000`); err != nil {
		t.Fatal(err)
	}
	one := validRequest("job-1", "attempt-1", "intent-1", "reservation-1")
	two := validRequest("job-2", "attempt-2", "intent-2", "reservation-2")
	one.RequiredBytes, two.RequiredBytes = 60000000000, 60000000000
	one.MaxPending, two.MaxPending = 32, 32
	one.MaxUnknown, two.MaxUnknown = 2, 2
	two.IdentityKey = "identity-2"
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, request := range []Request{one, two} {
		go func(r Request) { <-start; _, err := ReserveAtomic(context.Background(), database, r); results <- err }(request)
	}
	close(start)
	success, rejected := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if strings.Contains(err.Error(), "INSUFFICIENT_EFFECTIVE_FREE") {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	var free, debt, attempts int64
	if err := database.QueryRow(`SELECT effective_free_bytes,(SELECT SUM(reserved_bytes) FROM capacity_reservations),(SELECT count(*) FROM placement_attempts) FROM capacity_epochs`).Scan(&free, &debt, &attempts); err != nil {
		t.Fatal(err)
	}
	if success != 1 || rejected != 1 || free != 40000000000 || debt != 60000000000 || attempts != 1 {
		t.Fatalf("success=%d rejected=%d free=%d debt=%d attempts=%d", success, rejected, free, debt, attempts)
	}
}

func TestConcurrentSameIdentityAcrossInstancesHasOneOwner(t *testing.T) {
	database := openReservationDatabase(t)
	database.SetMaxOpenConns(4)
	seedReservationFixtures(t, database, "job-1")
	seedReservationFixtures(t, database, "job-2")
	if _, err := database.Exec(`INSERT INTO storage_targets(storage_target_id,instance_id,path,storage_verification_state,created_at,updated_at) SELECT 'target-2','qb-2',path,storage_verification_state,created_at,updated_at FROM storage_targets;
		INSERT INTO capacity_epochs(capacity_epoch_id,storage_target_id,allocation_epoch,capacity_state,physical_free_bytes,effective_free_bytes,snapshot_generation,snapshot_revision,snapshot_digest,created_at) SELECT 'epoch-2','target-2',allocation_epoch,capacity_state,physical_free_bytes,effective_free_bytes,snapshot_generation,snapshot_revision,snapshot_digest,created_at FROM capacity_epochs`); err != nil {
		t.Fatal(err)
	}
	one := validRequest("job-1", "attempt-1", "intent-1", "reservation-1")
	two := validRequest("job-2", "attempt-2", "intent-2", "reservation-2")
	two.InstanceID, two.StorageTargetID, two.CapacityEpochID = "qb-2", "target-2", "epoch-2"
	start, results := make(chan struct{}), make(chan error, 2)
	for _, request := range []Request{one, two} {
		go func(r Request) { <-start; _, err := ReserveAtomic(context.Background(), database, r); results <- err }(request)
	}
	close(start)
	success := 0
	for range 2 {
		if err := <-results; err == nil {
			success++
		} else if !strings.Contains(err.Error(), "UNIQUE constraint failed: placement_attempts.identity_key") {
			t.Fatal(err)
		}
	}
	var attempts, reservations, free int
	if err := database.QueryRow(`SELECT (SELECT count(*) FROM placement_attempts),(SELECT count(*) FROM capacity_reservations),(SELECT sum(effective_free_bytes) FROM capacity_epochs)`).Scan(&attempts, &reservations, &free); err != nil {
		t.Fatal(err)
	}
	if success != 1 || attempts != 1 || reservations != 1 || free != 1700 {
		t.Fatalf("success=%d attempts=%d reservations=%d free=%d", success, attempts, reservations, free)
	}
}

func TestFailedDurableHandoffRollsBackBudgetAndOwnership(t *testing.T) {
	database := openReservationDatabase(t)
	seedReservationFixtures(t, database, "job-1")
	request := validRequest("job-1", "attempt-1", "intent-1", "reservation-1")
	request.Persist = func(ctx context.Context, tx *db.ImmediateTx) error { return errors.New("injected handoff failure") }
	if _, err := ReserveAtomic(context.Background(), database, request); err == nil {
		t.Fatal("handoff failure ignored")
	}
	var free, attempts int64
	if err := database.QueryRow(`SELECT effective_free_bytes,(SELECT count(*) FROM placement_attempts) FROM capacity_epochs`).Scan(&free, &attempts); err != nil {
		t.Fatal(err)
	}
	if free != 900 || attempts != 0 {
		t.Fatalf("free=%d attempts=%d", free, attempts)
	}
	request.Persist = nil
	if _, err := ReserveAtomic(context.Background(), database, request); err != nil {
		t.Fatal(err)
	}
	if _, err := ReserveAtomic(context.Background(), database, request); err == nil {
		t.Fatal("same attempt reserved twice")
	}
	if err := database.QueryRow(`SELECT effective_free_bytes FROM capacity_epochs`).Scan(&free); err != nil || free != 800 {
		t.Fatalf("free=%d err=%v", free, err)
	}
}
