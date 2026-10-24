package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/retention"
	"github.com/vertex-pro/vertex-pro/internal/rssfetch"
	"github.com/vertex-pro/vertex-pro/internal/settings"
)

func TestSettingsApplyOnRestartToSessionsRetentionAndRSS(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	config := Config{Database: db.Config{Path: filepath.Join(t.TempDir(), "settings-runtime.db")}, Hosts: []string{"vertex.local"}, Now: func() time.Time { return now }}
	runtime, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { runtime.Close() }()
	if err := runtime.Auth.Initialize(ctx, "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	login := func(ttl time.Duration) string {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"password":"correct-horse-battery"}`))
		request.Host = "vertex.local"
		runtime.Handler.ServeHTTP(response, request)
		var result struct {
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || result.ExpiresAt != now.Add(ttl).Format(time.RFC3339) {
			t.Fatalf("login=%d %s", response.Code, response.Body.String())
		}
		for _, cookie := range response.Result().Cookies() {
			if cookie.Name == httpapi.SessionCookieName {
				if cookie.MaxAge != int(ttl/time.Second) {
					t.Fatalf("cookie ttl = %d", cookie.MaxAge)
				}
				return cookie.Value
			}
		}
		t.Fatal("missing session cookie")
		return ""
	}
	original := runtime.Settings
	service, err := settings.New(runtime.Database, config.Now)
	if err != nil {
		t.Fatal(err)
	}
	values := original.Values
	values.Timezone, values.SessionTTLHours = "Asia/Shanghai", 2
	values.RSSPollIntervalMinutes, values.RateLimitPerMinute, values.MaxConcurrentRSSFetches = 7, 60, 2
	values.RequestBurst = 2
	values.StatsRawRetentionDays, values.StatsHourlyRetentionDays = 2, 4
	if _, err := service.Update(ctx, original.Revision, values); err != nil {
		t.Fatal(err)
	}
	if runtime.Settings != original {
		t.Fatal("running snapshot changed without restart")
	}
	oldSession := login(24 * time.Hour)
	for _, age := range []int{3, 1} {
		at := now.Add(-time.Duration(age) * 24 * time.Hour).Format(time.RFC3339)
		if _, err := runtime.Database.Exec(`INSERT INTO instance_counter_samples(instance_id, sampled_at, download_bytes, upload_bytes, download_rate, upload_rate) VALUES ('settings-test', ?, 1, 2, 3, 4)`, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, age := range []int{5, 3} {
		at := now.Add(-time.Duration(age) * 24 * time.Hour).Format(time.RFC3339)
		if _, err := runtime.Database.Exec(`INSERT INTO instance_traffic_hourly(instance_id, bucket_start, download_bytes, upload_bytes, sample_count, revision, updated_at) VALUES ('settings-test', ?, 1, 2, 1, 1, ?)`, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	config.Retention = &RetentionConfig{Interval: time.Hour, Policy: retention.DefaultPolicy()}
	runtime, err = New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Settings.Values != values || runtime.Settings.Revision != 2 {
		t.Fatalf("active settings=%+v", runtime.Settings)
	}
	if valid, err := runtime.Auth.ValidateSession(ctx, oldSession, now.Add(3*time.Hour)); err != nil || !valid {
		t.Fatalf("existing session expiry was rewritten: %v, %v", valid, err)
	}
	login(2 * time.Hour)
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var raw, hourly int
		if err := runtime.Database.QueryRow(`SELECT COUNT(*) FROM instance_counter_samples`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := runtime.Database.QueryRow(`SELECT COUNT(*) FROM instance_traffic_hourly`).Scan(&hourly); err != nil {
			t.Fatal(err)
		}
		if raw == 1 && hourly == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("configured retention was not applied: raw=%d hourly=%d", raw, hourly)
		case <-ticker.C:
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) { _, _ = response.Write([]byte("<rss/>")) }))
	defer server.Close()
	if _, err := runtime.rssFetcher.Fetch(ctx, rssfetch.Request{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.rssFetcher.Fetch(ctx, rssfetch.Request{URL: server.URL}); err != nil {
		t.Fatal("saved burst was not applied", err)
	}
	limited, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := runtime.rssFetcher.Fetch(limited, rssfetch.Request{URL: server.URL}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runtime ignored saved RSS rate limit: %v", err)
	}
}
