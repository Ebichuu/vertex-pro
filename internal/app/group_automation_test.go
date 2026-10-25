package app

import (
	"bytes"
	"context"

	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/dispatchstage"
	"github.com/vertex-pro/vertex-pro/internal/historyproject"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

// This is a local HTTP fixture test, not evidence for a real qB profile.
// Workers, persistence, RSS matching, reservation, Add and confirmation are real.
// Its 60-second correctness waits include instrumented SQLite under -race.
// Latency thresholds are enforced separately by the pipeline burst tests.
func TestGroupBatchLifecycleAndQuiPolicy(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(base.Add(time.Hour).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	var mu sync.Mutex
	entries := []string{"old-baseline"}
	tasks := map[string]map[string]map[string]any{"2": {}, "7": {}}
	adds := map[string]int{}
	rids := map[string]int{}
	var invalidAdd, unrelatedCalls int
	policies := map[string]control.Automation{}
	free := int64(1 << 30)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/rss":
			fmt.Fprint(w, `<rss version="2.0"><channel><title>Fixture</title>`)
			for _, name := range entries {
				fmt.Fprintf(w, `<item><guid>%s</guid><title>%s</title><enclosure url="http://%s/%s.torrent" length="4096" type="application/x-bittorrent"/></item>`, name, name, r.Host, name)
			}
			fmt.Fprint(w, `</channel></rss>`)
			return
		case "/api/instances":
			fmt.Fprint(w, `[{"id":2,"name":"Two","connectionStatus":"connected","isActive":true},{"id":7,"name":"Seven","connectionStatus":"connected","isActive":true},{"id":18,"name":"Unrelated offline","connectionStatus":"disconnected","isActive":true}]`)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".torrent") {
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write(automationTorrent(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".torrent")))
			return
		}
		for _, id := range []string{"2", "7"} {
			if r.URL.Path == "/api/instances/"+id+"/transfer-info" {
				fmt.Fprint(w, `{"dl_info_speed":0,"up_info_speed":0,"dl_info_data":0,"up_info_data":0}`)
				return
			}
			if r.URL.Path == "/api/instances/"+id+"/capabilities" {
				fmt.Fprint(w, `{"webAPIVersion":"2.11.2"}`)
				return
			}
			if r.URL.Path == "/api/instances/"+id+"/automations" || r.URL.Path == "/api/instances/"+id+"/automations/1" {
				if r.Header.Get("X-API-Key") != "control-key" {
					t.Error("missing control authentication")
				}
				if r.Method == "GET" {
					list := []control.Automation{}
					if p, ok := policies[id]; ok {
						list = append(list, p)
					}
					json.NewEncoder(w).Encode(list)
					return
				}
				var p control.Automation
				if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				p.ID = 1
				policies[id] = p
				json.NewEncoder(w).Encode(p)
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
				rids[id]++
				json.NewEncoder(w).Encode(map[string]any{"rid": rids[id], "full_update": true, "torrents": tasks[id], "server_state": map[string]any{"free_space_on_disk": free}})
			case "torrents/add":
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Errorf("multipart: %v", err)
					w.WriteHeader(400)
					return
				}
				defer r.MultipartForm.RemoveAll()
				file, _, err := r.FormFile("torrents")
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				payload, _ := io.ReadAll(file)
				file.Close()
				parsed, err := torrentparse.Parse(payload, torrentparse.DefaultLimits())
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				identity, err := torrentidentity.FromTorrent(parsed)
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if r.FormValue("stopped") != "false" || r.FormValue("autoTMM") != "false" || r.FormValue("savepath") != "/managed" || r.FormValue("category") != "fixture-group" || !strings.Contains(r.FormValue("tags"), "vertex-policy-") || r.FormValue("upLimit") != "100000000" || r.FormValue("sequentialDownload") != "true" || !bytes.Contains(payload, []byte("group-")) {
					invalidAdd++
				}
				adds[id]++
				tasks[id][identity.V1] = map[string]any{"name": "Fixture", "amount_left": 4096, "state": "downloading", "downloaded": 128, "uploaded": 0, "ratio": 0, "save_path": "/managed"}
				fmt.Fprint(w, "Ok.")
			default:
				http.NotFound(w, r)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/instances/18/") {
			unrelatedCalls++
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "automation.db")
	database, err := db.Open(ctx, db.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	runner, _ := db.DefaultRunner()
	if err := runner.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	binding := automationTestBinding(t, database, base)
	stamp := base.Format(time.RFC3339)
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`UPDATE app_settings SET rate_limit_per_minute=600`, nil},
		{`INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES('s','Fixture',?,1,?,?)`, []any{server.URL, stamp, stamp}},
		{`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES('f','s','Fixture',?,1,1,?,?)`, []any{server.URL + "/rss", stamp, stamp}},
	} {
		if _, err := database.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO instance_groups(group_id,name,members_json,updated_at) VALUES('test-group','Test group','["2","7"]',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	definition := map[string]any{"condition_tree": map[string]any{"version": 1, "root": map[string]any{"kind": "leaf", "field": "title", "operator": "contains", "value": map[string]any{"type": "string", "string": "group-"}}}, "action_set_json": map[string]any{"target_mode": "INSTANCE_GROUP", "target_group_id": "test-group", "category": "fixture-group", "tags": []string{"vertex-fixture"}, "max_jobs": 10, "upload_limit_bytes": 100000000, "sequential_download": true, "seed_time_seconds": 3600, "delete_mode": "TORRENT_ONLY"}}
	payload, _ := json.Marshal(definition)
	if _, err := database.Exec(`INSERT INTO rules(rule_id,site_id,name,enabled,priority,revision,state,definition_json,created_at,updated_at) VALUES('rule-group','s','Group rule',1,10,1,'PENDING',?,?,?)`, payload, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	database.Close()
	cfg := Config{Database: db.Config{Path: path}, Hosts: []string{"vertex.local"}, Now: now, Scheduler: &SchedulerConfig{Enabled: true, PollInterval: 20 * time.Millisecond, AllowedArtifactHosts: []string{strings.TrimPrefix(server.URL, "http://")}}}
	t.Setenv("AUTOMATION_CONTROL", "control-key")
	for _, id := range []string{"2", "7"} {
		t.Setenv("AUTOMATION_PROXY_"+id, "key-"+id)
		cfg.QuiInstances = append(cfg.QuiInstances, QuiConfig{BaseURL: server.URL, InstanceID: id, APIKey: secrets.SecretRef{Provider: "env", Key: "AUTOMATION_CONTROL"}, ProxyKey: secrets.SecretRef{Provider: "env", Key: "AUTOMATION_PROXY_" + id}, Store: secrets.EnvStore{}, SnapshotRoot: filepath.Join(dir, "snapshots"), SyncInterval: 200 * time.Millisecond, ProfileVerificationBinding: &binding, VertexCommit: binding.VertexCommit, VertexWorktreeClean: true, EnableDispatch: true, DispatchStage: &DispatchStageConfig{Enabled: true, Storage: dispatchstage.StorageConfig{InstanceID: id, StorageTargetID: "target-" + id, Path: "/managed"}, Interval: 20 * time.Millisecond, AllowedArtifactHosts: cfg.Scheduler.AllowedArtifactHosts}})
	}
	runtime, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { runtime.Close() }()
	waitAutomationWithin(t, 60*time.Second, func() bool {
		var initialized int
		return runtime.Database.QueryRow(`SELECT initialized FROM feed_runtime_state WHERE feed_id='f'`).Scan(&initialized) == nil && initialized == 1
	})
	var count int
	if err := runtime.Database.QueryRow(`SELECT COUNT(*) FROM race_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("baseline created jobs: %d, %v", count, err)
	}

	// Open the feed only once both independently prewarmed policy bindings are
	// ready. A target still warming is deliberately ineligible for allocation.
	if _, err := runtime.Database.Exec(`UPDATE feeds SET enabled=0 WHERE feed_id='f'`); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(2 * time.Minute))
	for _, worker := range append([]*Runtime{runtime}, runtime.children...) {
		if err := worker.dispatchStage.Warm(ctx); err != nil {
			t.Fatal(err)
		}
		if err := worker.TickSync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	for i := 0; i < 12; i++ {
		entries = append(entries, fmt.Sprintf("group-%02d", i))
	}
	mu.Unlock()
	if _, err := runtime.Database.Exec(`UPDATE feeds SET enabled=1 WHERE feed_id='f'`); err != nil {
		t.Fatal(err)
	}
	waitAutomationWithin(t, 60*time.Second, func() bool {
		return runtime.Database.QueryRow(`SELECT COUNT(*) FROM race_jobs WHERE job_state='HASH_SEEN'`).Scan(&count) == nil && count == 10
	}, runtime.Database)
	mu.Lock()
	if adds["2"] == 0 || adds["7"] == 0 || adds["2"]+adds["7"] != 10 || invalidAdd != 0 || unrelatedCalls != 0 {
		t.Errorf("distribution %v invalid %d unrelated %d", adds, invalidAdd, unrelatedCalls)
	}
	mu.Unlock()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(2 * time.Minute))
	runtime, err = New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	entries = append(entries, "group-after-restart")
	mu.Unlock()
	clock.Add(int64(2 * time.Minute))
	waitAutomationWithin(t, 60*time.Second, func() bool {
		var at string
		return runtime.Database.QueryRow(`SELECT last_success_at FROM feed_runtime_state WHERE feed_id='f'`).Scan(&at) == nil && at == now().Format(time.RFC3339Nano)
	})
	if err := runtime.Database.QueryRow(`SELECT COUNT(*) FROM race_jobs`).Scan(&count); err != nil || count != 10 {
		t.Fatalf("batch count %d err %v", count, err)
	}
	// A completed task releases its reservation and projects seeding statistics.
	var completedHash, removedHash, removedInstance string
	mu.Lock()
	for id, items := range tasks {
		for hash, task := range items {
			if completedHash == "" {
				completedHash = hash
				task["amount_left"] = 0
				task["state"] = "uploading"
				task["downloaded"] = 4096
				task["uploaded"] = 8192
				task["ratio"] = 2
				task["completion_on"] = now().Unix()
				continue
			}
			if removedHash == "" {
				removedHash, removedInstance = hash, id
			}
		}
	}
	mu.Unlock()
	waitAutomationWithin(t, 60*time.Second, func() bool {
		var n int
		return runtime.Database.QueryRow(`SELECT COUNT(*) FROM capacity_reservations WHERE release_reason='TERMINAL_COMPLETED'`).Scan(&n) == nil && n == 1
	})
	entriesView, err := (historyproject.Projection{}).Project(ctx, runtime.Database)
	if err != nil {
		t.Fatal(err)
	}
	seeded := 0
	for _, entry := range entriesView {
		if entry.UserStatus.ID == "SEEDING" {
			seeded++
			if entry.Title == "" || entry.SiteName != "Fixture" || entry.FinishedAt == "" {
				t.Errorf("incomplete history %+v", entry)
			}
		}
	}
	if seeded != 1 {
		t.Fatalf("seeded %d", seeded)
	}
	mu.Lock()
	delete(tasks[removedInstance], removedHash)
	mu.Unlock()
	waitAutomationWithin(t, 60*time.Second, func() bool {
		var n int
		return runtime.Database.QueryRow(`SELECT COUNT(*) FROM capacity_reservations WHERE release_reason='TERMINAL_QB_REMOVED'`).Scan(&n) == nil && n == 1
	})
	var removalState string
	if err := runtime.Database.QueryRow(`SELECT j.job_state FROM race_jobs j JOIN placement_attempts a ON a.attempt_id=j.current_attempt_id WHERE a.qb_torrent_id=?`, removedHash).Scan(&removalState); err != nil || removalState != "RECONCILED_CONFIRMED" {
		t.Fatalf("removal state %s %v", removalState, err)
	}
	// Disabling the rule must disable both remote delete policies.
	if _, err := runtime.Database.Exec(`UPDATE rules SET enabled=0 WHERE rule_id='rule-group'`); err != nil {
		t.Fatal(err)
	}
	if err := runtime.syncDisabledPolicies(ctx); err != nil {
		t.Fatal(err)
	}
	waitAutomationWithin(t, 60*time.Second, func() bool {
		var n int
		return runtime.Database.QueryRow(`SELECT COUNT(*) FROM qui_policy_bindings WHERE enabled=0`).Scan(&n) == nil && n == 2
	})
	mu.Lock()
	defer mu.Unlock()
	for id, p := range policies {
		if p.Enabled {
			t.Errorf("policy still enabled on %s", id)
		}
		raw, _ := json.Marshal(p.Conditions)
		if !bytes.Contains(raw, []byte(`"SEEDING_TIME"`)) || !bytes.Contains(raw, []byte(`"3600"`)) {
			t.Errorf("wrong delete condition %s", raw)
		}
	}
	if adds["2"]+adds["7"] != 10 {
		t.Errorf("repeated adds %v", adds)
	}
}
