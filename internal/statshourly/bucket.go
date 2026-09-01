package statshourly

import (
	"errors"
	"time"
)

// BucketStart 返回样本所属的 UTC 小时桶起点。统计一律以 UTC 存储，
// 展示时区换算只发生在查询/展示层（设计 §19.1：UTC 存储、配置时区展示）。
func BucketStart(sampledAt time.Time) time.Time {
	utc := sampledAt.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), utc.Hour(), 0, 0, 0, time.UTC)
}

// Accumulate 把一条样本增量并入既有小时桶聚合。同一样本只能归属一个
// 小时桶；传入的 bucket 必须与目标桶起点一致，revision 随写入递增。
type Bucket struct {
	InstanceID    string
	BucketStart   time.Time
	DownloadBytes int64
	UploadBytes   int64
	SampleCount   int64
	Revision      int64
	UpdatedAt     time.Time
}

func Accumulate(bucket *Bucket, delta DownloadUpload, sampledAt time.Time) error {
	if bucket == nil {
		return errors.New("bucket is required")
	}
	if !BucketStart(sampledAt).Equal(bucket.BucketStart) {
		return errors.New("sample does not belong to this bucket")
	}
	if delta.DownloadBytes < 0 || delta.UploadBytes < 0 {
		return errors.New("delta must not be negative")
	}
	bucket.DownloadBytes += delta.DownloadBytes
	bucket.UploadBytes += delta.UploadBytes
	bucket.SampleCount++
	bucket.Revision++
	bucket.UpdatedAt = sampledAt.UTC()
	return nil
}

type DownloadUpload struct {
	DownloadBytes int64
	UploadBytes   int64
}

// DisplayStart 把 UTC 桶起点换算到展示时区；跨日/月与 DST 由 time 包语义
// 决定，换算不改变存储值。
func DisplayStart(bucketStart time.Time, location *time.Location) (time.Time, error) {
	if location == nil {
		return time.Time{}, errors.New("display location is required")
	}
	return bucketStart.In(location), nil
}
