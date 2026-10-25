package dispatchstage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchorchestrator"
)

const reselectAfter = 30 * time.Second
const maxAutomaticAttempts = 3

// Reselection retains the old reservation until a replacement can be committed.
// Only local evidence is consulted; the coordinator never waits for network IO.
func (c *Coordinator) reselect(ctx context.Context, origin *Stage, job jobRow) (*dispatchorchestrator.Result, error) {
	var attempt, instance, raw string
	var since sql.NullString
	var count int
	err := origin.database.QueryRowContext(ctx, `SELECT q.attempt_id,q.instance_id,q.params_json,q.unavailable_since,a.attempt_no
	 FROM dispatch_queue q JOIN placement_attempts a ON a.attempt_id=q.attempt_id
	 JOIN race_jobs j ON j.current_attempt_id=a.attempt_id
	 WHERE j.job_id=? AND j.job_state='RESERVED' AND a.attempt_state='RESERVED' AND a.dispatch_state='NOT_STARTED'`, job.jobID).Scan(&attempt, &instance, &raw, &since, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var frozen queuedParams
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		return nil, err
	}
	if frozen.Decision.Winner.TargetMode != "INSTANCE_GROUP" || count >= maxAutomaticAttempts {
		return nil, nil
	}
	if enabled, err := origin.ruleEnabled(ctx, job.jobID); err != nil || !enabled {
		return nil, err
	}
	available := false
	for _, s := range c.stages {
		if s.config.Storage.InstanceID != instance {
			continue
		}
		collect := s.config.CollectForSelection
		if collect == nil {
			collect = s.config.Collect
		}
		e, release, err := collect(ctx)
		if err == nil {
			err = s.validateSnapshot(ctx, s.database, e)
		}
		// Positive identity evidence prevents moving even when policy, path or
		// admission changed. External/manual additions must not be duplicated.
		if err == nil {
			if _, present := e.Snapshot.Torrents[job.identityKey]; present {
				if release != nil {
					release()
				}
				_, err := origin.database.ExecContext(ctx, `UPDATE dispatch_queue SET unavailable_since=NULL WHERE attempt_id=?`, attempt)
				return nil, err
			}
		}
		if err == nil {
			err = s.validateSavePath(frozen.Params.SavePath, e)
		}
		if err == nil {
			err = s.sendAdmission(ctx, s.database)
		}
		if err == nil && !s.policyTargetAllowed(ctx, frozen.Decision.Winner) {
			err = errors.New("GROUP_MEMBERSHIP_CHANGED")
		}
		actual, _ := json.Marshal(queuedParams{Params: frozen.Params, Profile: s.config.Profile, Storage: s.config.Storage, Decision: frozen.Decision})
		expected, _ := json.Marshal(frozen)
		if err == nil && string(actual) != string(expected) {
			err = errors.New("QUEUED_TARGET_CONFIGURATION_CHANGED")
		}
		if err == nil && frozen.Decision.Winner.SeedTimeSeconds > 0 {
			if s.config.EnsurePolicy == nil {
				err = errors.New("POLICY_NOT_READY")
			} else {
				_, err = s.config.EnsurePolicy(ctx, frozen.Decision.Winner)
			}
		}
		if err == nil {
			// Use the sender's projection, which already includes this task's
			// reservation. Do not require another task-size worth of free space.
			_, err = s.prepareCapacity(ctx, e)
		}
		if release != nil {
			release()
		}
		available = err == nil
		break
	}
	if available {
		_, err := origin.database.ExecContext(ctx, `UPDATE dispatch_queue SET unavailable_since=NULL WHERE attempt_id=?`, attempt)
		return nil, err
	}
	now := origin.config.Now().UTC()
	if !since.Valid {
		_, err := origin.database.ExecContext(ctx, `UPDATE dispatch_queue SET unavailable_since=? WHERE attempt_id=? AND unavailable_since IS NULL`, now.Format(time.RFC3339Nano), attempt)
		return nil, err
	}
	start, err := time.Parse(time.RFC3339Nano, since.String)
	if err != nil || now.Sub(start) < reselectAfter {
		return nil, err
	}
	job.replaceAttempt = attempt
	return c.dispatch(ctx, origin, job)
}

// Called inside the new reservation transaction. Any failure (including new
// capacity, duplicate identity or waiter persistence) rolls the old chain back.
// Begin and this transaction compete for the same SQLite writer lock: once
// STARTED wins, this proof cannot succeed and no replacement can be sent.
func (s *Stage) releaseForReselection(ctx context.Context, tx *db.ImmediateTx, job jobRow) error {
	var eligible int
	cutoff := s.config.Now().UTC().Add(-reselectAfter).Format(time.RFC3339Nano)
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM dispatch_queue q JOIN placement_attempts a ON a.attempt_id=q.attempt_id
	 WHERE q.attempt_id=? AND q.job_id=? AND q.instance_id<>? AND julianday(q.unavailable_since)<=julianday(?) AND a.attempt_no<?
	 AND json_extract(q.params_json,'$.Decision.Winner.TargetMode')='INSTANCE_GROUP'`, job.replaceAttempt, job.jobID, s.config.Storage.InstanceID, cutoff, maxAutomaticAttempts).Scan(&eligible); err != nil {
		return err
	}
	if eligible != 1 {
		return errors.New("RESELECT_NOT_ELIGIBLE")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM job_rule_snapshots snap JOIN instance_groups g ON g.group_id=json_extract(snap.aggregation_json,'$.Winner.TargetGroupID'),json_each(g.members_json) m WHERE snap.job_id=? AND m.value=?`, job.jobID, s.config.Storage.InstanceID).Scan(&eligible); err != nil {
		return err
	}
	if eligible != 1 {
		return errors.New("GROUP_MEMBERSHIP_CHANGED")
	}
	if err := s.cancelQueuedTx(ctx, tx, job.replaceAttempt, job.jobID, "TARGET_UNAVAILABLE_RESELECT"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE race_jobs SET job_state='READY',placement_state='PREPARED',preparation_revision=preparation_revision+1 WHERE job_id=? AND job_state='ADD_FAILED' AND current_attempt_id IS NULL`, job.jobID)
	return err
}
