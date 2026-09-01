package syncwaiter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

const (
	AttemptReserved             = "RESERVED"
	AttemptAddSent              = "ADD_SENT"
	AttemptAwaitingConfirmation = "AWAITING_CONFIRMATION"
	AttemptUnknown              = "UNKNOWN"
	WaiterActive                = "ACTIVE"
	WaiterClosed                = "CLOSED"
)

type Request struct {
	WaiterID               string
	AttemptID              string
	InstanceID             string
	RegisteredGeneration   int64
	SourceSnapshotRevision int64
	IdentityKey            string
	ConfirmationDeadline   string
	ReconciliationDeadline string
	CreatedAt              string
}

type Waiter struct {
	ID                     string
	AttemptID              string
	InstanceID             string
	Generation             int64
	SourceRevision         int64
	IdentityKey            string
	ConfirmationDeadline   string
	ReconciliationDeadline string
	State                  string
}

type Notification struct {
	Generation          int64
	SnapshotRevision    int64
	ObservedIdentityKey string
}

type NotifyResult struct {
	Waiter          Waiter
	Ignored         bool
	IdentityMatched bool
}

func Register(ctx context.Context, database *sql.DB, request Request) (Waiter, error) {
	if err := validateRequest(request); err != nil {
		return Waiter{}, err
	}
	var result Waiter
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var instanceID, identityKey, attemptState, dispatchState string
		err := transaction.QueryRowContext(ctx, `SELECT instance_id, identity_key, attempt_state, dispatch_state FROM placement_attempts WHERE attempt_id = ?`, request.AttemptID).Scan(&instanceID, &identityKey, &attemptState, &dispatchState)
		if err == sql.ErrNoRows {
			return errors.New("ATTEMPT_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read waiter attempt: %w", err)
		}
		if instanceID != request.InstanceID || identityKey != request.IdentityKey {
			return errors.New("WAITER_ATTEMPT_FACT_MISMATCH")
		}
		// UNKNOWN 链仍在等待新鲜快照对账，崩溃恢复时同样允许重新注册。
		if attemptState != AttemptReserved && attemptState != AttemptAddSent && attemptState != AttemptAwaitingConfirmation && attemptState != AttemptUnknown {
			return errors.New("ATTEMPT_NOT_WAITABLE")
		}
		if attemptState == AttemptReserved && dispatchState != "NOT_STARTED" {
			return errors.New("RESERVED_ATTEMPT_DISPATCH_FACT_INVALID")
		}
		if _, err := transaction.ExecContext(ctx, `SELECT 1 FROM sync_snapshots WHERE instance_id = ? AND sync_generation = ? AND snapshot_revision = ? AND is_complete = 1`, request.InstanceID, request.RegisteredGeneration, request.SourceSnapshotRevision); err != nil {
			return fmt.Errorf("check waiter source snapshot: %w", err)
		}
		var complete int
		if err := transaction.QueryRowContext(ctx, `SELECT is_complete FROM sync_snapshots WHERE instance_id = ? AND sync_generation = ? AND snapshot_revision = ?`, request.InstanceID, request.RegisteredGeneration, request.SourceSnapshotRevision).Scan(&complete); err == sql.ErrNoRows {
			return errors.New("WAITER_SOURCE_SNAPSHOT_NOT_FOUND")
		} else if err != nil {
			return fmt.Errorf("read waiter source snapshot: %w", err)
		} else if complete != 1 {
			return errors.New("WAITER_SOURCE_SNAPSHOT_INCOMPLETE")
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO sync_waiters(waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.WaiterID, request.AttemptID, request.InstanceID, request.RegisteredGeneration, request.SourceSnapshotRevision, request.IdentityKey, request.ConfirmationDeadline, request.ReconciliationDeadline, request.CreatedAt); err != nil {
			return fmt.Errorf("insert sync waiter: %w", err)
		}
		result = Waiter{ID: request.WaiterID, AttemptID: request.AttemptID, InstanceID: request.InstanceID, Generation: request.RegisteredGeneration, SourceRevision: request.SourceSnapshotRevision, IdentityKey: request.IdentityKey, ConfirmationDeadline: request.ConfirmationDeadline, ReconciliationDeadline: request.ReconciliationDeadline, State: WaiterActive}
		return nil
	})
	if err != nil {
		return Waiter{}, err
	}
	return result, nil
}

func Notify(ctx context.Context, database *sql.DB, waiterID string, notification Notification, closedAt string) (NotifyResult, error) {
	if strings.TrimSpace(waiterID) == "" || strings.TrimSpace(closedAt) == "" {
		return NotifyResult{}, errors.New("waiter ID and closed_at are required")
	}
	if notification.Generation < 0 || notification.SnapshotRevision < 0 {
		return NotifyResult{}, errors.New("notification revision is invalid")
	}
	var result NotifyResult
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var waiter Waiter
		var state string
		err := transaction.QueryRowContext(ctx, `SELECT waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state FROM sync_waiters WHERE waiter_id = ?`, waiterID).Scan(&waiter.ID, &waiter.AttemptID, &waiter.InstanceID, &waiter.Generation, &waiter.SourceRevision, &waiter.IdentityKey, &waiter.ConfirmationDeadline, &waiter.ReconciliationDeadline, &state)
		if err == sql.ErrNoRows {
			return errors.New("WAITER_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read sync waiter: %w", err)
		}
		waiter.State = state
		result.Waiter = waiter
		if state == WaiterClosed {
			result.Ignored = true
			return nil
		}
		if notification.Generation < waiter.Generation || (notification.Generation == waiter.Generation && notification.SnapshotRevision <= waiter.SourceRevision) {
			result.Ignored = true
			return nil
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE sync_waiters SET waiter_state = 'CLOSED', closed_at = ? WHERE waiter_id = ? AND waiter_state = 'ACTIVE'`, closedAt, waiterID); err != nil {
			return fmt.Errorf("close notified waiter: %w", err)
		}
		result.Waiter.State = WaiterClosed
		result.IdentityMatched = strings.TrimSpace(notification.ObservedIdentityKey) == waiter.IdentityKey
		return nil
	})
	if err != nil {
		return NotifyResult{}, err
	}
	return result, nil
}

func Close(ctx context.Context, database *sql.DB, waiterID, closedAt string) (bool, error) {
	if strings.TrimSpace(waiterID) == "" || strings.TrimSpace(closedAt) == "" {
		return false, errors.New("waiter ID and closed_at are required")
	}
	var changed bool
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		result, err := transaction.ExecContext(ctx, `UPDATE sync_waiters SET waiter_state = 'CLOSED', closed_at = ? WHERE waiter_id = ? AND waiter_state = 'ACTIVE'`, closedAt, waiterID)
		if err != nil {
			return fmt.Errorf("close sync waiter: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read waiter close result: %w", err)
		}
		changed = count == 1
		if !changed {
			var exists int
			if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_waiters WHERE waiter_id = ?`, waiterID).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return errors.New("WAITER_NOT_FOUND")
			}
		}
		return nil
	})
	return changed, err
}

func RestoreActive(ctx context.Context, database *sql.DB, instanceID string) ([]Waiter, error) {
	if strings.TrimSpace(instanceID) == "" {
		return nil, errors.New("instance ID is required")
	}
	rows, err := database.QueryContext(ctx, `SELECT waiter_id, attempt_id, instance_id, registered_generation, source_snapshot_revision, identity_key, confirmation_deadline, reconciliation_deadline, waiter_state FROM sync_waiters WHERE instance_id = ? AND waiter_state = 'ACTIVE' ORDER BY waiter_id`, instanceID)
	if err != nil {
		return nil, fmt.Errorf("load active waiters: %w", err)
	}
	defer rows.Close()
	result := make([]Waiter, 0)
	for rows.Next() {
		var waiter Waiter
		if err := rows.Scan(&waiter.ID, &waiter.AttemptID, &waiter.InstanceID, &waiter.Generation, &waiter.SourceRevision, &waiter.IdentityKey, &waiter.ConfirmationDeadline, &waiter.ReconciliationDeadline, &waiter.State); err != nil {
			return nil, fmt.Errorf("scan active waiter: %w", err)
		}
		result = append(result, waiter)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active waiters: %w", err)
	}
	return result, nil
}

func validateRequest(request Request) error {
	values := map[string]string{"waiter_id": request.WaiterID, "attempt_id": request.AttemptID, "instance_id": request.InstanceID, "identity_key": request.IdentityKey, "confirmation_deadline": request.ConfirmationDeadline, "reconciliation_deadline": request.ReconciliationDeadline, "created_at": request.CreatedAt}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.RegisteredGeneration < 0 || request.SourceSnapshotRevision < 0 {
		return errors.New("waiter revision is invalid")
	}
	confirmation, err := time.Parse(time.RFC3339, request.ConfirmationDeadline)
	if err != nil {
		return errors.New("confirmation deadline is invalid")
	}
	reconciliation, err := time.Parse(time.RFC3339, request.ReconciliationDeadline)
	if err != nil {
		return errors.New("reconciliation deadline is invalid")
	}
	if reconciliation.Before(confirmation) {
		return errors.New("reconciliation deadline precedes confirmation deadline")
	}
	return nil
}
