package statsstore

import (
	"context"
	"testing"
	"time"
)

func TestRecordCountersBaselinesReplayAndReset(t *testing.T) {
	database := openDatabase(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	samples := []struct {
		minutes  int
		down, up int64
	}{
		{0, 1000, 2000}, {1, 1100, 2200}, {1, 1100, 2200}, {0, 900, 1900},
		{2, 10, 20}, {3, 40, 60},
	}
	for _, sample := range samples {
		if err := RecordCounters(ctx, database, "qb-a", at.Add(time.Duration(sample.minutes)*time.Minute), sample.down, sample.up, 1, 2); err != nil {
			t.Fatal(err)
		}
	}
	var down, up, count int64
	if err := database.QueryRow(`SELECT download_bytes,upload_bytes,sample_count FROM instance_traffic_hourly WHERE instance_id='qb-a'`).Scan(&down, &up, &count); err != nil {
		t.Fatal(err)
	}
	if down != 130 || up != 240 || count != 2 {
		t.Fatalf("traffic=%d/%d count=%d", down, up, count)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM instance_counter_samples WHERE instance_id='qb-a'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("raw samples=%d", count)
	}
	if err := RecordCounters(ctx, database, "qb-a", at.Add(4*time.Minute), -1, 70, 1, 2); err == nil {
		t.Fatal("negative counter accepted")
	}
}
