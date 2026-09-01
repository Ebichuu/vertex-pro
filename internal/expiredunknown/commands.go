package expiredunknown

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
	"github.com/vertex-pro/vertex-pro/internal/firstconfirm"
)

var (
	ErrNotExpirable    = errors.New("attempt is not a manually closable expired unknown")
	ErrInvalidEvidence = errors.New("manual closure evidence is not a fresh complete snapshot")
	ErrOwnerAmbiguous  = errors.New("identity owner is ambiguous")
)

const (
	CommandConfirmNoEffectAndExpire = "CONFIRM_NO_EFFECT_AND_EXPIRE"
	CommandConfirmEffect            = "CONFIRM_EFFECT"

	ReasonManualNoEffect = "MANUAL_CONFIRM_NO_EFFECT"
)

// Evidence 是人工闭环唯一可接受的证据：当前连接代际的新鲜完整合并 qB 快照。
// 人工意见、页面截图或陈旧快照不构成确定事实。
type Evidence struct {
	SyncGeneration   int64
	SnapshotRevision int64
	SnapshotDigest   string
	IdentityPresent  bool
	QBTorrentID      string
}

type Request struct {
	CommandID       string
	JobID           string
	AttemptID       string
	SubmitIntentID  string
	ReservationID   string
	StorageTargetID string
	InstanceID      string
	IdentityKey     string
	BindingID       string
	Evidence        Evidence
	Now             time.Time
}

type Result struct {
	CommandID  string
	Idempotent bool
}

type chainFacts struct {
	attemptState     string
	reservationState string
	preGeneration    int64
	preRevision      int64
	preDigest        string
	preFact          string
	waiterID         string
}

// ConfirmNoEffectAndExpire 执行第一条人工闭环：新鲜权威快照证明无副作用后，
// 在单个事务中把旧 Attempt 标记为 RECONCILED_NO_EFFECT、RaceJob 连续推进
// RECONCILED_NO_EFFECT -> EXPIRED、关闭 waiter、以 NO_EFFECT 释放旧 Reservation，
// 并写入 SUCCEEDED 命令审计。该终局不创建新 Attempt，也不恢复自动重试。
func ConfirmNoEffectAndExpire(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateBase(request); err != nil {
		return Result{}, err
	}
	if request.Evidence.IdentityPresent {
		err := fmt.Errorf("%w: no-effect closure requires an absent identity", ErrInvalidEvidence)
		recordFailure(ctx, database, request, CommandConfirmNoEffectAndExpire, err)
		return Result{}, err
	}
	result := Result{CommandID: request.CommandID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		done, err := replayNoEffect(ctx, transaction, request)
		if err != nil {
			return err
		}
		if done {
			result.Idempotent = true
			return nil
		}
		chain, err := loadChainFacts(ctx, transaction, request)
		if err != nil {
			return err
		}
		if chain.reservationState != "ACTIVE" {
			return fmt.Errorf("%w: reservation state %s", ErrNotExpirable, chain.reservationState)
		}
		if err := checkEvidenceFresh(request.Evidence, chain); err != nil {
			return err
		}
		if err := checkOwnerUnclaimed(ctx, transaction, request.IdentityKey); err != nil {
			return err
		}
		timestamp := request.Now.UTC().Format(time.RFC3339)
		update, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='RECONCILED_NO_EFFECT' WHERE attempt_id=? AND attempt_state='EXPIRED_UNKNOWN' AND dispatch_state='STARTED'`, request.AttemptID)
		if err != nil {
			return fmt.Errorf("close attempt: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("close attempt rows=%d err=%v", rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state='RECONCILED_NO_EFFECT', current_attempt_id=NULL, updated_at=? WHERE job_id=? AND job_state='EXPIRED_UNKNOWN' AND current_attempt_id=?`, timestamp, request.JobID, request.AttemptID)
		if err != nil {
			return fmt.Errorf("reconcile job: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("reconcile job rows=%d err=%v", rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state='EXPIRED', updated_at=? WHERE job_id=? AND job_state='RECONCILED_NO_EFFECT'`, timestamp, request.JobID)
		if err != nil {
			return fmt.Errorf("expire job: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("expire job rows=%d err=%v", rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE capacity_reservations SET reservation_state='RELEASED', release_reason='NO_EFFECT', released_unconsumed_bytes=reserved_bytes-reflected_physical_bytes, released_at=?, revision=revision+1 WHERE reservation_id=? AND attempt_id=? AND reservation_state='ACTIVE'`, timestamp, request.ReservationID, request.AttemptID)
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
		audit := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', 'EXPIRED_UNKNOWN', 'RECONCILED_NO_EFFECT', ?, '{}', ?)`, []any{request.JobID, request.AttemptID, ReasonManualNoEffect, timestamp}},
			{`INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, 'job.state.changed', 'EXPIRED_UNKNOWN', 'RECONCILED_NO_EFFECT', ?, '{}', ?)`, []any{request.JobID, ReasonManualNoEffect, timestamp}},
			{`INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, 'job.state.changed', 'RECONCILED_NO_EFFECT', 'EXPIRED', ?, '{}', ?)`, []any{request.JobID, ReasonManualNoEffect, timestamp}},
		}
		for _, event := range audit {
			if _, err := transaction.ExecContext(ctx, event.query, event.args...); err != nil {
				return fmt.Errorf("insert audit event: %w", err)
			}
		}
		return insertCommand(ctx, transaction, request, CommandConfirmNoEffectAndExpire, `{"outcome":"RECONCILED_NO_EFFECT","job_state":"EXPIRED"}`, "")
	})
	if err != nil {
		recordFailure(ctx, database, request, CommandConfirmNoEffectAndExpire, err)
		return Result{}, err
	}
	return result, nil
}

// ConfirmEffect 执行第二条人工闭环：新鲜权威快照确认本次副作用已存在时，
// 复用第 8.7.3 节首次确认原子事务绑定唯一 qb_torrent_id，让 Reservation 进入
// REFLECTING 并开始观测；不得直接释放容量。
func ConfirmEffect(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateBase(request); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(request.BindingID) == "" {
		return Result{}, fmt.Errorf("binding_id is required")
	}
	if !request.Evidence.IdentityPresent || strings.TrimSpace(request.Evidence.QBTorrentID) == "" {
		err := fmt.Errorf("%w: effect confirmation requires a present identity with qb torrent id", ErrInvalidEvidence)
		recordFailure(ctx, database, request, CommandConfirmEffect, err)
		return Result{}, err
	}
	var chain chainFacts
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		item, err := loadChainFacts(ctx, transaction, request)
		if err != nil {
			return err
		}
		if item.attemptState != "EXPIRED_UNKNOWN" && item.attemptState != "RECONCILED_CONFIRMED" {
			return fmt.Errorf("%w: attempt state %s", ErrNotExpirable, item.attemptState)
		}
		if item.reservationState != "ACTIVE" && item.reservationState != "REFLECTING" {
			return fmt.Errorf("%w: reservation state %s", ErrNotExpirable, item.reservationState)
		}
		if err := checkEvidenceFresh(request.Evidence, item); err != nil {
			return err
		}
		if err := checkOwnerUnclaimedExcept(ctx, transaction, request.IdentityKey, request.StorageTargetID, request.Evidence.QBTorrentID); err != nil {
			return err
		}
		chain = item
		return nil
	})
	if err != nil {
		recordFailure(ctx, database, request, CommandConfirmEffect, err)
		return Result{}, err
	}
	confirmResult, err := firstconfirm.Confirm(ctx, database, firstconfirm.Request{
		BindingID:              request.BindingID,
		AttemptID:              request.AttemptID,
		SubmitIntentID:         request.SubmitIntentID,
		ReservationID:          request.ReservationID,
		JobID:                  request.JobID,
		StorageTargetID:        request.StorageTargetID,
		IdentityKey:            request.IdentityKey,
		QBTorrentID:            request.Evidence.QBTorrentID,
		SyncGeneration:         request.Evidence.SyncGeneration,
		SnapshotRevision:       request.Evidence.SnapshotRevision,
		SnapshotDigest:         request.Evidence.SnapshotDigest,
		PreAddSyncGeneration:   chain.preGeneration,
		PreAddSnapshotRevision: chain.preRevision,
		PreAddSnapshotDigest:   chain.preDigest,
		EvidenceDigest:         evidenceFingerprint(request.Evidence),
		Now:                    request.Now,
	})
	if err != nil {
		recordFailure(ctx, database, request, CommandConfirmEffect, err)
		return Result{}, err
	}
	if err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		if chain.waiterID != "" {
			if _, err := transaction.ExecContext(ctx, `UPDATE sync_waiters SET waiter_state='CLOSED', closed_at=COALESCE(closed_at,?) WHERE waiter_id=? AND waiter_state='ACTIVE'`, request.Now.UTC().Format(time.RFC3339), chain.waiterID); err != nil {
				return fmt.Errorf("close waiter: %w", err)
			}
		}
		return insertCommand(ctx, transaction, request, CommandConfirmEffect, fmt.Sprintf(`{"outcome":"RECONCILED_CONFIRMED","binding_id":%q}`, request.BindingID), "")
	}); err != nil {
		return Result{}, fmt.Errorf("record command: %w", err)
	}
	return Result{CommandID: request.CommandID, Idempotent: confirmResult.Idempotent}, nil
}

func loadChainFacts(ctx context.Context, transaction *db.ImmediateTx, request Request) (chainFacts, error) {
	var chain chainFacts
	var attemptJob, attemptTarget, attemptIdentity string
	err := transaction.QueryRowContext(ctx, `SELECT attempt_state, job_id, storage_target_id, identity_key FROM placement_attempts WHERE attempt_id = ?`, request.AttemptID).Scan(&chain.attemptState, &attemptJob, &attemptTarget, &attemptIdentity)
	if errors.Is(err, sql.ErrNoRows) {
		return chain, fmt.Errorf("%w: attempt missing", ErrNotExpirable)
	}
	if err != nil {
		return chain, fmt.Errorf("read attempt: %w", err)
	}
	if attemptJob != request.JobID || attemptTarget != request.StorageTargetID || attemptIdentity != request.IdentityKey {
		return chain, fmt.Errorf("%w: attempt facts mismatch", ErrNotExpirable)
	}
	var intentAttempt, intentIdentity string
	err = transaction.QueryRowContext(ctx, `SELECT attempt_id, identity_key, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact FROM submit_intents WHERE submit_intent_id = ?`, request.SubmitIntentID).Scan(&intentAttempt, &intentIdentity, &chain.preGeneration, &chain.preRevision, &chain.preDigest, &chain.preFact)
	if errors.Is(err, sql.ErrNoRows) {
		return chain, fmt.Errorf("%w: submit intent missing", ErrNotExpirable)
	}
	if err != nil {
		return chain, fmt.Errorf("read submit intent: %w", err)
	}
	if intentAttempt != request.AttemptID || intentIdentity != request.IdentityKey {
		return chain, fmt.Errorf("%w: submit intent mismatch", ErrNotExpirable)
	}
	if chain.preFact != "ABSENT" {
		return chain, fmt.Errorf("%w: pre-add present identity belongs to the already-present branch", ErrNotExpirable)
	}
	var reservationState string
	err = transaction.QueryRowContext(ctx, `SELECT reservation_state FROM capacity_reservations WHERE reservation_id = ? AND attempt_id = ?`, request.ReservationID, request.AttemptID).Scan(&reservationState)
	if errors.Is(err, sql.ErrNoRows) {
		return chain, fmt.Errorf("%w: reservation unavailable", ErrNotExpirable)
	}
	if err != nil {
		return chain, fmt.Errorf("read reservation: %w", err)
	}
	chain.reservationState = reservationState
	err = transaction.QueryRowContext(ctx, `SELECT waiter_id FROM sync_waiters WHERE attempt_id = ?`, request.AttemptID).Scan(&chain.waiterID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return chain, fmt.Errorf("read waiter: %w", err)
	}
	return chain, nil
}

func replayNoEffect(ctx context.Context, transaction *db.ImmediateTx, request Request) (bool, error) {
	var attemptState, jobState, reservationState, releaseReason string
	err := transaction.QueryRowContext(ctx, `SELECT a.attempt_state, j.job_state, r.reservation_state, COALESCE(r.release_reason,'') FROM placement_attempts a JOIN race_jobs j ON j.job_id = a.job_id JOIN capacity_reservations r ON r.reservation_id = ? AND r.attempt_id = a.attempt_id WHERE a.attempt_id = ?`, request.ReservationID, request.AttemptID).Scan(&attemptState, &jobState, &reservationState, &releaseReason)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read replay facts: %w", err)
	}
	if attemptState == "RECONCILED_NO_EFFECT" && jobState == "EXPIRED" && reservationState == "RELEASED" && releaseReason == "NO_EFFECT" {
		return true, nil
	}
	if attemptState != "EXPIRED_UNKNOWN" {
		return false, fmt.Errorf("%w: attempt state %s", ErrNotExpirable, attemptState)
	}
	return false, nil
}

func checkEvidenceFresh(evidence Evidence, chain chainFacts) error {
	if evidence.SyncGeneration < chain.preGeneration || (evidence.SyncGeneration == chain.preGeneration && evidence.SnapshotRevision <= chain.preRevision) || evidence.SnapshotDigest == chain.preDigest {
		return fmt.Errorf("%w: snapshot is not newer than the add baseline", ErrInvalidEvidence)
	}
	return nil
}

func checkOwnerUnclaimed(ctx context.Context, transaction *db.ImmediateTx, identityKey string) error {
	var bound int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM capacity_task_bindings WHERE identity_key = ?`, identityKey).Scan(&bound); err != nil {
		return fmt.Errorf("read identity bindings: %w", err)
	}
	if bound > 0 {
		return fmt.Errorf("%w: identity already attributed to another owner", ErrOwnerAmbiguous)
	}
	return nil
}

func checkOwnerUnclaimedExcept(ctx context.Context, transaction *db.ImmediateTx, identityKey, storageTargetID, qbTorrentID string) error {
	var bound int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM capacity_task_bindings WHERE identity_key = ? AND NOT (storage_target_id = ? AND qb_torrent_id = ?)`, identityKey, storageTargetID, qbTorrentID).Scan(&bound); err != nil {
		return fmt.Errorf("read identity bindings: %w", err)
	}
	if bound > 0 {
		return fmt.Errorf("%w: identity already attributed to another owner", ErrOwnerAmbiguous)
	}
	return nil
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

// recordFailure 尽力把被拒绝的命令写入审计；审计本身失败不改变拒绝结果。
func recordFailure(ctx context.Context, database *sql.DB, request Request, commandType string, failure error) {
	if database == nil {
		return
	}
	_ = db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		return insertCommand(ctx, transaction, request, commandType, "", "EXPIRED_UNKNOWN_COMMAND_REJECTED")
	})
}

func fingerprint(request Request, commandType string) string {
	source := fmt.Sprintf("%s|%s|%s|%d|%d|%s|%t|%s", commandType, request.AttemptID, request.SubmitIntentID, request.Evidence.SyncGeneration, request.Evidence.SnapshotRevision, request.Evidence.SnapshotDigest, request.Evidence.IdentityPresent, request.Evidence.QBTorrentID)
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}

func evidenceFingerprint(evidence Evidence) string {
	source := fmt.Sprintf("snapshot|%d|%d|%s", evidence.SyncGeneration, evidence.SnapshotRevision, evidence.SnapshotDigest)
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}

func validateBase(request Request) error {
	values := map[string]string{"command_id": request.CommandID, "job_id": request.JobID, "attempt_id": request.AttemptID, "submit_intent_id": request.SubmitIntentID, "reservation_id": request.ReservationID, "storage_target_id": request.StorageTargetID, "instance_id": request.InstanceID, "identity_key": request.IdentityKey}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.Now.IsZero() {
		return errors.New("command time is required")
	}
	if strings.TrimSpace(request.Evidence.SnapshotDigest) == "" || request.Evidence.SyncGeneration < 0 || request.Evidence.SnapshotRevision < 0 {
		return fmt.Errorf("%w: snapshot evidence is incomplete", ErrInvalidEvidence)
	}
	return nil
}
