package dispatchcommit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/addfacts"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchworker"
	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
	"github.com/vertex-pro/vertex-pro/internal/reservation"
)

const (
	AttemptAddSent              = "ADD_SENT"
	AttemptAwaitingConfirmation = "AWAITING_CONFIRMATION"
	AttemptAddFailed            = "ADD_FAILED"
	AttemptUnknown              = "UNKNOWN"
)

var ErrDispatchAlreadyStarted = errors.New("DISPATCH_ALREADY_STARTED")

type BeginRequest struct {
	AttemptID      string
	SubmitIntentID string
	WaiterID       string
	Now            time.Time
	Clock          func() time.Time
	Recheck        func(context.Context, *db.ImmediateTx) error
}

type BeginResult struct {
	AttemptID string
	State     string
}

type Request struct {
	AttemptID      string
	SubmitIntentID string
	WaiterID       string
	Result         dispatchworker.Result
	DispatchErr    error
	Now            time.Time
}

type Result struct {
	AttemptID  string
	State      string
	Idempotent bool
}

// Begin atomically persists the irreversible dispatch marker before any
// network sender is invoked. Once this commits, the Attempt is ADD_SENT with a
// POSSIBLE_EFFECT fact. A caller must never send the same Attempt again, even
// if the process crashes before it receives a response.
func Begin(ctx context.Context, database *sql.DB, request BeginRequest) (BeginResult, error) {
	if database == nil {
		return BeginResult{}, errors.New("database is required")
	}
	if err := validateIdentifiers(request.AttemptID, request.SubmitIntentID, request.WaiterID, request.Now); err != nil {
		return BeginResult{}, err
	}
	result := BeginResult{AttemptID: request.AttemptID, State: AttemptAddSent}
	var expired bool
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var attemptState, attemptDispatch, sideEffect, jobID, jobState string
		var currentAttempt sql.NullString
		err := transaction.QueryRowContext(ctx, `SELECT a.attempt_state, a.dispatch_state, COALESCE(a.side_effect_fact,''), a.job_id, j.job_state, j.current_attempt_id FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id WHERE a.attempt_id=?`, request.AttemptID).Scan(&attemptState, &attemptDispatch, &sideEffect, &jobID, &jobState, &currentAttempt)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("ATTEMPT_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read dispatch attempt: %w", err)
		}
		if attemptDispatch == addfacts.DispatchStarted || attemptState != "RESERVED" {
			return ErrDispatchAlreadyStarted
		}
		if attemptDispatch != addfacts.DispatchNotStarted || sideEffect != "" || jobState != "RESERVED" || !currentAttempt.Valid || currentAttempt.String != request.AttemptID {
			return errors.New("DISPATCH_CHAIN_FACT_MISMATCH")
		}

		var intentDispatch string
		err = transaction.QueryRowContext(ctx, `SELECT dispatch_state FROM submit_intents WHERE submit_intent_id=? AND attempt_id=?`, request.SubmitIntentID, request.AttemptID).Scan(&intentDispatch)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("SUBMIT_INTENT_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read submit intent: %w", err)
		}
		if intentDispatch != addfacts.DispatchNotStarted {
			return ErrDispatchAlreadyStarted
		}

		var waiterState string
		err = transaction.QueryRowContext(ctx, `SELECT waiter_state FROM sync_waiters WHERE waiter_id=? AND attempt_id=?`, request.WaiterID, request.AttemptID).Scan(&waiterState)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("SYNC_WAITER_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read sync waiter: %w", err)
		}
		if waiterState != "ACTIVE" {
			return errors.New("SYNC_WAITER_NOT_ACTIVE")
		}
		if request.Clock != nil {
			request.Now = request.Clock()
		}
		expired, err = reservation.ExpireParticipationTx(ctx, transaction, jobID, request.Now)
		if err != nil || expired {
			return err
		}
		if request.Recheck != nil {
			if err := request.Recheck(ctx, transaction); err != nil {
				return err
			}
		}

		timestamp := request.Now.UTC().Format(time.RFC3339Nano)
		if changed, err := execOne(ctx, transaction, `UPDATE submit_intents SET dispatch_state='STARTED', dispatch_started_at=? WHERE submit_intent_id=? AND attempt_id=? AND dispatch_state='NOT_STARTED'`, timestamp, request.SubmitIntentID, request.AttemptID); err != nil {
			return fmt.Errorf("start submit intent: %w", err)
		} else if !changed {
			return errors.New("SUBMIT_INTENT_START_CONFLICT")
		}
		if changed, err := execOne(ctx, transaction, `UPDATE placement_attempts SET attempt_state='ADD_SENT', dispatch_state='STARTED', side_effect_fact='POSSIBLE_EFFECT' WHERE attempt_id=? AND attempt_state='RESERVED' AND dispatch_state='NOT_STARTED' AND side_effect_fact IS NULL`, request.AttemptID); err != nil {
			return fmt.Errorf("start dispatch attempt: %w", err)
		} else if !changed {
			return errors.New("ATTEMPT_DISPATCH_START_CONFLICT")
		}
		if changed, err := execOne(ctx, transaction, `UPDATE race_jobs SET job_state='ADD_SENT', updated_at=? WHERE job_id=? AND job_state='RESERVED' AND current_attempt_id=?`, timestamp, jobID, request.AttemptID); err != nil {
			return fmt.Errorf("start dispatch job: %w", err)
		} else if !changed {
			return errors.New("JOB_DISPATCH_START_CONFLICT")
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', 'RESERVED', 'ADD_SENT', 'DISPATCH_STARTED', '{}', ?)`, jobID, request.AttemptID, timestamp); err != nil {
			return fmt.Errorf("insert dispatch-start event: %w", err)
		}
		return nil
	})
	if err != nil {
		return BeginResult{}, err
	}
	if expired {
		return BeginResult{}, reservation.ErrParticipationExpired
	}
	return result, nil
}

// Record atomically persists the response fact from one already-started
// network dispatch. It never retries network I/O. A known-not-sent result
// closes the Attempt as ADD_FAILED; all results for which a side effect remains
// possible advance to AWAITING_CONFIRMATION or UNKNOWN and converge through
// Sync. The dispatch_state is monotonic and never returns to NOT_STARTED.
func Record(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if database == nil {
		return Result{}, errors.New("database is required")
	}
	if err := validateIdentifiers(request.AttemptID, request.SubmitIntentID, request.WaiterID, request.Now); err != nil {
		return Result{}, err
	}
	if request.Result.AttemptID != request.AttemptID {
		return Result{}, errors.New("dispatch result attempt mismatch")
	}

	target, sideEffect, err := targetFor(request)
	if err != nil {
		return Result{}, err
	}
	result := Result{AttemptID: request.AttemptID, State: target}
	err = db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var attemptState, attemptDispatch, currentSideEffect, jobID, jobState string
		var currentAttempt sql.NullString
		err := transaction.QueryRowContext(ctx, `SELECT a.attempt_state, a.dispatch_state, COALESCE(a.side_effect_fact,''), a.job_id, j.job_state, j.current_attempt_id FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id WHERE a.attempt_id=?`, request.AttemptID).Scan(&attemptState, &attemptDispatch, &currentSideEffect, &jobID, &jobState, &currentAttempt)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("ATTEMPT_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read dispatch attempt: %w", err)
		}

		var intentDispatch string
		var responseFactAt sql.NullString
		err = transaction.QueryRowContext(ctx, `SELECT dispatch_state, response_fact_at FROM submit_intents WHERE submit_intent_id=? AND attempt_id=?`, request.SubmitIntentID, request.AttemptID).Scan(&intentDispatch, &responseFactAt)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("SUBMIT_INTENT_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read submit intent: %w", err)
		}

		if committedFactsMatch(target, sideEffect, request.AttemptID, attemptState, attemptDispatch, currentSideEffect, jobState, currentAttempt, intentDispatch, responseFactAt) {
			result.Idempotent = true
			return nil
		}
		if (attemptState == "HASH_SEEN" || attemptState == "RECONCILED_CONFIRMED") && jobState == attemptState && currentAttempt.Valid && currentAttempt.String == request.AttemptID && attemptDispatch == addfacts.DispatchStarted && currentSideEffect == "OBSERVED_EFFECT" && intentDispatch == addfacts.DispatchStarted {
			if !responseFactAt.Valid {
				if _, err := transaction.ExecContext(ctx, `UPDATE submit_intents SET response_fact_at=? WHERE submit_intent_id=? AND response_fact_at IS NULL`, request.Now.UTC().Format(time.RFC3339Nano), request.SubmitIntentID); err != nil {
					return err
				}
			}
			result.State, result.Idempotent = attemptState, true
			return nil
		}
		if attemptState != AttemptAddSent || attemptDispatch != addfacts.DispatchStarted || currentSideEffect != addfacts.SideEffectPossible || jobState != AttemptAddSent || !currentAttempt.Valid || currentAttempt.String != request.AttemptID || intentDispatch != addfacts.DispatchStarted {
			return errors.New("DISPATCH_CHAIN_FACT_MISMATCH")
		}

		var waiterState string
		err = transaction.QueryRowContext(ctx, `SELECT waiter_state FROM sync_waiters WHERE waiter_id=? AND attempt_id=?`, request.WaiterID, request.AttemptID).Scan(&waiterState)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("SYNC_WAITER_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read sync waiter: %w", err)
		}
		if waiterState != "ACTIVE" {
			return errors.New("SYNC_WAITER_NOT_ACTIVE")
		}
		var observed int
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM pending_confirmations WHERE attempt_id=?`, request.AttemptID).Scan(&observed); err != nil {
			return err
		}
		if observed > 0 && target == AttemptAddFailed {
			target, sideEffect, result.State = AttemptAwaitingConfirmation, addfacts.SideEffectPossible, AttemptAwaitingConfirmation
		}

		timestamp := request.Now.UTC().Format(time.RFC3339Nano)
		if changed, err := execOne(ctx, transaction, `UPDATE submit_intents SET response_fact_at=? WHERE submit_intent_id=? AND attempt_id=? AND dispatch_state='STARTED' AND response_fact_at IS NULL`, timestamp, request.SubmitIntentID, request.AttemptID); err != nil {
			return fmt.Errorf("record submit response fact: %w", err)
		} else if !changed {
			return errors.New("SUBMIT_INTENT_RESPONSE_CONFLICT")
		}

		if target == AttemptAddFailed {
			if changed, err := execOne(ctx, transaction, `UPDATE placement_attempts SET attempt_state='ADD_FAILED', side_effect_fact='FAILED_NO_EFFECT', finished_at=? WHERE attempt_id=? AND attempt_state='ADD_SENT' AND dispatch_state='STARTED' AND side_effect_fact='POSSIBLE_EFFECT'`, timestamp, request.AttemptID); err != nil {
				return fmt.Errorf("fail dispatch attempt: %w", err)
			} else if !changed {
				return errors.New("ATTEMPT_DISPATCH_RESULT_CONFLICT")
			}
			if changed, err := execOne(ctx, transaction, `UPDATE race_jobs SET job_state='ADD_FAILED', current_attempt_id=NULL, updated_at=? WHERE job_id=? AND job_state='ADD_SENT' AND current_attempt_id=?`, timestamp, jobID, request.AttemptID); err != nil {
				return fmt.Errorf("fail dispatch job: %w", err)
			} else if !changed {
				return errors.New("JOB_DISPATCH_RESULT_CONFLICT")
			}
		} else {
			if changed, err := execOne(ctx, transaction, `UPDATE placement_attempts SET attempt_state=? WHERE attempt_id=? AND attempt_state='ADD_SENT' AND dispatch_state='STARTED' AND side_effect_fact='POSSIBLE_EFFECT'`, target, request.AttemptID); err != nil {
				return fmt.Errorf("commit dispatch attempt: %w", err)
			} else if !changed {
				return errors.New("ATTEMPT_DISPATCH_RESULT_CONFLICT")
			}
			if changed, err := execOne(ctx, transaction, `UPDATE race_jobs SET job_state=?, updated_at=? WHERE job_id=? AND job_state='ADD_SENT' AND current_attempt_id=?`, target, timestamp, jobID, request.AttemptID); err != nil {
				return fmt.Errorf("commit dispatch job: %w", err)
			} else if !changed {
				return errors.New("JOB_DISPATCH_RESULT_CONFLICT")
			}
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', 'ADD_SENT', ?, ?, '{}', ?)`, jobID, request.AttemptID, target, reasonCode(target), timestamp); err != nil {
			return fmt.Errorf("insert dispatch-result event: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if err := firstconfirm.Replay(ctx, database, request.AttemptID); err != nil {
		return result, err
	}
	return result, nil
}

func targetFor(request Request) (string, string, error) {
	if request.Result.DispatchState == addfacts.DispatchNotStarted && request.Result.SideEffectFact == addfacts.SideEffectFailedNoEffect {
		return AttemptAddFailed, addfacts.SideEffectFailedNoEffect, nil
	}
	if request.Result.DispatchState != addfacts.DispatchStarted || request.Result.SideEffectFact != addfacts.SideEffectPossible {
		return "", "", errors.New("unsupported dispatch facts")
	}
	if request.DispatchErr == nil {
		facts, err := addfacts.Classify(request.Result.Response)
		if err != nil {
			return "", "", fmt.Errorf("classify dispatch facts: %w", err)
		}
		if facts.AttemptStateHint == addfacts.AttemptHintAddSent {
			return AttemptAwaitingConfirmation, addfacts.SideEffectPossible, nil
		}
	}
	return AttemptUnknown, addfacts.SideEffectPossible, nil
}

func committedFactsMatch(target, sideEffect, attemptID, attemptState, attemptDispatch, currentSideEffect, jobState string, currentAttempt sql.NullString, intentDispatch string, responseFactAt sql.NullString) bool {
	if attemptState != target || attemptDispatch != addfacts.DispatchStarted || currentSideEffect != sideEffect || jobState != target || intentDispatch != addfacts.DispatchStarted || !responseFactAt.Valid {
		return false
	}
	if target == AttemptAddFailed {
		return !currentAttempt.Valid
	}
	return currentAttempt.Valid && currentAttempt.String == attemptID
}

func validateIdentifiers(attemptID, submitIntentID, waiterID string, now time.Time) error {
	for name, value := range map[string]string{"attempt_id": attemptID, "submit_intent_id": submitIntentID, "waiter_id": waiterID} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if now.IsZero() {
		return errors.New("dispatch time is required")
	}
	return nil
}

func execOne(ctx context.Context, transaction *db.ImmediateTx, query string, args ...any) (bool, error) {
	result, err := transaction.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func reasonCode(state string) string {
	switch state {
	case AttemptAwaitingConfirmation:
		return "ADD_DISPATCH_ACCEPTED"
	case AttemptAddFailed:
		return "ADD_DISPATCH_NOT_SENT"
	default:
		return "ADD_DISPATCH_EFFECT_UNKNOWN"
	}
}
