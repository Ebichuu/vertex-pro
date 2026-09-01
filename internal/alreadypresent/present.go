package alreadypresent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/amountleft"
	"github.com/vertex-pro/vertex-pro/internal/db"
)

var ErrNotAlreadyPresent = errors.New("attempt is not eligible for the already-present branch")

const (
	DebitKindNone      = "NONE"
	DebitKindBound     = "UNKNOWN_AMOUNT_LEFT_BOUND"
	DebitKindUnbounded = "UNBOUNDED_AMOUNT_LEFT"
)

type Request struct {
	BindingID             string
	AttemptID             string
	SubmitIntentID        string
	ReservationID         string
	JobID                 string
	StorageTargetID       string
	InstanceID            string
	IdentityKey           string
	QBTorrentID           string
	AmountLeftKnown       bool
	AmountLeft            int64
	AmountLeftErrorCode   string
	UncertaintyDebitKind  string
	UncertaintyDebitBytes int64
	Now                   time.Time
}

type Result struct {
	Idempotent    bool
	AttemptID     string
	JobID         string
	BindingID     string
	ReservationID string
}

// Record 在单个 BEGIN IMMEDIATE 中完成 Already-present 原子分支：先确保唯一
// UNMANAGED binding（证据固定为 SubmitIntent 冻结的 Add 前基线快照），再把
// Attempt/Job 推进到 ALREADY_PRESENT 并以 ALREADY_PRESENT 释放本次 Reservation。
// 该分支从不发起网络 dispatch，也不把已存在的 qB Torrent 归因给本次 Add。
func Record(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	result := Result{AttemptID: request.AttemptID, JobID: request.JobID, BindingID: request.BindingID, ReservationID: request.ReservationID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var attemptState, dispatchState, attemptJob, attemptTarget, attemptIdentity string
		err := transaction.QueryRowContext(ctx, `SELECT attempt_state, dispatch_state, job_id, storage_target_id, identity_key FROM placement_attempts WHERE attempt_id = ?`, request.AttemptID).Scan(&attemptState, &dispatchState, &attemptJob, &attemptTarget, &attemptIdentity)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: attempt missing", ErrNotAlreadyPresent)
		}
		if err != nil {
			return fmt.Errorf("read attempt: %w", err)
		}
		if attemptState == "ALREADY_PRESENT" {
			if err := verifyReplay(ctx, transaction, request); err != nil {
				return err
			}
			result.Idempotent = true
			return nil
		}
		if attemptState != "RESERVED" || dispatchState != "NOT_STARTED" {
			return fmt.Errorf("%w: attempt state %s/%s", ErrNotAlreadyPresent, attemptState, dispatchState)
		}
		if attemptJob != request.JobID || attemptTarget != request.StorageTargetID || attemptIdentity != request.IdentityKey {
			return fmt.Errorf("%w: attempt facts mismatch", ErrNotAlreadyPresent)
		}

		var intentAttempt, intentInstance, intentIdentity, intentDispatch, preFact, preDigest string
		var preTorrent sql.NullString
		var preGeneration, preRevision int64
		err = transaction.QueryRowContext(ctx, `SELECT attempt_id, instance_id, identity_key, dispatch_state, pre_add_identity_fact, pre_add_qb_torrent_id, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest FROM submit_intents WHERE submit_intent_id = ?`, request.SubmitIntentID).Scan(&intentAttempt, &intentInstance, &intentIdentity, &intentDispatch, &preFact, &preTorrent, &preGeneration, &preRevision, &preDigest)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: submit intent missing", ErrNotAlreadyPresent)
		}
		if err != nil {
			return fmt.Errorf("read submit intent: %w", err)
		}
		if intentAttempt != request.AttemptID || intentInstance != request.InstanceID || intentIdentity != request.IdentityKey {
			return fmt.Errorf("%w: submit intent mismatch", ErrNotAlreadyPresent)
		}
		if intentDispatch != "NOT_STARTED" || preFact != "PRESENT" || !preTorrent.Valid || preTorrent.String != request.QBTorrentID {
			return fmt.Errorf("%w: submit intent did not freeze a pre-add present identity", ErrNotAlreadyPresent)
		}

		if err := ensureUnmanagedBinding(ctx, transaction, request, preGeneration, preRevision, preDigest); err != nil {
			return err
		}
		if !request.AmountLeftKnown {
			if _, err := transaction.ExecContext(ctx, `UPDATE capacity_epochs SET capacity_state='CAPACITY_UNCERTAIN' WHERE storage_target_id = ? AND capacity_state='TRUSTED'`, request.StorageTargetID); err != nil {
				return fmt.Errorf("mark capacity uncertain: %w", err)
			}
		}
		update, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='ALREADY_PRESENT', side_effect_fact='OBSERVED_EFFECT', qb_torrent_id=?, confirmed_at=? WHERE attempt_id=? AND attempt_state='RESERVED'`, request.QBTorrentID, request.Now.UTC().Format(time.RFC3339), request.AttemptID)
		if err != nil {
			return fmt.Errorf("advance attempt: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("advance attempt rows=%d err=%v", rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state='ALREADY_PRESENT', current_attempt_id=NULL, observation_state='NOT_APPLICABLE', updated_at=? WHERE job_id=? AND job_state='RESERVED' AND current_attempt_id=?`, request.Now.UTC().Format(time.RFC3339), request.JobID, request.AttemptID)
		if err != nil {
			return fmt.Errorf("advance job: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("advance job rows=%d err=%v", rows, err)
		}
		update, err = transaction.ExecContext(ctx, `UPDATE capacity_reservations SET reservation_state='RELEASED', release_reason='ALREADY_PRESENT', released_unconsumed_bytes=reserved_bytes-reflected_physical_bytes, released_at=?, revision=revision+1 WHERE reservation_id=? AND reservation_state='ACTIVE'`, request.Now.UTC().Format(time.RFC3339), request.ReservationID)
		if err != nil {
			return fmt.Errorf("release reservation: %w", err)
		}
		if rows, err := update.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("release reservation rows=%d err=%v", rows, err)
		}
		occurredAt := request.Now.UTC().Format(time.RFC3339)
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', 'RESERVED', 'ALREADY_PRESENT', 'PRE_ADD_IDENTITY_PRESENT', '{}', ?)`, request.JobID, request.AttemptID, occurredAt); err != nil {
			return fmt.Errorf("insert attempt event: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, 'job.state.changed', 'RESERVED', 'ALREADY_PRESENT', 'PRE_ADD_IDENTITY_PRESENT', '{}', ?)`, request.JobID, occurredAt); err != nil {
			return fmt.Errorf("insert job event: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func verifyReplay(ctx context.Context, transaction *db.ImmediateTx, request Request) error {
	var torrentID string
	err := transaction.QueryRowContext(ctx, `SELECT COALESCE(qb_torrent_id,'') FROM placement_attempts WHERE attempt_id = ?`, request.AttemptID).Scan(&torrentID)
	if err != nil {
		return fmt.Errorf("read recorded attempt: %w", err)
	}
	if torrentID != request.QBTorrentID {
		return fmt.Errorf("%w: conflicting already-present fact", ErrNotAlreadyPresent)
	}
	var ownerKind, bindingIdentity, bindingTorrent string
	err = transaction.QueryRowContext(ctx, `SELECT owner_kind, COALESCE(identity_key,''), qb_torrent_id FROM capacity_task_bindings WHERE binding_id = ?`, request.BindingID).Scan(&ownerKind, &bindingIdentity, &bindingTorrent)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (ownerKind != "UNMANAGED" || bindingIdentity != request.IdentityKey || bindingTorrent != request.QBTorrentID)) {
		return fmt.Errorf("%w: binding is not the recorded unmanaged owner", ErrNotAlreadyPresent)
	}
	if err != nil {
		return fmt.Errorf("read recorded binding: %w", err)
	}
	var reservationState, releaseReason string
	err = transaction.QueryRowContext(ctx, `SELECT reservation_state, COALESCE(release_reason,'') FROM capacity_reservations WHERE reservation_id = ?`, request.ReservationID).Scan(&reservationState, &releaseReason)
	if err != nil || reservationState != "RELEASED" || releaseReason != "ALREADY_PRESENT" {
		return fmt.Errorf("%w: reservation was not released as already-present", ErrNotAlreadyPresent)
	}
	return nil
}

func ensureUnmanagedBinding(ctx context.Context, transaction *db.ImmediateTx, request Request, generation, revision int64, digest string) error {
	var bindingID, ownerKind, bindingIdentity string
	err := transaction.QueryRowContext(ctx, `SELECT binding_id, owner_kind, COALESCE(identity_key,'') FROM capacity_task_bindings WHERE storage_target_id = ? AND qb_torrent_id = ?`, request.StorageTargetID, request.QBTorrentID).Scan(&bindingID, &ownerKind, &bindingIdentity)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, identity_key, owner_kind, amount_left_state, amount_left, amount_left_error_code, uncertainty_debit_kind, uncertainty_debit_bytes, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES (?, ?, ?, ?, 'UNMANAGED', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, request.BindingID, request.StorageTargetID, request.QBTorrentID, request.IdentityKey, amountLeftState(request), amountLeftValue(request), amountLeftErrorCode(request), debitKind(request), request.UncertaintyDebitBytes, generation, revision, digest, request.Now.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("insert unmanaged binding: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read existing binding: %w", err)
	}
	if ownerKind != "UNMANAGED" || bindingIdentity != request.IdentityKey {
		return fmt.Errorf("%w: qb torrent already has a conflicting binding", ErrNotAlreadyPresent)
	}
	return nil
}

func amountLeftState(request Request) string {
	if request.AmountLeftKnown {
		return string(amountleft.StateKnown)
	}
	return string(amountleft.StateUnknown)
}

func amountLeftValue(request Request) any {
	if request.AmountLeftKnown {
		return request.AmountLeft
	}
	return nil
}

func amountLeftErrorCode(request Request) any {
	if request.AmountLeftKnown {
		return nil
	}
	return request.AmountLeftErrorCode
}

func debitKind(request Request) string {
	if request.AmountLeftKnown {
		return DebitKindNone
	}
	return request.UncertaintyDebitKind
}

func validateRequest(request Request) error {
	values := map[string]string{"binding_id": request.BindingID, "attempt_id": request.AttemptID, "submit_intent_id": request.SubmitIntentID, "reservation_id": request.ReservationID, "job_id": request.JobID, "storage_target_id": request.StorageTargetID, "instance_id": request.InstanceID, "identity_key": request.IdentityKey, "qb_torrent_id": request.QBTorrentID}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.Now.IsZero() {
		return errors.New("record time is required")
	}
	if request.AmountLeftKnown {
		if request.AmountLeft < 0 {
			return errors.New("known amount_left is negative")
		}
		if strings.TrimSpace(request.AmountLeftErrorCode) != "" || (request.UncertaintyDebitKind != DebitKindNone && request.UncertaintyDebitKind != "") || request.UncertaintyDebitBytes != 0 {
			return errors.New("known amount_left cannot carry error code or uncertainty debit")
		}
		return nil
	}
	switch amountleft.ErrorCode(request.AmountLeftErrorCode) {
	case amountleft.ErrorMissingInFullSnapshot, amountleft.ErrorNegativeValue, amountleft.ErrorIntegerOverflow, amountleft.ErrorTypeMismatch, amountleft.ErrorNoTrustedPriorValue, amountleft.ErrorSchemaIncompatible:
	default:
		return errors.New("amount_left error code is invalid")
	}
	switch request.UncertaintyDebitKind {
	case DebitKindBound:
		if request.UncertaintyDebitBytes <= 0 {
			return errors.New("bounded uncertainty debit requires a positive byte count")
		}
	case DebitKindUnbounded:
		if request.UncertaintyDebitBytes != 0 {
			return errors.New("unbounded uncertainty debit must be zero bytes")
		}
	default:
		return errors.New("unknown amount_left requires an uncertainty debit kind")
	}
	return nil
}
