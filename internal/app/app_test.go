package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/profileverify"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
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
	if runtime.Dispatch != nil {
		t.Fatal("dispatch flow started without explicit enablement")
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

func TestNewRejectsDispatchForExpectedProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/instances":
			_, _ = response.Write([]byte(`[{"id":13,"connectionStatus":"connected","isActive":true}]`))
		case "/api/instances/13/transfer-info":
			_, _ = response.Write([]byte(`{"dl_info_speed":0,"up_info_speed":0,"dl_info_data":0,"up_info_data":0}`))
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
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	t.Setenv("TEST_QUI_API_KEY", "control-key")
	t.Setenv("TEST_QUI_PROXY_KEY", "proxy-key")
	_, err := New(context.Background(), Config{
		Database: db.Config{Path: filepath.Join(t.TempDir(), "vertex-pro.db")},
		Hosts:    []string{"vertex.local"},
		Qui: &QuiConfig{
			BaseURL:        server.URL,
			InstanceID:     "13",
			APIKey:         secrets.SecretRef{Provider: "env", Key: "TEST_QUI_API_KEY"},
			ProxyKey:       secrets.SecretRef{Provider: "env", Key: "TEST_QUI_PROXY_KEY"},
			Store:          secrets.EnvStore{},
			SnapshotRoot:   filepath.Join(t.TempDir(), "snapshots"),
			EnableDispatch: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "dispatch remains fail closed") {
		t.Fatalf("New() error = %v, want EXPECTED profile rejection", err)
	}
}

func TestNewEnablesDispatchOnlyFromExactStableVerificationCampaign(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/instances":
			_, _ = response.Write([]byte(`[{"id":13,"connectionStatus":"connected","isActive":true}]`))
		case "/api/instances/13/transfer-info":
			_, _ = response.Write([]byte(`{"dl_info_speed":0,"up_info_speed":0,"dl_info_data":0,"up_info_data":0}`))
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
			_, _ = response.Write([]byte(`{"rid":1,"full_update":true,"torrents":{}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	t.Setenv("TEST_QUI_API_KEY", "control-key")
	t.Setenv("TEST_QUI_PROXY_KEY", "proxy-key")

	path := filepath.Join(t.TempDir(), "verified.db")
	database, err := db.Open(context.Background(), db.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 2, 11, 0, 0, 0, time.UTC)
	qbFingerprint := sha256Digest(`{"application_version":"5.0.4","webapi_version":"2.11.2","build_info":{"libtorrent":"2.0.11.0"}}`)
	var latest profileverify.Run
	for repetition := 1; repetition <= 3; repetition++ {
		finished := base.Add(time.Duration(repetition) * time.Minute)
		verifiedAt := finished.Add(time.Second)
		latest = profileverify.Run{
			RunID: "runtime-run-" + string(rune('0'+repetition)), CampaignID: "runtime-campaign", Repetition: repetition, RequiredRepetitions: 3,
			InstanceID: "13", ProfileID: "QB_504", SourceKind: profileverify.SourceRealProxyLab,
			QBAppVersion: "5.0.4", QBBuildFingerprint: qbFingerprint, QBArtifactDigest: sha256Digest("qb-artifact"), WebAPIVersion: "2.11.2", LibtorrentVersion: "2.0.11.0",
			QuiVersion: "8fcfc4d2beaf5e68ccebe837d7c0d9be965c8896", QuiImageDigest: sha256Digest("qui-image"),
			ProxyRouteFingerprint: proxy.RouteFingerprint(), HarnessImageDigest: sha256Digest("harness-image"),
			VertexCommit: "0123456789abcdef0123456789abcdef01234567", VertexWorktreeClean: true, TestSuiteRevision: profileverify.CurrentTestSuiteRevision,
			AddFixtureDigest: sha256Digest("add"), SyncFixtureDigest: sha256Digest("sync"), TrackerFixtureDigest: sha256Digest("tracker"), TransferInfoFixtureDigest: sha256Digest("transfer"),
			VerificationReportDigest: sha256Digest("runtime-report-" + string(rune('0'+repetition))), RequiredCaseCount: 18, PassedCaseCount: 18,
			LabIsolated: true, LabReturnedEmpty: true, SecretsRedacted: true, Kill9CasePassed: true,
			VerificationState: profileverify.StateVerified, StartedAt: finished.Add(-time.Minute), FinishedAt: finished, VerifiedAt: &verifiedAt, CreatedAt: verifiedAt, ExpiresAt: verifiedAt.Add(24 * time.Hour),
		}
		if _, err := profileverify.Record(context.Background(), database, latest); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	binding := profileverify.BindingFromRun(latest)
	_, err = New(context.Background(), Config{
		Database: db.Config{Path: path}, Hosts: []string{"vertex.local"},
		Now: func() time.Time { return base.Add(30 * time.Minute) },
		Qui: &QuiConfig{
			BaseURL: server.URL, InstanceID: "13",
			APIKey: secrets.SecretRef{Provider: "env", Key: "TEST_QUI_API_KEY"}, ProxyKey: secrets.SecretRef{Provider: "env", Key: "TEST_QUI_PROXY_KEY"}, Store: secrets.EnvStore{},
			SnapshotRoot: filepath.Join(t.TempDir(), "dirty-snapshots"), ProfileVerificationBinding: &binding,
			VertexCommit: latest.VertexCommit, VertexWorktreeClean: false, EnableDispatch: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "dispatch remains fail closed") {
		t.Fatalf("dirty build error=%v", err)
	}

	runtime, err := New(context.Background(), Config{
		Database: db.Config{Path: path},
		Hosts:    []string{"vertex.local"},
		Now:      func() time.Time { return base.Add(30 * time.Minute) },
		Qui: &QuiConfig{
			BaseURL: server.URL, InstanceID: "13",
			APIKey: secrets.SecretRef{Provider: "env", Key: "TEST_QUI_API_KEY"}, ProxyKey: secrets.SecretRef{Provider: "env", Key: "TEST_QUI_PROXY_KEY"}, Store: secrets.EnvStore{},
			SnapshotRoot: filepath.Join(t.TempDir(), "snapshots"), SyncInterval: time.Hour,
			ProfileVerificationBinding: &binding, VertexCommit: latest.VertexCommit, VertexWorktreeClean: true, EnableDispatch: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.Dispatch == nil || runtime.Profile == nil || runtime.Profile.ProfileVerificationState != profileverify.StateVerified {
		t.Fatalf("runtime profile=%+v dispatch=%v", runtime.Profile, runtime.Dispatch)
	}
	if runtime.Profile.VerificationReportDigest != latest.VerificationReportDigest || runtime.Profile.VerifiedQBArtifactDigest != latest.QBArtifactDigest || runtime.Profile.VerifiedQuiVersion != latest.QuiVersion || runtime.Profile.VerifiedQuiImageDigest != latest.QuiImageDigest || runtime.Profile.VerifiedHarnessImageDigest != latest.HarnessImageDigest || runtime.Profile.ProfileVerifiedAt == nil {
		t.Fatalf("verified profile metadata=%+v", runtime.Profile)
	}
}

func TestNewFailsClosedOnRecoveryInvariantViolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery-invalid.db")
	database, err := db.Open(context.Background(), db.Config{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO storage_targets(storage_target_id, instance_id, path, storage_verification_state, created_at, updated_at) VALUES ('target-1','qb-1','/data','VERIFIED','2026-09-01T15:00:00Z','2026-09-01T15:00:00Z')`,
		`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES ('job-1','source-1','identity-1','READY','MATCHED','READY','PREPARED','2026-09-02T00:00:00Z','2026-09-02T00:00:00Z','2026-09-01T15:00:00Z','2026-09-01T15:00:00Z')`,
		`INSERT INTO placement_attempts(attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, attempt_state, dispatch_state, allocation_epoch, reserved_bytes, started_at) VALUES ('attempt-1','job-1','identity-1',1,'qb-1','target-1','RESERVED','NOT_STARTED',1,100,'2026-09-01T15:00:00Z')`,
		`INSERT INTO capacity_reservations(reservation_id, attempt_id, storage_target_id, reserved_bytes, allocation_epoch, reservation_state, reflected_physical_bytes) VALUES ('reservation-1','attempt-1','target-1',100,1,'ACTIVE',0)`,
		`INSERT INTO submit_intents(submit_intent_id, attempt_id, instance_id, identity_key, request_fingerprint, target_fingerprint, dispatch_state, pre_add_sync_generation, pre_add_snapshot_revision, pre_add_snapshot_digest, pre_add_identity_fact, created_at, dispatch_started_at) VALUES ('intent-1','attempt-1','qb-1','identity-1','request','target','STARTED',0,0,'baseline','ABSENT','2026-09-01T15:00:00Z','2026-09-01T15:00:01Z')`,
		`UPDATE race_jobs SET job_state='RESERVED', current_attempt_id='attempt-1' WHERE job_id='job-1'`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = New(context.Background(), Config{Database: db.Config{Path: path}, Hosts: []string{"vertex.local"}})
	if err == nil || !strings.Contains(err.Error(), "startup recovery failed closed") {
		t.Fatalf("New() error = %v, want recovery invariant failure", err)
	}
}

func sha256Digest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
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
