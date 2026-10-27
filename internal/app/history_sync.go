package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/syncwatcher"
)

// Observations describe confirmed deliveries only. Missing torrents never
// turn an UNKNOWN attempt into confirmed delivery.
func (runtime *Runtime) observeJobs(ctx context.Context, event syncwatcher.Event, now time.Time) error {
	return db.RunImmediate(ctx, runtime.Database, func(ctx context.Context, tx *db.ImmediateTx) error {
		var digest string
		if err := tx.QueryRowContext(ctx, `SELECT snapshot_digest FROM sync_snapshots WHERE instance_id=? AND sync_generation=? AND snapshot_revision=? AND is_complete=1`, event.InstanceID, event.Generation, event.Revision).Scan(&digest); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT j.job_id,a.qb_torrent_id FROM race_jobs j JOIN placement_attempts a ON a.attempt_id=j.current_attempt_id WHERE a.instance_id=? AND a.attempt_state IN ('HASH_SEEN','RECONCILED_CONFIRMED') AND a.qb_torrent_id IS NOT NULL`, event.InstanceID)
		if err != nil {
			return err
		}
		type job struct{ id, hash string }
		var jobs []job
		for rows.Next() {
			var item job
			if err := rows.Scan(&item.id, &item.hash); err != nil {
				rows.Close()
				return err
			}
			jobs = append(jobs, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		for _, item := range jobs {
			torrent, present := event.Snapshot.Torrents[item.hash]
			if !present {
				// A repeated callback for one snapshot is not a second absence.
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO job_observation_events(job_id,state,observed_at,snapshot_generation,snapshot_revision,snapshot_digest)
					SELECT ?,'DELETED',?,?,?,? WHERE EXISTS(SELECT 1 FROM job_observations WHERE job_id=? AND missing_count>=1 AND deleted_at='' AND (snapshot_generation<? OR (snapshot_generation=? AND snapshot_revision<?)))`, item.id, stamp, event.Generation, event.Revision, digest, item.id, event.Generation, event.Generation, event.Revision); err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO job_observations(job_id,instance_id,qb_torrent_id,observed_at,snapshot_generation,snapshot_revision,snapshot_digest,missing_count) VALUES(?,?,?,?,?,?,?,1)
					ON CONFLICT(job_id) DO UPDATE SET missing_count=missing_count+1,deleted_at=CASE WHEN missing_count>=1 AND deleted_at='' THEN excluded.observed_at ELSE deleted_at END,observed_at=excluded.observed_at,snapshot_generation=excluded.snapshot_generation,snapshot_revision=excluded.snapshot_revision,snapshot_digest=excluded.snapshot_digest
					WHERE deleted_at='' AND (snapshot_generation<excluded.snapshot_generation OR (snapshot_generation=excluded.snapshot_generation AND snapshot_revision<excluded.snapshot_revision))`, item.id, event.InstanceID, item.hash, stamp, event.Generation, event.Revision, digest)
				if err != nil {
					return err
				}
				continue
			}
			observation := observedTorrentState(torrent.State, torrent.AmountLeftState == "KNOWN" && torrent.AmountLeft != nil && *torrent.AmountLeft == 0)
			title := torrent.Name
			completed := ""
			if torrent.Has(syncmerge.FieldCompletionOn) && torrent.CompletionOn > 0 && torrent.CompletionOn < now.Unix()+60 {
				completed = time.Unix(torrent.CompletionOn, 0).UTC().Format(time.RFC3339)
			}
			downloaded := historyBytes(torrent, syncmerge.FieldDownloaded, torrent.Downloaded)
			uploaded := historyBytes(torrent, syncmerge.FieldUploaded, torrent.Uploaded)
			var ratioValue any
			if torrent.Has(syncmerge.FieldRatio) && torrent.Ratio >= 0 && !math.IsNaN(torrent.Ratio) && !math.IsInf(torrent.Ratio, 0) && torrent.Ratio < float64(math.MaxInt64) {
				ratioValue = torrent.Ratio
			}
			if observation != "" {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO job_observation_events(job_id,state,observed_at,snapshot_generation,snapshot_revision,snapshot_digest)
					SELECT ?,?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM job_observations WHERE job_id=? AND (state=? OR deleted_at!='' OR snapshot_generation>? OR (snapshot_generation=? AND snapshot_revision>=?)))`, item.id, observation, stamp, event.Generation, event.Revision, digest, item.id, observation, event.Generation, event.Generation, event.Revision); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO job_observations(job_id,instance_id,qb_torrent_id,title,state,downloaded,uploaded,ratio,completed_at,observed_at,snapshot_generation,snapshot_revision,snapshot_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(job_id) DO UPDATE SET title=excluded.title,state=excluded.state,downloaded=excluded.downloaded,uploaded=excluded.uploaded,ratio=excluded.ratio,completed_at=excluded.completed_at,observed_at=excluded.observed_at,snapshot_generation=excluded.snapshot_generation,snapshot_revision=excluded.snapshot_revision,snapshot_digest=excluded.snapshot_digest,missing_count=0
				WHERE deleted_at='' AND (snapshot_generation<excluded.snapshot_generation OR (snapshot_generation=excluded.snapshot_generation AND snapshot_revision<excluded.snapshot_revision))`, item.id, event.InstanceID, item.hash, title, observation, downloaded, uploaded, ratioValue, completed, stamp, event.Generation, event.Revision, digest)
			if err != nil {
				return fmt.Errorf("persist observation: %w", err)
			}
		}
		return nil
	})
}

func observedTorrentState(state string, complete bool) string {
	switch state {
	case "uploading", "stalledUP", "forcedUP", "queuedUP":
		if complete {
			return "SEEDING"
		}
	case "pausedUP", "stoppedUP":
		if complete {
			return "COMPLETED"
		}
	case "downloading", "stalledDL", "forcedDL", "queuedDL", "pausedDL", "stoppedDL", "metaDL", "checkingDL":
		return "DOWNLOADING"
	default:
		if strings.HasSuffix(state, "DL") {
			return "DOWNLOADING"
		}
	}
	return ""
}

// historyBytes is NULL unless qB reported a valid non-negative byte count.
func historyBytes(torrent *syncmerge.Torrent, field syncmerge.Field, value int64) any {
	if !torrent.Has(field) || value < 0 {
		return nil
	}
	return value
}
