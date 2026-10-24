package resourcesapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/rssservice"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/rsssourcedb"
)

func TestFeedRateWaitReportsCurrentAndCompletedWaitWithoutStaleLease(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	service, _ := rssservice.NewWithClock(database, func() time.Time { return now })
	_, err := service.CreateSite(ctx, rssservice.CreateSiteInput{ID: "s", Name: "Site", BaseURL: "https://site.example", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := service.CreateFeed(ctx, rssservice.CreateFeedInput{ID: "f", SiteID: "s", Name: "Feed", FeedURL: "https://site.example/rss", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at) VALUES('f',1,?)`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	store, _ := rsssourcedb.New(database)
	if _, err := store.Discover(ctx, "s", "f", []rsssource.Entry{{GUID: "one", URL: "https://site.example/one"}}, false, now); err != nil {
		t.Fatal(err)
	}
	work, err := store.Claim(ctx, "s", "f", now)
	if err != nil || work == nil {
		t.Fatal(err)
	}
	if err := store.RecordRateWait(ctx, *work, true, 0, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	d := Deps{Database: database, Now: func() time.Time { return now }}
	request := httptest.NewRequest("GET", "/api/v1/feeds/f", nil)
	payload := d.feedRuntimePayload(request, feed)
	if payload["rate_waiting"] != 1 || payload["rate_wait_ms"].(int64) < 9990 {
		t.Fatal(payload)
	}
	if err := store.RecordRateWait(ctx, *work, false, 10*time.Second, now); err != nil {
		t.Fatal(err)
	}
	payload = d.feedRuntimePayload(request, feed)
	if payload["rate_waiting"] != 0 || payload["rate_wait_ms"] != int64(10000) {
		t.Fatal(payload)
	}
	if err := store.RecordRateWait(ctx, *work, true, 0, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	payload = d.feedRuntimePayload(request, feed)
	if payload["rate_waiting"] != 0 || payload["rate_wait_ms"].(int64) > 120001 {
		t.Fatal("expired claim still counted", payload)
	}
	// Old attempts cannot overwrite a recovered consumer's current wait.
	recovered, err := store.Claim(ctx, "s", "f", now)
	if err != nil || recovered == nil {
		t.Fatal(err)
	}
	if err := store.RecordRateWait(ctx, *recovered, true, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRateWait(ctx, *work, false, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	payload = d.feedRuntimePayload(request, feed)
	if payload["rate_waiting"] != 1 || payload["rate_wait_ms"] != int64(10000) {
		t.Fatal("stale observer overwrote current attempt", payload)
	}
}
