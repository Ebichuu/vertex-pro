package resourcesapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/httpapi"
)

func TestDailySitesUTCAttributionAndHistoricalConfirmation(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sites(site_id,name,base_url,enabled,created_at,updated_at) VALUES ('empty','Empty','https://empty.test',0,'2026-08-28T00:00:00Z','2026-08-28T00:00:00Z')`)
	exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,created_at,updated_at) VALUES ('feed-2','site-1','Second','https://hdsky.me/second','2026-08-29T00:00:00Z','2026-08-29T00:00:00Z')`)
	for key, stamp := range map[string]string{
		"old": "2026-08-28T23:59:59.900Z", "midnight": "2026-08-29T00:00:00Z",
		"offset": "2026-08-29T08:00:00+08:00", "future": "2026-08-29T09:00:00Z",
	} {
		exec(`INSERT INTO rss_sources(site_id,source_key,guid,url,title,fingerprint,first_seen_at,updated_at) VALUES ('site-1',?,?,'https://hdsky.me/item','Example','fp',?,?)`, key, key, stamp, stamp)
	}
	// An older job confirmed today remains counted after torrent removal.
	exec(`UPDATE race_jobs SET source_id='site-1/old',created_at='2026-08-28T23:59:59.900Z',job_state='HASH_SEEN' WHERE job_id='job-1'`)
	exec(`UPDATE placement_attempts SET attempt_state='HASH_SEEN',side_effect_fact='OBSERVED_EFFECT',qb_torrent_id='hash-1',confirmation_sync_generation=5,confirmation_snapshot_revision=2,confirmation_evidence_digest='evidence',confirmed_at='2026-08-29T00:00:00.100Z' WHERE attempt_id='attempt-1'`)
	exec(`INSERT INTO history_deletion_facts(fact_id,job_id,identity_key,qb_torrent_id,storage_target_id,deletion_source,deleted_at,last_known_observation,last_uploaded_bytes,last_downloaded_bytes,last_seen_snapshot_digest,last_seen_snapshot_revision,created_at) VALUES ('deleted','job-1','identity-1','hash-1','target-1','QB_USER_REMOVED','2026-08-29T02:00:00Z','SEEDING',100,100,'after',2,'2026-08-29T02:00:00Z')`)
	for _, item := range []struct{ id, source, state, stamp string }{
		{"today-failed", "offset", "PREPARE_FAILED", "2026-08-29T08:00:00+08:00"},
		{"today-waiting", "midnight", "DISCOVERED", "2026-08-29T00:00:00.001Z"},
		{"old-failed", "old", "PREPARE_FAILED", "2026-08-28T23:59:59.900Z"},
		{"future", "future", "DISCOVERED", "2026-08-29T09:00:00Z"},
		{"unattributed", "other-source", "PREPARE_FAILED", "2026-08-29T01:00:00Z"},
	} {
		exec(`INSERT INTO race_jobs(job_id,source_id,job_state,participation_deadline,observation_deadline,created_at,updated_at) VALUES (?,?,?,'2026-08-30T00:00:00Z','2026-08-30T00:00:00Z',?,?)`, item.id, "site-1/"+item.source, item.state, item.stamp, item.stamp)
	}
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	auth := newAuth(t, database)
	session := login(t, auth)
	deps := Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	request.Host = "vertex.local"
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	response := httptest.NewRecorder()
	Router(deps).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Date     string `json:"today_date"`
		Timezone string `json:"today_timezone"`
		Sites    []struct {
			ID                                            string `json:"id"`
			Enabled                                       bool   `json:"enabled"`
			Feeds, Discovered, Jobs, Confirmed, Attention int
		} `json:"today_sites"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Date != "2026-08-29" || payload.Timezone != "UTC" || len(payload.Sites) != 2 {
		t.Fatalf("payload=%+v", payload)
	}
	empty, site := payload.Sites[0], payload.Sites[1]
	if empty.ID != "empty" || empty.Enabled || empty.Feeds+empty.Discovered+empty.Jobs+empty.Confirmed+empty.Attention != 0 {
		t.Fatalf("empty site=%+v", empty)
	}
	if site.ID != "site-1" || site.Feeds != 2 || site.Discovered != 2 || site.Jobs != 2 || site.Confirmed != 1 || site.Attention != 1 {
		t.Fatalf("site=%+v", site)
	}
	now = now.Add(24 * time.Hour)
	rows, err := deps.todaySites(request, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for _, key := range []string{"discovered", "jobs", "confirmed", "attention"} {
			if row[key] != 0 {
				t.Fatalf("previous day leaked: %v", row)
			}
		}
	}
}
