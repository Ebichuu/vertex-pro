package authoritativeremoval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

const (
	AttemptUnknown                = "UNKNOWN"
	AttemptExpiredUnknown         = "EXPIRED_UNKNOWN"
	AttemptReconciledNoEffect     = "RECONCILED_NO_EFFECT"
	JobUnknown                    = "UNKNOWN"
	JobExpiredUnknown             = "EXPIRED_UNKNOWN"
	JobReconciledNoEffect         = "RECONCILED_NO_EFFECT"
	ReservationActive             = "ACTIVE"
	ReservationReflecting         = "REFLECTING"
	ReservationReleased           = "RELEASED"
	ReleaseReasonQBTorrentRemoved = "TERMINAL_QB_REMOVED"
	OwnerReservation              = "RESERVATION"
)

type Request struct {
	ReservationID                 string
	BindingID                     string
	AttemptID                     string
	CapacityEpochID               string
	QBTorrentID                   string
	IdentityKey                   string
	SnapshotGeneration            int64
	SnapshotRevision              int64
	SnapshotDigest                string
	PhysicalFreeBytes             int64
	FreshPhysicalFree             bool
	AuthoritativeRemovalConfirmed bool
	RemovalObservedAt             string
	ReleasedAt                    string
}

type Result struct {
	ReservationID           string
	BindingID               string
	ReleasedUnconsumedBytes int64
}

func ReleaseRemoved(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	result := Result{ReservationID: request.ReservationID, BindingID: request.BindingID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var attemptID, storageTargetID, attemptState, jobID, jobState string
		var reservedBytes, reflectedBytes int64
		var reservationState string
		err := transaction.QueryRowContext(ctx, `SELECT r.attempt_id, r.storage_target_id, r.reserved_bytes, r.reflected_physical_bytes, r.reservation_state, a.attempt_state, a.job_id, j.job_state FROM capacity_reservations r JOIN placement_attempts a ON a.attempt_id = r.attempt_id JOIN race_jobs j ON j.job_id = a.job_id WHERE r.reservation_id = ?`, request.ReservationID).Scan(&attemptID, &storageTargetID, &reservedBytes, &reflectedBytes, &reservationState, &attemptState, &jobID, &jobState)
		if err == sql.ErrNoRows {
			return errors.New("RESERVATION_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read removal chain: %w", err)
		}
		if attemptID != request.AttemptID {
			return errors.New("ATTEMPT_MISMATCH")
		}
		if attemptState != AttemptUnknown && attemptState != AttemptExpiredUnknown {
			return errors.New("ATTEMPT_NOT_REMOVAL_READY")
		}
		if jobState != JobUnknown && jobState != JobExpiredUnknown {
			return errors.New("JOB_NOT_REMOVAL_READY")
		}
		if reservationState != ReservationActive && reservationState != ReservationReflecting {
			return errors.New("RESERVATION_NOT_ACTIVE")
		}
		if reflectedBytes != 0 {
			return errors.New("RESERVATION_ALREADY_REFLECTED")
		}
		if !request.AuthoritativeRemovalConfirmed {
			return errors.New("REMOVAL_NOT_AUTHORITATIVE")
		}
		if !request.FreshPhysicalFree {
			return errors.New("PHYSICAL_FREE_NOT_FRESH")
		}

		var epochState string
		var physicalFree sql.NullInt64
		var generation, revision int64
		var digest string
		err = transaction.QueryRowContext(ctx, `SELECT capacity_state, physical_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest FROM capacity_epochs WHERE capacity_epoch_id = ? AND storage_target_id = ?`, request.CapacityEpochID, storageTargetID).Scan(&epochState, &physicalFree, &generation, &revision, &digest)
		if err == sql.ErrNoRows {
			return errors.New("CAPACITY_EPOCH_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read removal capacity evidence: %w", err)
		}
		if epochState != "TRUSTED" || !physicalFree.Valid {
			return errors.New("CAPACITY_NOT_TRUSTED")
		}
		if physicalFree.Int64 != request.PhysicalFreeBytes || generation != request.SnapshotGeneration || revision != request.SnapshotRevision || digest != request.SnapshotDigest {
			return errors.New("PHYSICAL_FREE_EVIDENCE_MISMATCH")
		}

		var boundTarget, boundTorrent, boundIdentity, ownerKind, boundReservation string
		err = transaction.QueryRowContext(ctx, `SELECT storage_target_id, qb_torrent_id, COALESCE(identity_key, ''), owner_kind, COALESCE(reservation_id, '') FROM capacity_task_bindings WHERE binding_id = ?`, request.BindingID).Scan(&boundTarget, &boundTorrent, &boundIdentity, &ownerKind, &boundReservation)
		if err == sql.ErrNoRows {
			return errors.New("BINDING_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read removal binding: %w", err)
		}
		if ownerKind != OwnerReservation || boundReservation != request.ReservationID || boundTarget != storageTargetID {
			return errors.New("BINDING_RESERVATION_MISMATCH")
		}
		if boundTorrent != request.QBTorrentID || boundIdentity != request.IdentityKey {
			return errors.New("BINDING_IDENTITY_MISMATCH")
		}

		var attemptTorrent, attemptIdentity string
		err = transaction.QueryRowContext(ctx, `SELECT COALESCE(qb_torrent_id, ''), identity_key FROM placement_attempts WHERE attempt_id = ?`, request.AttemptID).Scan(&attemptTorrent, &attemptIdentity)
		if err == sql.ErrNoRows {
			return errors.New("ATTEMPT_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read removal attempt identity: %w", err)
		}
		if attemptTorrent != request.QBTorrentID || attemptIdentity != request.IdentityKey {
			return errors.New("ATTEMPT_IDENTITY_MISMATCH")
		}

		if _, err := transaction.ExecContext(ctx, `DELETE FROM capacity_task_bindings WHERE binding_id = ? AND owner_kind = 'RESERVATION' AND reservation_id = ?`, request.BindingID, request.ReservationID); err != nil {
			return fmt.Errorf("delete removed binding: %w", err)
		}
		releasedUnconsumed := reservedBytes - reflectedBytes
		if _, err := transaction.ExecContext(ctx, `UPDATE capacity_reservations SET reservation_state = 'RELEASED', release_reason = 'TERMINAL_QB_REMOVED', released_unconsumed_bytes = ?, released_at = ?, revision = revision + 1 WHERE reservation_id = ? AND reservation_state IN ('ACTIVE','REFLECTING')`, releasedUnconsumed, request.ReleasedAt, request.ReservationID); err != nil {
			return fmt.Errorf("release removed reservation: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state = 'RECONCILED_NO_EFFECT', side_effect_fact = 'OBSERVED_EFFECT', finished_at = ? WHERE attempt_id = ? AND attempt_state IN ('UNKNOWN','EXPIRED_UNKNOWN')`, request.ReleasedAt, request.AttemptID); err != nil {
			return fmt.Errorf("finish removed attempt: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state = 'RECONCILED_NO_EFFECT', current_attempt_id = NULL, observation_state = 'CLOSED', updated_at = ? WHERE job_id = ? AND job_state IN ('UNKNOWN','EXPIRED_UNKNOWN')`, request.ReleasedAt, jobID); err != nil {
			return fmt.Errorf("finish removed job: %w", err)
		}
		result.ReleasedUnconsumedBytes = releasedUnconsumed
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func validateRequest(request Request) error {
	values := map[string]string{"reservation_id": request.ReservationID, "binding_id": request.BindingID, "attempt_id": request.AttemptID, "capacity_epoch_id": request.CapacityEpochID, "qb_torrent_id": request.QBTorrentID, "identity_key": request.IdentityKey, "snapshot_digest": request.SnapshotDigest, "removal_observed_at": request.RemovalObservedAt, "released_at": request.ReleasedAt}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.SnapshotGeneration < 0 || request.SnapshotRevision < 0 || request.PhysicalFreeBytes < 0 {
		return errors.New("removal evidence numeric field is invalid")
	}
	return nil
}
