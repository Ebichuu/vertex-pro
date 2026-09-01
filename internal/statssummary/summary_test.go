package statssummary

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestDayWindowSumsAndMarksMissingHours(t *testing.T) {
	database := openMigrated(t)
	day := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	seedBucket(t, database, "qb-1", day.Add(2*time.Hour), 100, 10)
	seedBucket(t, database, "qb-1", day.Add(3*time.Hour), 200, 20)
	seedBucket(t, database, "qb-2", day.Add(3*time.Hour), 5, 1)

	summary, err := Summarize(context.Background(), database, Request{InstanceID: "qb-1", Range: RangeDay, Anchor: day})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DownloadBytes != 300 || summary.UploadBytes != 30 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.Series) != 24 || summary.MissingCount != 22 {
		t.Fatalf("series = %d missing = %d", len(summary.Series), summary.MissingCount)
	}
	if summary.Series[2].Missing || summary.Series[2].DownloadBytes != 100 || summary.Series[3].Missing {
		t.Fatalf("series points = %+v %+v", summary.Series[2], summary.Series[3])
	}
	if summary.Series[0].Missing != true || summary.Series[0].DownloadBytes != 0 {
		t.Fatalf("missing hour must stay missing, got %+v", summary.Series[0])
	}

	total, err := Summarize(context.Background(), database, Request{Range: RangeDay, Anchor: day})
	if err != nil {
		t.Fatal(err)
	}
	if total.DownloadBytes != 305 || total.UploadBytes != 31 {
		t.Fatalf("total summary = %+v", total)
	}
}

func TestWeekAndMonthWindows(t *testing.T) {
	database := openMigrated(t)
	// 2026-08-29 是周六；ISO 周窗口从周一 2026-08-24 开始。
	saturday := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	seedBucket(t, database, "qb-1", time.Date(2026, 8, 24, 5, 0, 0, 0, time.UTC), 70, 7)
	seedBucket(t, database, "qb-1", saturday, 1, 1)
	week, err := Summarize(context.Background(), database, Request{InstanceID: "qb-1", Range: RangeWeek, Anchor: saturday})
	if err != nil {
		t.Fatal(err)
	}
	if week.RangeStart != "2026-08-24T00:00:00Z" || week.RangeEnd != "2026-08-31T00:00:00Z" || week.DownloadBytes != 71 {
		t.Fatalf("week = %+v", week)
	}
	// 月窗口：8 月有 31*24 个小时桶。
	seedBucket(t, database, "qb-1", time.Date(2026, 8, 1, 23, 0, 0, 0, time.UTC), 31, 3)
	month, err := Summarize(context.Background(), database, Request{InstanceID: "qb-1", Range: RangeMonth, Anchor: saturday})
	if err != nil {
		t.Fatal(err)
	}
	if month.RangeStart != "2026-08-01T00:00:00Z" || month.RangeEnd != "2026-09-01T00:00:00Z" {
		t.Fatalf("month = %+v", month)
	}
	if len(month.Series) != 31*24 || month.DownloadBytes != 102 {
		t.Fatalf("month series = %d bytes = %d", len(month.Series), month.DownloadBytes)
	}
}

func TestCursorPaginationIsStable(t *testing.T) {
	database := openMigrated(t)
	day := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	for hour := 0; hour < 24; hour++ {
		seedBucket(t, database, "qb-1", day.Add(time.Duration(hour)*time.Hour), 10, 1)
	}
	firstPage, err := Summarize(context.Background(), database, Request{InstanceID: "qb-1", Range: RangeDay, Anchor: day, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Series) != 10 || firstPage.NextCursor == "" || firstPage.Series[0].BucketStart != "2026-08-29T00:00:00Z" {
		t.Fatalf("first page = %+v", firstPage)
	}
	secondPage, err := Summarize(context.Background(), database, Request{InstanceID: "qb-1", Range: RangeDay, Anchor: day, Limit: 10, Cursor: firstPage.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Series) != 10 || secondPage.Series[0].BucketStart != "2026-08-29T10:00:00Z" {
		t.Fatalf("second page starts at %s want 2026-08-29T10:00:00Z", secondPage.Series[0].BucketStart)
	}
	if secondPage.Series[0].Missing {
		t.Fatal("second page must continue with present buckets")
	}
}

func TestWindowBoundaries(t *testing.T) {
	if _, _, err := window("YEAR", time.Now()); err == nil {
		t.Fatal("unsupported range error = nil")
	}
	start, end, err := window(RangeDay, time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC))
	if err != nil || !start.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("window = %s..%s err=%v", start, end, err)
	}
}

func seedBucket(t *testing.T, database *sql.DB, instanceID string, bucketStart time.Time, download, upload int64) {
	t.Helper()
	_, err := database.Exec(`INSERT INTO instance_traffic_hourly(instance_id, bucket_start, download_bytes, upload_bytes, sample_count, revision, updated_at) VALUES (?, ?, ?, ?, 1, 1, ?)`,
		instanceID, bucketStart.UTC().Format(time.RFC3339), download, upload, bucketStart.UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "stats-summary.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}
