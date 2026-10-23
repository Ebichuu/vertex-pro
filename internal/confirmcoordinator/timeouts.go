package confirmcoordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/unknownreconciler"
)

// OnSyncUnavailable advances deadlines without inventing downloader evidence.
// A timeout retains the reservation and identity ownership and never retries Add.
func (coordinator *Coordinator) OnSyncUnavailable(ctx context.Context, instanceID string, now time.Time) ([]Outcome, error) {
	if instanceID == "" || now.IsZero() {
		return nil, fmt.Errorf("instance id and time are required")
	}
	if err := coordinator.advanceConfirmationTimeouts(ctx, instanceID, now); err != nil {
		return nil, err
	}
	resolved, err := unknownreconciler.Sweep(ctx, coordinator.database, eventSnapshotProvider{}, unknownreconciler.SweepInput{InstanceID: instanceID, Now: now.UTC()})
	if err != nil {
		return nil, err
	}
	outcomes := make([]Outcome, 0, len(resolved))
	for _, outcome := range resolved {
		outcomes = append(outcomes, Outcome{AttemptID: outcome.AttemptID, Action: outcome.Outcome})
	}
	return outcomes, nil
}

func (coordinator *Coordinator) advanceConfirmationTimeouts(ctx context.Context, instanceID string, now time.Time) error {
	return db.RunImmediate(ctx, coordinator.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		rows, err := tx.QueryContext(ctx, `SELECT a.attempt_id,a.job_id,w.confirmation_deadline FROM placement_attempts a JOIN sync_waiters w ON w.attempt_id=a.attempt_id WHERE a.instance_id=? AND a.attempt_state='AWAITING_CONFIRMATION' AND w.waiter_state='ACTIVE' ORDER BY a.attempt_id`, instanceID)
		if err != nil {
			return fmt.Errorf("list confirmation deadlines: %w", err)
		}
		type chain struct{ attemptID, jobID string }
		expired := []chain{}
		for rows.Next() {
			var item chain
			var raw string
			if err := rows.Scan(&item.attemptID, &item.jobID, &raw); err != nil {
				rows.Close()
				return err
			}
			deadline, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				rows.Close()
				return fmt.Errorf("invalid confirmation deadline: %w", err)
			}
			if !now.Before(deadline) {
				expired = append(expired, item)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		timestamp := now.UTC().Format(time.RFC3339Nano)
		for _, item := range expired {
			update, err := tx.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='UNKNOWN' WHERE attempt_id=? AND attempt_state='AWAITING_CONFIRMATION' AND dispatch_state='STARTED' AND side_effect_fact='POSSIBLE_EFFECT'`, item.attemptID)
			if err != nil {
				return err
			}
			if count, err := update.RowsAffected(); err != nil || count != 1 {
				return fmt.Errorf("confirmation timeout attempt facts mismatch: %s", item.attemptID)
			}
			update, err = tx.ExecContext(ctx, `UPDATE race_jobs SET job_state='UNKNOWN',updated_at=? WHERE job_id=? AND job_state='AWAITING_CONFIRMATION' AND current_attempt_id=?`, timestamp, item.jobID, item.attemptID)
			if err != nil {
				return err
			}
			if count, err := update.RowsAffected(); err != nil || count != 1 {
				return fmt.Errorf("confirmation timeout job facts mismatch: %s", item.jobID)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO race_events(job_id,attempt_id,event_type,from_state,to_state,reason_code,safe_metadata_json,occurred_at) VALUES (?,?,'attempt.state.changed','AWAITING_CONFIRMATION','UNKNOWN','CONFIRMATION_DEADLINE_EXPIRED','{}',?)`, item.jobID, item.attemptID, timestamp); err != nil {
				return err
			}
		}
		return nil
	})
}
