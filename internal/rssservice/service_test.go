package rssservice

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceCreatesAndListsSitesAndFeeds(t *testing.T) {
	database := openDatabase(t)
	defer database.Close()
	service, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	site, err := service.CreateSite(context.Background(), CreateSiteInput{ID: "site-a", Name: "Site A", BaseURL: "https://a.example", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if site.BaseURL != "https://a.example" {
		t.Fatalf("site=%+v", site)
	}
	feed, err := service.CreateFeed(context.Background(), CreateFeedInput{ID: "feed-a", SiteID: "site-a", Name: "Feed A", FeedURL: "https://a.example/rss", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if feed.SiteID != "site-a" || feed.PollMinutes != 15 {
		t.Fatalf("feed=%+v", feed)
	}
	sites, err := service.ListSites(context.Background())
	if err != nil || len(sites) != 1 {
		t.Fatalf("sites=%+v err=%v", sites, err)
	}
	feeds, err := service.ListFeeds(context.Background(), "site-a")
	if err != nil || len(feeds) != 1 {
		t.Fatalf("feeds=%+v err=%v", feeds, err)
	}
}

func TestFeedQueryCredentialsSurviveCreateAndUpdate(t *testing.T) {
	database := openDatabase(t)
	defer database.Close()
	service, _ := New(database)
	ctx := context.Background()
	if _, err := service.CreateSite(ctx, CreateSiteInput{ID: "site-query", Name: "Site", BaseURL: "https://site.example", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	url := "https://site.example/torrentrss.php?passkey=test-only&rows=10&paid=0&linktype=dl"
	feed, err := service.CreateFeed(ctx, CreateFeedInput{ID: "feed-query", SiteID: "site-query", Name: "RSS", FeedURL: url, Enabled: true})
	if err != nil || feed.FeedURL != url {
		t.Fatalf("query URL was not preserved: %v", err)
	}
	updatedURL := url + "&category=2"
	updated, err := service.UpdateFeed(ctx, UpdateFeedInput{ID: feed.ID, ExpectedUpdatedAt: feed.UpdatedAt, FeedURL: &updatedURL})
	if err != nil || updated.FeedURL != updatedURL {
		t.Fatalf("updated query URL was not preserved: %v", err)
	}
	for _, invalid := range []string{"https://user:secret@site.example/rss?passkey=x", "file:///rss?key=x", "https://site.example/rss#fragment", "https://site.example/rss?passkey=%zz"} {
		if _, err := validateFeedURL(invalid); err == nil {
			t.Fatal("invalid feed URL accepted")
		}
	}
}
func TestServiceRejectsInvalidInputAndCrossSiteDuplicates(t *testing.T) {
	database := openDatabase(t)
	defer database.Close()
	service, _ := New(database)
	cases := []CreateSiteInput{{ID: "", Name: "x", BaseURL: "https://a.example"}, {ID: "site", Name: "", BaseURL: "https://a.example"}, {ID: "site", Name: "x", BaseURL: "file:///tmp/a"}, {ID: "site", Name: "x", BaseURL: "https://a.example?token=secret"}}
	for _, input := range cases {
		if _, err := service.CreateSite(context.Background(), input); err == nil {
			t.Fatalf("site accepted=%+v", input)
		}
	}
	if _, err := service.CreateSite(context.Background(), CreateSiteInput{ID: "site-a", Name: "A", BaseURL: "https://a.example", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateFeed(context.Background(), CreateFeedInput{ID: "feed-a", SiteID: "missing", Name: "F", FeedURL: "https://a.example/rss", Enabled: true}); err == nil {
		t.Fatal("missing site accepted")
	}
	if _, err := service.CreateFeed(context.Background(), CreateFeedInput{ID: "feed-a", SiteID: "site-a", Name: "F", FeedURL: "https://a.example/rss", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateFeed(context.Background(), CreateFeedInput{ID: "feed-b", SiteID: "site-a", Name: "Invalid interval", FeedURL: "https://a.example/other", Enabled: true, PollMinutes: 1441}); err == nil {
		t.Fatal("invalid poll minutes accepted")
	}
}
func TestServiceUpdatesFeedWithAtomicETag(t *testing.T) {
	database := openDatabase(t)
	defer database.Close()
	clock := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	service, err := NewWithClock(database, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSite(context.Background(), CreateSiteInput{ID: "site-update", Name: "Site", BaseURL: "https://update.example", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	feed, err := service.CreateFeed(context.Background(), CreateFeedInput{ID: "feed-update", SiteID: "site-update", Name: "Old", FeedURL: "https://update.example/rss", Enabled: true, PollMinutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	name := "New"
	enabled := false
	pollMinutes := 30
	updated, err := service.UpdateFeed(context.Background(), UpdateFeedInput{ID: feed.ID, ExpectedUpdatedAt: feed.UpdatedAt, Name: &name, Enabled: &enabled, PollMinutes: &pollMinutes})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "New" || updated.Enabled || updated.PollMinutes != 30 || updated.UpdatedAt == feed.UpdatedAt {
		t.Fatalf("updated feed=%+v", updated)
	}
	if _, err := service.UpdateFeed(context.Background(), UpdateFeedInput{ID: feed.ID, ExpectedUpdatedAt: feed.UpdatedAt, FeedURL: stringPointer("https://update.example/stale")}); !errors.Is(err, ErrFeedRevisionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	if _, err := service.GetFeed(context.Background(), "missing-feed"); !errors.Is(err, ErrFeedNotFound) {
		t.Fatalf("missing feed error=%v", err)
	}
}

func stringPointer(value string) *string { return &value }

func openDatabase(t *testing.T) *sql.DB {
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "vertex-pro.db")})
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
	return database
}
