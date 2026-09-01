package db

import (
	"context"
	"errors"
	"fmt"
)

// AttemptUpdate contains only the mutable PlacementAttempt columns.
type AttemptUpdate struct {
	AttemptState                 *string
	DispatchState                *string
	SideEffectFact               *string
	QBTorrentID                  *string
	ConfirmationSyncGeneration   *int64
	ConfirmationSnapshotRevision *int64
	ConfirmationEvidenceDigest   *string
	ConfirmedAt                  *string
	FinishedAt                   *string
}

// Repository exposes explicit allowlisted persistence operations.
type Repository struct{}

// UpdatePlacementAttempt updates only mutable Attempt facts.
func (Repository) UpdatePlacementAttempt(ctx context.Context, transaction *ImmediateTx, attemptID string, update AttemptUpdate) error {
	if transaction == nil {
		return errors.New("transaction is required")
	}
	if attemptID == "" {
		return errors.New("attempt id is required")
	}
	assignments := make([]string, 0, 9)
	arguments := make([]any, 0, 10)
	if update.AttemptState != nil {
		assignments = append(assignments, "attempt_state = ?")
		arguments = append(arguments, *update.AttemptState)
	}
	if update.DispatchState != nil {
		assignments = append(assignments, "dispatch_state = ?")
		arguments = append(arguments, *update.DispatchState)
	}
	if update.SideEffectFact != nil {
		assignments = append(assignments, "side_effect_fact = ?")
		arguments = append(arguments, *update.SideEffectFact)
	}
	if update.QBTorrentID != nil {
		assignments = append(assignments, "qb_torrent_id = ?")
		arguments = append(arguments, *update.QBTorrentID)
	}
	if update.ConfirmationSyncGeneration != nil {
		assignments = append(assignments, "confirmation_sync_generation = ?")
		arguments = append(arguments, *update.ConfirmationSyncGeneration)
	}
	if update.ConfirmationSnapshotRevision != nil {
		assignments = append(assignments, "confirmation_snapshot_revision = ?")
		arguments = append(arguments, *update.ConfirmationSnapshotRevision)
	}
	if update.ConfirmationEvidenceDigest != nil {
		assignments = append(assignments, "confirmation_evidence_digest = ?")
		arguments = append(arguments, *update.ConfirmationEvidenceDigest)
	}
	if update.ConfirmedAt != nil {
		assignments = append(assignments, "confirmed_at = ?")
		arguments = append(arguments, *update.ConfirmedAt)
	}
	if update.FinishedAt != nil {
		assignments = append(assignments, "finished_at = ?")
		arguments = append(arguments, *update.FinishedAt)
	}
	if len(assignments) == 0 {
		return errors.New("attempt update is empty")
	}
	arguments = append(arguments, attemptID)
	query := "UPDATE placement_attempts SET " + joinAssignments(assignments) + " WHERE attempt_id = ?"
	result, err := transaction.ExecContext(ctx, query, arguments...)
	if err != nil {
		return fmt.Errorf("update placement attempt: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read placement attempt update count: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("placement attempt %q not found", attemptID)
	}
	return nil
}

func joinAssignments(assignments []string) string {
	result := assignments[0]
	for _, assignment := range assignments[1:] {
		result += ", " + assignment
	}
	return result
}
