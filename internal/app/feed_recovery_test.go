package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/matchpipeline"
	"github.com/vertex-pro/vertex-pro/internal/racejob"
	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/rsssource"
	"github.com/vertex-pro/vertex-pro/internal/rsssourcedb"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/ruleservice"
	"github.com/vertex-pro/vertex-pro/internal/schedulercycle"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

func recoveryRuntime(t *testing.T, now *time.Time, feedURL string) *Runtime {
	t.Helper()
	runtime, err := New(context.Background(), Config{Database: db.Config{Path: filepath.Join(t.TempDir(), "recovery.db")}, Hosts: []string{"vertex.local"}, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if _, err := runtime.Database.Exec(`INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES('s','Site','https://example.test',1,'2026-10-22','2026-10-22')`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Database.Exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES('f','s','Feed',?,1,1440,'2026-10-22','2026-10-22')`, feedURL); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestPendingConsumerRecoversBeforeDailyFeedPoll(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 12, 0, 0, 123456789, time.UTC)
	initial := now
	var fetches, downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rss" {
			if fetches.Add(1) > 1 {
				if r.Header.Get("If-None-Match") != "etag-one" || r.Header.Get("If-Modified-Since") != "Fri, 23 Oct 2026 12:00:00 GMT" {
					t.Error("recovery changed RSS validators")
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", "etag-one")
			w.Header().Set("Last-Modified", "Fri, 23 Oct 2026 12:00:00 GMT")
			fmt.Fprintf(w, `<rss><channel><item><guid>candidate</guid><title>Linux</title><link>http://%s/file.torrent</link></item></channel></rss>`, r.Host)
			return
		}
		if downloads.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write([]byte("d4:infod6:lengthi42e4:name8:file.bin12:piece lengthi16384e6:pieces20:01234567890123456789ee"))
	}))
	defer server.Close()
	runtime := recoveryRuntime(t, &now, server.URL+"/rss")
	// A previously initialized feed is due for its normal daily poll.
	if _, err := runtime.Database.Exec(`INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at) VALUES('f',1,?)`, now.Add(-24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	store, err := rsssourcedb.New(runtime.Database)
	if err != nil {
		t.Fatal(err)
	}
	dedupe, _ := rsssource.New(store)
	fetcher, _ := rssfetch.New(server.Client(), 1<<20)
	downloader, err := torrentdownload.New(torrentdownload.Config{Client: server.Client(), AllowedHosts: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := ruleservice.New(runtime.Database, nil)
	definition := json.RawMessage(`{"version":1,"root":{"kind":"leaf","field":"title","operator":"contains","value":{"type":"string","string":"Linux"}}}`)
	if _, err := rules.Create(ctx, ruleservice.CreateInput{SiteID: "s", Name: "Linux", Enabled: true, Definition: definition}); err != nil {
		t.Fatal(err)
	}
	jobs, _ := racejobstore.New(runtime.Database)
	creator, _ := racejob.New(jobs, nil)
	runtime.scheduler, err = schedulercycle.New(fetcher, downloader, dedupe, matchpipeline.Deps{Rules: &rules, JobCreator: creator, Now: runtime.now, MatchFactory: func(ruleservice.Rule) (ruleaggregate.Match, error) {
		return ruleaggregate.Match{TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb", ParticipationWindow: time.Hour}, nil
	}}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.RunSchedulerCycle(ctx); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 1 || downloads.Load() != 1 {
		t.Fatalf("initial requests: RSS=%d artifacts=%d", fetches.Load(), downloads.Load())
	}
	type pollState struct {
		Initialized                            int
		Polled, Success, Error, ETag, Modified string
		Entries, Matched                       int
	}
	readPoll := func() pollState {
		t.Helper()
		var state pollState
		if err := runtime.Database.QueryRow(`SELECT r.initialized,r.last_polled_at,r.last_success_at,r.last_error_code,r.last_entries,r.last_matched,f.etag,f.last_modified FROM feed_runtime_state r JOIN feeds f ON f.feed_id=r.feed_id WHERE r.feed_id='f'`).Scan(&state.Initialized, &state.Polled, &state.Success, &state.Error, &state.Entries, &state.Matched, &state.ETag, &state.Modified); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := readPoll()
	now = initial.Add(time.Minute - time.Nanosecond)
	if err := runtime.RunSchedulerCycle(ctx); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 1 {
		t.Fatal("consumer backoff was shortened")
	}
	now = initial.Add(time.Minute)
	feeds, err := runtime.databaseEnabledFeeds(ctx)
	if err != nil || len(feeds) != 1 || !feeds[0].RecoverOnly || feeds[0].BaselineOnly {
		t.Fatalf("recovery selection=%+v err=%v", feeds, err)
	}
	if err := runtime.RunSchedulerCycle(ctx); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 1 || downloads.Load() != 2 {
		t.Fatalf("recovery requests: RSS=%d artifacts=%d", fetches.Load(), downloads.Load())
	}
	after := readPoll()
	if after.Matched != 1 {
		t.Fatalf("asynchronous match count=%d", after.Matched)
	}
	after.Matched = before.Matched
	if after != before {
		t.Fatalf("recovery changed RSS request facts: before=%+v after=%+v", before, after)
	}
	var state string
	var attempts, jobCount int
	if err := runtime.Database.QueryRow(`SELECT state,attempts FROM rss_source_consumers WHERE feed_id='f'`).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Database.QueryRow(`SELECT count(*) FROM race_jobs`).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if state != "DONE" || attempts != 2 || jobCount != 1 {
		t.Fatalf("state=%s attempts=%d jobs=%d", state, attempts, jobCount)
	}
	now = initial.Add(24 * time.Hour)
	if err := runtime.RunSchedulerCycle(ctx); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 2 || downloads.Load() != 2 || readPoll().Polled != now.Format(time.RFC3339Nano) {
		t.Fatal("normal RSS cadence did not resume after recovery")
	}
}

func TestRecoverySelectionHonorsBaselineAndDisabledFeeds(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	runtime := recoveryRuntime(t, &now, "https://example.test/rss")
	store, _ := rsssourcedb.New(runtime.Database)
	if _, err := store.Discover(ctx, "s", "f", []rsssource.Entry{{GUID: "pending", URL: "https://example.test/torrent"}}, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Database.Exec(`INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at) VALUES('f',0,?)`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	assertFeeds := func(count int) []schedulercycle.FeedRef {
		t.Helper()
		feeds, err := runtime.databaseEnabledFeeds(ctx)
		if err != nil || len(feeds) != count {
			t.Fatalf("feeds=%+v err=%v", feeds, err)
		}
		return feeds
	}
	assertFeeds(0)
	now = now.Add(24 * time.Hour)
	if feeds := assertFeeds(1); !feeds[0].BaselineOnly || feeds[0].RecoverOnly {
		t.Fatal("uninitialized feed skipped baseline")
	}
	for _, query := range []string{
		`UPDATE feed_runtime_state SET initialized=1,last_polled_at='2026-10-25T12:00:00Z'`,
		`UPDATE feeds SET enabled=0`,
		`UPDATE feeds SET enabled=1; UPDATE sites SET enabled=0`,
	} {
		if _, err := runtime.Database.Exec(query); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(query, "UPDATE feed_runtime_state") {
			if feeds := assertFeeds(1); !feeds[0].RecoverOnly {
				t.Fatal("expired pending work was not selected for closure")
			}
		} else {
			assertFeeds(0)
		}
	}
}
