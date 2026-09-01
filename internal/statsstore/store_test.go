package statsstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/statshourly"
)

func TestAccumulateBatchGroupsSamplesInOneBucket(t *testing.T) {
	database := openDatabase(t)
	samples := []Sample{
		{InstanceID: "qb-a", SampledAt: time.Date(2026, 8, 31, 21, 1, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{DownloadBytes: 10, UploadBytes: 20}},
		{InstanceID: "qb-a", SampledAt: time.Date(2026, 8, 31, 21, 2, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{DownloadBytes: 30, UploadBytes: 40}},
		{InstanceID: "qb-a", SampledAt: time.Date(2026, 8, 31, 22, 0, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{DownloadBytes: 50, UploadBytes: 60}},
		{InstanceID: "qb-b", SampledAt: time.Date(2026, 8, 31, 21, 3, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{DownloadBytes: 70, UploadBytes: 80}},
	}
	result, err := AccumulateBatch(context.Background(), database, samples)
	if err != nil {
		t.Fatal(err)
	}
	if result.Samples != 4 || result.Buckets != 3 {
		t.Fatalf("result=%+v", result)
	}
	var download, upload, count, revision int64
	var updated string
	if err := database.QueryRow(`SELECT download_bytes, upload_bytes, sample_count, revision, updated_at FROM instance_traffic_hourly WHERE instance_id='qb-a' AND bucket_start='2026-08-31T21:00:00Z'`).Scan(&download, &upload, &count, &revision, &updated); err != nil {
		t.Fatal(err)
	}
	if download != 40 || upload != 60 || count != 2 || revision != 2 || updated != "2026-08-31T21:02:00Z" {
		t.Fatalf("bucket=%d/%d count=%d revision=%d updated=%s", download, upload, count, revision, updated)
	}

	result, err = AccumulateBatch(context.Background(), database, []Sample{{InstanceID: "qb-a", SampledAt: time.Date(2026, 8, 31, 21, 4, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{DownloadBytes: 5, UploadBytes: 6}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Buckets != 1 {
		t.Fatalf("second result=%+v", result)
	}
	if err := database.QueryRow(`SELECT download_bytes, upload_bytes, sample_count, revision FROM instance_traffic_hourly WHERE instance_id='qb-a' AND bucket_start='2026-08-31T21:00:00Z'`).Scan(&download, &upload, &count, &revision); err != nil {
		t.Fatal(err)
	}
	if download != 45 || upload != 66 || count != 3 || revision != 3 {
		t.Fatalf("updated bucket=%d/%d count=%d revision=%d", download, upload, count, revision)
	}
}

func TestAccumulateBatchIsAtomicOnInvalidSample(t *testing.T) {
	database := openDatabase(t)
	samples := []Sample{
		{InstanceID: "qb-a", SampledAt: time.Date(2026, 8, 31, 21, 1, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{UploadBytes: 10}},
		{InstanceID: "qb-a", SampledAt: time.Date(2026, 8, 31, 21, 2, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{DownloadBytes: -1}},
	}
	if _, err := AccumulateBatch(context.Background(), database, samples); err == nil {
		t.Fatal("invalid batch error=nil")
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM instance_traffic_hourly`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("bucket count=%d, want 0 after rejected batch", count)
	}
}

func TestAccumulateBatchRejectsOverflowBeforeWrite(t *testing.T) {
	database := openDatabase(t)
	samples := []Sample{
		{InstanceID: "qb-a", SampledAt: time.Date(2026, 8, 31, 21, 1, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{DownloadBytes: int64(^uint64(0) >> 1)}},
		{InstanceID: "qb-a", SampledAt: time.Date(2026, 8, 31, 21, 2, 0, 0, time.UTC), Delta: statshourly.DownloadUpload{DownloadBytes: 1}},
	}
	if _, err := AccumulateBatch(context.Background(), database, samples); err == nil {
		t.Fatal("overflow batch error=nil")
	}
}

func TestPruneUsesExplicitDurationsAndTouchesOnlyStats(t *testing.T) {
	database := openDatabase(t)
	old := "2026-08-29T00:00:00Z"
	recent := "2026-08-31T00:00:00Z"
	for _, statement := range []string{
		`INSERT INTO instance_counter_samples(instance_id, sampled_at, download_bytes, upload_bytes, download_rate, upload_rate) VALUES ('qb-a', '` + old + `', 1, 2, 3, 4)`,
		`INSERT INTO instance_counter_samples(instance_id, sampled_at, download_bytes, upload_bytes, download_rate, upload_rate) VALUES ('qb-a', '` + recent + `', 5, 6, 7, 8)`,
		`INSERT INTO instance_traffic_hourly(instance_id, bucket_start, download_bytes, upload_bytes, sample_count, revision, updated_at) VALUES ('qb-a', '` + old + `', 1, 2, 1, 1, '` + old + `')`,
		`INSERT INTO instance_traffic_hourly(instance_id, bucket_start, download_bytes, upload_bytes, sample_count, revision, updated_at) VALUES ('qb-a', '` + recent + `', 5, 6, 1, 1, '` + recent + `')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Prune(context.Background(), database, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), Retention{RawSamples: 24 * time.Hour, Hourly: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.RawSamples != 1 || result.Hourly != 1 {
		t.Fatalf("result=%+v", result)
	}
	var raw, hourly int
	if err := database.QueryRow(`SELECT COUNT(*) FROM instance_counter_samples`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM instance_traffic_hourly`).Scan(&hourly); err != nil {
		t.Fatal(err)
	}
	if raw != 1 || hourly != 1 {
		t.Fatalf("remaining raw=%d hourly=%d", raw, hourly)
	}
}

func TestPruneRequiresPositiveRetention(t *testing.T) {
	if _, err := Prune(context.Background(), openDatabase(t), time.Now(), Retention{}); err == nil {
		t.Fatal("invalid retention error=nil")
	}
}

func openDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "stats.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}
