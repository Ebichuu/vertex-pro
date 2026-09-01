package firstconfirm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

var (
	ErrNotConfirmable = errors.New("attempt is not confirmable")
	ErrStaleSnapshot  = errors.New("confirmation snapshot is not newer than add baseline")
)

type Request struct {
	BindingID              string
	AttemptID              string
	SubmitIntentID         string
	ReservationID          string
	JobID                  string
	StorageTargetID        string
	IdentityKey            string
	QBTorrentID            string
	SyncGeneration         int64
	SnapshotRevision       int64
	SnapshotDigest         string
	PreAddSyncGeneration   int64
	PreAddSnapshotRevision int64
	PreAddSnapshotDigest   string
	EvidenceDigest         string
	Now                    time.Time
}

type Result struct {
	Idempotent    bool
	AttemptID     string
	JobID         string
	BindingID     string
	ReservationID string
}

func Confirm(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validate(request); err != nil {
		return Result{}, err
	}
	result := Result{AttemptID: request.AttemptID, JobID: request.JobID, BindingID: request.BindingID, ReservationID: request.ReservationID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var attemptState, dispatchState, sideEffect, attemptJob, attemptTarget, attemptIdentity string
		var attemptReservation string
		err := transaction.QueryRowContext(ctx, `SELECT attempt_state, dispatch_state, COALESCE(side_effect_fact,''), job_id, storage_target_id, identity_key FROM placement_attempts WHERE attempt_id = ?`, request.AttemptID).Scan(&attemptState, &dispatchState, &sideEffect, &attemptJob, &attemptTarget, &attemptIdentity)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: attempt missing", ErrNotConfirmable)
		}
		if err != nil {
			return fmt.Errorf("read attempt: %w", err)
		}
		if attemptState == "HASH_SEEN" || attemptState == "RECONCILED_CONFIRMED" {
			var existingID, existingGen, existingRev, existingDigest string
			if err := transaction.QueryRowContext(ctx, `SELECT COALESCE(qb_torrent_id,''), COALESCE(confirmation_sync_generation,''), COALESCE(confirmation_snapshot_revision,''), COALESCE(confirmation_evidence_digest,'') FROM placement_attempts WHERE attempt_id = ?`, request.AttemptID).Scan(&existingID, &existingGen, &existingRev, &existingDigest); err != nil {
				return fmt.Errorf("read confirmed attempt: %w", err)
			}
			if existingID == request.QBTorrentID && existingGen == fmt.Sprint(request.SyncGeneration) && existingRev == fmt.Sprint(request.SnapshotRevision) && existingDigest == request.EvidenceDigest {
				result.Idempotent = true
				return nil
			}
			return fmt.Errorf("%w: conflicting confirmation", ErrNotConfirmable)
		}
		confirmTarget := "HASH_SEEN"
		expectedJobState := "AWAITING_CONFIRMATION"
		switch attemptState {
		case "UNKNOWN":
			confirmTarget = "RECONCILED_CONFIRMED"
			expectedJobState = "UNKNOWN"
		case "EXPIRED_UNKNOWN":
			confirmTarget = "RECONCILED_CONFIRMED"
			expectedJobState = "EXPIRED_UNKNOWN"
		case "AWAITING_CONFIRMATION":
		default:
			return fmt.Errorf("%w: attempt facts mismatch", ErrNotConfirmable)
		}
		if dispatchState != "STARTED" || attemptJob != request.JobID || attemptTarget != request.StorageTargetID || attemptIdentity != request.IdentityKey {
			return fmt.Errorf("%w: attempt facts mismatch", ErrNotConfirmable)
		}
		if sideEffect != "POSSIBLE_EFFECT" && !(attemptState != "AWAITING_CONFIRMATION" && sideEffect == "OBSERVED_EFFECT") {
			return fmt.Errorf("%w: attempt facts mismatch", ErrNotConfirmable)
		}
		if err := transaction.QueryRowContext(ctx, `SELECT reservation_id FROM capacity_reservations WHERE reservation_id = ? AND attempt_id = ? AND storage_target_id = ? AND reservation_state IN ('ACTIVE','REFLECTING')`, request.ReservationID, request.AttemptID, request.StorageTargetID).Scan(&attemptReservation); err != nil {
			return fmt.Errorf("%w: reservation unavailable", ErrNotConfirmable)
		}
		var intentAttempt, intentInstance, intentIdentity string
		if err := transaction.QueryRowContext(ctx, `SELECT attempt_id, instance_id, identity_key FROM submit_intents WHERE submit_intent_id = ?`, request.SubmitIntentID).Scan(&intentAttempt, &intentInstance, &intentIdentity); err != nil {
			return fmt.Errorf("%w: submit intent unavailable", ErrNotConfirmable)
		}
		if intentAttempt != request.AttemptID || intentIdentity != request.IdentityKey || strings.TrimSpace(intentInstance) == "" {
			return fmt.Errorf("%w: submit intent mismatch", ErrNotConfirmable)
		}
		var jobState string
		var currentAttempt sql.NullString
		if err := transaction.QueryRowContext(ctx, `SELECT job_state, current_attempt_id FROM race_jobs WHERE job_id = ?`, request.JobID).Scan(&jobState, &currentAttempt); err != nil {
			return fmt.Errorf("read job: %w", err)
		}
		if !currentAttempt.Valid || currentAttempt.String != request.AttemptID || jobState != expectedJobState {
			return fmt.Errorf("%w: job facts mismatch", ErrNotConfirmable)
		}
		var bindingTarget, bindingTorrent string
		if err := transaction.QueryRowContext(ctx, `SELECT storage_target_id, qb_torrent_id FROM capacity_task_bindings WHERE storage_target_id = ? AND qb_torrent_id = ?`, request.StorageTargetID, request.QBTorrentID).Scan(&bindingTarget, &bindingTorrent); err == nil {
			return fmt.Errorf("%w: torrent already bound", ErrNotConfirmable)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read binding: %w", err)
		}
		if request.SyncGeneration < request.PreAddSyncGeneration || (request.SyncGeneration == request.PreAddSyncGeneration && request.SnapshotRevision <= request.PreAddSnapshotRevision) || request.SnapshotDigest == request.PreAddSnapshotDigest {
			return ErrStaleSnapshot
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO capacity_task_bindings(binding_id, storage_target_id, qb_torrent_id, identity_key, owner_kind, reservation_id, amount_left_state, amount_left_error_code, uncertainty_debit_kind, uncertainty_debit_bytes, snapshot_generation, snapshot_revision, snapshot_digest, updated_at) VALUES (?, ?, ?, ?, 'RESERVATION', ?, 'UNKNOWN', 'MISSING_IN_FULL_SNAPSHOT', 'NONE', 0, ?, ?, ?, ?)`, request.BindingID, request.StorageTargetID, request.QBTorrentID, request.IdentityKey, request.ReservationID, request.SyncGeneration, request.SnapshotRevision, request.SnapshotDigest, request.Now.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("insert binding: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state=?, side_effect_fact='OBSERVED_EFFECT', qb_torrent_id=?, confirmation_sync_generation=?, confirmation_snapshot_revision=?, confirmation_evidence_digest=?, confirmed_at=? WHERE attempt_id=? AND attempt_state=?`, confirmTarget, request.QBTorrentID, request.SyncGeneration, request.SnapshotRevision, request.EvidenceDigest, request.Now.UTC().Format(time.RFC3339), request.AttemptID, attemptState); err != nil {
			return fmt.Errorf("confirm attempt: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state=?, observation_state='ACTIVE', updated_at=? WHERE job_id=? AND job_state=?`, confirmTarget, request.Now.UTC().Format(time.RFC3339), request.JobID, expectedJobState); err != nil {
			return fmt.Errorf("confirm job: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE capacity_reservations SET reservation_state='REFLECTING', revision=revision+1 WHERE reservation_id=? AND reservation_state='ACTIVE'`, request.ReservationID); err != nil {
			return fmt.Errorf("advance reservation: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO race_events(job_id, attempt_id, event_type, from_state, to_state, reason_code, safe_metadata_json, occurred_at) VALUES (?, ?, 'attempt.state.changed', ?, ?, 'IDENTITY_CONFIRMED', '{}', ?)`, request.JobID, request.AttemptID, attemptState, confirmTarget, request.Now.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("insert confirmation event: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func validate(request Request) error {
	for name, value := range map[string]string{"binding_id": request.BindingID, "attempt_id": request.AttemptID, "submit_intent_id": request.SubmitIntentID, "reservation_id": request.ReservationID, "job_id": request.JobID, "storage_target_id": request.StorageTargetID, "identity_key": request.IdentityKey, "qb_torrent_id": request.QBTorrentID, "snapshot_digest": request.SnapshotDigest, "pre_add_snapshot_digest": request.PreAddSnapshotDigest, "evidence_digest": request.EvidenceDigest} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.Now.IsZero() || request.SyncGeneration < 0 || request.SnapshotRevision < 0 || request.PreAddSyncGeneration < 0 || request.PreAddSnapshotRevision < 0 {
		return errors.New("confirmation facts are invalid")
	}
	if request.SyncGeneration < request.PreAddSyncGeneration || (request.SyncGeneration == request.PreAddSyncGeneration && request.SnapshotRevision <= request.PreAddSnapshotRevision) || request.SnapshotDigest == request.PreAddSnapshotDigest {
		return ErrStaleSnapshot
	}
	return nil
}
