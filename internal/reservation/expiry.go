package reservation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
)

var ErrParticipationExpired = errors.New("PARTICIPATION_DEADLINE_EXPIRED")

// ExpireParticipation closes only work which has never begun a network send.
// The clock is read after acquiring the write lock so lock contention cannot
// extend the participation window. Equality with the deadline is expired.
func ExpireParticipation(ctx context.Context, database *sql.DB, jobID string, now func() time.Time) (bool, error) {
	if database == nil || now == nil {
		return false, errors.New("database and participation clock are required")
	}
	var expired bool
	err := db.RunImmediate(ctx, database, func(ctx context.Context, tx *db.ImmediateTx) error {
		var err error
		expired, err = ExpireParticipationTx(ctx, tx, jobID, now())
		return err
	})
	return expired, err
}

// ExpireParticipationTx is shared by queue admission, reservation and Begin.
// A true result must be committed, not returned as a transaction error.
func ExpireParticipationTx(ctx context.Context, tx *db.ImmediateTx, jobID string, now time.Time) (bool, error) {
	if now.IsZero() {
		return false, errors.New("participation check time is required")
	}
	var state, deadline string
	var attempt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT job_state,participation_deadline,current_attempt_id FROM race_jobs WHERE job_id=?`, jobID).Scan(&state, &deadline, &attempt); err != nil {
		return false, err
	}
	if state == racejob.StateExpired {
		return true, nil
	}
	switch state {
	case racejob.StateDiscovered, racejob.StatePreparing, racejob.StateReady, racejob.StateWaitingCapacity, racejob.StateReserved:
	default:
		// In particular ADD_SENT/AWAITING_CONFIRMATION/UNKNOWN retain their
		// identity ownership, reservation and confirmation/reconciliation path.
		return false, nil
	}
	until, err := time.Parse(time.RFC3339Nano, deadline)
	if err != nil {
		return false, errors.New("PARTICIPATION_DEADLINE_INVALID")
	}
	if now.Before(until) {
		return false, nil
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	if state == racejob.StateReserved {
		if !attempt.Valid {
			return false, errors.New("EXPIRY_ATTEMPT_MISSING")
		}
		var safe int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM placement_attempts a
			JOIN submit_intents s ON s.attempt_id=a.attempt_id
			JOIN capacity_reservations r ON r.attempt_id=a.attempt_id
			WHERE a.attempt_id=? AND a.job_id=? AND a.attempt_state='RESERVED'
			AND a.dispatch_state='NOT_STARTED' AND a.side_effect_fact IS NULL
			AND s.dispatch_state='NOT_STARTED' AND s.dispatch_started_at IS NULL AND s.response_fact_at IS NULL
			AND r.reservation_state='ACTIVE' AND r.reflected_physical_bytes=0
			AND NOT EXISTS(SELECT 1 FROM capacity_task_bindings b WHERE b.reservation_id=r.reservation_id)`, attempt.String, jobID).Scan(&safe); err != nil {
			return false, err
		}
		if safe != 1 {
			return false, errors.New("EXPIRY_NO_EFFECT_NOT_PROVEN")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='ADD_FAILED',side_effect_fact='FAILED_NO_EFFECT',finished_at=? WHERE attempt_id=?`, stamp, attempt.String); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE capacity_reservations SET reservation_state='RELEASED',release_reason='NO_EFFECT',released_unconsumed_bytes=reserved_bytes,released_at=?,revision=revision+1 WHERE attempt_id=?`, stamp, attempt.String); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sync_waiters SET waiter_state='CLOSED',closed_at=? WHERE attempt_id=? AND waiter_state='ACTIVE'`, stamp, attempt.String); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO race_events(job_id,attempt_id,event_type,from_state,to_state,reason_code,safe_metadata_json,occurred_at) VALUES(?,?,'attempt.state.changed','RESERVED','ADD_FAILED','PARTICIPATION_DEADLINE_EXPIRED','{}',?)`, jobID, attempt.String, stamp); err != nil {
			return false, err
		}
	} else if attempt.Valid {
		return false, errors.New("EXPIRY_UNEXPECTED_ATTEMPT")
	}
	// Follow the existing job state machine rather than inventing RESERVED ->
	// EXPIRED or DISCOVERED -> EXPIRED shortcuts.
	steps := []string{racejob.StateExpired}
	if state == racejob.StateDiscovered {
		steps = append([]string{racejob.StatePreparing}, steps...)
	} else if state == racejob.StateReserved {
		steps = append([]string{racejob.StateAddFailed}, steps...)
	}
	for _, next := range steps {
		if _, _, err := racejob.Transition(racejob.Job{ID: jobID, JobState: state}, next, racejob.TransitionInput{Now: now}); err != nil {
			return false, fmt.Errorf("expire job transition: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE race_jobs SET job_state=?,current_attempt_id=NULL,placement_wait_reason='PARTICIPATION_DEADLINE_EXPIRED',updated_at=? WHERE job_id=?`, next, stamp, jobID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO race_events(job_id,event_type,from_state,to_state,reason_code,safe_metadata_json,occurred_at) VALUES(?,'job.state.changed',?,?,'PARTICIPATION_DEADLINE_EXPIRED','{}',?)`, jobID, state, next, stamp); err != nil {
			return false, err
		}
		state = next
	}
	return true, nil
}
