package resourcesapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/diagnostics"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/settings"
)

func settingsAPI(t *testing.T) (http.Handler, string, *settings.Service, settings.Settings) {
	t.Helper()
	database := openMigrated(t)
	auth := newAuth(t, database)
	now := func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }
	service, err := settings.New(database, now)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewResponseValidator("../../contracts/openapi/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	return Router(Deps{Database: database, Auth: auth, Settings: service, ActiveSettings: initial.Values,
		Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: now, Validator: validator, Version: "settings-test"}), login(t, auth), service, initial
}

func requestSettings(router http.Handler, method, path, token, body, etag string, csrf bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: token})
	}
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	if csrf {
		request.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "test-csrf"})
		request.Header.Set("X-CSRF-Token", "test-csrf")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestSettingsPermissionsCSRFAndInputValidation(t *testing.T) {
	router, token, service, initial := settingsAPI(t)
	body, _ := json.Marshal(initial.Values)
	for _, path := range []string{"/api/v1/settings", "/api/v1/settings/diagnostics"} {
		for _, session := range []string{"", "invalid-session"} {
			response := requestSettings(router, http.MethodGet, path, session, "", "", false)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unprotected %s: %d", path, response.Code)
			}
		}
		if response := requestSettings(Router(Deps{}), http.MethodGet, path, "", "", "", false); response.Code != http.StatusUnauthorized {
			t.Fatal("nil auth bypassed settings guard")
		}
	}
	if response := requestSettings(router, http.MethodPut, "/api/v1/settings", token, string(body), `"1"`, false); response.Code != http.StatusForbidden {
		t.Fatalf("CSRF bypass: %d %s", response.Code, response.Body.String())
	}
	for _, test := range []struct {
		name, body, etag string
		status           int
	}{
		{"no revision", string(body), "", 400},
		{"multiple revisions", string(body), `"1", "2"`, 400},
		{"unknown field", strings.TrimSuffix(string(body), "}") + `,"security":{"csrf_protection":false}}`, `"1"`, 400},
		{"empty", `{}`, `"1"`, 422},
		{"null", `null`, `"1"`, 422},
		{"fraction", strings.Replace(string(body), `"session_ttl_hours":24`, `"session_ttl_hours":1.5`, 1), `"1"`, 400},
		{"blank timezone", strings.Replace(string(body), `"timezone":"UTC"`, `"timezone":" "`, 1), `"1"`, 422},
		{"oversized", strings.Repeat(" ", 4096) + string(body), `"1"`, 400},
		{"trailing", string(body) + ` {}`, `"1"`, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := requestSettings(router, http.MethodPut, "/api/v1/settings", token, test.body, test.etag, true)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	current, err := service.Load(context.Background())
	if err != nil || current != initial {
		t.Fatalf("rejected write changed settings: %+v, %v", current, err)
	}
}

func TestSettingsSaveConflictAndRedactedDiagnostics(t *testing.T) {
	router, token, service, initial := settingsAPI(t)
	loaded := requestSettings(router, http.MethodGet, "/api/v1/settings", token, "", "", false)
	if loaded.Code != 200 || loaded.Header().Get("ETag") != `"1"` || loaded.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("load=%d headers=%v body=%s", loaded.Code, loaded.Header(), loaded.Body.String())
	}
	values := initial.Values
	values.Timezone = "Asia/Shanghai"
	values.RateLimitPerMinute = 45
	body, _ := json.Marshal(values)
	saved := requestSettings(router, http.MethodPut, "/api/v1/settings", token, string(body), loaded.Header().Get("ETag"), true)
	if saved.Code != 200 || saved.Header().Get("ETag") != `"2"` {
		t.Fatalf("save=%d %s", saved.Code, saved.Body.String())
	}
	var response struct {
		Values          settings.Values
		Active          settings.Values
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Values != values || response.Active != initial.Values || !response.RestartRequired {
		t.Fatalf("runtime snapshot changed before restart: %+v", response)
	}
	conflict := requestSettings(router, http.MethodPut, "/api/v1/settings", token, string(body), `"1"`, true)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	// Secrets and URLs elsewhere in storage must never enter a settings support report.
	if _, err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	download := requestSettings(router, http.MethodGet, "/api/v1/settings/diagnostics", token, "", "", false)
	if download.Code != 200 || !strings.Contains(download.Header().Get("Content-Disposition"), "attachment") || download.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("download=%d %s", download.Code, download.Body.String())
	}
	var report diagnostics.Report
	if err := json.Unmarshal(download.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Version != "settings-test" || len(report.Checks) == 0 || !report.Checks[0].OK || len(report.Digest) != 71 || report.Config["timezone"] != "UTC" {
		t.Fatalf("report=%+v", report)
	}
	for _, secret := range []string{token, "test-csrf", "correct-horse-battery", "vertex_session", "password_hash", "feed_url"} {
		if strings.Contains(download.Body.String(), secret) {
			t.Fatalf("diagnostics leaked %q", secret)
		}
	}
	if fmt.Sprint(report.Config["settings_revision"]) != "2" {
		t.Fatalf("wrong saved revision: %+v", report.Config)
	}
}
