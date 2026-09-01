package submitintent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

const (
	AttemptReserved    = "RESERVED"
	DispatchNotStarted = "NOT_STARTED"
	CapacityTrusted    = "TRUSTED"
	IdentityAbsent     = "ABSENT"
	IdentityPresent    = "PRESENT"
)

type Request struct {
	SubmitIntentID         string
	AttemptID              string
	CapacityEpochID        string
	InstanceID             string
	IdentityKey            string
	RequestFingerprint     string
	TargetFingerprint      string
	PreAddSyncGeneration   int64
	PreAddSnapshotRevision int64
	PreAddSnapshotDigest   string
	PreAddIdentityFact     string
	PreAddQBTorrentID      string
	CreatedAt              string
}

type Result struct {
	SubmitIntentID string
	AttemptID      string
}

func Create(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	result := Result{SubmitIntentID: request.SubmitIntentID, AttemptID: request.AttemptID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var instanceID, identityKey, storageTargetID, attemptState, dispatchState string
		var jobID string
		var currentAttempt sql.NullString
		err := transaction.QueryRowContext(ctx, `SELECT a.job_id, a.instance_id, a.identity_key, a.storage_target_id, a.attempt_state, a.dispatch_state, j.current_attempt_id FROM placement_attempts a JOIN race_jobs j ON j.job_id = a.job_id WHERE a.attempt_id = ?`, request.AttemptID).Scan(&jobID, &instanceID, &identityKey, &storageTargetID, &attemptState, &dispatchState, &currentAttempt)
		if err == sql.ErrNoRows {
			return errors.New("ATTEMPT_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read submit attempt: %w", err)
		}
		if instanceID != request.InstanceID || identityKey != request.IdentityKey {
			return errors.New("ATTEMPT_FACT_MISMATCH")
		}
		if attemptState != AttemptReserved || dispatchState != DispatchNotStarted {
			return errors.New("ATTEMPT_NOT_READY_FOR_INTENT")
		}
		if jobID == "" {
			return errors.New("ATTEMPT_JOB_MISSING")
		}
		var jobState string
		if err := transaction.QueryRowContext(ctx, `SELECT job_state FROM race_jobs WHERE job_id = ?`, jobID).Scan(&jobState); err != nil {
			return fmt.Errorf("read submit job: %w", err)
		}
		if jobState == "READY" {
			if currentAttempt.Valid {
				return errors.New("CURRENT_ATTEMPT_ALREADY_SET")
			}
		} else if jobState == "RESERVED" {
			if !currentAttempt.Valid || currentAttempt.String != request.AttemptID {
				return errors.New("CURRENT_ATTEMPT_MISMATCH")
			}
		} else {
			return errors.New("JOB_NOT_READY_FOR_INTENT")
		}

		var epochTarget, epochState, digest string
		var generation, revision int64
		err = transaction.QueryRowContext(ctx, `SELECT storage_target_id, capacity_state, snapshot_generation, snapshot_revision, snapshot_digest FROM capacity_epochs WHERE capacity_epoch_id = ?`, request.CapacityEpochID).Scan(&epochTarget, &epochState, &generation, &revision, &digest)
		if err == sql.ErrNoRows {
			return errors.New("CAPACITY_EPOCH_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read submit baseline: %w", err)
		}
		if epochTarget != storageTargetID {
			return errors.New("BASELINE_STORAGE_TARGET_MISMATCH")
		}
		if epochState != CapacityTrusted {
			return errors.New("BASELINE_CAPACITY_NOT_TRUSTED")
		}
		if generation != request.PreAddSyncGeneration || revision != request.PreAddSnapshotRevision || digest != request.PreAddSnapshotDigest {
			return errors.New("BASELINE_NOT_FRESH")
		}

		if _, err := transaction.ExecContext(ctx, `INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, pre_add_qb_torrent_id, created_at) VALUES (?, ?, ?, ?, ?, ?, 'NOT_STARTED', ?, ?, ?, ?, ?, ?)`, request.SubmitIntentID, request.AttemptID, request.InstanceID, request.IdentityKey, request.RequestFingerprint, request.TargetFingerprint, request.PreAddSyncGeneration, request.PreAddSnapshotRevision, request.PreAddSnapshotDigest, request.PreAddIdentityFact, nullable(request.PreAddQBTorrentID), request.CreatedAt); err != nil {
			return fmt.Errorf("insert submit intent: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func validateRequest(request Request) error {
	values := map[string]string{"submit_intent_id": request.SubmitIntentID, "attempt_id": request.AttemptID, "capacity_epoch_id": request.CapacityEpochID, "instance_id": request.InstanceID, "identity_key": request.IdentityKey, "request_fingerprint": request.RequestFingerprint, "target_fingerprint": request.TargetFingerprint, "pre_add_snapshot_digest": request.PreAddSnapshotDigest, "pre_add_identity_fact": request.PreAddIdentityFact, "created_at": request.CreatedAt}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.PreAddSyncGeneration < 0 || request.PreAddSnapshotRevision < 0 {
		return errors.New("baseline revision is invalid")
	}
	if request.PreAddIdentityFact != IdentityAbsent && request.PreAddIdentityFact != IdentityPresent {
		return errors.New("PRE_ADD_IDENTITY_FACT_INVALID")
	}
	if request.PreAddIdentityFact == IdentityAbsent && strings.TrimSpace(request.PreAddQBTorrentID) != "" {
		return errors.New("ABSENT_IDENTITY_CANNOT_HAVE_QB_ID")
	}
	if request.PreAddIdentityFact == IdentityPresent && strings.TrimSpace(request.PreAddQBTorrentID) == "" {
		return errors.New("PRESENT_IDENTITY_REQUIRES_QB_ID")
	}
	return nil
}
