package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/confirmcoordinator"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/qui/control"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/syncwatcher"
)

func TestDispatchEvidenceRequiresEnabledControlInstance(t *testing.T) {
	for _, tc := range []struct {
		name, instances string
		allowed         bool
	}{
		{"enabled", `[{"id":2,"name":"selected","enabled":true}]`, true},
		{"disabled", `[{"id":2,"name":"selected","enabled":false}]`, false},
		{"removed", `[{"id":7,"name":"other","enabled":true}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			var preferenceCalls, transferCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/instances":
					fmt.Fprint(w, tc.instances)
				case "/api/instances/2/transfer-info":
					transferCalls.Add(1)
					fmt.Fprint(w, `{"dl_info_speed":0,"up_info_speed":0,"dl_info_data":0,"up_info_data":0}`)
				case "/proxy/test-key/api/v2/sync/maindata":
					fmt.Fprint(w, `{"rid":1,"full_update":true,"torrents":{},"server_state":{"free_space_on_disk":1024}}`)
				case "/proxy/test-key/api/v2/app/preferences":
					preferenceCalls.Add(1)
					fmt.Fprint(w, `{"save_path":"/managed","temp_path_enabled":false}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			runtime, err := New(ctx, Config{Database: db.Config{Path: filepath.Join(dir, "monitoring.db")}, Hosts: []string{"vertex.local"}, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			runtime.instanceID, runtime.snapshotRoot = "2", filepath.Join(dir, "snapshots")
			t.Setenv("MONITORING_TEST_KEY", "test-key")
			key := secrets.SecretRef{Provider: "env", Key: "MONITORING_TEST_KEY"}
			runtime.controlClient, err = control.New(ctx, control.Config{BaseURL: server.URL, APIKey: key, Store: secrets.EnvStore{}, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			runtime.proxyClient, err = proxy.New(ctx, proxy.Config{BaseURL: server.URL, ProxyKey: key, Store: secrets.EnvStore{}, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			store, err := snapshot.New(runtime.Database, runtime.snapshotRoot)
			if err != nil {
				t.Fatal(err)
			}
			runtime.watcher, err = syncwatcher.New(runtime.instanceID, runtime.proxyClient, store)
			if err != nil {
				t.Fatal(err)
			}
			runtime.confirmations, err = confirmcoordinator.New(runtime.Database)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.Database.Exec(`INSERT INTO sync_instance_state(instance_id,sync_state,sync_generation,rid,snapshot_revision,snapshot_digest,updated_at) VALUES('2','READY',0,0,0,'',?)`, now.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			_, release, err := runtime.collectDispatchEvidence(ctx)
			if release != nil {
				release()
			}
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, evidence error=%v", tc.allowed, err)
			}
			var state string
			if err := runtime.Database.QueryRow(`SELECT sync_state FROM sync_instance_state WHERE instance_id='2'`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			wantState, wantCalls := "OFFLINE", int32(0)
			if tc.allowed {
				wantState, wantCalls = "READY", 1
			}
			if state != wantState || preferenceCalls.Load() != wantCalls || transferCalls.Load() != wantCalls {
				t.Fatalf("state=%s preferences=%d transfers=%d", state, preferenceCalls.Load(), transferCalls.Load())
			}
		})
	}
}
