package rsssourcedb

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

var benchStart = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

func benchConsumerStore(b *testing.B) (*Store, *sql.DB) {
	b.Helper()
	database := storeDatabase(b)
	consumerFeeds(b, database)
	if _, err := database.Exec(`INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at) VALUES('a',1,?)`, benchStart.Format(time.RFC3339Nano)); err != nil {
		b.Fatal(err)
	}
	store, err := New(database)
	if err != nil {
		b.Fatal(err)
	}
	return store, database
}

func benchEntries(prefix string, from, n int) []rsssource.Entry {
	entries := make([]rsssource.Entry, 0, n)
	for i := from; i < from+n; i++ {
		size := int64(1<<30 + i)
		entries = append(entries, rsssource.Entry{
			GUID: fmt.Sprintf("%s-%d", prefix, i), URL: fmt.Sprintf("https://site.example/download.php?id=%d", i),
			Title: fmt.Sprintf("Some.Release.Name.2026.1080p.WEB-DL-%s%d", prefix, i), SizeBytes: &size, PublishedAt: "Mon, 26 Oct 2026 00:00:00 +0000",
		})
	}
	return entries
}

// seedHistory adds n finished consumers (10% matched) discovered before the
// feed's last poll, as a long-running feed accumulates them.
func seedHistory(b *testing.B, database *sql.DB, n int) {
	b.Helper()
	if n == 0 {
		return
	}
	stamp := benchStart.Add(-time.Hour).Format("2006-01-02T15:04:05.000000000Z")
	if _, err := database.Exec(`WITH RECURSIVE seq(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM seq WHERE i+1<?)
		INSERT INTO rss_sources(site_id,source_key,guid,url,title,size_bytes,fingerprint,first_seen_at,updated_at,legacy_seen)
		SELECT 'site-1','guid:h-'||i,'h-'||i,'https://site.example/h/'||i,'History '||i,i,'fp-'||i,?,?,0 FROM seq`, n, stamp, stamp); err != nil {
		b.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO rss_source_consumers(site_id,feed_id,source_key,source_json,state,attempts,next_attempt_at,deadline_at,discovered_at,matched)
		SELECT site_id,'a',source_key,'{}','DONE',1,?,?,?,CASE WHEN CAST(substr(source_key,8) AS INTEGER)%10=0 THEN 1 ELSE 0 END FROM rss_sources WHERE source_key LIKE 'guid:h-%'`, stamp, stamp, stamp); err != nil {
		b.Fatal(err)
	}
}

// One feed poll persisting 100 entries: all new, all already known (the usual
// steady state), or a typical mix.
func BenchmarkDiscover(b *testing.B) {
	for _, tc := range []struct {
		name       string
		known, new int
	}{{"new=100", 0, 100}, {"known=100", 100, 0}, {"known=95,new=5", 95, 5}} {
		b.Run(tc.name, func(b *testing.B) {
			store, _ := benchConsumerStore(b)
			ctx := context.Background()
			known := benchEntries("k", 0, tc.known)
			if _, err := store.Discover(ctx, "site-1", "a", known, false, benchStart); err != nil {
				b.Fatal(err)
			}
			next := 0
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				entries := append(append([]rsssource.Entry(nil), known...), benchEntries("n", next, tc.new)...)
				next += tc.new
				if _, err := store.Discover(ctx, "site-1", "a", entries, false, benchStart); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Claim and finish one consumer on a feed that already holds `history`
// finished consumers.
func BenchmarkClaimFinish(b *testing.B) {
	for _, history := range []int{0, 10_000, 100_000} {
		b.Run(fmt.Sprintf("history=%d", history), func(b *testing.B) {
			store, database := benchConsumerStore(b)
			seedHistory(b, database, history)
			ctx := context.Background()
			now := benchStart.Add(time.Minute)
			next := 0
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				work, err := store.Claim(ctx, "site-1", "a", now)
				if err != nil {
					b.Fatal(err)
				}
				if work == nil {
					b.StopTimer()
					if _, err := store.Discover(ctx, "site-1", "a", benchEntries("w", next, 100), false, now); err != nil {
						b.Fatal(err)
					}
					next += 100
					b.StartTimer()
					if work, err = store.Claim(ctx, "site-1", "a", now); err != nil || work == nil {
						b.Fatalf("claim after refill = %v, %v", work, err)
					}
				}
				work.Matched = true
				if err := store.Finish(ctx, *work, "", now); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
