package historydeletion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

var ErrNotRecordable = errors.New("deletion fact is not recordable")

const (
	SourceQBUserRemoved = "QB_USER_REMOVED"
	SourceQUILifecycle  = "QUI_LIFECYCLE"
	SourceUnknownOrigin = "UNKNOWN_ORIGIN"
)

// Fact 是一次权威删除的用户侧历史记录：qB Torrent 消失后，History 仍能
// 展示它曾经去过哪个下载器、最后已知状态、累计上传量、分享率与删除发现
// 时间（设计 §2、§8.7.5）。binding 行删除不影响本表。
type Fact struct {
	FactID           string
	JobID            string
	IdentityKey      string
	QBTorrentID      string
	StorageTargetID  string
	DeletionSource   string
	DeletedAt        string
	LastObservation  string
	LastUploaded     int64
	LastDownloaded   int64
	LastRatio        *float64
	SnapshotDigest   string
	SnapshotRevision int64
}

type Request struct {
	FactID           string
	JobID            string
	IdentityKey      string
	QBTorrentID      string
	StorageTargetID  string
	DeletionSource   string
	DeletedAt        time.Time
	LastObservation  string
	LastUploaded     int64
	LastDownloaded   int64
	LastRatio        *float64
	SnapshotDigest   string
	SnapshotRevision int64
	Now              time.Time
}

type Result struct {
	FactID     string
	Idempotent bool
}

// Record 幂等落库删除事实：同一 (job_id, qb_torrent_id) 重复记录且证据一致时
// 返回 Idempotent；冲突证据拒绝。本函数不删除 binding——binding 的移除由
// 权威删除事务（CAP-12）负责，本表只保证历史不随之消失。
func Record(ctx context.Context, database *sql.DB, request Request) (Result, error) {
	if err := validate(request); err != nil {
		return Result{}, err
	}
	result := Result{FactID: request.FactID}
	err := db.RunImmediate(ctx, database, func(ctx context.Context, transaction *db.ImmediateTx) error {
		var existingDigest string
		err := transaction.QueryRowContext(ctx, `SELECT last_seen_snapshot_digest FROM history_deletion_facts WHERE job_id = ? AND qb_torrent_id = ?`, request.JobID, request.QBTorrentID).Scan(&existingDigest)
		switch {
		case err == nil:
			if existingDigest != request.SnapshotDigest {
				return fmt.Errorf("%w: conflicting deletion evidence", ErrNotRecordable)
			}
			result.Idempotent = true
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read deletion fact: %w", err)
		}
		var jobIdentity string
		err = transaction.QueryRowContext(ctx, `SELECT identity_key FROM race_jobs WHERE job_id = ?`, request.JobID).Scan(&jobIdentity)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: job missing", ErrNotRecordable)
		}
		if err != nil {
			return fmt.Errorf("read job: %w", err)
		}
		if jobIdentity != request.IdentityKey {
			return fmt.Errorf("%w: identity mismatch", ErrNotRecordable)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO history_deletion_facts(fact_id, job_id, identity_key, qb_torrent_id, storage_target_id, deletion_source, deleted_at, last_known_observation, last_uploaded_bytes, last_downloaded_bytes, last_ratio, last_seen_snapshot_digest, last_seen_snapshot_revision, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			request.FactID, request.JobID, request.IdentityKey, request.QBTorrentID, request.StorageTargetID,
			request.DeletionSource, request.DeletedAt.UTC().Format(time.RFC3339), request.LastObservation,
			request.LastUploaded, request.LastDownloaded, nullableRatio(request.LastRatio),
			request.SnapshotDigest, request.SnapshotRevision, request.Now.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("insert deletion fact: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// ForJob 返回一个 Job 的全部删除事实（按删除时间倒序）。
func ForJob(ctx context.Context, database *sql.DB, jobID string) ([]Fact, error) {
	rows, err := database.QueryContext(ctx, `SELECT fact_id, job_id, identity_key, qb_torrent_id, storage_target_id, deletion_source, deleted_at, last_known_observation, last_uploaded_bytes, last_downloaded_bytes, last_ratio, last_seen_snapshot_digest, last_seen_snapshot_revision FROM history_deletion_facts WHERE job_id = ? ORDER BY deleted_at DESC, fact_id`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query deletion facts: %w", err)
	}
	defer rows.Close()
	facts := make([]Fact, 0)
	for rows.Next() {
		var fact Fact
		var ratio sql.NullFloat64
		if err := rows.Scan(&fact.FactID, &fact.JobID, &fact.IdentityKey, &fact.QBTorrentID, &fact.StorageTargetID, &fact.DeletionSource, &fact.DeletedAt, &fact.LastObservation, &fact.LastUploaded, &fact.LastDownloaded, &ratio, &fact.SnapshotDigest, &fact.SnapshotRevision); err != nil {
			return nil, fmt.Errorf("scan deletion fact: %w", err)
		}
		if ratio.Valid {
			value := ratio.Float64
			fact.LastRatio = &value
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

func nullableRatio(ratio *float64) any {
	if ratio == nil {
		return nil
	}
	return *ratio
}

func validate(request Request) error {
	values := map[string]string{"fact_id": request.FactID, "job_id": request.JobID, "identity_key": request.IdentityKey, "qb_torrent_id": request.QBTorrentID, "storage_target_id": request.StorageTargetID, "snapshot_digest": request.SnapshotDigest}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if request.DeletedAt.IsZero() || request.Now.IsZero() {
		return errors.New("deletion times are required")
	}
	switch request.DeletionSource {
	case SourceQBUserRemoved, SourceQUILifecycle, SourceUnknownOrigin:
	default:
		return fmt.Errorf("%w: deletion source %s", ErrNotRecordable, request.DeletionSource)
	}
	switch request.LastObservation {
	case "DOWNLOADING", "SEEDING", "COMPLETED":
	default:
		return fmt.Errorf("%w: last known observation %s", ErrNotRecordable, request.LastObservation)
	}
	if request.LastUploaded < 0 || request.LastDownloaded < 0 || request.SnapshotRevision < 0 {
		return errors.New("deletion counters are invalid")
	}
	if request.LastRatio != nil && *request.LastRatio < 0 {
		return errors.New("last ratio is invalid")
	}
	return nil
}
