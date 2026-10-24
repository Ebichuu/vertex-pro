package rsssourcedb

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

func consumerFeeds(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, id := range []string{"a", "b", "baseline"} {
		if _, err := database.Exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,created_at,updated_at) VALUES(?,'site-1',?,?,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, id, "https://site.example/"+id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConsumerLegacyUpgradeDoesNotReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	database, err := db.Open(ctx, db.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	all, _ := db.DefaultRunner()
	old := db.Runner{}
	for _, m := range all.Migrations {
		if m.Version < 19 {
			old.Migrations = append(old.Migrations, m)
		}
	}
	if err := old.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sites(site_id,name,base_url,created_at,updated_at) VALUES('site-1','Site','https://site.example','2026-01-01','2026-01-01'); INSERT INTO rss_sources(site_id,source_key,guid,url,title,fingerprint,first_seen_at,updated_at) VALUES('site-1','guid:old','old','https://site.example/old','Old','old','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if err := all.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	consumerFeeds(t, database)
	store, _ := New(database)
	now := time.Now()
	for _, feed := range []string{"a", "b"} {
		if _, err := store.Discover(ctx, "site-1", feed, []rsssource.Entry{{GUID: "old", Title: "Updated old", URL: "https://site.example/old"}}, false, now); err != nil {
			t.Fatal(err)
		}
		work, err := store.Claim(ctx, "site-1", feed, now)
		if err != nil || work != nil {
			t.Fatalf("legacy replay: %v %v", work, err)
		}
	}
	var oldFlag, skipped int
	database.QueryRow(`SELECT legacy_seen FROM rss_sources`).Scan(&oldFlag)
	database.QueryRow(`SELECT COUNT(*) FROM rss_source_consumers WHERE state='SKIPPED'`).Scan(&skipped)
	if oldFlag != 1 || skipped != 2 {
		t.Fatalf("legacy=%d skipped=%d", oldFlag, skipped)
	}
}

func TestConsumerDiscoveryAtomicAndFeedScoped(t *testing.T) {
	database := storeDatabase(t)
	consumerFeeds(t, database)
	store, _ := New(database)
	ctx := context.Background()
	now := time.Now()
	entry := rsssource.Entry{GUID: "new", Title: "A", URL: "https://site.example/a.torrent", PublishedAt: "2026-10-23T00:00:00Z"}
	result, err := store.Discover(ctx, "site-1", "a", []rsssource.Entry{entry, {URL: "http://%zz?passkey=private-test-value"}}, false, now)
	if err != nil || len(result.New) != 1 || len(result.Invalid) != 1 || result.Invalid[0].Index != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM rss_sources`).Scan(&n)
	if n != 1 {
		t.Fatal("valid entry was lost")
	}
	var event string
	if err := database.QueryRow(`SELECT safe_message||safe_data_json FROM system_events WHERE code='RSS_ENTRY_INVALID'`).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(event, "passkey") || strings.Contains(event, "private-test-value") {
		t.Fatal("invalid input leaked into event")
	}
	if _, err := store.Discover(ctx, "site-1", "a", []rsssource.Entry{entry}, false, now); err != nil {
		t.Fatal(err)
	}
	entry.Title = "B"
	entry.URL = "https://site.example/b.torrent"
	if _, err := store.Discover(ctx, "site-1", "b", []rsssource.Entry{entry}, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Discover(ctx, "site-1", "baseline", []rsssource.Entry{entry}, true, now); err != nil {
		t.Fatal(err)
	}
	a, err := store.Claim(ctx, "site-1", "a", now)
	if err != nil || a == nil || a.Source.Title != "A" || a.Source.PublishedAt == "" {
		t.Fatalf("first input lost: %+v %v", a, err)
	}
	b, err := store.Claim(ctx, "site-1", "b", now)
	if err != nil || b == nil || b.Source.Title != "B" {
		t.Fatalf("second feed lost: %+v %v", b, err)
	}
	if work, err := store.Claim(ctx, "site-1", "baseline", now); err != nil || work != nil {
		t.Fatal("baseline must not consume overlapping source")
	}
	if err := store.Finish(ctx, *a, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Discover(ctx, "site-1", "a", []rsssource.Entry{entry}, false, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if work, err := store.Claim(ctx, "site-1", "a", now.Add(time.Hour)); err != nil || work != nil {
		t.Fatal("DONE source was replayed")
	}
}

func TestDiscoveryKeeps29ValidEntriesAndRollsBackDatabaseFailure(t *testing.T) {
	for _, dbFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(dbFailure), func(t *testing.T) {
			database := storeDatabase(t)
			consumerFeeds(t, database)
			store, _ := New(database)
			entries := []rsssource.Entry{{URL: "http://%zz"}}
			for i := 0; i < 29; i++ {
				entries = append(entries, rsssource.Entry{GUID: fmt.Sprint(i), URL: fmt.Sprintf("https://site.example/%d", i)})
			}
			if dbFailure {
				if _, err := database.Exec(`CREATE TRIGGER reject_consumer BEFORE INSERT ON rss_source_consumers WHEN NEW.source_key='guid:15' BEGIN SELECT RAISE(ABORT,'fixture write failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			result, err := store.Discover(context.Background(), "site-1", "a", entries, false, time.Now())
			want := 29
			if dbFailure {
				want = 0
				if err == nil || len(result.New) != 0 {
					t.Fatal("failed transaction reported success")
				}
			} else if err != nil || len(result.Invalid) != 1 {
				t.Fatal(err, result)
			}
			for _, table := range []string{"rss_sources", "rss_source_consumers"} {
				var n int
				if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != want {
					t.Fatalf("%s count=%d err=%v", table, n, err)
				}
			}
		})
	}
}

func TestConsumerLeaseRetryBoundsAndStaleCompletion(t *testing.T) {
	database := storeDatabase(t)
	consumerFeeds(t, database)
	store, _ := New(database)
	ctx := context.Background()
	now := time.Now()
	entries := []rsssource.Entry{{GUID: "new", URL: "https://site.example/new"}}
	if _, err := store.Discover(ctx, "site-1", "a", entries, false, now); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(ctx, "site-1", "a", now)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	reopened, _ := New(database)
	if work, err := reopened.Claim(ctx, "site-1", "a", now.Add(time.Minute)); err != nil || work != nil {
		t.Fatal("lease allowed concurrent consumption")
	}
	now = now.Add(rsssource.ConsumerLease)
	second, err := reopened.Claim(ctx, "site-1", "a", now)
	if err != nil || second == nil || second.Attempts != 2 {
		t.Fatal("crash recovery missing", err)
	}
	if err := store.Finish(ctx, *first, "", now); err != nil {
		t.Fatal(err)
	}
	var state string
	database.QueryRow(`SELECT state FROM rss_source_consumers`).Scan(&state)
	if state != "PENDING" {
		t.Fatal("stale completion won")
	}
	current := second
	for attempt := 2; attempt <= rsssource.MaxConsumerAttempts; attempt++ {
		if current.Attempts != attempt {
			t.Fatal("attempt reset")
		}
		if err := reopened.Finish(ctx, *current, "RSS_ITEM_PROCESSING_FAILED", now); err != nil {
			t.Fatal(err)
		}
		if work, err := reopened.Claim(ctx, "site-1", "a", now); err != nil || work != nil {
			t.Fatal("backoff ignored")
		}
		now = now.Add(rsssource.ConsumerRetry(attempt))
		next, err := reopened.Claim(ctx, "site-1", "a", now)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == rsssource.MaxConsumerAttempts {
			if next != nil {
				t.Fatal("retry limit ignored")
			}
		} else {
			if next == nil {
				t.Fatal("retry missing")
			}
			current = next
		}
	}
	database.QueryRow(`SELECT state FROM rss_source_consumers`).Scan(&state)
	if state != "FAILED" {
		t.Fatal(state)
	}
	if _, err := store.Discover(ctx, "site-1", "b", entries, false, now); err != nil {
		t.Fatal(err)
	}
	if work, err := store.Claim(ctx, "site-1", "b", now.Add(rsssource.ConsumerLifetime)); err != nil || work != nil {
		t.Fatal("deadline ignored")
	}
}

func TestConsumerClaimHasOnlyOneConcurrentOwner(t *testing.T) {
	database := storeDatabase(t)
	consumerFeeds(t, database)
	store, _ := New(database)
	ctx := context.Background()
	now := time.Now()
	if _, err := store.Discover(ctx, "site-1", "a", []rsssource.Entry{{GUID: "one", URL: "https://site.example/one"}}, false, now); err != nil {
		t.Fatal(err)
	}
	type result struct {
		work *rsssource.Consumer
		err  error
	}
	results := make(chan result, 8)
	for i := 0; i < 8; i++ {
		go func() { work, err := store.Claim(ctx, "site-1", "a", now); results <- result{work, err} }()
	}
	owners := 0
	for i := 0; i < 8; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.work != nil {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("concurrent owners=%d", owners)
	}
}
