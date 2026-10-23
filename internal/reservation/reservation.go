package reservation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

const (
	JobStateReady          = "READY"
	RuleStateMatched       = "MATCHED"
	TorrentStateReady      = "READY"
	PlacementStatePrepared = "PREPARED"
	CapacityStateTrusted   = "TRUSTED"
	StorageStateVerified   = "VERIFIED"
	IdentityAbsent         = "ABSENT"
	IdentityPresent        = "PRESENT"
)

type Request struct {
	JobID                         string
	AttemptID                     string
	SubmitIntentID                string
	ReservationID                 string
	IdentityKey                   string
	InstanceID                    string
	StorageTargetID               string
	RequiredBytes                 int64
	CapacityEpochID               string
	AllocationEpoch               int64
	CapacityRevision              int64
	SnapshotGeneration            int64
	SnapshotRevision              int64
	SnapshotDigest                string
	RequestFingerprint            string
	TargetFingerprint             string
	PreAddIdentityFact            string
	PreAddQBTorrentID             string
	FinalCandidateRecheckRevision int64
	MaxInflight                   int64
	Now                           string
}

type Result struct {
	AttemptID       string
	SubmitIntentID  string
	ReservationID   string
	AttemptNo       int64
	AllocationEpoch int64
}

func ReserveAtomic(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	result := Result{AttemptID: request.AttemptID, SubmitIntentID: request.SubmitIntentID, ReservationID: request.ReservationID, AllocationEpoch: request.AllocationEpoch}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var jobState, ruleState, torrentState, placementState string
		var currentAttempt sql.NullString
		var preparationRevision int64
		err := transaction.QueryRowContext(ctx, `SELECT job_state, rule_state, torrent_state, placement_state, preparation_revision, current_attempt_id FROM race_jobs WHERE job_id = ?`, request.JobID).Scan(&jobState, &ruleState, &torrentState, &placementState, &preparationRevision, &currentAttempt)
		if err == sql.ErrNoRows {
			return errors.New("RACE_JOB_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read race job: %w", err)
		}
		if jobState != JobStateReady || currentAttempt.Valid {
			return errors.New("RACE_JOB_NOT_READY")
		}
		if ruleState != RuleStateMatched || torrentState != TorrentStateReady || placementState != PlacementStatePrepared {
			return errors.New("PREPARATION_NOT_READY")
		}
		if preparationRevision < 0 {
			return errors.New("PREPARATION_REVISION_INVALID")
		}

		var instanceID, storageState string
		err = transaction.QueryRowContext(ctx, `SELECT instance_id, storage_verification_state FROM storage_targets WHERE storage_target_id = ?`, request.StorageTargetID).Scan(&instanceID, &storageState)
		if err == sql.ErrNoRows {
			return errors.New("STORAGE_TARGET_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read storage target: %w", err)
		}
		if instanceID != request.InstanceID {
			return errors.New("STORAGE_TARGET_INSTANCE_MISMATCH")
		}
		if storageState != StorageStateVerified {
			return errors.New("STORAGE_UNVERIFIED")
		}

		var epochState string
		var effectiveFree sql.NullInt64
		var storedEpoch, storedGeneration, storedRevision int64
		var storedDigest string
		err = transaction.QueryRowContext(ctx, `SELECT capacity_state, effective_free_bytes, allocation_epoch, snapshot_generation, snapshot_revision, snapshot_digest FROM capacity_epochs WHERE capacity_epoch_id = ? AND storage_target_id = ?`, request.CapacityEpochID, request.StorageTargetID).Scan(&epochState, &effectiveFree, &storedEpoch, &storedGeneration, &storedRevision, &storedDigest)
		if err == sql.ErrNoRows {
			return errors.New("CAPACITY_EPOCH_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read capacity epoch: %w", err)
		}
		if epochState != CapacityStateTrusted || !effectiveFree.Valid {
			return errors.New("CAPACITY_NOT_TRUSTED")
		}
		if effectiveFree.Int64 < request.RequiredBytes {
			return errors.New("INSUFFICIENT_EFFECTIVE_FREE")
		}
		if storedEpoch != request.AllocationEpoch || storedGeneration != request.SnapshotGeneration || storedRevision != request.SnapshotRevision || storedDigest != request.SnapshotDigest {
			return errors.New("CAPACITY_REVISION_MISMATCH")
		}

		var inflight int64
		// Confirmed submissions retain their identity and capacity ownership,
		// but no longer occupy an in-flight Add slot.
		err = transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM placement_attempts WHERE instance_id = ? AND attempt_state IN ('RESERVED','ADD_SENT','AWAITING_CONFIRMATION','UNKNOWN','EXPIRED_UNKNOWN')`, request.InstanceID).Scan(&inflight)
		if err != nil {
			return fmt.Errorf("count inflight attempts: %w", err)
		}
		if inflight >= request.MaxInflight {
			return errors.New("INFLIGHT_LIMIT_REACHED")
		}

		err = transaction.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM placement_attempts WHERE job_id = ?`, request.JobID).Scan(&result.AttemptNo)
		if err != nil {
			return fmt.Errorf("allocate attempt number: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES (?, ?, ?, ?, ?, ?, 'RESERVED', 'NOT_STARTED', ?, ?, ?)`, request.AttemptID, request.JobID, request.IdentityKey, result.AttemptNo, request.InstanceID, request.StorageTargetID, request.AllocationEpoch, request.RequiredBytes, request.Now); err != nil {
			return fmt.Errorf("insert placement attempt: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, pre_add_qb_torrent_id, created_at) VALUES (?, ?, ?, ?, ?, ?, 'NOT_STARTED', ?, ?, ?, ?, ?, ?)`, request.SubmitIntentID, request.AttemptID, request.InstanceID, request.IdentityKey, request.RequestFingerprint, request.TargetFingerprint, request.SnapshotGeneration, request.SnapshotRevision, request.SnapshotDigest, request.PreAddIdentityFact, nullable(request.PreAddQBTorrentID), request.Now); err != nil {
			return fmt.Errorf("insert submit intent: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state) VALUES (?, ?, ?, ?, ?, 'ACTIVE')`, request.ReservationID, request.AttemptID, request.StorageTargetID, request.RequiredBytes, request.AllocationEpoch); err != nil {
			return fmt.Errorf("insert reservation: %w", err)
		}
		resultUpdate, err := transaction.ExecContext(ctx, `UPDATE race_jobs SET current_attempt_id = ?, job_state = 'RESERVED', final_candidate_recheck_revision = ?, final_candidate_recheck_at = ?, updated_at = ? WHERE job_id = ? AND job_state = 'READY' AND current_attempt_id IS NULL`, request.AttemptID, request.FinalCandidateRecheckRevision, request.Now, request.Now, request.JobID)
		if err != nil {
			return fmt.Errorf("update race job reservation: %w", err)
		}
		rows, err := resultUpdate.RowsAffected()
		if err != nil {
			return fmt.Errorf("read race job update count: %w", err)
		}
		if rows != 1 {
			return errors.New("RACE_JOB_RESERVATION_UPDATE_CONFLICT")
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func validateRequest(request Request) error {
	values := map[string]string{"job_id": request.JobID, "attempt_id": request.AttemptID, "submit_intent_id": request.SubmitIntentID, "reservation_id": request.ReservationID, "identity_key": request.IdentityKey, "instance_id": request.InstanceID, "storage_target_id": request.StorageTargetID, "capacity_epoch_id": request.CapacityEpochID, "snapshot_digest": request.SnapshotDigest, "request_fingerprint": request.RequestFingerprint, "target_fingerprint": request.TargetFingerprint, "now": request.Now}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.RequiredBytes < 0 || request.AllocationEpoch < 0 || request.CapacityRevision < 0 || request.SnapshotGeneration < 0 || request.SnapshotRevision < 0 || request.FinalCandidateRecheckRevision < 0 {
		return errors.New("numeric request field is invalid")
	}
	if request.MaxInflight <= 0 {
		return errors.New("max inflight is invalid")
	}
	if request.PreAddIdentityFact != IdentityAbsent && request.PreAddIdentityFact != IdentityPresent {
		return errors.New("pre-add identity fact is invalid")
	}
	if request.PreAddIdentityFact == IdentityAbsent && request.PreAddQBTorrentID != "" {
		return errors.New("absent identity cannot have qB torrent id")
	}
	if request.PreAddIdentityFact == IdentityPresent && request.PreAddQBTorrentID == "" {
		return errors.New("present identity requires qB torrent id")
	}
	return nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
