package resourcesapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/adminauth"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/httpapi"
	"github.com/vertex-pro/vertex-pro/internal/realtimecards"
	"github.com/vertex-pro/vertex-pro/internal/rsspreview"
	"github.com/vertex-pro/vertex-pro/internal/rssservice"
	"github.com/vertex-pro/vertex-pro/internal/snapshot"
	"github.com/vertex-pro/vertex-pro/internal/sseserver"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

func TestRouterRequiresSession(t *testing.T) {
	database := openMigrated(t)
	auth := newAuth(t, database)
	router := Router(Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sites", nil)
	request.Host = "vertex.local"
	router.ServeHTTP(response, request)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnauthorized || envelope.Error.Code != "UNAUTHENTICATED" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestResourceEndpointsReturnContractPayloads(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	auth := newAuth(t, database)
	session := login(t, auth)
	validator, err := NewResponseValidator("../../contracts/openapi/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{Database: database, Auth: auth, Validator: validator, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) }})
	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/sites", "HDSky"},
		{"/api/v1/feeds", "feed-1"},
		{"/api/v1/feeds/feed-1/preparations", "items"},
		{"/api/v1/rules", "free-rule"},
		{"/api/v1/jobs", "Ubuntu 24.04 LTS"},
		{"/api/v1/instances", "qb-1"},
		{"/api/v1/dashboard", "user_status_counts"},
		{"/api/v1/stats/downloaders/summary?range=day&instance_id=qb-1", "bucket_start"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Host = "vertex.local"
			request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if !contains(response.Body.String(), test.want) {
				t.Fatalf("body = %s", response.Body.String())
			}
		})
	}
}

func TestAcknowledgeUnknownWiresManualClosure(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	auth := newAuth(t, database)
	session := login(t, auth)
	root := t.TempDir()
	store, err := snapshot.New(database, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(context.Background(), "qb-1", 5, 3, syncmerge.Snapshot{RID: 1, Torrents: map[string]syncmerge.Torrent{"identity-1": {ID: "identity-1", Fields: map[string]any{}}}}, true); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	if _, err := database.Exec(`UPDATE sync_instance_state SET updated_at=? WHERE instance_id='qb-1'`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{Database: database, Auth: auth, SnapshotRoot: root, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: func() time.Time { return now }})
	body := `{"action":"CONFIRM_EFFECT","command_id":"cmd-1","binding_id":"binding-1","evidence":{"sync_generation":5,"snapshot_revision":2,"snapshot_digest":"after","identity_present":true,"qb_torrent_id":"torrent-9"}}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/acknowledge-unknown", stringReader(body))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "confirm-effect-key")
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	request.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var jobState string
	if err := database.QueryRow(`SELECT job_state FROM race_jobs WHERE job_id='job-1'`).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if jobState != "RECONCILED_CONFIRMED" {
		t.Fatalf("job state = %s", jobState)
	}
}

func TestRuleWriteEndpointsPersistAndGuardRevision(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	auth := newAuth(t, database)
	session := login(t, auth)
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	router := Router(Deps{
		Database: database,
		Auth:     auth,
		Web:      &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}},
		Now:      func() time.Time { return now },
	})
	post := httptest.NewRequest(http.MethodPost, "/api/v1/rules", stringReader(`{"id":"rule-new","site_id":"site-1","name":"New rule","priority":50,"definition":{"condition_tree":{}}}`))
	post.Host = "vertex.local"
	post.Header.Set("Content-Type", "application/json")
	post.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	post.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	post.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, post)
	if response.Code != http.StatusCreated || !contains(response.Body.String(), `"revision":1`) {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != "/api/v1/rules/rule-new" {
		t.Fatalf("location=%q", got)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/rules/rule-new", nil)
	get.Host = "vertex.local"
	get.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, get)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("get status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	var revision int
	if err := database.QueryRow(`SELECT revision FROM rules WHERE rule_id='rule-new'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("created revision=%d", revision)
	}

	put := httptest.NewRequest(http.MethodPut, "/api/v1/rules/rule-new", stringReader(`{"enabled":false,"priority":80}`))
	put.Host = "vertex.local"
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("If-Match", `"1"`)
	put.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	put.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	put.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, put)
	if response.Code != http.StatusOK || !contains(response.Body.String(), `"revision":2`) {
		t.Fatalf("update status=%d body=%s", response.Code, response.Body.String())
	}
	var enabled bool
	if err := database.QueryRow(`SELECT enabled FROM rules WHERE rule_id='rule-new'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("updated rule is still enabled")
	}

	put = httptest.NewRequest(http.MethodPut, "/api/v1/rules/rule-new", stringReader(`{"priority":90}`))
	put.Host = "vertex.local"
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("If-Match", `"1"`)
	put.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	put.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	put.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, put)
	if response.Code != http.StatusConflict || !contains(response.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("stale update status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCreateFeedPersistsAndMapsForeignKeyErrors(t *testing.T) {
	database := openMigrated(t)
	auth := newAuth(t, database)
	session := login(t, auth)
	if _, err := database.Exec(`INSERT INTO sites(site_id,name,base_url,created_at,updated_at) VALUES(?,?,?,?,?)`, "site-feed", "Feed Site", "https://feed.example", "2026-08-29T07:00:00Z", "2026-08-29T07:00:00Z"); err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{
		Database: database,
		Auth:     auth,
		Web:      &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}},
		Now:      func() time.Time { return time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC) },
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/feeds", stringReader(`{"id":"feed-new","site_id":"site-feed","name":"Main RSS","feed_url":"https://feed.example/rss","enabled":true}`))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	request.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("Location") != "/api/v1/feeds/feed-new" {
		t.Fatalf("create status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var name, feedURL string
	if err := database.QueryRow(`SELECT name,feed_url FROM feeds WHERE feed_id='feed-new'`).Scan(&name, &feedURL); err != nil {
		t.Fatal(err)
	}
	if name != "Main RSS" || feedURL != "https://feed.example/rss" {
		t.Fatalf("feed=%q %q", name, feedURL)
	}
	var pollMinutes int
	if err := database.QueryRow(`SELECT poll_minutes FROM feeds WHERE feed_id='feed-new'`).Scan(&pollMinutes); err != nil {
		t.Fatal(err)
	}
	if pollMinutes != 15 {
		t.Fatalf("poll_minutes=%d", pollMinutes)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/feeds", stringReader(`{"id":"feed-new","site_id":"site-feed","name":"Duplicate","feed_url":"https://feed.example/duplicate"}`))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	request.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !contains(response.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("duplicate feed status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/feeds", stringReader(`{"id":"feed-missing","site_id":"missing","name":"Missing","feed_url":"https://feed.example/missing"}`))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	request.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !contains(response.Body.String(), "NOT_FOUND") {
		t.Fatalf("missing site status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCreateFeedRequiresCSRFBeforeDatabaseWrite(t *testing.T) {
	database := openMigrated(t)
	auth := newAuth(t, database)
	session := login(t, auth)
	if _, err := database.Exec(`INSERT INTO sites(site_id,name,base_url,created_at,updated_at) VALUES(?,?,?,?,?)`, "site-feed", "Feed Site", "https://feed.example", "2026-08-29T07:00:00Z", "2026-08-29T07:00:00Z"); err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/feeds", stringReader(`{"id":"feed-blocked","site_id":"site-feed","name":"Blocked","feed_url":"https://feed.example/blocked"}`))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !contains(response.Body.String(), "FORBIDDEN") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM feeds`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("feed count=%d", count)
	}
}

func TestFeedReadUpdateAndReadOnlyTestCommand(t *testing.T) {
	database := openMigrated(t)
	auth := newAuth(t, database)
	session := login(t, auth)
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	if _, err := database.Exec(`INSERT INTO sites(site_id,name,base_url,created_at,updated_at) VALUES(?,?,?,?,?)`, "site-feed", "Feed Site", "https://feed.example", now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, "feed-read", "site-feed", "Read RSS", "https://feed.example/rss", true, 15, now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	testCalls := 0
	router := Router(Deps{
		Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: func() time.Time { return now },
		FeedTest: func(_ context.Context, feed rssservice.Feed) (rsspreview.Preview, error) {
			testCalls++
			if feed.ID != "feed-read" {
				t.Fatalf("tested feed=%+v", feed)
			}
			return rsspreview.Preview{FeedURL: feed.FeedURL, Format: "rss", Entries: []rsspreview.Entry{{ID: "entry-1", Title: "Read only", Link: "https://feed.example/1", Published: "2026-08-29T08:00:00Z"}}}, nil
		},
	})
	get := httptest.NewRequest(http.MethodGet, "/api/v1/feeds/feed-read", nil)
	get.Host = "vertex.local"
	get.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, get)
	if response.Code != http.StatusOK || response.Header().Get("ETag") == "" || !contains(response.Body.String(), `"poll_minutes":15`) {
		t.Fatalf("get status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	etag := response.Header().Get("ETag")

	put := httptest.NewRequest(http.MethodPut, "/api/v1/feeds/feed-read", stringReader(`{"enabled":false,"poll_minutes":30}`))
	put.Host = "vertex.local"
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("If-Match", etag)
	put.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	put.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	put.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, put)
	if response.Code != http.StatusOK || response.Header().Get("ETag") == etag || !contains(response.Body.String(), `"poll_minutes":30`) || !contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("put status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	newETag := response.Header().Get("ETag")

	put = httptest.NewRequest(http.MethodPut, "/api/v1/feeds/feed-read", stringReader(`{"name":"stale"}`))
	put.Host = "vertex.local"
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("If-Match", etag)
	put.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	put.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	put.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, put)
	if response.Code != http.StatusConflict || !contains(response.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("stale put status=%d body=%s", response.Code, response.Body.String())
	}

	test := httptest.NewRequest(http.MethodPost, "/api/v1/feeds/feed-read/test", stringReader(`{}`))
	test.Host = "vertex.local"
	test.Header.Set("Content-Type", "application/json")
	test.Header.Set("Idempotency-Key", "feed-test-1")
	test.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	test.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	test.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, test)
	if response.Code != http.StatusAccepted || !contains(response.Body.String(), `"state":"SUCCEEDED"`) || !contains(response.Body.String(), "Read only") {
		t.Fatalf("test status=%d body=%s", response.Code, response.Body.String())
	}
	commandID := response.Header().Get("Location")
	if commandID == "" {
		t.Fatal("test command location is empty")
	}
	replay := httptest.NewRequest(http.MethodPost, "/api/v1/feeds/feed-read/test", stringReader(`{}`))
	replay.Host = "vertex.local"
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Idempotency-Key", "feed-test-1")
	replay.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	replay.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	replay.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, replay)
	if response.Code != http.StatusAccepted || response.Header().Get("Location") != commandID || testCalls != 1 {
		t.Fatalf("replay status=%d location=%q calls=%d body=%s", response.Code, response.Header().Get("Location"), testCalls, response.Body.String())
	}
	command := httptest.NewRequest(http.MethodGet, commandID, nil)
	command.Host = "vertex.local"
	command.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, command)
	if response.Code != http.StatusOK || !contains(response.Body.String(), `"revision":2`) || !contains(response.Body.String(), `"state":"SUCCEEDED"`) {
		t.Fatalf("command status=%d body=%s", response.Code, response.Body.String())
	}

	differentQuery := httptest.NewRequest(http.MethodPost, "/api/v1/feeds/feed-read/test?limit=10", stringReader(`{}`))
	differentQuery.Host = "vertex.local"
	differentQuery.Header.Set("Content-Type", "application/json")
	differentQuery.Header.Set("Idempotency-Key", "feed-test-1")
	differentQuery.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	differentQuery.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	differentQuery.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, differentQuery)
	if response.Code != http.StatusConflict || !contains(response.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("different request status=%d body=%s", response.Code, response.Body.String())
	}

	_ = newETag
}

func TestFeedUpdateAndTestRequireCSRF(t *testing.T) {
	database := openMigrated(t)
	auth := newAuth(t, database)
	session := login(t, auth)
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := database.Exec(`INSERT INTO sites(site_id,name,base_url,created_at,updated_at) VALUES(?,?,?,?,?)`, "site-feed", "Feed Site", "https://feed.example", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO feeds(feed_id,site_id,name,feed_url,enabled,poll_minutes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, "feed-csrf", "site-feed", "RSS", "https://feed.example/rss", true, 15, now, now); err != nil {
		t.Fatal(err)
	}
	router := Router(Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/feeds/feed-csrf", stringReader(`{"enabled":false}`))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", fmt.Sprintf("%q", now))
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !contains(response.Body.String(), "FORBIDDEN") {
		t.Fatalf("update without CSRF status=%d body=%s", response.Code, response.Body.String())
	}
	var enabled bool
	if err := database.QueryRow(`SELECT enabled FROM feeds WHERE feed_id='feed-csrf'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("feed changed despite missing CSRF")
	}
}

func TestCreateSitePersistsAndReturnsResourceHeaders(t *testing.T) {
	database := openMigrated(t)
	auth := newAuth(t, database)
	session := login(t, auth)
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	router := Router(Deps{
		Database: database,
		Auth:     auth,
		Web:      &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}},
		Now:      func() time.Time { return now },
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sites", stringReader(`{"id":"site-new","name":"New Site","base_url":"https://new.example","enabled":true}`))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	request.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != "/api/v1/sites/site-new" {
		t.Fatalf("location=%q", got)
	}
	if got := response.Header().Get("ETag"); got == "" {
		t.Fatal("missing ETag")
	}
	var name string
	if err := database.QueryRow(`SELECT name FROM sites WHERE site_id='site-new'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "New Site" {
		t.Fatalf("name=%q", name)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/sites", stringReader(`{"id":"site-other","name":"Other","base_url":"https://new.example"}`))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: httpapi.CSRFCookieName, Value: "csrf-1"})
	request.Header.Set(httpapi.CSRFHeaderName, "csrf-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !contains(response.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("duplicate status=%d body=%s", response.Code, response.Body.String())
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sites`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("site count=%d", count)
	}
}

func TestCreateSiteRequiresCSRFBeforeDatabaseWrite(t *testing.T) {
	database := openMigrated(t)
	auth := newAuth(t, database)
	session := login(t, auth)
	router := Router(Deps{Database: database, Auth: auth, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sites", stringReader(`{"id":"site-blocked","name":"Blocked","base_url":"https://blocked.example"}`))
	request.Host = "vertex.local"
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !contains(response.Body.String(), "FORBIDDEN") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sites`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("site count=%d", count)
	}
}

func newAuth(t *testing.T, database *sql.DB) *adminauth.Service {
	t.Helper()
	service, err := adminauth.New(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Initialize(context.Background(), "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	return service
}

func login(t *testing.T, auth *adminauth.Service) string {
	t.Helper()
	session, err := auth.Login(context.Background(), "correct-horse-battery", time.Date(2026, 8, 29, 7, 50, 0, 0, time.UTC), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return session.Token
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "resources-api.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestRealtimeEndpointAndDataQualityFields(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	auth := newAuth(t, database)
	session := login(t, auth)
	hub := sseserver.NewHub()
	registry := realtimecards.NewRegistry(hub, 30*time.Second)
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	registry.Update(realtimecards.Card{InstanceID: "qb-1", SyncState: "READY", DownloadRate: 2048, UploadRate: 256, DownloadingCount: 2, SeedingCount: 4, Source: "SYNC_SERVER_STATE"}, now)
	router := Router(Deps{Database: database, Auth: auth, Realtime: registry, Web: &httpapi.WebProtection{AllowedHosts: []string{"vertex.local"}}, Now: func() time.Time { return now }})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/stats/downloaders/realtime", nil)
	request.Host = "vertex.local"
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !contains(response.Body.String(), "SYNC_SERVER_STATE") || !contains(response.Body.String(), "FRESH") {
		t.Fatalf("realtime = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	request.Host = "vertex.local"
	request.AddCookie(&http.Cookie{Name: httpapi.SessionCookieName, Value: session})
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("instances status = %d", response.Code)
	}
	body := response.Body.String()
	if !contains(body, "data_quality") || !contains(body, "realtime_freshness") {
		t.Fatalf("instances body = %s", body)
	}
}
