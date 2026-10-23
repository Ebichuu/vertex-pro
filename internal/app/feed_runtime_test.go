package app

import (
	"context"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/schedulercycle"
	"path/filepath"
	"testing"
	"time"
)

func TestFeedBaselineAndCadenceSurviveRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cfg := Config{Database: db.Config{Path: filepath.Join(t.TempDir(), "feed.db")}, Hosts: []string{"vertex.local"}, Now: func() time.Time { return now }}
	runtime, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	if _, err := runtime.Database.Exec(`INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES('s','Site','https://example.test',1,'2026-10-22T12:00:00Z','2026-10-22T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Database.Exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES('f','s','Feed','https://example.test/rss',1,5,'2026-10-22T12:00:00Z','2026-10-22T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	feeds, err := runtime.databaseEnabledFeeds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 1 || !feeds[0].BaselineOnly {
		t.Fatalf("first feeds=%+v", feeds)
	}
	if err := runtime.recordFeedPoll(ctx, feeds[0], schedulercycle.Result{ETag: "etag-v1", Entries: 2}, nil); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err = New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	feeds, err = runtime.databaseEnabledFeeds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 0 {
		t.Fatal("polled before feed interval")
	}
	now = now.Add(5 * time.Minute)
	feeds, err = runtime.databaseEnabledFeeds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 1 || feeds[0].BaselineOnly || feeds[0].ETag != "etag-v1" {
		t.Fatalf("resumed feeds=%+v", feeds)
	}
	if _, err := runtime.Database.Exec(`UPDATE sites SET enabled=0 WHERE site_id='s'`); err != nil {
		t.Fatal(err)
	}
	feeds, err = runtime.databaseEnabledFeeds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 0 {
		t.Fatal("disabled site was polled")
	}
}
