package statshourly

import (
	"errors"
	"testing"
	"time"
)

func TestBucketStartIsUTCHourFloor(t *testing.T) {
	sampled := time.Date(2026, 8, 29, 10, 37, 42, 0, time.UTC)
	bucket := BucketStart(sampled)
	if bucket.Hour() != 10 || bucket.Minute() != 0 || bucket.Second() != 0 || bucket.Location() != time.UTC {
		t.Fatalf("bucket = %s", bucket)
	}
	// 非时区输入同样落到 UTC 小时桶。
	local := time.Date(2026, 8, 29, 18, 37, 0, 0, time.FixedZone("CST", 8*3600))
	if got := BucketStart(local); !got.Equal(time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("bucket = %s", got)
	}
}

func TestAccumulateRejectsForeignBucketAndNegativeDelta(t *testing.T) {
	bucket := &Bucket{InstanceID: "qb-1", BucketStart: time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)}
	if err := Accumulate(bucket, DownloadUpload{DownloadBytes: 1}, time.Date(2026, 8, 29, 11, 5, 0, 0, time.UTC)); err == nil {
		t.Fatal("foreign bucket error = nil")
	}
	if err := Accumulate(bucket, DownloadUpload{DownloadBytes: -1}, time.Date(2026, 8, 29, 10, 5, 0, 0, time.UTC)); err == nil {
		t.Fatal("negative delta error = nil")
	}
}

func TestAccumulateAggregatesAndBumpsRevision(t *testing.T) {
	bucketStart := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	bucket := &Bucket{InstanceID: "qb-1", BucketStart: bucketStart}
	if err := Accumulate(bucket, DownloadUpload{DownloadBytes: 100, UploadBytes: 200}, bucketStart.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := Accumulate(bucket, DownloadUpload{DownloadBytes: 50, UploadBytes: 10}, bucketStart.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if bucket.DownloadBytes != 150 || bucket.UploadBytes != 210 || bucket.SampleCount != 2 || bucket.Revision != 2 {
		t.Fatalf("bucket = %+v", bucket)
	}
}

func TestDisplayStartConvertsAcrossDayBoundary(t *testing.T) {
	// UTC 2026-08-29 17:00 在东八区已是 8 月 30 日凌晨 1 点（跨日展示）。
	bucket := time.Date(2026, 8, 29, 17, 0, 0, 0, time.UTC)
	display, err := DisplayStart(bucket, time.FixedZone("CST", 8*3600))
	if err != nil {
		t.Fatal(err)
	}
	if display.Day() != 30 || display.Hour() != 1 {
		t.Fatalf("display = %s", display)
	}
	if _, err := DisplayStart(bucket, nil); !errors.Is(err, errors.New("display location is required")) && err == nil {
		t.Fatal("nil location error = nil")
	}
}
