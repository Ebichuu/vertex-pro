package app

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

// Exercise the complete Runtime boundary, including its startup recovery and
// background feed selection. No qui client or Add sender is configured.
func TestDeferredPreparationSurvivesRuntimeRestart(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprintf("redirect_%t", redirect), func(t *testing.T) {
			ctx := context.Background()
			var downloads, entries, polls atomic.Int32
			target := probeLoopbackServer(t, "127.0.0.41", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/hold" {
					w.Header().Set("Content-Length", "4")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				downloads.Add(1)
				w.Header().Set("Content-Type", "application/x-bittorrent")
				_, _ = w.Write(automationTorrent("resume-fixture"))
			})
			entry := probeLoopbackServer(t, "127.0.0.42", func(w http.ResponseWriter, r *http.Request) {
				entries.Add(1)
				http.Redirect(w, r, target.URL+"/torrent", http.StatusFound)
			})
			artifact := target.URL + "/torrent"
			if redirect {
				artifact = entry.URL + "/entry"
			}
			feed := probeLoopbackServer(t, "127.0.0.43", func(w http.ResponseWriter, r *http.Request) {
				polls.Add(1)
				fmt.Fprintf(w, `<rss><channel><item><guid>resume-fixture</guid><title>resume-fixture</title><enclosure url="%s" length="4096" type="application/x-bittorrent"/></item></channel></rss>`, artifact)
			})
			path := filepath.Join(t.TempDir(), "restart.db")
			database, err := db.Open(ctx, db.Config{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			runner, err := db.DefaultRunner()
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Apply(ctx, database); err != nil {
				t.Fatal(err)
			}
			stamp := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
			for _, q := range []struct {
				query string
				args  []any
			}{
				{`INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES('s','Site',?,1,?,?)`, []any{feed.URL, stamp, stamp}},
				{`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES('f','s','Feed',?,0,60,?,?)`, []any{feed.URL, stamp, stamp}},
				{`INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at) VALUES('f',1,?)`, []any{stamp}},
				{`UPDATE app_settings SET rate_limit_per_minute=600`, nil},
				{`INSERT INTO rules(rule_id,site_id,name,enabled,priority,revision,state,definition_json,created_at,updated_at) VALUES('r','s','Rule',1,10,1,'PENDING',?,?,?)`, []any{`{"condition_tree":{"version":1,"root":{"kind":"leaf","field":"title","operator":"contains","value":{"type":"string","string":"resume-"}}},"action_set_json":{"target_mode":"FIXED_INSTANCE","target_instance_id":"fixture"}}`, stamp, stamp}},
			} {
				if _, err := database.Exec(q.query, q.args...); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Database: db.Config{Path: path}, Hosts: []string{"vertex.local"}, Scheduler: &SchedulerConfig{Enabled: true, PollInterval: 20 * time.Millisecond, AllowedArtifactHosts: []string{strings.TrimPrefix(target.URL, "http://"), strings.TrimPrefix(entry.URL, "http://")}}}
			start := func() *Runtime {
				t.Helper()
				r, err := New(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = r.Close() })
				return r
			}
			stop := func(r *Runtime) {
				t.Helper()
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
				if stats := r.Database.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 {
					t.Fatalf("runtime retained database connections: %+v", stats)
				}
				if err := r.Database.Ping(); err == nil {
					t.Fatal("runtime database was not actually closed")
				}
			}
			first := start()
			// Occupy the same limiter used by production preparation, without
			// giving these two controlled requests their own consumer leases.
			client := first.rssFetcher.ArtifactClient(time.Minute)
			var held []*http.Response
			for range 2 {
				response, err := client.Get(target.URL + "/hold")
				if err != nil {
					t.Fatal(err)
				}
				held = append(held, response)
				defer response.Body.Close()
			}
			if _, err := first.Database.Exec(`UPDATE feeds SET enabled=1 WHERE feed_id='f'`); err != nil {
				t.Fatal(err)
			}
			probeWait(t, 5*time.Second, func() bool {
				var waiting int
				return first.Database.QueryRow(`SELECT count(*) FROM rss_source_consumers WHERE state='PENDING' AND attempts=0 AND artifact_deferred_until IS NOT NULL AND artifact_resume_url=?`, target.URL+"/torrent").Scan(&waiting) == nil && waiting == 1
			})
			// Pause the feed to settle any in-flight claim before closing. The
			// disabled setting must survive restart and keep the continuation idle.
			if _, err := first.Database.Exec(`UPDATE feeds SET enabled=0 WHERE feed_id='f'`); err != nil {
				t.Fatal(err)
			}
			probeWait(t, 5*time.Second, func() bool {
				first.preparationMu.Lock()
				defer first.preparationMu.Unlock()
				for _, n := range first.preparing {
					if n != 0 {
						return false
					}
				}
				return true
			})
			var generation int64
			var resume, due string
			var hops, attempts int
			if err := first.Database.QueryRow(`SELECT claim_generation,artifact_resume_url,artifact_redirects,artifact_deferred_until,attempts FROM rss_source_consumers`).Scan(&generation, &resume, &hops, &due, &attempts); err != nil {
				t.Fatal(err)
			}
			wantHops := 0
			if redirect {
				wantHops = 1
			}
			if generation < 1 || attempts != 0 || hops != wantHops || resume != target.URL+"/torrent" || downloads.Load() != 0 {
				t.Fatal("invalid pre-restart continuation")
			}
			stop(first)
			for _, response := range held {
				_ = response.Body.Close()
			}

			second := start()
			if err := second.RunSchedulerCycle(ctx); err != nil {
				t.Fatal(err)
			}
			// The persisted slot retry is 250 ms and idle workers poll within a
			// second. Leave the feed disabled beyond both boundaries so this
			// assertion cannot pass merely because the continuation is not due.
			time.Sleep(1200 * time.Millisecond)
			var unchanged int
			if err := second.Database.QueryRow(`SELECT count(*) FROM rss_source_consumers WHERE state='PENDING' AND claim_generation=? AND artifact_resume_url=? AND artifact_redirects=? AND artifact_deferred_until=? AND attempts=0`, generation, resume, hops, due).Scan(&unchanged); err != nil || unchanged != 1 || downloads.Load() != 0 {
				t.Fatal("restart lost or ran disabled continuation", err)
			}
			if _, err := second.Database.Exec(`UPDATE feeds SET enabled=1 WHERE feed_id='f'`); err != nil {
				t.Fatal(err)
			}
			probeWait(t, 5*time.Second, func() bool {
				var done, jobs int
				return second.Database.QueryRow(`SELECT count(*) FROM rss_source_consumers WHERE state='DONE' AND attempts=1 AND claim_generation>? AND artifact_resume_url='' AND artifact_redirects=0 AND artifact_deferred_until IS NULL`, generation).Scan(&done) == nil && done == 1 && second.Database.QueryRow(`SELECT count(*) FROM race_jobs`).Scan(&jobs) == nil && jobs == 1
			})
			stop(second)
			third := start()
			for range 3 {
				if err := third.RunSchedulerCycle(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var jobs, artifacts int
			if err := third.Database.QueryRow(`SELECT (SELECT count(*) FROM race_jobs),(SELECT count(*) FROM job_torrent_artifacts)`).Scan(&jobs, &artifacts); err != nil || jobs != 1 || artifacts != 1 {
				t.Fatal("completed work replayed", jobs, artifacts, err)
			}
			if downloads.Load() != 1 || entries.Load() != int32(wantHops) || polls.Load() != 1 {
				t.Fatalf("duplicate requests: downloads=%d entries=%d RSS=%d", downloads.Load(), entries.Load(), polls.Load())
			}
			stop(third)
			t.Logf("RUNTIME_RESTART redirect=%t starts=3 artifacts=1 jobs=1 downloads=1 entry_requests=%d RSS_requests=1 disabled_continuation_preserved=true", redirect, entries.Load())
		})
	}
}
