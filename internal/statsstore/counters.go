package statsstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/statsdelta"
)

// RecordCounters commits the raw sample and its hourly delta together. Replayed
// or out-of-order samples cannot add traffic twice. The first sample is a baseline.
func RecordCounters(ctx context.Context, database *sql.DB, instanceID string, at time.Time, downloaded, uploaded, downloadRate, uploadRate int64) error {
	if instanceID == "" || at.IsZero() || downloaded < 0 || uploaded < 0 || downloadRate < 0 || uploadRate < 0 {
		return errors.New("invalid counter sample")
	}
	stamp := at.UTC().Format(time.RFC3339Nano)
	return db.RunImmediate(ctx, database, func(ctx context.Context, tx *db.ImmediateTx) error {
		var previous statsdelta.Counter
		var previousAt string
		err := tx.QueryRowContext(ctx, `SELECT download_bytes, upload_bytes, sampled_at FROM instance_counter_samples WHERE instance_id=? ORDER BY sample_id DESC LIMIT 1`, instanceID).Scan(&previous.Downloaded, &previous.Uploaded, &previousAt)
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if found {
			previousTime, err := time.Parse(time.RFC3339Nano, previousAt)
			if err != nil {
				return fmt.Errorf("invalid previous sample timestamp: %w", err)
			}
			if !at.After(previousTime) {
				return nil
			}
		}
		outcome, err := statsdelta.Advance(previous, found, statsdelta.Counter{Downloaded: downloaded, Uploaded: uploaded})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO instance_counter_samples(instance_id,sampled_at,download_bytes,upload_bytes,download_rate,upload_rate) VALUES(?,?,?,?,?,?)`, instanceID, stamp, downloaded, uploaded, downloadRate, uploadRate); err != nil {
			return err
		}
		if outcome.Kind != statsdelta.KindDelta {
			return nil
		}
		bucket := at.UTC().Truncate(time.Hour).Format(time.RFC3339)
		var currentDownload, currentUpload int64
		err = tx.QueryRowContext(ctx, `SELECT download_bytes, upload_bytes FROM instance_traffic_hourly WHERE instance_id=? AND bucket_start=?`, instanceID, bucket).Scan(&currentDownload, &currentUpload)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, ok := safeAdd(currentDownload, outcome.Delta.Downloaded); !ok {
			return errors.New("download traffic overflow")
		}
		if _, ok := safeAdd(currentUpload, outcome.Delta.Uploaded); !ok {
			return errors.New("upload traffic overflow")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO instance_traffic_hourly(instance_id,bucket_start,download_bytes,upload_bytes,sample_count,revision,updated_at) VALUES(?,?,?,?,1,1,?) ON CONFLICT(instance_id,bucket_start) DO UPDATE SET download_bytes=download_bytes+excluded.download_bytes,upload_bytes=upload_bytes+excluded.upload_bytes,sample_count=sample_count+1,revision=revision+1,updated_at=excluded.updated_at`, instanceID, bucket, outcome.Delta.Downloaded, outcome.Delta.Uploaded, stamp)
		return err
	})
}
