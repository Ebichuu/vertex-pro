package terminalrelease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

const (
	AttemptHashSeen                = "HASH_SEEN"
	AttemptReconciledConfirmed     = "RECONCILED_CONFIRMED"
	JobHashSeen                    = "HASH_SEEN"
	JobReconciledConfirmed         = "RECONCILED_CONFIRMED"
	ReservationActive              = "ACTIVE"
	ReservationReflecting          = "REFLECTING"
	ReservationReleased            = "RELEASED"
	ReleaseReasonTerminalCompleted = "TERMINAL_COMPLETED"
	OwnerReservation               = "RESERVATION"
	OwnerUnmanaged                 = "UNMANAGED"
	AmountKnown                    = "KNOWN"
)

type Request struct {
	ReservationID          string
	ReflectionID           string
	BindingID              string
	AttemptID              string
	CapacityEpochID        string
	SnapshotGeneration     int64
	SnapshotRevision       int64
	SnapshotDigest         string
	ReflectedPhysicalBytes int64
	PhysicalFreeBytes      int64
	FreshPhysicalFree      bool
	ObservedAt             string
	ReleasedAt             string
}

type Result struct {
	ReservationID           string
	ReflectionID            string
	BindingID               string
	ReleasedUnconsumedBytes int64
}

func ReleaseCompleted(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	result := Result{ReservationID: request.ReservationID, ReflectionID: request.ReflectionID, BindingID: request.BindingID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var attemptID, attemptState, jobID, jobState, storageTargetID string
		var reservedBytes, reflectedBytes int64
		var reservationState string
		err := transaction.QueryRowContext(ctx, `SELECT r.attempt_id, r.storage_target_id, r.reserved_bytes, r.reflected_physical_bytes, r.reservation_state, a.attempt_state, a.job_id, j.job_state FROM capacity_reservations r JOIN placement_attempts a ON a.attempt_id = r.attempt_id JOIN race_jobs j ON j.job_id = a.job_id WHERE r.reservation_id = ?`, request.ReservationID).Scan(&attemptID, &storageTargetID, &reservedBytes, &reflectedBytes, &reservationState, &attemptState, &jobID, &jobState)
		if err == sql.ErrNoRows {
			return errors.New("RESERVATION_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read completion chain: %w", err)
		}
		if attemptID != request.AttemptID {
			return errors.New("ATTEMPT_MISMATCH")
		}
		if attemptState != AttemptHashSeen && attemptState != AttemptReconciledConfirmed {
			return errors.New("ATTEMPT_NOT_COMPLETION_READY")
		}
		if jobState != JobHashSeen && jobState != JobReconciledConfirmed {
			return errors.New("JOB_NOT_COMPLETION_READY")
		}
		if reservationState != ReservationActive && reservationState != ReservationReflecting {
			return errors.New("RESERVATION_NOT_ACTIVE")
		}
		if reflectedBytes != 0 {
			return errors.New("RESERVATION_ALREADY_REFLECTED")
		}
		if request.ReflectedPhysicalBytes != reservedBytes {
			return errors.New("REFLECTION_MUST_EQUAL_RESERVED_BYTES")
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
			return fmt.Errorf("read completion capacity evidence: %w", err)
		}
		if epochState != "TRUSTED" || !physicalFree.Valid {
			return errors.New("CAPACITY_NOT_TRUSTED")
		}
		if !request.FreshPhysicalFree {
			return errors.New("PHYSICAL_FREE_NOT_FRESH")
		}
		if physicalFree.Int64 != request.PhysicalFreeBytes || generation != request.SnapshotGeneration || revision != request.SnapshotRevision || digest != request.SnapshotDigest {
			return errors.New("PHYSICAL_FREE_EVIDENCE_MISMATCH")
		}

		var ownerKind, boundReservation, amountState string
		var amountLeft sql.NullInt64
		err = transaction.QueryRowContext(ctx, `SELECT owner_kind, COALESCE(reservation_id, ''), amount_left_state, amount_left FROM capacity_task_bindings WHERE binding_id = ? AND storage_target_id = ?`, request.BindingID, storageTargetID).Scan(&ownerKind, &boundReservation, &amountState, &amountLeft)
		if err == sql.ErrNoRows {
			return errors.New("BINDING_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read completion binding: %w", err)
		}
		if ownerKind != OwnerReservation || boundReservation != request.ReservationID {
			return errors.New("BINDING_RESERVATION_MISMATCH")
		}
		if amountState != AmountKnown || !amountLeft.Valid || amountLeft.Int64 != 0 {
			return errors.New("AMOUNT_LEFT_NOT_ZERO_KNOWN")
		}

		if _, err := transaction.ExecContext(ctx, `INSERT INTO reservation_reflections(reflection_id, reservation_id, snapshot_generation, snapshot_revision, snapshot_digest, reflected_physical_bytes, observed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, request.ReflectionID, request.ReservationID, request.SnapshotGeneration, request.SnapshotRevision, request.SnapshotDigest, request.ReflectedPhysicalBytes, request.ObservedAt); err != nil {
			return fmt.Errorf("insert completion reflection: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE capacity_task_bindings SET owner_kind = 'UNMANAGED', reservation_id = NULL, uncertainty_debit_kind = 'NONE', uncertainty_debit_bytes = 0, revision = revision + 1, updated_at = ?, snapshot_generation = ?, snapshot_revision = ?, snapshot_digest = ? WHERE binding_id = ? AND owner_kind = 'RESERVATION' AND reservation_id = ?`, request.ReleasedAt, request.SnapshotGeneration, request.SnapshotRevision, request.SnapshotDigest, request.BindingID, request.ReservationID); err != nil {
			return fmt.Errorf("convert completion binding: %w", err)
		}
		releasedUnconsumed := reservedBytes - request.ReflectedPhysicalBytes
		if _, err := transaction.ExecContext(ctx, `UPDATE capacity_reservations SET reflected_physical_bytes = ?, reservation_state = 'RELEASED', release_reason = 'TERMINAL_COMPLETED', released_unconsumed_bytes = ?, released_at = ?, revision = revision + 1 WHERE reservation_id = ? AND reservation_state IN ('ACTIVE','REFLECTING')`, request.ReflectedPhysicalBytes, releasedUnconsumed, request.ReleasedAt, request.ReservationID); err != nil {
			return fmt.Errorf("release completed reservation: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state = 'RECONCILED_CONFIRMED', side_effect_fact = 'OBSERVED_EFFECT', finished_at = ? WHERE attempt_id = ? AND attempt_state IN ('HASH_SEEN','RECONCILED_CONFIRMED')`, request.ReleasedAt, request.AttemptID); err != nil {
			return fmt.Errorf("finish completed attempt: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE race_jobs SET job_state = 'RECONCILED_CONFIRMED', updated_at = ? WHERE job_id = ? AND job_state IN ('HASH_SEEN','RECONCILED_CONFIRMED')`, request.ReleasedAt, jobID); err != nil {
			return fmt.Errorf("finish completed job: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func validateRequest(request Request) error {
	values := map[string]string{"reservation_id": request.ReservationID, "reflection_id": request.ReflectionID, "binding_id": request.BindingID, "attempt_id": request.AttemptID, "capacity_epoch_id": request.CapacityEpochID, "snapshot_digest": request.SnapshotDigest, "observed_at": request.ObservedAt, "released_at": request.ReleasedAt}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.SnapshotGeneration < 0 || request.SnapshotRevision < 0 || request.ReflectedPhysicalBytes < 0 || request.PhysicalFreeBytes < 0 {
		return errors.New("completion evidence numeric field is invalid")
	}
	return nil
}
