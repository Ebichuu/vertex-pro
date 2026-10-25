package resourcesapi

import (
	"context"
	"encoding/json"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/rsssourcedb"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPreparationsExposeProgressWithoutPrivateSourceAndRecoverExpiredLease(t *testing.T) {
	database := openMigrated(t)
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	service, _ := rssservice.NewWithClock(database, func() time.Time { return now })
	if _, err := service.CreateSite(ctx, rssservice.CreateSiteInput{ID: "s", Name: "Site", BaseURL: "https://site.example", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateFeed(ctx, rssservice.CreateFeedInput{ID: "f", SiteID: "s", Name: "Feed", FeedURL: "https://site.example/rss", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	store, _ := rsssourcedb.New(database)
	size := int64(1000000000)
	if _, err := store.Discover(ctx, "s", "f", []rsssource.Entry{{GUID: "one", URL: "https://site.example/one?passkey=private-credential", Title: "One", SizeBytes: &size}, {GUID: "two", URL: "https://site.example/two?passkey=private-credential", Title: "Two"}}, false, now); err != nil {
		t.Fatal(err)
	}
	d := Deps{Database: database, Now: func() time.Time { return now }}
	read := func(query string) ([]map[string]any, string) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/feeds/f/preparations"+query, nil)
		r.SetPathValue("id", "f")
		w := httptest.NewRecorder()
		d.listPreparations(w, r)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-credential") || strings.Contains(w.Body.String(), "source_key") || strings.Contains(w.Body.String(), "https://") {
			t.Fatal("private input exposed")
		}
		var page struct {
			Items []map[string]any `json:"items"`
			Next  string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page.Items, page.Next
	}
	rows, next := read("?limit=1")
	if len(rows) != 1 || next == "" || rows[0]["state"] != "QUEUED" {
		t.Fatalf("rows=%v next=%s", rows, next)
	}
	second, end := read("?limit=1&cursor=" + next)
	if len(second) != 1 || end != "" || second[0]["id"] == rows[0]["id"] {
		t.Fatal("pagination")
	}
	work, err := store.Claim(ctx, "s", "f", now)
	if err != nil || work == nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		rows, _ := read("")
		for _, row := range rows {
			if row["title"] == work.Source.Title {
				if row["state"] != want {
					t.Fatalf("got=%v want=%s", row, want)
				}
				return
			}
		}
		t.Fatal("work missing")
	}
	check("PREPARING")
	if err = store.RecordRateWait(ctx, *work, true, 0, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	check("RATE_WAIT")
	now = now.Add(rsssource.ConsumerLease)
	check("QUEUED")
	work, err = store.Claim(ctx, "s", "f", now)
	if err != nil || work == nil {
		t.Fatal(err)
	}
	if err = store.Finish(ctx, *work, "TORRENT_FETCH_FAILED", now); err != nil {
		t.Fatal(err)
	}
	check("RETRY_WAIT")
	now = now.Add(rsssource.ConsumerLifetime)
	check("FAILED")
	r := httptest.NewRequest("GET", "/api/v1/feeds/f/preparations?cursor=-1", nil)
	r.SetPathValue("id", "f")
	w := httptest.NewRecorder()
	d.listPreparations(w, r)
	if w.Code != 422 {
		t.Fatal(w.Code)
	}
}
