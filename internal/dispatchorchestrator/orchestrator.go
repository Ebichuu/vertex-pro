// Package dispatchorchestrator composes the production Add chain: placement
// decision, capacity reservation, sync waiter registration, and exactly-one
// dispatch. It is fail closed: when the placement gate rejects (for example
// because the qB profile is not VERIFIED) nothing is reserved and nothing is
// sent. Confirmation and reconciliation stay in firstconfirm and
// unknownreconciler, which operate on the same chain the orchestrator created.
package dispatchorchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/dispatchflow"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/placement"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
	"github.com/vertex-pro/vertex-pro/internal/syncwaiter"
)

// Request carries everything one Add needs. The caller has already built the
// Add request from the detected profile and the torrent artifact; the
// orchestrator never inspects artifact bytes.
type Request struct {
	// Placement is the candidate set for this Add. The orchestrator enforces
	// the profile/capacity gates by requiring exactly one selected candidate.
	Placement placement.DecisionInput
	// Reservation is the chain template; identity and snapshot facts must be
	// filled in by the caller from the pre-add read-only review.
	Reservation reservation.Request
	// Waiter registers the confirmation window for this attempt.
	Waiter syncwaiter.Request
	// Dispatch is the dispatchflow request minus the database.
	Dispatch dispatchflow.Request
	// Sender is the production qui proxy sender.
	Sender dispatchworker.Sender
	// Prepare creates the chain prerequisites the upstream stages own: the
	// race job for the matched RSS item, storage/capacity reference rows, and
	// the pre-add snapshot state. Production composes this from the race job
	// creator and the capacity snapshotter; tests seed exact rows.
	Prepare func(context.Context, *sql.DB) error
	// Now supplies decision times; defaults to UTC wall clock.
	Now func() time.Time
}

// Result reports the states the orchestrator left behind.
type Result struct {
	ReservationID       string
	AttemptID           string
	SubmitIntentID      string
	WaiterID            string
	PlacementSelected   bool
	ReservationState    string
	AttemptAfterReserve string
	Dispatch            dispatchflow.Result
}

// Run executes placement -> reservation -> waiter registration -> dispatch.
// The durable dispatch marker, single-send guarantee, and response fact commit
// are owned by dispatchflow; the orchestrator only forwards its outcome.
func Run(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if database == nil {
		return Result{}, fmt.Errorf("database is required")
	}
	now := request.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if request.Prepare != nil {
		if err := request.Prepare(ctx, database); err != nil {
			return Result{}, fmt.Errorf("prepare chain prerequisites: %w", err)
		}
	}

	decision, err := placement.Decide(request.Placement)
	if err != nil {
		return Result{}, fmt.Errorf("placement decision: %w", err)
	}
	if decision.Selected == nil {
		rejection := "PLACEMENT_NOT_SELECTED"
		if len(decision.Rejected) == 1 {
			rejection = decision.Rejected[0].Reason
		}
		return Result{}, &RejectionError{Reason: rejection}
	}

	request.Reservation.Clock = now
	reserved, err := reservation.ReserveAtomic(ctx, database, request.Reservation)
	if err != nil {
		return Result{}, fmt.Errorf("reserve capacity: %w", err)
	}
	request.Waiter.AttemptID = reserved.AttemptID
	if _, err := syncwaiter.Register(ctx, database, request.Waiter); err != nil {
		return Result{}, fmt.Errorf("register sync waiter: %w", err)
	}
	runner, err := dispatchflow.New(database, request.Sender, now)
	if err != nil {
		return Result{}, fmt.Errorf("initialize dispatch flow: %w", err)
	}
	outcome, err := runner.Run(ctx, request.Dispatch)
	if err != nil {
		return Result{}, fmt.Errorf("dispatch: %w", err)
	}
	return Result{
		ReservationID:       request.Reservation.ReservationID,
		AttemptID:           reserved.AttemptID,
		SubmitIntentID:      reserved.SubmitIntentID,
		WaiterID:            request.Waiter.WaiterID,
		PlacementSelected:   true,
		ReservationState:    "ACTIVE",
		AttemptAfterReserve: "RESERVED",
		Dispatch:            outcome,
	}, nil
}

// RejectionError reports a fail-closed placement gate. Callers can inspect the
// machine-readable reason (for example PROFILE_NOT_VERIFIED) without string
// matching.
type RejectionError struct {
	Reason string
}

func (err *RejectionError) Error() string {
	return "dispatch orchestrator rejected the add: " + err.Reason
}
