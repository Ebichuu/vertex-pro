package statsstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/statshourly"
)

const (
	DefaultRawSampleRetention = 30 * 24 * time.Hour
	DefaultHourlyRetention    = 180 * 24 * time.Hour
)

type Sample struct {
	InstanceID string
	SampledAt  time.Time
	Delta      statshourly.DownloadUpload
}

type BatchResult struct {
	Samples int
	Buckets int
}

type Retention struct {
	RawSamples time.Duration
	Hourly     time.Duration
}

type PruneResult struct {
	RawSamples int64
	Hourly     int64
}

// AccumulateBatch validates and groups all samples before opening one SQLite
// write transaction. A bucket's revision advances by its number of samples.
func AccumulateBatch(ctx context.Context, database *sql.DB, samples []Sample) (BatchResult, error) {
	if database == nil {
		return BatchResult{}, errors.New("database is required")
	}
	buckets := make(map[bucketKey]bucketValue)
	for _, sample := range samples {
		if err := validateSample(sample); err != nil {
			return BatchResult{}, err
		}
		key := bucketKey{InstanceID: strings.TrimSpace(sample.InstanceID), BucketStart: statshourly.BucketStart(sample.SampledAt)}
		value := buckets[key]
		var ok bool
		value.DownloadBytes, ok = safeAdd(value.DownloadBytes, sample.Delta.DownloadBytes)
		if !ok {
			return BatchResult{}, errors.New("batch download bytes overflow")
		}
		value.UploadBytes, ok = safeAdd(value.UploadBytes, sample.Delta.UploadBytes)
		if !ok {
			return BatchResult{}, errors.New("batch upload bytes overflow")
		}
		value.SampleCount++
		if value.LatestSample.IsZero() || sample.SampledAt.UTC().After(value.LatestSample) {
			value.LatestSample = sample.SampledAt.UTC()
		}
		buckets[key] = value
	}
	if len(samples) == 0 {
		return BatchResult{}, nil
	}
	keys := make([]bucketKey, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].InstanceID != keys[right].InstanceID {
			return keys[left].InstanceID < keys[right].InstanceID
		}
		return keys[left].BucketStart.Before(keys[right].BucketStart)
	})
	if err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		for _, key := range keys {
			value := buckets[key]
			var current bucketValue
			var currentUpdatedAt string
			var ok bool
			err := transaction.QueryRowContext(ctx, `SELECT download_bytes, upload_bytes, sample_count, revision, updated_at FROM instance_traffic_hourly WHERE instance_id = ? AND bucket_start = ?`, key.InstanceID, key.BucketStart.Format(time.RFC3339)).Scan(&current.DownloadBytes, &current.UploadBytes, &current.SampleCount, &current.Revision, &currentUpdatedAt)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				_, err = transaction.ExecContext(ctx, `INSERT INTO instance_traffic_hourly(instance_id, bucket_start, download_bytes, upload_bytes, sample_count, revision, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, key.InstanceID, key.BucketStart.Format(time.RFC3339), value.DownloadBytes, value.UploadBytes, value.SampleCount, value.SampleCount, value.LatestSample.Format(time.RFC3339))
			case err != nil:
				err = fmt.Errorf("read traffic bucket: %w", err)
			default:
				current.DownloadBytes, ok = safeAdd(current.DownloadBytes, value.DownloadBytes)
				if !ok {
					return errors.New("traffic download bytes overflow")
				}
				current.UploadBytes, ok = safeAdd(current.UploadBytes, value.UploadBytes)
				if !ok {
					return errors.New("traffic upload bytes overflow")
				}
				current.SampleCount, ok = safeAdd(current.SampleCount, value.SampleCount)
				if !ok {
					return errors.New("traffic sample count overflow")
				}
				current.Revision, ok = safeAdd(current.Revision, value.SampleCount)
				if !ok {
					return errors.New("traffic revision overflow")
				}
				updatedAt := currentUpdatedAt
				if value.LatestSample.Format(time.RFC3339) > updatedAt {
					updatedAt = value.LatestSample.Format(time.RFC3339)
				}
				_, err = transaction.ExecContext(ctx, `UPDATE instance_traffic_hourly SET download_bytes = ?, upload_bytes = ?, sample_count = ?, revision = ?, updated_at = ? WHERE instance_id = ? AND bucket_start = ?`, current.DownloadBytes, current.UploadBytes, current.SampleCount, current.Revision, updatedAt, key.InstanceID, key.BucketStart.Format(time.RFC3339))
			}
			if err != nil {
				return fmt.Errorf("write traffic bucket: %w", err)
			}
		}
		return nil
	}); err != nil {
		return BatchResult{}, err
	}
	return BatchResult{Samples: len(samples), Buckets: len(buckets)}, nil
}

// Prune removes only stats rows before explicit UTC cutoffs. It never touches
// torrent, identity, placement, reservation, or history tables.
func Prune(ctx context.Context, database *sql.DB, now time.Time, retention Retention) (PruneResult, error) {
	if database == nil {
		return PruneResult{}, errors.New("database is required")
	}
	if now.IsZero() {
		return PruneResult{}, errors.New("now is required")
	}
	if retention.RawSamples <= 0 || retention.Hourly <= 0 {
		return PruneResult{}, errors.New("retention durations must be positive")
	}
	result := PruneResult{}
	if err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		cutoff := now.UTC().Add(-retention.RawSamples).Format(time.RFC3339)
		deleted, err := transaction.ExecContext(ctx, `DELETE FROM instance_counter_samples WHERE sampled_at < ?`, cutoff)
		if err != nil {
			return fmt.Errorf("prune raw samples: %w", err)
		}
		result.RawSamples, err = deleted.RowsAffected()
		if err != nil {
			return fmt.Errorf("read raw sample prune count: %w", err)
		}
		cutoff = now.UTC().Add(-retention.Hourly).Format(time.RFC3339)
		deleted, err = transaction.ExecContext(ctx, `DELETE FROM instance_traffic_hourly WHERE bucket_start < ?`, cutoff)
		if err != nil {
			return fmt.Errorf("prune hourly traffic: %w", err)
		}
		result.Hourly, err = deleted.RowsAffected()
		if err != nil {
			return fmt.Errorf("read hourly prune count: %w", err)
		}
		return nil
	}); err != nil {
		return PruneResult{}, err
	}
	return result, nil
}

type bucketKey struct {
	InstanceID  string
	BucketStart time.Time
}

type bucketValue struct {
	DownloadBytes int64
	UploadBytes   int64
	SampleCount   int64
	Revision      int64
	LatestSample  time.Time
}

func validateSample(sample Sample) error {
	if strings.TrimSpace(sample.InstanceID) == "" {
		return errors.New("instance ID is required")
	}
	if sample.SampledAt.IsZero() {
		return errors.New("sampled at is required")
	}
	if sample.Delta.DownloadBytes < 0 || sample.Delta.UploadBytes < 0 {
		return errors.New("sample delta must not be negative")
	}
	return nil
}

func safeAdd(left, right int64) (int64, bool) {
	if right > 0 && left > int64(^uint64(0)>>1)-right {
		return 0, false
	}
	if right < 0 && left < -int64(^uint64(0)>>1)-1-right {
		return 0, false
	}
	return left + right, true
}
