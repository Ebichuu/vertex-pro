package capacityreconciler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/capacityprojection"
	"github.com/vertex-pro/vertex-pro/internal/db"
)

const (
	StateTrusted          = "TRUSTED"
	StateUncertain        = "CAPACITY_UNCERTAIN"
	StateReconciling      = "RECONCILING"
	ReservationActive     = "ACTIVE"
	ReservationReflecting = "REFLECTING"
)

type Request struct {
	StorageTargetID     string
	CapacityEpochID     string
	PhysicalFreeBytes   int64
	SnapshotGeneration  int64
	SnapshotRevision    int64
	SnapshotDigest      string
	StableSnapshotCount int
	CompleteBaseline    bool
	OwnersUnique        bool
	LiabilitiesCovered  bool
	NoUnknownAmountLeft bool
	NoUnquantifiedRisk  bool
	SafetyMarginBytes   int64
	CreatedAt           string
}

type Result struct {
	State              string
	CapacityEpochID    string
	AllocationEpoch    int64
	EffectiveFreeBytes *int64
	Reasons            []string
}

func Reconcile(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	result := Result{State: StateReconciling, CapacityEpochID: request.CapacityEpochID, Reasons: make([]string, 0)}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var currentState, targetID string
		var currentAllocation int64
		err := transaction.QueryRowContext(ctx, `SELECT storage_target_id, allocation_epoch, capacity_state FROM capacity_epochs WHERE capacity_epoch_id = ?`, request.CapacityEpochID).Scan(&targetID, &currentAllocation, &currentState)
		if err == sql.ErrNoRows {
			return errors.New("CAPACITY_EPOCH_NOT_FOUND")
		}
		if err != nil {
			return fmt.Errorf("read reconciliation epoch: %w", err)
		}
		if targetID != request.StorageTargetID {
			return errors.New("STORAGE_TARGET_MISMATCH")
		}
		if currentState == StateTrusted {
			return errors.New("RECONCILIATION_ALREADY_TRUSTED")
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE capacity_epochs SET capacity_state = 'RECONCILING', physical_free_bytes = NULL, effective_free_bytes = NULL WHERE capacity_epoch_id = ?`, request.CapacityEpochID); err != nil {
			return fmt.Errorf("mark capacity reconciling: %w", err)
		}

		reasons := reconciliationReasons(request)
		unknownCount, err := countUnknownAmounts(ctx, transaction, request.StorageTargetID)
		if err != nil {
			return err
		}
		if unknownCount > 0 {
			reasons = append(reasons, "UNKNOWN_AMOUNT_LEFT_PRESENT")
		}
		if len(reasons) > 0 {
			result.State = StateReconciling
			result.Reasons = reasons
			return nil
		}

		entries, err := readLedgerEntries(ctx, transaction, request.StorageTargetID, request.SafetyMarginBytes)
		if err != nil {
			return err
		}
		projection := capacityprojection.Project(capacityprojection.Input{PhysicalFreeBytes: request.PhysicalFreeBytes, Entries: entries})
		if projection.State != capacityprojection.StateTrusted || projection.EffectiveFreeBytes == nil {
			result.State = StateUncertain
			result.Reasons = append(result.Reasons, projection.Reasons...)
			return nil
		}
		newAllocation := currentAllocation + 1
		newEpochID := fmt.Sprintf("%s-reconciled-%d", request.CapacityEpochID, newAllocation)
		if _, err := transaction.ExecContext(ctx, `INSERT INTO capacity_epochs(capacity_epoch_id, storage_target_id, allocation_epoch, capacity_state, physical_free_bytes, effective_free_bytes, snapshot_generation, snapshot_revision, snapshot_digest, created_at) VALUES (?, ?, ?, 'TRUSTED', ?, ?, ?, ?, ?, ?)`, newEpochID, request.StorageTargetID, newAllocation, request.PhysicalFreeBytes, *projection.EffectiveFreeBytes, request.SnapshotGeneration, request.SnapshotRevision, request.SnapshotDigest, request.CreatedAt); err != nil {
			return fmt.Errorf("publish reconciled epoch: %w", err)
		}
		result.State = StateTrusted
		result.CapacityEpochID = newEpochID
		result.AllocationEpoch = newAllocation
		result.EffectiveFreeBytes = projection.EffectiveFreeBytes
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func reconciliationReasons(request Request) []string {
	reasons := make([]string, 0)
	if request.StableSnapshotCount < 2 {
		reasons = append(reasons, "STABLE_SNAPSHOT_COUNT_INSUFFICIENT")
	}
	if !request.CompleteBaseline {
		reasons = append(reasons, "COMPLETE_BASELINE_REQUIRED")
	}
	if !request.OwnersUnique {
		reasons = append(reasons, "OWNER_UNIQUENESS_UNPROVEN")
	}
	if !request.LiabilitiesCovered {
		reasons = append(reasons, "LIABILITIES_NOT_COVERED")
	}
	if !request.NoUnknownAmountLeft {
		reasons = append(reasons, "UNKNOWN_AMOUNT_LEFT_UNPROVEN")
	}
	if !request.NoUnquantifiedRisk {
		reasons = append(reasons, "UNQUANTIFIED_RISK_PRESENT")
	}
	return reasons
}

func countUnknownAmounts(ctx context.Context, transaction *db.ImmediateTx, storageTargetID string) (int, error) {
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM capacity_task_bindings WHERE storage_target_id = ? AND amount_left_state = 'UNKNOWN'`, storageTargetID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unknown amount left: %w", err)
	}
	return count, nil
}

func readLedgerEntries(ctx context.Context, transaction *db.ImmediateTx, storageTargetID string, safetyMargin int64) ([]capacityprojection.LedgerEntry, error) {
	entries := []capacityprojection.LedgerEntry{{Key: "safety-margin", Kind: capacityprojection.KindSafetyMargin, Bytes: safetyMargin}}
	rows, err := transaction.QueryContext(ctx, `SELECT reservation_id, reserved_bytes FROM capacity_reservations WHERE storage_target_id = ? AND reservation_state IN ('ACTIVE','REFLECTING')`, storageTargetID)
	if err != nil {
		return nil, fmt.Errorf("read active reservations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var bytes int64
		if err := rows.Scan(&id, &bytes); err != nil {
			return nil, fmt.Errorf("scan active reservation: %w", err)
		}
		entries = append(entries, capacityprojection.LedgerEntry{Key: "reservation:" + id, Kind: capacityprojection.KindReservation, Bytes: bytes})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active reservations: %w", err)
	}
	rows, err = transaction.QueryContext(ctx, `SELECT binding_id, COALESCE(amount_left, 0), uncertainty_debit_bytes FROM capacity_task_bindings WHERE storage_target_id = ? AND owner_kind = 'UNMANAGED' AND amount_left_state = 'KNOWN'`, storageTargetID)
	if err != nil {
		return nil, fmt.Errorf("read unmanaged liabilities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var amount, debit int64
		if err := rows.Scan(&id, &amount, &debit); err != nil {
			return nil, fmt.Errorf("scan unmanaged liability: %w", err)
		}
		entries = append(entries, capacityprojection.LedgerEntry{Key: "unmanaged:" + id, Kind: capacityprojection.KindUnmanagedLiability, Bytes: amount + debit})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unmanaged liabilities: %w", err)
	}
	return entries, nil
}

func validateRequest(request Request) error {
	values := map[string]string{"storage_target_id": request.StorageTargetID, "capacity_epoch_id": request.CapacityEpochID, "snapshot_digest": request.SnapshotDigest, "created_at": request.CreatedAt}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.PhysicalFreeBytes < 0 || request.SnapshotGeneration < 0 || request.SnapshotRevision < 0 || request.SafetyMarginBytes < 0 {
		return errors.New("reconciliation numeric field is invalid")
	}
	return nil
}
