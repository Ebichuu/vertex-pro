package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/secrets"
)

func TestNewMigratesDatabaseAndMountsProtectedAPI(t *testing.T) {
	runtime, err := New(context.Background(), Config{
		Database: db.Config{Path: filepath.Join(t.TempDir(), "vertex-pro.db"), MaxOpenConns: 1, MaxIdleConns: 1},
		Hosts:    []string{"vertex.local"},
		Now:      func() time.Time { return time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	var migrationCount int
	if err := runtime.Database.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Ready(context.Background()); err != nil {
		t.Fatalf("runtime readiness = %v", err)
	}

	unauthenticated := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	request.Host = "vertex.local"
	runtime.Handler.ServeHTTP(unauthenticated, request)
	if unauthenticated.Code != http.StatusUnauthorized || !strings.Contains(unauthenticated.Body.String(), "UNAUTHENTICATED") {
		t.Fatalf("unauthenticated response = %d %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	if err := runtime.Auth.Initialize(context.Background(), "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	session, err := runtime.Auth.Login(context.Background(), "correct-horse-battery", time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	authenticated := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	request.Host = "vertex.local"
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session.Token})
	runtime.Handler.ServeHTTP(authenticated, request)
	if authenticated.Code != http.StatusOK {
		t.Fatalf("authenticated response = %d %s", authenticated.Code, authenticated.Body.String())
	}
	var dashboard map[string]any
	if err := json.Unmarshal(authenticated.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if _, ok := dashboard["instances"]; !ok {
		t.Fatalf("dashboard response lacks instances: %s", authenticated.Body.String())
	}
}

func TestNewStartsReadOnlyQuiProfileAndSyncRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/instances":
			if request.Header.Get("X-API-Key") != "control-key" {
				http.Error(response, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = response.Write([]byte(`[{"id":13,"name":"qB 5.0.4","connectionStatus":"connected","isActive":true}]`))
		case "/api/instances/13/transfer-info":
			_, _ = response.Write([]byte(`{"dl_info_speed":1,"up_info_speed":2,"dl_info_data":3,"up_info_data":4}`))
		case "/api/instances/13/capabilities":
			_, _ = response.Write([]byte(`{"webAPIVersion":"2.11.2"}`))
		case "/proxy/proxy-key/api/v2/app/version":
			_, _ = response.Write([]byte(`v5.0.4`))
		case "/proxy/proxy-key/api/v2/app/webapiVersion":
			_, _ = response.Write([]byte(`2.11.2`))
		case "/proxy/proxy-key/api/v2/app/buildInfo":
			_, _ = response.Write([]byte(`{"libtorrent":"2.0.11.0"}`))
		case "/proxy/proxy-key/api/v2/app/processInfo":
			http.NotFound(response, request)
		case "/proxy/proxy-key/api/v2/sync/maindata":
			_, _ = response.Write([]byte(`{"rid":42,"full_update":true,"torrents":{"hash-1":{"name":"existing task","amount_left":10}}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	t.Setenv("TEST_QUI_API_KEY", "control-key")
	t.Setenv("TEST_QUI_PROXY_KEY", "proxy-key")
	runtime, err := New(context.Background(), Config{
		Database: db.Config{Path: filepath.Join(t.TempDir(), "vertex-pro.db"), MaxOpenConns: 1, MaxIdleConns: 1},
		Hosts:    []string{"vertex.local"},
		Qui: &QuiConfig{
			BaseURL:      server.URL,
			InstanceID:   "13",
			APIKey:       secrets.SecretRef{Provider: "env", Key: "TEST_QUI_API_KEY"},
			ProxyKey:     secrets.SecretRef{Provider: "env", Key: "TEST_QUI_PROXY_KEY"},
			Store:        secrets.EnvStore{},
			SnapshotRoot: filepath.Join(t.TempDir(), "snapshots"),
			SyncInterval: time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.Profile == nil || runtime.Profile.ID != "QB_504" {
		t.Fatalf("runtime profile = %+v", runtime.Profile)
	}
	var state string
	var rid, revision int
	if err := runtime.Database.QueryRow(`SELECT sync_state, rid, snapshot_revision FROM sync_instance_state WHERE instance_id = '13'`).Scan(&state, &rid, &revision); err != nil {
		t.Fatal(err)
	}
	if state != "READY" || rid != 42 || revision != 1 {
		t.Fatalf("sync state = %s, rid = %d, revision = %d", state, rid, revision)
	}
}

func TestNewRejectsIncompleteStartupSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incomplete.db")
	database, err := db.Open(context.Background(), db.Config{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (1, 'baseline', 'invalid', '2026-08-31T13:00:00Z')"); err != nil {
		t.Fatal(err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = New(context.Background(), Config{Database: db.Config{Path: path}, Hosts: []string{"vertex.local"}})
	if err == nil || !strings.Contains(err.Error(), "migration") {
		t.Fatalf("New() error = %v, want migration failure", err)
	}
}
