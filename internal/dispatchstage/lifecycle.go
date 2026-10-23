package dispatchstage

import (
	"context"
	"fmt"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"time"
)

// RefreshCapacity is called while the runtime holds its sync lock. Completion
// and removal release reservations using this exact fresh physical snapshot.
func (s *Stage) RefreshCapacity(ctx context.Context, e Evidence) error {
	_, err := s.prepareCapacity(ctx, e)
	return err
}

func (s *Stage) reconcileCompleted(ctx context.Context, tx *db.ImmediateTx, e Evidence, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.reservation_id,r.reserved_bytes,r.reflected_physical_bytes,a.qb_torrent_id,a.attempt_id,a.job_id FROM capacity_reservations r JOIN placement_attempts a ON a.attempt_id=r.attempt_id JOIN race_jobs j ON j.current_attempt_id=a.attempt_id WHERE a.instance_id=? AND r.storage_target_id=? AND r.reservation_state IN ('ACTIVE','REFLECTING') AND a.attempt_state IN ('HASH_SEEN','RECONCILED_CONFIRMED') AND j.job_state IN ('HASH_SEEN','RECONCILED_CONFIRMED')`, s.config.Storage.InstanceID, s.config.Storage.StorageTargetID)
	if err != nil {
		return err
	}
	type reservation struct {
		id                 string
		bytes, reflected   int64
		hash, attempt, job string
	}
	var list []reservation
	for rows.Next() {
		var r reservation
		if err := rows.Scan(&r.id, &r.bytes, &r.reflected, &r.hash, &r.attempt, &r.job); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, r := range list {
		t, exists := e.Snapshot.Torrents[r.hash]
		completed := exists && t.AmountLeftState == "KNOWN" && t.AmountLeft != nil && *t.AmountLeft == 0
		removed := false
		if !exists {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_observations WHERE job_id=? AND missing_count>=2 AND deleted_at!='' AND snapshot_generation=? AND snapshot_revision=? AND snapshot_digest=?`, r.job, e.Record.Generation, e.Record.Revision, e.Record.Digest).Scan(&count); err != nil {
				return err
			}
			removed = count == 1
		}
		if exists && t.AmountLeftState == "KNOWN" && t.AmountLeft != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE capacity_task_bindings SET amount_left_state='KNOWN',amount_left=?,amount_left_error_code=NULL,uncertainty_debit_kind='NONE',uncertainty_debit_bytes=0,snapshot_generation=?,snapshot_revision=?,snapshot_digest=?,updated_at=?,revision=revision+1 WHERE reservation_id=?`, *t.AmountLeft, e.Record.Generation, e.Record.Revision, e.Record.Digest, stamp, r.id); err != nil {
				return err
			}
		}
		if !completed && !removed {
			continue
		}
		reason := "TERMINAL_QB_REMOVED"
		reflected := r.reflected
		if completed {
			reason = "TERMINAL_COMPLETED"
			reflected = r.bytes
			if _, err := tx.ExecContext(ctx, `UPDATE capacity_task_bindings SET amount_left_state='KNOWN',amount_left=0,amount_left_error_code=NULL,owner_kind='UNMANAGED',reservation_id=NULL,uncertainty_debit_kind='NONE',uncertainty_debit_bytes=0,snapshot_generation=?,snapshot_revision=?,snapshot_digest=?,revision=revision+1,updated_at=? WHERE reservation_id=? AND owner_kind='RESERVATION'`, e.Record.Generation, e.Record.Revision, e.Record.Digest, stamp, r.id); err != nil {
				return err
			}
			if reflected > r.reflected {
				if _, err := tx.ExecContext(ctx, `INSERT INTO reservation_reflections(reflection_id,reservation_id,snapshot_generation,snapshot_revision,snapshot_digest,reflected_physical_bytes,observed_at) VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("completion-%s-%d", r.id, e.Record.Revision), r.id, e.Record.Generation, e.Record.Revision, e.Record.Digest, reflected-r.reflected, stamp); err != nil {
					return err
				}
			}
		} else {
			if _, err := tx.ExecContext(ctx, `DELETE FROM capacity_task_bindings WHERE reservation_id=?`, r.id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE capacity_reservations SET reservation_state='RELEASED',release_reason=?,reflected_physical_bytes=?,released_unconsumed_bytes=reserved_bytes-?,released_at=?,revision=revision+1 WHERE reservation_id=?`, reason, reflected, reflected, stamp, r.id); err != nil {
			return err
		}
		// Removing a confirmed torrent never rewrites its delivery as NO_EFFECT.
		if _, err := tx.ExecContext(ctx, `UPDATE placement_attempts SET attempt_state='RECONCILED_CONFIRMED',side_effect_fact='OBSERVED_EFFECT',finished_at=COALESCE(finished_at,?) WHERE attempt_id=?`, stamp, r.attempt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE race_jobs SET job_state='RECONCILED_CONFIRMED',updated_at=? WHERE job_id=?`, stamp, r.job); err != nil {
			return err
		}
	}
	// A completed reservation becomes an unmanaged binding. Remove that stale
	// binding only after the job has a durable deletion observation and this
	// complete snapshot still lacks the torrent.
	rows, err = tx.QueryContext(ctx, `SELECT b.binding_id,b.qb_torrent_id FROM capacity_task_bindings b
		JOIN job_observations o ON o.instance_id=? AND o.qb_torrent_id=b.qb_torrent_id
		WHERE b.storage_target_id=? AND b.owner_kind='UNMANAGED' AND o.deleted_at!=''
		AND (b.snapshot_generation<o.snapshot_generation OR (b.snapshot_generation=o.snapshot_generation AND b.snapshot_revision<=o.snapshot_revision))`, s.config.Storage.InstanceID, s.config.Storage.StorageTargetID)
	if err != nil {
		return err
	}
	type staleBinding struct{ id, hash string }
	var stale []staleBinding
	for rows.Next() {
		var b staleBinding
		if err := rows.Scan(&b.id, &b.hash); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range stale {
		if _, present := e.Snapshot.Torrents[b.hash]; present {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM capacity_task_bindings WHERE binding_id=? AND owner_kind='UNMANAGED'`, b.id); err != nil {
			return err
		}
	}
	return nil
}
