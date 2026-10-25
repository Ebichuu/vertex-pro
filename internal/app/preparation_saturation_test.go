package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

// Exercises the finite preparation pool using production Runtime workers,
// SQLite and loopback HTTP. No qui instance or Add sender is configured.
// H remains blocked until K starts: permission waiters must yield their workers.
func TestPreparationPoolSaturation(t *testing.T) {
	if testing.Short() {
		t.Skip("default-budget preparation-pool measurement with a controlled 3-second hold")
	}
	runPreparationPoolIsolation(t, 30)
}

// Short mode still exercises the Runtime isolation and continuation assertions
// under the race detector, without measuring default-budget batch throughput.
func TestPreparationPoolIsolationFast(t *testing.T) {
	runPreparationPoolIsolation(t, 600)
}

func runPreparationPoolIsolation(t *testing.T, perMinute int) {
	for _, scenario := range []struct {
		origins  int
		redirect bool
	}{{1, false}, {4, false}, {4, true}} {
		t.Run(fmt.Sprintf("rss_origins_%d_redirect_%t", scenario.origins, scenario.redirect), func(t *testing.T) {
			origins := scenario.origins
			ctx := context.Background()
			gate := make(chan struct{})
			var release sync.Once
			releaseH := func() { release.Do(func() { close(gate) }) }
			var activeH, maxH atomic.Int32
			h := probeLoopbackServer(t, "127.0.0.10", func(w http.ResponseWriter, r *http.Request) {
				n := activeH.Add(1)
				defer activeH.Add(-1)
				for old := maxH.Load(); n > old; old = maxH.Load() {
					if maxH.CompareAndSwap(old, n) {
						break
					}
				}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/x-bittorrent")
				_, _ = w.Write(automationTorrent(strings.TrimPrefix(r.URL.Path, "/")))
			})
			var redirectCalls atomic.Int32
			entry := probeLoopbackServer(t, "127.0.0.12", func(w http.ResponseWriter, r *http.Request) {
				redirectCalls.Add(1)
				http.Redirect(w, r, h.URL+r.URL.Path, http.StatusFound)
			})
			artifactURL := h.URL
			if scenario.redirect {
				artifactURL = entry.URL
			}
			kStarted := make(chan time.Time, 1)
			k := probeLoopbackServer(t, "127.0.0.11", func(w http.ResponseWriter, r *http.Request) {
				select {
				case kStarted <- time.Now():
				default:
				}
				w.Header().Set("Content-Type", "application/x-bittorrent")
				_, _ = w.Write(automationTorrent("probe-k"))
			})
			var rssKReturned atomic.Int64
			kFeed := probeLoopbackServer(t, "127.0.0.20", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `<rss><channel><item><guid>probe-k</guid><title>probe-k</title><enclosure url="%s/probe-k" length="4096" type="application/x-bittorrent"/></item></channel></rss>`, k.URL)
				w.(http.Flusher).Flush()
				rssKReturned.Store(time.Now().UnixNano())
			})
			var feeds []string
			for i := 0; i < origins; i++ {
				origin := i
				feed := probeLoopbackServer(t, fmt.Sprintf("127.0.0.%d", 30+i), func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, "<rss><channel>")
					for j := 0; j < 8/origins; j++ {
						name := fmt.Sprintf("probe-h-%d-%d", origin, j)
						fmt.Fprintf(w, `<item><guid>%s</guid><title>%s</title><enclosure url="%s/%s" length="4096" type="application/x-bittorrent"/></item>`, name, name, artifactURL, name)
					}
					fmt.Fprint(w, "</channel></rss>")
				})
				feeds = append(feeds, feed.URL)
			}
			path := filepath.Join(t.TempDir(), "probe.db")
			database, err := db.Open(ctx, db.Config{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			migrations, err := db.DefaultRunner()
			if err != nil {
				t.Fatal(err)
			}
			if err := migrations.Apply(ctx, database); err != nil {
				t.Fatal(err)
			}
			addFeed := func(id, url string, enabled int) {
				stamp := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
				queries := []struct {
					sql  string
					args []any
				}{
					{`INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES(?,?,?,1,?,?)`, []any{id, id, url, stamp, stamp}},
					{`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES(?,?,?,?,?,60,?,?)`, []any{id, id, id, url, enabled, stamp, stamp}},
					{`INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at) VALUES(?,1,?)`, []any{id, stamp}},
				}
				for _, q := range queries {
					if _, err := database.Exec(q.sql, q.args...); err != nil {
						t.Fatal(err)
					}
				}
				definition := `{"condition_tree":{"version":1,"root":{"kind":"leaf","field":"title","operator":"contains","value":{"type":"string","string":"probe-"}}},"action_set_json":{"target_mode":"FIXED_INSTANCE","target_instance_id":"fixture"}}`
				if _, err := database.Exec(`INSERT INTO rules(rule_id,site_id,name,enabled,priority,revision,state,definition_json,created_at,updated_at) VALUES(?,?,?,1,10,1,'PENDING',?,?,?)`, id, id, id, definition, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			for i, url := range feeds {
				addFeed(fmt.Sprintf("h-%d", i), url, 1)
			}
			addFeed("k", kFeed.URL, 0)
			if _, err := database.Exec(`UPDATE app_settings SET rate_limit_per_minute=?`, perMinute); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			runtime, err := New(ctx, Config{Database: db.Config{Path: path}, Hosts: []string{"vertex.local"}, Scheduler: &SchedulerConfig{Enabled: true, PollInterval: 20 * time.Millisecond, AllowedArtifactHosts: []string{strings.TrimPrefix(h.URL, "http://"), strings.TrimPrefix(k.URL, "http://"), strings.TrimPrefix(entry.URL, "http://")}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := runtime.Close(); err != nil {
					t.Errorf("close measurement runtime: %v", err)
				}
				if stats := runtime.Database.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 {
					t.Errorf("database connections remain after worker shutdown: %+v", stats)
				}
			}()
			defer releaseH()
			if runtime.Settings.MaxConcurrentPreparations != 8 || runtime.Settings.MaxPreparationsPerHost != 2 || runtime.Settings.RateLimitPerMinute != perMinute || runtime.Settings.RequestBurst != 4 {
				t.Fatal("unexpected default preparation settings")
			}
			claimed, waiting := 0, 0
			probeWait(t, 15*time.Second, func() bool {
				err := runtime.Database.QueryRow(`SELECT count(*) FILTER(WHERE preparation_started_at IS NOT NULL),count(*) FILTER(WHERE rate_wait_started_at IS NOT NULL) FROM rss_source_consumers WHERE feed_id LIKE 'h-%' AND state='PENDING'`).Scan(&claimed, &waiting)
				// Both the old blocking implementation and the yielding one reach
				// this setup, so the K assertion below detects the original bug.
				return err == nil && activeH.Load() == 2 && (origins == 1 || waiting == 6) && (claimed == 2 || claimed == 8)
			})
			runtime.preparationMu.Lock()
			workers := 0
			for key, count := range runtime.preparing {
				if strings.HasPrefix(key, "site:") {
					workers += count
				}
			}
			runtime.preparationMu.Unlock()
			if workers > 8 {
				t.Fatalf("global preparation bound exceeded: %d", workers)
			}
			if _, err := runtime.Database.Exec(`UPDATE feeds SET enabled=1 WHERE feed_id='k'`); err != nil {
				t.Fatal(err)
			}
			var discovered string
			probeWait(t, 5*time.Second, func() bool {
				return runtime.Database.QueryRow(`SELECT discovered_at FROM rss_source_consumers WHERE feed_id='k'`).Scan(&discovered) == nil
			})
			persisted, err := time.Parse(time.RFC3339Nano, discovered)
			if err != nil {
				t.Fatal(err)
			}
			var kAt time.Time
			beforeRelease := false
			// H is still blocked: K must use an available worker independently.
			timer := time.NewTimer(3 * time.Second)
			select {
			case kAt = <-kStarted:
				beforeRelease = true
			case <-timer.C:
			}
			timer.Stop()
			if !beforeRelease {
				t.Fatal("H permission wait prevented K from preparing")
			}
			var kClaims int
			if err := runtime.Database.QueryRow(`SELECT attempts FROM rss_source_consumers WHERE feed_id='k'`).Scan(&kClaims); err != nil {
				t.Fatal(err)
			}
			released := time.Now()
			releaseH()
			if !beforeRelease {
				select {
				case kAt = <-kStarted:
				case <-time.After(10 * time.Second):
					t.Fatal("K never started after H released")
				}
			}
			probeWait(t, 20*time.Second, func() bool {
				var done, jobs int
				return runtime.Database.QueryRow(`SELECT count(*) FROM rss_source_consumers WHERE state='DONE'`).Scan(&done) == nil && done == 9 && runtime.Database.QueryRow(`SELECT count(*) FROM race_jobs`).Scan(&jobs) == nil && jobs == 9
			})
			if maxH.Load() != 2 {
				t.Fatalf("actual H concurrency=%d", maxH.Load())
			}
			if scenario.redirect && redirectCalls.Load() != 8 {
				t.Fatalf("redirect entry fetched %d times, want once per H artifact", redirectCalls.Load())
			}
			var totalAttempts int
			if err := runtime.Database.QueryRow(`SELECT sum(attempts) FROM rss_source_consumers`).Scan(&totalAttempts); err != nil || totalAttempts != 9 {
				t.Fatalf("permission waits spent failure attempts: attempts=%d err=%v", totalAttempts, err)
			}
			result := map[string]any{"rss_origins_for_H": origins, "H_jobs": 8, "K_jobs": 1, "preparation_workers_occupied_before_K": workers, "H_http_active": 2, "H_waiting_for_permission": waiting, "K_started_before_H_release": beforeRelease, "K_attempts_before_H_release": kClaims, "K_rss_response_to_http_ms": float64(kAt.Sub(time.Unix(0, rssKReturned.Load())).Microseconds()) / 1000, "K_discovered_to_http_ms": float64(kAt.Sub(persisted).Microseconds()) / 1000, "H_release_to_K_http_ms": float64(kAt.Sub(released).Microseconds()) / 1000, "all_consumers_done": 9, "prepared_jobs": 9, "max_H_http": maxH.Load(), "real_downloads_added": 0}
			result["redirect_to_H"], result["redirect_entry_requests"], result["total_attempts"] = scenario.redirect, redirectCalls.Load(), totalAttempts
			result["rate_limit_per_minute"] = perMinute
			raw, _ := json.Marshal(result)
			t.Logf("SATURATION_PROBE %s", raw)
		})
	}
}

func probeLoopbackServer(t *testing.T, host string, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", host+":0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func probeWait(t *testing.T, limit time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("probe did not reach the expected state")
}
