package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

// Real workers and HTTP, with entirely local synthetic RSS/torrent/qui data.
// Both delays last 20 seconds; neither delay is shortened to make the test pass.
func TestPipelineBurst30WithSlowPreparationAndSlowInstance(t *testing.T) {
	if testing.Short() {
		t.Skip("20-second fault injection")
	}
	ctx := context.Background()
	ids := []string{"2", "7", "9"}
	var mu sync.Mutex
	tasks := map[string]map[string]map[string]any{"2": {}, "7": {}, "9": {}}
	adds := map[string]int{}
	rids := map[string]int{}
	var requests, active, maxActive atomic.Int32
	var delayedAdd atomic.Bool
	slowPrepare, slowSend := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss":
			requests.Add(1)
			fmt.Fprint(w, `<rss><channel><title>Burst</title>`)
			for i := 0; i < 30; i++ {
				fmt.Fprintf(w, `<item><guid>%02d</guid><title>target%s-%02d</title><enclosure url="http://%s/target%s-%02d.torrent" length="4096" type="application/x-bittorrent"/></item>`, i, ids[i%3], i, r.Host, ids[i%3], i)
			}
			fmt.Fprint(w, `</channel></rss>`)
			return
		case "/api/instances":
			fmt.Fprint(w, `[{"id":2,"name":"A","isActive":true},{"id":7,"name":"B","isActive":true},{"id":9,"name":"C","isActive":true}]`)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".torrent") {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maxActive.Load(); n > old; old = maxActive.Load() {
				if maxActive.CompareAndSwap(old, n) {
					break
				}
			}
			if r.URL.Path == "/target2-00.torrent" {
				close(slowPrepare)
				timer := time.NewTimer(20 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write(automationTorrent(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".torrent")))
			return
		}
		for _, id := range ids {
			if r.URL.Path == "/api/instances/"+id+"/transfer-info" {
				fmt.Fprint(w, `{"dl_info_speed":1024,"up_info_speed":0,"dl_info_data":1024,"up_info_data":0}`)
				return
			}
			if r.URL.Path == "/api/instances/"+id+"/capabilities" {
				fmt.Fprint(w, `{"webAPIVersion":"2.11.2"}`)
				return
			}
			prefix := "/proxy/key-" + id + "/api/v2/"
			if !strings.HasPrefix(r.URL.Path, prefix) {
				continue
			}
			switch strings.TrimPrefix(r.URL.Path, prefix) {
			case "app/version":
				fmt.Fprint(w, "v5.0.4")
			case "app/webapiVersion":
				fmt.Fprint(w, "2.11.2")
			case "app/buildInfo":
				fmt.Fprint(w, `{"libtorrent":"2.0.11.0"}`)
			case "app/preferences":
				fmt.Fprint(w, `{"save_path":"/managed","temp_path_enabled":false}`)
			case "sync/maindata":
				mu.Lock()
				rids[id]++
				json.NewEncoder(w).Encode(map[string]any{"rid": rids[id], "full_update": true, "torrents": tasks[id], "server_state": map[string]any{"free_space_on_disk": int64(1 << 40)}})
				mu.Unlock()
			case "torrents/add":
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				defer r.MultipartForm.RemoveAll()
				file, _, err := r.FormFile("torrents")
				if err != nil {
					t.Error(err)
					return
				}
				body, err := io.ReadAll(file)
				file.Close()
				if err != nil {
					t.Error(err)
					return
				}
				parsed, err := torrentparse.Parse(body, torrentparse.DefaultLimits())
				if err != nil {
					t.Error(err)
					return
				}
				identity, err := torrentidentity.FromTorrent(parsed)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				adds[id]++
				if _, exists := tasks[id][identity.V1]; exists {
					t.Error("duplicate Add")
				}
				tasks[id][identity.V1] = map[string]any{"name": parsed.Name, "state": "downloading", "amount_left": 3072, "downloaded": 1024, "save_path": "/managed"}
				mu.Unlock()
				if id == "2" && delayedAdd.CompareAndSwap(false, true) {
					close(slowSend)
					timer := time.NewTimer(20 * time.Second)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-r.Context().Done():
						return
					}
				}
				fmt.Fprint(w, "Ok.")
			default:
				http.NotFound(w, r)
			}
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "burst.db")
	database, err := db.Open(ctx, db.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	migrations, _ := db.DefaultRunner()
	if err := migrations.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	binding := automationTestBinding(t, database, time.Now().UTC().Add(-time.Hour))
	for _, query := range []string{
		`UPDATE app_settings SET rate_limit_per_minute=600`,
		`INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES('s','Fixture','https://fixture.test',1,'2026-10-23T00:00:00Z','2026-10-23T00:00:00Z')`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES('f','s','Burst',?,1,60,'2026-10-23T00:00:00Z','2026-10-23T00:00:00Z')`, server.URL+"/rss"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO feed_runtime_state(feed_id,initialized,last_polled_at) VALUES('f',1,'2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		definition := map[string]any{"condition_tree": map[string]any{"version": 1, "root": map[string]any{"kind": "leaf", "field": "title", "operator": "contains", "value": map[string]any{"type": "string", "string": "target" + id + "-"}}}, "action_set_json": map[string]any{"target_mode": "FIXED_INSTANCE", "target_instance_id": id}}
		raw, _ := json.Marshal(definition)
		if _, err := database.Exec(`INSERT INTO rules(rule_id,site_id,name,enabled,priority,revision,state,definition_json,created_at,updated_at) VALUES(?,'s',?,1,10,1,'PENDING',?,'2026-10-23T00:00:00Z','2026-10-23T00:00:00Z')`, "rule-"+id, id, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Database: db.Config{Path: path}, Hosts: []string{"vertex.local"}, Scheduler: &SchedulerConfig{Enabled: true, PollInterval: time.Hour, AllowedArtifactHosts: []string{strings.TrimPrefix(server.URL, "http://")}}}
	t.Setenv("BURST_CONTROL", "control-key")
	for _, id := range ids {
		t.Setenv("BURST_PROXY_"+id, "key-"+id)
		cfg.QuiInstances = append(cfg.QuiInstances, QuiConfig{BaseURL: server.URL, InstanceID: id, HTTPClient: &http.Client{Timeout: 30 * time.Second}, APIKey: secrets.SecretRef{Provider: "env", Key: "BURST_CONTROL"}, ProxyKey: secrets.SecretRef{Provider: "env", Key: "BURST_PROXY_" + id}, Store: secrets.EnvStore{}, SnapshotRoot: filepath.Join(dir, "snapshots"), SyncInterval: 200 * time.Millisecond, ProfileVerificationBinding: &binding, VertexCommit: binding.VertexCommit, VertexWorktreeClean: true, EnableDispatch: true, DispatchStage: &DispatchStageConfig{Enabled: true, Storage: dispatchstage.StorageConfig{InstanceID: id, StorageTargetID: "target-" + id, Path: "/managed"}, Interval: time.Hour, AllowedArtifactHosts: cfg.Scheduler.AllowedArtifactHosts}})
	}
	began := time.Now()
	runtime, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close pipeline runtime: %v", err)
		}
		if stats := runtime.Database.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 {
			t.Errorf("database connections remain after worker shutdown: %+v", stats)
		}
	}()
	for _, started := range []chan struct{}{slowPrepare, slowSend} {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("fault injection not reached")
		}
	}
	waitAutomation(t, func() bool {
		var ready, confirmed int
		if runtime.Database.QueryRow(`SELECT count(*) FROM job_torrent_artifacts`).Scan(&ready) != nil || ready != 29 {
			return false
		}
		if runtime.Database.QueryRow(`SELECT count(*) FROM placement_attempts WHERE instance_id IN ('7','9') AND attempt_state='HASH_SEEN'`).Scan(&confirmed) != nil {
			return false
		}
		return confirmed == 20
	}, runtime.Database)
	var pending int
	if err := runtime.Database.QueryRow(`SELECT count(*) FROM pending_confirmations`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("early observations=%d err=%v", pending, err)
	}
	if elapsed := time.Since(began); elapsed >= 20*time.Second {
		t.Fatalf("independent work waited for slow path: %v", elapsed)
	} else {
		t.Logf("29 ready and B/C confirmed before slow paths completed: %s", elapsed)
	}
	if maxActive.Load() > 2 {
		t.Fatalf("site preparation concurrency exceeded 2: %d", maxActive.Load())
	}
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		var count int
		if runtime.Database.QueryRow(`SELECT count(*) FROM race_jobs WHERE job_state IN ('HASH_SEEN','RECONCILED_CONFIRMED')`).Scan(&count) == nil && count == 30 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("burst failed to finish")
		case <-poll.C:
		}
	}
	rows, err := runtime.Database.Query(`SELECT torrent_ready_at,add_started_at FROM dispatch_pipeline_times WHERE add_started_at IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var latencies []time.Duration
	for rows.Next() {
		var ready, started string
		if err := rows.Scan(&ready, &started); err != nil {
			t.Fatal(err)
		}
		a, err := time.Parse(time.RFC3339Nano, ready)
		if err != nil {
			t.Fatal(err)
		}
		b, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			t.Fatal(err)
		}
		latencies = append(latencies, b.Sub(a))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) != 30 {
		t.Fatalf("timing samples=%d", len(latencies))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range ids {
		if adds[id] != 10 {
			t.Fatalf("instance %s Add count %d", id, adds[id])
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("preparation altered RSS cadence: %d polls", requests.Load())
	}
	t.Logf("30 samples ready-to-Add p50=%s p95=%s max=%s; end-to-end=%s; max site preparation=%d; 10 Adds each on A/B/C", latencies[14], latencies[28], latencies[29], time.Since(began), maxActive.Load())
}
