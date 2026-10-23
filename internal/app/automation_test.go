package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
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
	"github.com/vertex-pro/vertex-pro/internal/profileverify"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

// This is a local HTTP fixture test, not evidence for a real qB profile.
// Workers, persistence, RSS matching, reservation, Add and confirmation are real.
func TestAutomationWorkersRouteTwoInstancesAndSurviveRestart(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(base.Add(time.Hour).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	var mu sync.Mutex
	entries := []string{"old-baseline"}
	tasks := map[string]map[string]map[string]any{"2": {}, "7": {}}
	adds := map[string]int{}
	downloads := map[string]int{}
	rids := map[string]int{}
	var invalidAdd, unrelatedCalls int
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
			downloads[r.URL.Path]++
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
				if r.FormValue("stopped") != "false" || r.FormValue("autoTMM") != "false" || r.FormValue("savepath") != "/managed" || r.FormValue("category") != "fixture-"+id || r.FormValue("tags") != "vertex-fixture" || !bytes.Contains(payload, []byte("target"+id+"-")) {
					invalidAdd++
				}
				adds[id]++
				tasks[id][identity.V1] = map[string]any{"name": "Fixture", "amount_left": 4096, "state": "stoppedDL", "save_path": "/managed"}
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
	for _, id := range []string{"2", "7"} {
		definition := map[string]any{"condition_tree": map[string]any{"version": 1, "root": map[string]any{"kind": "leaf", "field": "title", "operator": "contains", "value": map[string]any{"type": "string", "string": "target" + id + "-"}}}, "action_set_json": map[string]any{"target_mode": "FIXED_INSTANCE", "target_instance_id": id, "category": "fixture-" + id, "tags": []string{"vertex-fixture"}}}
		payload, _ := json.Marshal(definition)
		if _, err := database.Exec(`INSERT INTO rules(rule_id,site_id,name,enabled,priority,revision,state,definition_json,created_at,updated_at) VALUES(?,'s',?,1,10,1,'PENDING',?,?,?)`, "rule-"+id, "Target "+id, payload, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	database.Close()
	cfg := Config{Database: db.Config{Path: path}, Hosts: []string{"vertex.local"}, Now: now, Scheduler: &SchedulerConfig{Enabled: true, PollInterval: 20 * time.Millisecond, AllowedArtifactHosts: []string{strings.TrimPrefix(server.URL, "http://")}}}
	t.Setenv("AUTOMATION_CONTROL", "control-key")
	for _, id := range []string{"2", "7"} {
		t.Setenv("AUTOMATION_PROXY_"+id, "key-"+id)
		cfg.QuiInstances = append(cfg.QuiInstances, QuiConfig{BaseURL: server.URL, InstanceID: id, APIKey: secrets.SecretRef{Provider: "env", Key: "AUTOMATION_CONTROL"}, ProxyKey: secrets.SecretRef{Provider: "env", Key: "AUTOMATION_PROXY_" + id}, Store: secrets.EnvStore{}, SnapshotRoot: filepath.Join(dir, "snapshots"), SyncInterval: 20 * time.Millisecond, ProfileVerificationBinding: &binding, VertexCommit: binding.VertexCommit, VertexWorktreeClean: true, EnableDispatch: true, DispatchStage: &DispatchStageConfig{Enabled: true, Storage: dispatchstage.StorageConfig{InstanceID: id, StorageTargetID: "target-" + id, Path: "/managed"}, Interval: 20 * time.Millisecond, AllowedArtifactHosts: cfg.Scheduler.AllowedArtifactHosts}})
	}
	runtime, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { runtime.Close() }()
	waitAutomation(t, func() bool {
		var initialized int
		return runtime.Database.QueryRow(`SELECT initialized FROM feed_runtime_state WHERE feed_id='f'`).Scan(&initialized) == nil && initialized == 1
	})
	var count int
	if err := runtime.Database.QueryRow(`SELECT COUNT(*) FROM race_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("baseline created jobs: %d, %v", count, err)
	}
	mu.Lock()
	entries = append(entries, "target2-first", "target7-first")
	mu.Unlock()
	clock.Add(int64(2 * time.Minute))
	waitAutomation(t, func() bool {
		return runtime.Database.QueryRow(`SELECT COUNT(*) FROM race_jobs WHERE job_state='HASH_SEEN'`).Scan(&count) == nil && count == 2
	})
	// A confirmed Add must release its dispatch slot without releasing capacity.
	mu.Lock()
	entries = append(entries, "target2-second")
	mu.Unlock()
	clock.Add(int64(2 * time.Minute))
	waitAutomation(t, func() bool {
		return runtime.Database.QueryRow(`SELECT COUNT(*) FROM race_jobs WHERE job_state='HASH_SEEN'`).Scan(&count) == nil && count == 3
	})
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(2 * time.Minute))
	runtime, err = New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	waitAutomation(t, func() bool {
		var at string
		return runtime.Database.QueryRow(`SELECT last_success_at FROM feed_runtime_state WHERE feed_id='f'`).Scan(&at) == nil && at == now().Format(time.RFC3339Nano)
	})
	// An externally added torrent is observed and recorded without another Add.
	parsed, _ := torrentparse.Parse(automationTorrent("target2-existing"), torrentparse.DefaultLimits())
	identity, _ := torrentidentity.FromTorrent(parsed)
	mu.Lock()
	tasks["2"][identity.V1] = map[string]any{"name": "Existing", "amount_left": 4096, "state": "stoppedDL", "save_path": "/managed"}
	entries = append(entries, "target2-existing")
	mu.Unlock()
	clock.Add(int64(2 * time.Minute))
	waitAutomation(t, func() bool {
		var present int
		return runtime.Database.QueryRow(`SELECT COUNT(*) FROM race_jobs WHERE job_state='ALREADY_PRESENT'`).Scan(&present) == nil && present == 1
	})
	// Fresh physical capacity is used instead of any previous capacity epoch.
	mu.Lock()
	free = 4096
	entries = append(entries, "target7-no-space")
	mu.Unlock()
	clock.Add(int64(2 * time.Minute))
	waitAutomation(t, func() bool {
		var failures int
		return runtime.Database.QueryRow(`SELECT COUNT(*) FROM race_jobs WHERE placement_wait_reason IS NOT NULL`).Scan(&failures) == nil && failures > 0
	})
	mu.Lock()
	defer mu.Unlock()
	if adds["2"] != 2 || adds["7"] != 1 || invalidAdd != 0 || unrelatedCalls != 0 {
		t.Fatalf("adds=%v invalid=%d unrelated=%d", adds, invalidAdd, unrelatedCalls)
	}
	if len(downloads) != 5 {
		t.Fatalf("unexpected torrent downloads: %v", downloads)
	}
	for name, count := range downloads {
		if count != 1 {
			t.Fatalf("torrent %s downloaded %d times; expected one across matching, dispatch and restart", name, count)
		}
	}
}

func waitAutomation(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("automatic worker did not reach the expected persisted state")
}

func automationTorrent(name string) []byte {
	var data bytes.Buffer
	piece := sha1.Sum([]byte{0})
	fmt.Fprintf(&data, "d4:infod6:lengthi4096e4:name%d:%s12:piece lengthi16384e6:pieces20:", len(name), name)
	data.Write(piece[:])
	data.WriteString("ee")
	return data.Bytes()
}

func automationTestBinding(t *testing.T, database *sql.DB, base time.Time) profileverify.Binding {
	t.Helper()
	var latest profileverify.Run
	for repetition := 1; repetition <= 3; repetition++ {
		finished := base.Add(time.Duration(repetition) * time.Minute)
		verified := finished.Add(time.Second)
		latest = profileverify.Run{RunID: fmt.Sprintf("fixture-run-%d", repetition), CampaignID: "fixture-campaign", Repetition: repetition, RequiredRepetitions: 3, InstanceID: "fixture", ProfileID: "QB_504", SourceKind: profileverify.SourceRealProxyLab, QBAppVersion: "5.0.4", QBBuildFingerprint: sha256Digest(`{"application_version":"5.0.4","webapi_version":"2.11.2","build_info":{"libtorrent":"2.0.11.0"}}`), QBArtifactDigest: sha256Digest("fixture-qb"), WebAPIVersion: "2.11.2", LibtorrentVersion: "2.0.11.0", QuiVersion: "8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896", QuiImageDigest: sha256Digest("fixture-qui"), ProxyRouteFingerprint: proxy.RouteFingerprint(), HarnessImageDigest: sha256Digest("fixture-harness"), VertexCommit: "0123456789abcdef0123456789abcdef01234567", VertexWorktreeClean: true, TestSuiteRevision: profileverify.CurrentTestSuiteRevision, AddFixtureDigest: sha256Digest("add"), SyncFixtureDigest: sha256Digest("sync"), TrackerFixtureDigest: sha256Digest("tracker"), TransferInfoFixtureDigest: sha256Digest("transfer"), VerificationReportDigest: sha256Digest(fmt.Sprint(repetition)), RequiredCaseCount: 21, PassedCaseCount: 21, LabIsolated: true, LabReturnedEmpty: true, SecretsRedacted: true, Kill9CasePassed: true, VerificationState: profileverify.StateVerified, StartedAt: finished.Add(-time.Minute), FinishedAt: finished, VerifiedAt: &verified, CreatedAt: verified, ExpiresAt: verified.Add(24 * time.Hour)}
		if _, err := profileverify.Record(context.Background(), database, latest); err != nil {
			t.Fatal(err)
		}
	}
	return profileverify.BindingFromRun(latest)
}
