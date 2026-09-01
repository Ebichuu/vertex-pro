package statssummary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Range 是冻结的汇总口径（设计 §19.1 / STAT-04）：day/week/month，
// 一律基于 UTC 桶起点计算窗口。
type Range string

const (
	RangeDay   Range = "DAY"
	RangeWeek  Range = "WEEK"
	RangeMonth Range = "MONTH"
)

type Request struct {
	InstanceID string // 为空表示跨实例 total
	Range      Range
	Anchor     time.Time // 窗口锚点（UTC）
	Limit      int       // series 分页上限；<=0 表示不限制
	Cursor     string    // 上一页返回的 NextCursor（UTC RFC3339）
}

type SeriesPoint struct {
	BucketStart   string
	DownloadBytes int64
	UploadBytes   int64
	Missing       bool
}

type Summary struct {
	InstanceID    string
	Range         Range
	RangeStart    string
	RangeEnd      string
	DownloadBytes int64
	UploadBytes   int64
	Series        []SeriesPoint
	NextCursor    string
	MissingCount  int
}

// Summarize 汇总 [rangeStart, rangeEnd) 内的 UTC 小时桶：窗口内缺失的小时
// 保持 missing（bytes 为 0 且 Missing=true），绝不插值成有流量。Cursor 为
// 稳定 opaque 分页（最后一个已返回桶的 UTC RFC3339 起点）。
func Summarize(ctx context.Context, database *sql.DB, request Request) (Summary, error) {
	if database == nil {
		return Summary{}, errors.New("database is required")
	}
	if request.Anchor.IsZero() {
		return Summary{}, errors.New("anchor is required")
	}
	start, end, err := window(request.Range, request.Anchor.UTC())
	if err != nil {
		return Summary{}, err
	}
	hours := int(end.Sub(start) / time.Hour)
	summary := Summary{
		InstanceID: request.InstanceID,
		Range:      request.Range,
		RangeStart: start.Format(time.RFC3339),
		RangeEnd:   end.Format(time.RFC3339),
	}
	totals, err := loadTotals(ctx, database, request.InstanceID, start, end)
	if err != nil {
		return Summary{}, err
	}
	summary.DownloadBytes = totals.download
	summary.UploadBytes = totals.upload

	perBucket, err := loadBuckets(ctx, database, request.InstanceID, start, end)
	if err != nil {
		return Summary{}, err
	}
	applyCursor := strings.TrimSpace(request.Cursor) != ""
	limit := request.Limit
	for i := 0; i < hours; i++ {
		bucketStart := start.Add(time.Duration(i) * time.Hour)
		key := bucketStart.Format(time.RFC3339)
		if applyCursor {
			if key <= request.Cursor {
				continue
			}
		}
		point, ok := perBucket[bucketStart]
		if !ok {
			point = SeriesPoint{Missing: true}
			summary.MissingCount++
		}
		if limit > 0 && len(summary.Series) >= limit {
			summary.NextCursor = lastReturnedCursor(summary.Series)
			break
		}
		summary.Series = append(summary.Series, SeriesPoint{BucketStart: key, DownloadBytes: point.DownloadBytes, UploadBytes: point.UploadBytes, Missing: point.Missing})
	}
	return summary, nil
}

func lastReturnedCursor(series []SeriesPoint) string {
	if len(series) == 0 {
		return ""
	}
	return series[len(series)-1].BucketStart
}

type totals struct {
	download int64
	upload   int64
}

func loadTotals(ctx context.Context, database *sql.DB, instanceID string, start, end time.Time) (totals, error) {
	query := `SELECT COALESCE(SUM(download_bytes),0), COALESCE(SUM(upload_bytes),0) FROM instance_traffic_hourly WHERE bucket_start >= ? AND bucket_start < ?`
	args := []any{start.Format(time.RFC3339), end.Format(time.RFC3339)}
	if instanceID != "" {
		query += ` AND instance_id = ?`
		args = append(args, instanceID)
	}
	var result totals
	if err := database.QueryRowContext(ctx, query, args...).Scan(&result.download, &result.upload); err != nil {
		return totals{}, fmt.Errorf("load totals: %w", err)
	}
	return result, nil
}

func loadBuckets(ctx context.Context, database *sql.DB, instanceID string, start, end time.Time) (map[time.Time]SeriesPoint, error) {
	query := `SELECT bucket_start, SUM(download_bytes), SUM(upload_bytes) FROM instance_traffic_hourly WHERE bucket_start >= ? AND bucket_start < ?`
	args := []any{start.Format(time.RFC3339), end.Format(time.RFC3339)}
	if instanceID != "" {
		query += ` AND instance_id = ?`
		args = append(args, instanceID)
	}
	query += ` GROUP BY bucket_start`
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load buckets: %w", err)
	}
	defer rows.Close()
	result := make(map[time.Time]SeriesPoint)
	for rows.Next() {
		var bucketStart string
		var point SeriesPoint
		if err := rows.Scan(&bucketStart, &point.DownloadBytes, &point.UploadBytes); err != nil {
			return nil, fmt.Errorf("scan bucket: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, bucketStart)
		if err != nil {
			return nil, fmt.Errorf("parse bucket start: %w", err)
		}
		point.BucketStart = parsed.Format(time.RFC3339)
		result[parsed] = point
	}
	return result, rows.Err()
}

func window(rangeKind Range, anchor time.Time) (time.Time, time.Time, error) {
	utc := anchor.UTC()
	switch rangeKind {
	case RangeDay:
		start := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 0, 1), nil
	case RangeWeek:
		weekday := int(utc.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
		start := day.AddDate(0, 0, -(weekday - 1))
		return start, start.AddDate(0, 0, 7), nil
	case RangeMonth:
		start := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0), nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("unsupported range %s", rangeKind)
}
