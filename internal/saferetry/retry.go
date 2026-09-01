package saferetry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

var (
	ErrNotRetryable    = errors.New("attempt chain is not closable for retry")
	ErrInvalidEvidence = errors.New("no-effect evidence is not a fresh complete snapshot")
	ErrOwnerAmbiguous  = errors.New("identity owner is ambiguous")
)

const (
	CommandCloseChainAndRequeue = "CLOSE_CHAIN_AND_REQUEUE"
	CommandConfirmNoEffectRetry = "CONFIRM_NO_EFFECT_AND_RETRY"

	ReasonCloseChainRequeue = "CLOSE_CHAIN_REQUEUE"
	ReasonCloseChainExpired = "CLOSE_CHAIN_EXPIRED"
	ReasonConfirmedNoEffect = "FRESH_SNAPSHOT_IDENTITY_ABSENT"
)

// Evidence 仅用于 UNKNOWN 入口的重试命令：新鲜完整合并快照证明无副作用。
type Evidence struct {
	SyncGeneration   int64
	SnapshotRevision int64
	SnapshotDigest   string
	IdentityPresent  bool
	QBTorrentID      string
}

type Request struct {
	CommandID             string
	JobID                 string
	AttemptID             string
	SubmitIntentID        string
	ReservationID         string
	StorageTargetID       string
	InstanceID            string
	IdentityKey           string
	MaxAttempts           int
	ParticipationDeadline string
	Evidence              Evidence
	Now                   time.Time
}

type Result struct {
	CommandID  string
	JobState   string
	Idempotent bool
}

type chainFacts struct {
	attemptState     string
	attemptNo        int
	jobState         string
	reservationState string
	preGeneration    int64
	preRevision      int64
	preDigest        string
	waiterID         string
}

// CloseChainAndRequeue 执行第 16.3 节两阶段重试的阶段 A：对已产生
// ADD_FAILED 或 RECONCILED_NO_EFFECT 事实的旧链，在单个事务中释放
// Reservation、关闭 waiter，并把 RaceJob 放回 READY（参与窗口与 retry
// budget 仍有效）或 EXPIRED。本命令绝不创建新 Attempt/SubmitIntent/
// Reservation，也不返回任何预分配的新 ID；阶段 B 的重新 Placement 由
// 独立调度从 READY 重新执行。
func CloseChainAndRequeue(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateBase(request); err != nil {
		return Result{}, err
	}
	result := Result{CommandID: request.CommandID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		chain, err := loadClosedChain(ctx, transaction, request)
		if err != nil {
			return err
		}
		targetJobState, reason, err := decideTarget(ctx, transaction, request)
		if err != nil {
			return err
		}
		if err := releaseReservationAndCloseWaiter(ctx, transaction, request, chain); err != nil {
			return err
		}
		if err := advanceJob(ctx, transaction, request, chain.jobState, targetJobState, reason); err != nil {
			return err
		}
		result.JobState = targetJobState
		return insertCommand(ctx, transaction, request, CommandCloseChainAndRequeue, outcomeJSON(targetJobState), "")
	})
	if err != nil {
		recordFailure(ctx, database, request, CommandCloseChainAndRequeue, err)
		return Result{}, err
	}
	return result, nil
}

// ConfirmNoEffectAndRetry 对仍处于 UNKNOWN 的旧链执行第 16.8 节矩阵中的
// CONFIRM_NO_EFFECT_AND_RETRY：新鲜权威快照证明无副作用后，在同一事务把
// Attempt 推进为 RECONCILED_NO_EFFECT、释放旧链，并按窗口与预算把 RaceJob
// 放回 READY 或 EXPIRED。EXPIRED_UNKNOWN 入口不适用本命令：ADD-11 的人工
// 闭环必须走向 EXPIRED，不得恢复自动重试。
func ConfirmNoEffectAndRetry(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateBase(request); err != nil {
		return Result{}, err
	}
	if request.Evidence.IdentityPresent || strings.TrimSpace(request.Evidence.SnapshotDigest) == "" {
		err := fmt.Errorf("%w: retry requires an absent identity in a fresh snapshot", ErrInvalidEvidence)
		recordFailure(ctx, database, request, CommandConfirmNoEffectRetry, err)
		return Result{}, err
	}
	result := Result{CommandID: request.CommandID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		chain, err := loadUnknownChain(ctx, transaction, request)
		if err != nil {
			return err
		}
		if err := checkEvidenceFresh(request.Evidence, chain); err != nil {
			return err
		}
		var bound int
		if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM capacity_task_bindings WHERE identity_key = ?`, request.IdentityKey).Scan(&bound); err != nil {
			return fmt.Errorf("read identity bindings: %w", err)
		}
		if bound > 0 {
			return fmt.Errorf("%w: identity already attributed to another owner", ErrOwnerAmbiguous)
		}
		targetJobState, reason, err := decideTarget(ctx, transaction, request)
		if err != nil {
			return err
		}
		timestamp := request.Now.UTC().Format(time.RFC3339)
		update, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='RECONCILED_NO_EFFECT', finished_at=? WHERE attempt_id=? AND attempt_state='UNKNOWN' AND dispatch_state='STARTED'`, timestamp, request.AttemptID)
		if err != nil {
			return fmt.Errorf("reconcile attempt: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("reconcile attempt rows=%d err=%v", rows, err)
		}
		if err := releaseReservationAndCloseWaiter(ctx, transaction, request, chain); err != nil {
			return err
		}
		update, err = transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state='RECONCILED_NO_EFFECT', current_attempt_id=NULL, updated_at=? WHERE job_id=? AND job_state='UNKNOWN' AND current_attempt_id=?`, timestamp, request.JobID, request.AttemptID)
		if err != nil {
			return fmt.Errorf("reconcile job: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("reconcile job rows=%d err=%v", rows, err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, 'job.state.changed', 'UNKNOWN', 'RECONCILED_NO_EFFECT', ?, '{}', ?)`, request.JobID, ReasonConfirmedNoEffect, timestamp); err != nil {
			return fmt.Errorf("insert job event: %w", err)
		}
		if err := advanceJob(ctx, transaction, request, "RECONCILED_NO_EFFECT", targetJobState, reason); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', 'UNKNOWN', 'RECONCILED_NO_EFFECT', ?, '{}', ?)`, request.JobID, request.AttemptID, ReasonConfirmedNoEffect, timestamp); err != nil {
			return fmt.Errorf("insert attempt event: %w", err)
		}
		result.JobState = targetJobState
		return insertCommand(ctx, transaction, request, CommandConfirmNoEffectRetry, outcomeJSON(targetJobState), "")
	})
	if err != nil {
		recordFailure(ctx, database, request, CommandConfirmNoEffectRetry, err)
		return Result{}, err
	}
	return result, nil
}

func loadClosedChain(ctx context.Context, transaction *db.ImmediateTx, request Request) (chainFacts, error) {
	chain, err := loadChain(ctx, transaction, request)
	if err != nil {
		return chain, err
	}
	if chain.attemptState != "ADD_FAILED" && chain.attemptState != "RECONCILED_NO_EFFECT" {
		return chain, fmt.Errorf("%w: attempt state %s", ErrNotRetryable, chain.attemptState)
	}
	if chain.jobState != chain.attemptState {
		return chain, fmt.Errorf("%w: job state %s does not mirror attempt", ErrNotRetryable, chain.jobState)
	}
	if chain.reservationState != "ACTIVE" {
		return chain, fmt.Errorf("%w: reservation state %s", ErrNotRetryable, chain.reservationState)
	}
	return chain, nil
}

func loadUnknownChain(ctx context.Context, transaction *db.ImmediateTx, request Request) (chainFacts, error) {
	chain, err := loadChain(ctx, transaction, request)
	if err != nil {
		return chain, err
	}
	if chain.attemptState != "UNKNOWN" {
		return chain, fmt.Errorf("%w: attempt state %s", ErrNotRetryable, chain.attemptState)
	}
	if chain.jobState != "UNKNOWN" {
		return chain, fmt.Errorf("%w: job state %s", ErrNotRetryable, chain.jobState)
	}
	if chain.reservationState != "ACTIVE" {
		return chain, fmt.Errorf("%w: reservation state %s", ErrNotRetryable, chain.reservationState)
	}
	var intentAttempt string
	err = transaction.QueryRowContext(ctx, `SELECT attempt_id, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest FROM submit_intents WHERE submit_intent_id = ?`, request.SubmitIntentID).Scan(&intentAttempt, &chain.preGeneration, &chain.preRevision, &chain.preDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return chain, fmt.Errorf("%w: submit intent missing", ErrNotRetryable)
	}
	if err != nil {
		return chain, fmt.Errorf("read submit intent: %w", err)
	}
	if intentAttempt != request.AttemptID {
		return chain, fmt.Errorf("%w: submit intent mismatch", ErrNotRetryable)
	}
	return chain, nil
}

func loadChain(ctx context.Context, transaction *db.ImmediateTx, request Request) (chainFacts, error) {
	var chain chainFacts
	var attemptJob, attemptTarget, attemptIdentity string
	var currentAttempt sql.NullString
	err := transaction.QueryRowContext(ctx, `SELECT a.attempt_state, a.attempt_no, a.job_id, a.storage_target_id, a.identity_key, j.job_state, j.current_attempt_id, r.reservation_state FROM placement_attempts a JOIN race_jobs j ON j.job_id = a.job_id JOIN capacity_reservations r ON r.attempt_id = a.attempt_id WHERE a.attempt_id = ? AND r.reservation_id = ?`, request.AttemptID, request.ReservationID).Scan(&chain.attemptState, &chain.attemptNo, &attemptJob, &attemptTarget, &attemptIdentity, &chain.jobState, &currentAttempt, &chain.reservationState)
	if errors.Is(err, sql.ErrNoRows) {
		return chain, fmt.Errorf("%w: chain missing", ErrNotRetryable)
	}
	if err != nil {
		return chain, fmt.Errorf("read chain: %w", err)
	}
	if attemptJob != request.JobID || attemptTarget != request.StorageTargetID || attemptIdentity != request.IdentityKey {
		return chain, fmt.Errorf("%w: attempt facts mismatch", ErrNotRetryable)
	}
	if currentAttempt.Valid && currentAttempt.String != request.AttemptID {
		return chain, fmt.Errorf("%w: current attempt mismatch", ErrNotRetryable)
	}
	err = transaction.QueryRowContext(ctx, `SELECT waiter_id FROM sync_waiters WHERE attempt_id = ?`, request.AttemptID).Scan(&chain.waiterID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return chain, fmt.Errorf("read waiter: %w", err)
	}
	return chain, nil
}

// decideTarget 固定重排判定：参与窗口未过期且历史 Attempt 数量仍低于
// retry budget 时放回 READY，否则进入 EXPIRED。
func decideTarget(ctx context.Context, transaction *db.ImmediateTx, request Request) (string, string, error) {
	deadline, err := time.Parse(time.RFC3339, request.ParticipationDeadline)
	if err != nil {
		return "", "", fmt.Errorf("parse participation deadline: %w", err)
	}
	if !request.Now.UTC().After(deadline) {
		var attempts int
		if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM placement_attempts WHERE job_id = ?`, request.JobID).Scan(&attempts); err != nil {
			return "", "", fmt.Errorf("count attempts: %w", err)
		}
		if attempts < request.MaxAttempts {
			return "READY", ReasonCloseChainRequeue, nil
		}
	}
	return "EXPIRED", ReasonCloseChainExpired, nil
}

func checkEvidenceFresh(evidence Evidence, chain chainFacts) error {
	if evidence.SyncGeneration < chain.preGeneration || (evidence.SyncGeneration == chain.preGeneration && evidence.SnapshotRevision <= chain.preRevision) || evidence.SnapshotDigest == chain.preDigest {
		return fmt.Errorf("%w: snapshot is not newer than the add baseline", ErrInvalidEvidence)
	}
	return nil
}

func releaseReservationAndCloseWaiter(ctx context.Context, transaction *db.ImmediateTx, request Request, chain chainFacts) error {
	timestamp := request.Now.UTC().Format(time.RFC3339)
	update, err := transaction.ExecContext(ctx, `UPDATE capacity_reservations SET reservation_state='RELEASED', release_reason='NO_EFFECT', released_unconsumed_bytes=reserved_bytes-reflected_physical_bytes, released_at=?, revision=revision+1 WHERE reservation_id=? AND attempt_id=? AND reservation_state='ACTIVE'`, timestamp, request.ReservationID, request.AttemptID)
	if err != nil {
		return fmt.Errorf("release reservation: %w", err)
	}
	if rows, err := update.RowsAffected(); err != nil || rows != 1 {
		return fmt.Errorf("release reservation rows=%d err=%v", rows, err)
	}
	if chain.waiterID != "" {
		if _, err := transaction.ExecContext(ctx, `UPDATE sync_waiters SET waiter_state='CLOSED', closed_at=COALESCE(closed_at,?) WHERE waiter_id=? AND waiter_state='ACTIVE'`, timestamp, chain.waiterID); err != nil {
			return fmt.Errorf("close waiter: %w", err)
		}
	}
	return nil
}

func advanceJob(ctx context.Context, transaction *db.ImmediateTx, request Request, fromState, toState, reason string) error {
	timestamp := request.Now.UTC().Format(time.RFC3339)
	update, err := transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state=?, updated_at=? WHERE job_id=? AND job_state=? AND current_attempt_id IS NULL`, toState, timestamp, request.JobID, fromState)
	if err != nil {
		return fmt.Errorf("advance job: %w", err)
	}
	if rows, err := update.RowsAffected(); err != nil || rows != 1 {
		return fmt.Errorf("advance job rows=%d err=%v", rows, err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, 'job.state.changed', ?, ?, ?, '{}', ?)`, request.JobID, fromState, toState, reason, timestamp); err != nil {
		return fmt.Errorf("insert job event: %w", err)
	}
	return nil
}

func outcomeJSON(targetJobState string) string {
	return fmt.Sprintf(`{"outcome":%q,"job_state":%q}`, targetJobState, targetJobState)
}

func insertCommand(ctx context.Context, transaction *db.ImmediateTx, request Request, commandType, resultJSON, errorCode string) error {
	status := "SUCCEEDED"
	timestamp := request.Now.UTC().Format(time.RFC3339)
	var resultPtr, errorPtr any
	if errorCode != "" {
		status = "FAILED"
		errorPtr = errorCode
	} else {
		resultPtr = resultJSON
	}
	expiresAt := request.Now.UTC().Add(24 * time.Hour).Format(time.RFC3339)
	if _, err := transaction.ExecContext(ctx, `INSERT INTO api_commands(command_id, command_type, subject_id, aggregate_type, aggregate_id, command_status, request_fingerprint, result_json, error_code, created_at, started_at, finished_at, expires_at) VALUES (?, ?, ?, 'race_job', ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(command_id) DO NOTHING`, request.CommandID, commandType, request.JobID, request.JobID, status, fingerprint(request, commandType), resultPtr, errorPtr, timestamp, timestamp, timestamp, expiresAt); err != nil {
		return fmt.Errorf("insert command audit: %w", err)
	}
	return nil
}

func recordFailure(ctx context.Context, database *sql.DB, request Request, commandType string, failure error) {
	if database == nil {
		return
	}
	_ = db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		return insertCommand(ctx, transaction, request, commandType, "", "RETRY_COMMAND_REJECTED")
	})
}

func fingerprint(request Request, commandType string) string {
	source := fmt.Sprintf("%s|%s|%d|%d|%s|%t", commandType, request.AttemptID, request.Evidence.SyncGeneration, request.Evidence.SnapshotRevision, request.Evidence.SnapshotDigest, request.Evidence.IdentityPresent)
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}

func validateBase(request Request) error {
	values := map[string]string{"command_id": request.CommandID, "job_id": request.JobID, "attempt_id": request.AttemptID, "reservation_id": request.ReservationID, "storage_target_id": request.StorageTargetID, "instance_id": request.InstanceID, "identity_key": request.IdentityKey, "participation_deadline": request.ParticipationDeadline}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.Now.IsZero() {
		return errors.New("command time is required")
	}
	if request.MaxAttempts < 1 {
		return errors.New("max attempts is invalid")
	}
	return nil
}
