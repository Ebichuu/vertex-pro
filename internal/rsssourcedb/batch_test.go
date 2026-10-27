package rsssourcedb

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

// The indexed recount must agree with the former julianday() query on the
// same rows: matched, discovered at/after the poll (RFC3339Nano, trimmed), and
// never legacy rows with an empty discovered_at.
func TestRefreshLastMatchedMatchesFormerQuery(t *testing.T) {
	database := storeDatabase(t)
	consumerFeeds(t, database)
	ctx := context.Background()
	polled := time.Date(2026, 10, 26, 12, 0, 0, 500_000_000, time.UTC)
	if _, err := database.Exec(`INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at) VALUES('a',1,?)`, polled.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		discovered string
		matched    int
	}{
		{stamp(polled.Add(-time.Second)), 1}, {stamp(polled), 1}, {stamp(polled.Add(time.Millisecond)), 1},
		{stamp(polled.Add(time.Hour)), 0}, {"", 1}, {stamp(polled.Add(-time.Nanosecond * 2_000_000)), 1},
	}
	for i, row := range rows {
		key := fmt.Sprintf("guid:%d", i)
		if _, err := database.Exec(`INSERT INTO rss_sources(site_id,source_key,guid,url,title,fingerprint,first_seen_at,updated_at,legacy_seen) VALUES('site-1',?,?,'','t','fp','x','x',0)`, key, key); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO rss_source_consumers(site_id,feed_id,source_key,source_json,state,next_attempt_at,deadline_at,discovered_at,matched) VALUES('site-1','a',?,'{}','DONE','x','x',?,?)`, key, row.discovered, row.matched); err != nil {
			t.Fatal(err)
		}
	}
	var former int
	if err := database.QueryRow(`SELECT count(*) FROM rss_source_consumers c, feed_runtime_state f WHERE f.feed_id='a' AND c.feed_id='a' AND c.matched=1 AND julianday(c.discovered_at)>=julianday(f.last_polled_at)`).Scan(&former); err != nil {
		t.Fatal(err)
	}
	if err := db.RunImmediate(ctx, database, func(ctx context.Context, tx *db.ImmediateTx) error { return RefreshLastMatched(ctx, tx, "a") }); err != nil {
		t.Fatal(err)
	}
	var got int
	if err := database.QueryRow(`SELECT last_matched FROM feed_runtime_state WHERE feed_id='a'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 2 || got != former {
		t.Fatalf("last_matched = %d, former query = %d, want 2", got, former)
	}
	if err := db.RunImmediate(ctx, database, func(ctx context.Context, tx *db.ImmediateTx) error { return RefreshLastMatched(ctx, tx, "b") }); err != nil {
		t.Fatalf("feed without runtime state: %v", err)
	}
}

func TestLastMatchedCountUsesPartialIndex(t *testing.T) {
	database := storeDatabase(t)
	rows, err := database.Query(`EXPLAIN QUERY PLAN SELECT count(*) FROM rss_source_consumers c WHERE c.feed_id=? AND c.matched=1 AND c.discovered_at>=?`, "a", stamp(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if joined := strings.Join(plan, "; "); !strings.Contains(joined, "ix_consumer_matched_discovered") {
		t.Fatalf("query plan = %s", joined)
	}
}

func TestClaimCarriesRuleFactsAndDiscoveryTime(t *testing.T) {
	database := storeDatabase(t)
	consumerFeeds(t, database)
	store, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	discovered := time.Date(2026, 10, 26, 8, 0, 0, 123456789, time.UTC)
	if _, err := store.Discover(ctx, "site-1", "a", []rsssource.Entry{{GUID: "x", Title: "X", Category: " Movies ", Publisher: "Alice"}}, false, discovered); err != nil {
		t.Fatal(err)
	}
	work, err := store.Claim(ctx, "site-1", "a", discovered.Add(time.Second))
	if err != nil || work == nil {
		t.Fatalf("claim = %v, %v", work, err)
	}
	if work.Source.Category != "Movies" || work.Source.Publisher != "Alice" || !work.DiscoveredAt.Equal(discovered) {
		t.Fatalf("claimed = %+v discovered=%v", work.Source, work.DiscoveredAt)
	}
	var fingerprint string
	database.QueryRow(`SELECT fingerprint FROM rss_sources WHERE source_key='guid:x'`).Scan(&fingerprint)
	if want, _ := rsssource.Normalize("site-1", rsssource.Entry{GUID: "x", Title: "X"}); fingerprint != want.Fingerprint {
		t.Fatal("category/publisher changed the dedupe fingerprint")
	}
}

// Batching spans several chunks and keeps per-entry outcomes: new, in-batch
// duplicate, changed, unchanged and legacy-skipped.
func TestDiscoverBatchesAcrossChunks(t *testing.T) {
	database := storeDatabase(t)
	consumerFeeds(t, database)
	store, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	if _, err := database.Exec(`INSERT INTO rss_sources(site_id,source_key,guid,url,title,fingerprint,first_seen_at,updated_at,legacy_seen) VALUES('site-1','guid:legacy','legacy','','Legacy','old','x','x',1)`); err != nil {
		t.Fatal(err)
	}
	entries := make([]rsssource.Entry, 0, 260)
	for i := 0; i < 250; i++ {
		entries = append(entries, rsssource.Entry{GUID: fmt.Sprintf("g-%d", i), Title: fmt.Sprintf("T %d", i)})
	}
	entries = append(entries, rsssource.Entry{GUID: "g-7", Title: "T 7"}, rsssource.Entry{GUID: "legacy", Title: "Legacy renamed"})
	first, err := store.Discover(ctx, "site-1", "a", entries, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.New) != 250 || first.Duplicates != 1 || len(first.Updated) != 1 {
		t.Fatalf("first = new %d dup %d updated %d", len(first.New), first.Duplicates, len(first.Updated))
	}
	entries[3].Title = "T 3 repack"
	second, err := store.Discover(ctx, "site-1", "a", entries[:250], false, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.New) != 0 || len(second.Updated) != 1 || second.Updated[0].GUID != "g-3" || second.Duplicates != 249 {
		t.Fatalf("second = new %d updated %d dup %d", len(second.New), len(second.Updated), second.Duplicates)
	}
	var pending, skipped, sources int
	database.QueryRow(`SELECT count(*) FROM rss_source_consumers WHERE feed_id='a' AND state='PENDING'`).Scan(&pending)
	database.QueryRow(`SELECT count(*) FROM rss_source_consumers WHERE feed_id='a' AND state='SKIPPED' AND source_key='guid:legacy'`).Scan(&skipped)
	database.QueryRow(`SELECT count(*) FROM rss_sources WHERE site_id='site-1'`).Scan(&sources)
	if pending != 250 || skipped != 1 || sources != 251 {
		t.Fatalf("pending=%d legacy skipped=%d sources=%d", pending, skipped, sources)
	}
}
