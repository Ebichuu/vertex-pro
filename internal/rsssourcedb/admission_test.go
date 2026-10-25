package rsssourcedb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
)

func TestDeferredConsumerSurvivesDatabaseReopenAndFencesReusedAttempt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "deferred.db")
	database, err := db.Open(ctx, db.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	runner, _ := db.DefaultRunner()
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sites(site_id,name,base_url,created_at,updated_at) VALUES('site-1','Site','https://site.example','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	consumerFeeds(t, database)
	store, _ := New(database)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.Discover(ctx, "site-1", "a", []rsssource.Entry{{GUID: "one", URL: "https://site.example/entry"}}, false, now); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(ctx, "site-1", "a", now)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	resume := "https://download.example/file"
	if err := store.Defer(ctx, *first, time.Now().Add(time.Hour), resume, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = db.Open(ctx, db.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, _ = New(database)
	if work, err := store.Claim(ctx, "site-1", "a", now.Add(time.Minute)); err != nil || work != nil {
		t.Fatalf("future permission deadline lost after restart: %v %v", work, err)
	}
	// Make the durable wall-clock wake due; the domain clock stays deterministic.
	if _, err := database.Exec(`UPDATE rss_source_consumers SET artifact_deferred_until=?`, stamp(time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second, err := store.Claim(ctx, "site-1", "a", now)
	if err != nil || second == nil {
		t.Fatal(err)
	}
	if second.Attempts != first.Attempts || second.ClaimGeneration <= first.ClaimGeneration || second.ResumeURL != resume || second.Redirects != 1 {
		t.Fatalf("continuation lost: %+v", second)
	}
	if err := store.Finish(ctx, *first, "", now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRateWait(ctx, *first, true, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Defer(ctx, *first, time.Now(), "https://stale.example", 0, now); err != nil {
		t.Fatal(err)
	}
	var state string
	var waitMS, attempts, waiting int
	if err := database.QueryRow(`SELECT state,attempts,rate_wait_ms,rate_wait_started_at IS NOT NULL FROM rss_source_consumers`).Scan(&state, &attempts, &waitMS, &waiting); err != nil {
		t.Fatal(err)
	}
	if state != "PENDING" || attempts != 1 || waitMS < 995 || waitMS > 1005 || waiting != 0 {
		t.Fatalf("stale claim changed current work: %s %d %d %d", state, attempts, waitMS, waiting)
	}
	if err := store.Finish(ctx, *second, "", now); err != nil {
		t.Fatal(err)
	}
	var continuation string
	if err := database.QueryRow(`SELECT state,artifact_resume_url FROM rss_source_consumers`).Scan(&state, &continuation); err != nil || state != "DONE" || continuation != "" {
		t.Fatalf("finish=%s continuation=%q err=%v", state, continuation, err)
	}
}

func TestDeferredConsumersDoNotSpendRetriesButFailuresAndDeadlineRemainBounded(t *testing.T) {
	database := storeDatabase(t)
	consumerFeeds(t, database)
	store, _ := New(database)
	ctx := context.Background()
	now := time.Now()
	if _, err := store.Discover(ctx, "site-1", "a", []rsssource.Entry{{GUID: "one", URL: "https://site.example/one"}}, false, now); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		work, err := store.Claim(ctx, "site-1", "a", now)
		if err != nil || work == nil || work.Attempts != 1 {
			t.Fatalf("permission waits exhausted attempts: %+v %v", work, err)
		}
		for range 2 { // Duplicate completion must return the retry budget just once.
			if err := store.Defer(ctx, *work, time.Now().Add(-time.Second), work.Source.URL, 0, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	for attempt := 1; attempt <= rsssource.MaxConsumerAttempts; attempt++ {
		work, err := store.Claim(ctx, "site-1", "a", now)
		if err != nil || work == nil || work.Attempts != attempt {
			t.Fatalf("attempt=%d work=%+v err=%v", attempt, work, err)
		}
		if err := store.Finish(ctx, *work, "RSS_ITEM_PROCESSING_FAILED", now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(rsssource.ConsumerRetry(attempt))
	}
	if work, err := store.Claim(ctx, "site-1", "a", now); err != nil || work != nil {
		t.Fatal("failed consumer was reclaimed", err)
	}
	if _, err := store.Discover(ctx, "site-1", "b", []rsssource.Entry{{GUID: "deadline", URL: "https://site.example/deadline"}}, false, now); err != nil {
		t.Fatal(err)
	}
	work, err := store.Claim(ctx, "site-1", "b", now)
	if err != nil || work == nil {
		t.Fatal(err)
	}
	if err := store.Defer(ctx, *work, time.Now().Add(48*time.Hour), work.Source.URL, 0, now); err != nil {
		t.Fatal(err)
	}
	if item, err := store.Claim(ctx, "site-1", "b", work.Deadline); err != nil || item != nil {
		t.Fatal("expired yielded consumer reclaimed", err)
	}
	var state, code string
	if err := database.QueryRow(`SELECT state,last_error_code FROM rss_source_consumers WHERE feed_id='b'`).Scan(&state, &code); err != nil || state != "FAILED" || code != "PROCESSING_DEADLINE" {
		t.Fatal(state, code, err)
	}
}

func TestPreparationAdmissionMigrationKeepsExistingLeaseAndCompletedWork(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "v23.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	all, _ := db.DefaultRunner()
	prior := db.Runner{}
	for _, migration := range all.Migrations {
		if migration.Version < 24 {
			prior.Migrations = append(prior.Migrations, migration)
		}
	}
	if err := prior.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sites(site_id,name,base_url,created_at,updated_at) VALUES('site-1','Site','https://site.example','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	consumerFeeds(t, database)
	store, _ := New(database)
	now := time.Now()
	if _, err := store.Discover(ctx, "site-1", "a", []rsssource.Entry{{GUID: "done", URL: "https://site.example/done"}, {GUID: "pending", URL: "https://site.example/pending"}}, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE rss_source_consumers SET state='DONE',attempts=1 WHERE source_key='guid:done';`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE rss_source_consumers SET attempts=1,next_attempt_at=?,preparation_started_at=? WHERE source_key='guid:pending'`, stamp(now.Add(rsssource.ConsumerLease)), stamp(now)); err != nil {
		t.Fatal(err)
	}
	if err := all.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	if work, err := store.Claim(ctx, "site-1", "a", now); err != nil || work != nil {
		t.Fatal("migration reopened existing lease", err)
	}
	work, err := store.Claim(ctx, "site-1", "a", now.Add(rsssource.ConsumerLease))
	if err != nil || work == nil || work.Source.SourceKey != "guid:pending" || work.Attempts != 2 || work.ClaimGeneration != 1 || work.ResumeURL != "" || work.Redirects != 0 {
		t.Fatalf("migration changed existing work: %+v %v", work, err)
	}
	if err := store.Finish(ctx, *work, "", now.Add(rsssource.ConsumerLease)); err != nil {
		t.Fatal(err)
	}
	if work, err := store.Claim(ctx, "site-1", "a", now.Add(time.Hour)); err != nil || work != nil {
		t.Fatal("migration replayed completed work", err)
	}
}
